// Package elevenlabs adapts the ElevenLabs Speech-to-Text (Scribe) API to the
// Transcriber interface.
//
// Audio is transcoded locally to a small mono Opus file and uploaded in one
// request. Scribe handles long-form audio natively, so this adapter should not
// be wrapped in chunked.
package elevenlabs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"transcriber/internal/procutil"
	"transcriber/internal/transcriber"
)

const (
	defaultBaseURL = "https://api.elevenlabs.io"
	defaultModelID = "scribe_v2"

	// Keyterm limits enforced by the API; terms outside them are dropped
	// rather than failing the whole request.
	maxKeyterms     = 100
	maxKeytermChars = 50
	maxKeytermWords = 5

	maxAttempts    = 3
	maxErrBodySize = 512
)

type Config struct {
	ID          string
	DisplayName string
	APIKey      string
	// ModelID is the Scribe model, e.g. "scribe_v2" or "scribe_v1".
	ModelID string
	// BaseURL defaults to https://api.elevenlabs.io.
	BaseURL   string
	FFmpegBin string
	// HTTPClient defaults to a client with no overall timeout; the job
	// context bounds the request instead, since uploads of long audio are slow.
	HTTPClient *http.Client
	// Transcode converts the input into the file that gets uploaded. Defaults
	// to an ffmpeg mono Opus transcode; tests override it.
	Transcode func(ctx context.Context, inputPath, outPath string) error
	// RetryBackoff is the base delay between retries (doubled each attempt).
	RetryBackoff time.Duration
}

type Adapter struct {
	cfg Config
}

func New(cfg Config) *Adapter {
	if cfg.ID == "" {
		cfg.ID = "elevenlabs-scribe"
	}
	if cfg.ModelID == "" {
		cfg.ModelID = defaultModelID
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.FFmpegBin == "" {
		cfg.FFmpegBin = "ffmpeg"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = 2 * time.Second
	}
	a := &Adapter{cfg: cfg}
	if a.cfg.Transcode == nil {
		a.cfg.Transcode = a.transcodeFFmpeg
	}
	return a
}

func (a *Adapter) ID() string { return a.cfg.ID }

func (a *Adapter) Name() string {
	if a.cfg.DisplayName != "" {
		return a.cfg.DisplayName
	}
	return "ElevenLabs " + a.cfg.ModelID
}

func (a *Adapter) Transcribe(ctx context.Context, req transcriber.Request, onProgress transcriber.ProgressFunc) (*transcriber.Result, error) {
	if a.cfg.APIKey == "" {
		return nil, errors.New("elevenlabs: API key not configured")
	}
	if onProgress == nil {
		onProgress = func(float64) {}
	}
	if err := os.MkdirAll(req.WorkDir, 0o755); err != nil {
		return nil, err
	}
	start := time.Now()

	audioPath := filepath.Join(req.WorkDir, "elevenlabs_audio.ogg")
	if err := a.cfg.Transcode(ctx, req.InputPath, audioPath); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("elevenlabs transcode: %w", err)
	}
	onProgress(0.1)

	body, err := a.postWithRetry(ctx, audioPath, req)
	if err != nil {
		return nil, err
	}
	onProgress(0.9)

	// Raw response is kept in scratch for debugging (-keep-work-dirs).
	_ = os.WriteFile(filepath.Join(req.WorkDir, "elevenlabs_raw.json"), body, 0o644)

	tr, err := parseResponse(body, req.Language)
	if err != nil {
		return nil, err
	}
	onProgress(1.0)
	return &transcriber.Result{
		Transcription: tr,
		ModelUsed:     a.cfg.ID,
		Duration:      time.Since(start),
	}, nil
}

// transcodeFFmpeg writes a 16 kHz mono Opus file, which is a fraction of the
// size of the WAV/video inputs and plenty for speech recognition.
func (a *Adapter) transcodeFFmpeg(ctx context.Context, inputPath, outPath string) error {
	cmd := exec.CommandContext(ctx, a.cfg.FFmpegBin,
		"-nostdin", "-loglevel", "error", "-y",
		"-i", inputPath,
		"-vn",
		"-ar", "16000",
		"-ac", "1",
		"-c:a", "libopus",
		"-b:a", "32k",
		outPath,
	)
	procutil.KillGroupOnCancel(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// apiError is a non-2xx response from the API.
type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("elevenlabs: HTTP %d: %s", e.Status, e.Body)
}

func (e *apiError) retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

func (a *Adapter) postWithRetry(ctx context.Context, audioPath string, req transcriber.Request) ([]byte, error) {
	backoff := a.cfg.RetryBackoff
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		body, err := a.post(ctx, audioPath, req)
		if err == nil {
			return body, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		lastErr = err
		var ae *apiError
		if errors.As(err, &ae) && !ae.retryable() {
			return nil, err
		}
		if attempt == maxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return nil, lastErr
}

func (a *Adapter) post(ctx context.Context, audioPath string, req transcriber.Request) ([]byte, error) {
	f, err := os.Open(audioPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Stream the multipart body so long recordings aren't buffered in memory.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		pw.CloseWithError(a.writeForm(mw, f, req))
	}()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.BaseURL+"/v1/speech-to-text", pr)
	if err != nil {
		pr.Close()
		return nil, err
	}
	httpReq.Header.Set("xi-api-key", a.cfg.APIKey)
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	httpReq.Header.Set("Accept", "application/json")

	resp, err := a.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		pr.Close()
		return nil, fmt.Errorf("elevenlabs request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > maxErrBodySize {
			msg = msg[:maxErrBodySize] + "…"
		}
		return nil, &apiError{Status: resp.StatusCode, Body: msg}
	}
	return body, nil
}

func (a *Adapter) writeForm(mw *multipart.Writer, audio io.Reader, req transcriber.Request) error {
	fields := [][2]string{
		{"model_id", a.cfg.ModelID},
		{"timestamps_granularity", "word"},
		{"tag_audio_events", "false"},
	}
	if lang := strings.ToLower(strings.TrimSpace(req.Language)); lang != "" && lang != "auto" {
		fields = append(fields, [2]string{"language_code", lang})
	}
	if a.supportsKeyterms() {
		for _, t := range keyterms(req.Prompt) {
			fields = append(fields, [2]string{"keyterms", t})
		}
	}
	for _, kv := range fields {
		if err := mw.WriteField(kv[0], kv[1]); err != nil {
			return err
		}
	}
	part, err := mw.CreateFormFile("file", "audio.ogg")
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, audio); err != nil {
		return err
	}
	return mw.Close()
}

// scribe_v1 rejects keyterms; newer models accept them.
func (a *Adapter) supportsKeyterms() bool {
	return !strings.HasPrefix(a.cfg.ModelID, "scribe_v1")
}

// keyterms turns a whisper-style prompt (a comma/newline separated list of
// names and terms) into Scribe keyterms. Entries that are too long to be a
// term, e.g. prose sentences, are dropped.
func keyterms(prompt string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range strings.FieldsFunc(prompt, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';'
	}) {
		t := strings.Join(strings.Fields(raw), " ")
		t = strings.TrimRight(t, ".")
		if t == "" || len([]rune(t)) > maxKeytermChars || len(strings.Fields(t)) > maxKeytermWords {
			continue
		}
		key := strings.ToLower(t)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
		if len(out) == maxKeyterms {
			break
		}
	}
	return out
}
