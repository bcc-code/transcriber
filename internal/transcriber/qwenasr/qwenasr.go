// Package qwenasr is the adapter for the Qwen3-ASR service (qwen-asr/ in this
// repo), which runs NbAiLab's Nordlyd fine-tune on the local GPU.
//
// The model returns text without timestamps. The service transcribes short
// pieces (<= ~20 s) of audio separately and returns each with its span, so
// this adapter spreads each piece's words over that span by length. The
// estimates are meant to be replaced by the forced aligner (aligned package);
// without it, word times are only approximate.
package qwenasr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"transcriber/internal/transcriber"
)

type Config struct {
	ID          string
	DisplayName string
	// URL is the service base URL, e.g. http://qwen-asr:8000.
	URL string
	// DefaultLanguage is reported when the request says "auto" and the model
	// gives no recognisable language.
	DefaultLanguage string
	Client          *http.Client
}

type Adapter struct{ cfg Config }

func New(cfg Config) *Adapter {
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	return &Adapter{cfg: cfg}
}

func (a *Adapter) ID() string   { return a.cfg.ID }
func (a *Adapter) Name() string { return a.cfg.DisplayName }

type request struct {
	AudioPath string `json:"audio_path"`
	Language  string `json:"language"`
	Context   string `json:"context,omitempty"`
}

type response struct {
	Model    string `json:"model"`
	Language string `json:"language"`
	Segments []struct {
		Start float64 `json:"start"`
		End   float64 `json:"end"`
		Text  string  `json:"text"`
	} `json:"segments"`
	Error string `json:"error"`
}

func (a *Adapter) Transcribe(ctx context.Context, req transcriber.Request, onProgress transcriber.ProgressFunc) (*transcriber.Result, error) {
	start := time.Now()
	lang := strings.ToLower(req.Language)
	if lang == "" {
		lang = "auto"
	}
	body, err := json.Marshal(request{AudioPath: req.InputPath, Language: lang, Context: req.Prompt})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.URL+"/transcribe", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := a.cfg.Client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("qwen-asr: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("qwen-asr: %w", err)
	}
	var out response
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("qwen-asr: status %d: %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("qwen-asr: status %d: %s", resp.StatusCode, out.Error)
	}

	tr := &transcriber.Transcription{Segments: []transcriber.Segment{}}
	texts := make([]string, 0, len(out.Segments))
	for _, s := range out.Segments {
		text := strings.Join(strings.Fields(s.Text), " ")
		if text == "" {
			continue
		}
		tr.Segments = append(tr.Segments, transcriber.Segment{
			ID:    len(tr.Segments),
			Text:  text,
			Start: s.Start,
			End:   s.End,
			Words: spreadWords(text, s.Start, s.End),
		})
		texts = append(texts, text)
	}
	tr.Text = strings.Join(texts, " ")
	tr.Language = out.Language
	if tr.Language == "" || tr.Language == "auto" {
		if lang != "auto" {
			tr.Language = lang
		} else {
			tr.Language = a.cfg.DefaultLanguage
		}
	}

	if onProgress != nil {
		onProgress(1)
	}
	return &transcriber.Result{Transcription: tr, ModelUsed: a.cfg.ID, Duration: time.Since(start)}, nil
}

// spreadWords splits text into words and gives each a share of [start, end]
// proportional to its length (plus one for the following space).
func spreadWords(text string, start, end float64) []transcriber.Word {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil
	}
	total := 0
	for _, f := range fields {
		total += utf8.RuneCountInString(f) + 1
	}
	span := max(end-start, 0)
	words := make([]transcriber.Word, len(fields))
	t := start
	for i, f := range fields {
		n := float64(utf8.RuneCountInString(f))
		share := (n + 1) / float64(total) * span
		// The word takes its share minus the trailing space.
		w := transcriber.Word{Text: f, Start: t, End: t + share*n/(n+1)}
		if i == len(fields)-1 {
			w.End = end
		}
		words[i] = w
		t += share
	}
	return words
}
