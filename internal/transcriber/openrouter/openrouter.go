// Package openrouter adapts the OpenRouter speech-to-text endpoint
// (POST /api/v1/audio/transcriptions) to the Transcriber interface.
//
// Each input is transcoded locally to a small mono Opus file and uploaded as
// multipart form data, asking for verbose_json with segment and word
// timestamps. The endpoint caps uploads at 25 MB, so register this adapter
// wrapped in chunked to keep every request well below that.
package openrouter

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
	defaultBaseURL = "https://openrouter.ai/api/v1"
	defaultModel   = "openai/whisper-large-v3"
	appTitle       = "transcriber"

	maxAttempts    = 3
	maxErrBodySize = 512
)

type Config struct {
	ID          string
	DisplayName string
	APIKey      string
	// Model is the OpenRouter model slug, e.g. "openai/whisper-large-v3".
	Model string
	// BaseURL defaults to https://openrouter.ai/api/v1.
	BaseURL   string
	FFmpegBin string
	// HTTPClient defaults to a client with no overall timeout; the job
	// context bounds the request instead.
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
	if cfg.Model == "" {
		cfg.Model = defaultModel
	}
	if cfg.ID == "" {
		cfg.ID = "openrouter/" + cfg.Model
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
	return "OpenRouter " + a.cfg.Model
}

func (a *Adapter) Transcribe(ctx context.Context, req transcriber.Request, onProgress transcriber.ProgressFunc) (*transcriber.Result, error) {
	if a.cfg.APIKey == "" {
		return nil, errors.New("openrouter: API key not configured")
	}
	if onProgress == nil {
		onProgress = func(float64) {}
	}
	if err := os.MkdirAll(req.WorkDir, 0o755); err != nil {
		return nil, err
	}
	start := time.Now()

	audioPath := filepath.Join(req.WorkDir, "openrouter_audio.ogg")
	if err := a.cfg.Transcode(ctx, req.InputPath, audioPath); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("openrouter transcode: %w", err)
	}
	onProgress(0.1)

	body, err := a.postWithRetry(ctx, audioPath, req)
	if err != nil {
		return nil, err
	}
	onProgress(0.9)

	// Raw response is kept in scratch for debugging (-keep-work-dirs).
	_ = os.WriteFile(filepath.Join(req.WorkDir, "openrouter_raw.json"), body, 0o644)

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

// transcodeFFmpeg writes a 16 kHz mono Opus file (~14 MB per hour).
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
	return fmt.Sprintf("openrouter: HTTP %d: %s", e.Status, e.Body)
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

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		pw.CloseWithError(a.writeForm(mw, f, req))
	}()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.BaseURL+"/audio/transcriptions", pr)
	if err != nil {
		pr.Close()
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+a.cfg.APIKey)
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("X-OpenRouter-Title", appTitle)

	resp, err := a.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		pr.Close()
		return nil, fmt.Errorf("openrouter request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openrouter read response: %w", err)
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
		{"model", a.cfg.Model},
		{"response_format", "verbose_json"},
		{"timestamp_granularities[]", "segment"},
		{"timestamp_granularities[]", "word"},
		{"temperature", "0"},
	}
	if lang := strings.ToLower(strings.TrimSpace(req.Language)); lang != "" && lang != "auto" {
		fields = append(fields, [2]string{"language", lang})
	}
	if p := strings.TrimSpace(req.Prompt); p != "" {
		fields = append(fields, [2]string{"prompt", p})
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
