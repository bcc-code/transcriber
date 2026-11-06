// Package omnilingual adapts the Meta Omnilingual ASR sidecar (omnilingual/ at
// the repo root) to the Transcriber interface.
//
// The model is Python-only (fairseq2 + PyTorch) and a 7B checkpoint takes far
// too long to load per chunk, so it runs as a long-lived HTTP service and this
// adapter just uploads audio to it. The sidecar splits the audio on silence
// into regions of at most 30 s (the model rejects inputs over 40 s), and each
// region comes back as one segment. There are no word timestamps, so subtitle
// formats emit one cue per segment.
package omnilingual

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"transcriber/internal/transcriber"
)

type Config struct {
	ID          string
	DisplayName string
	// BaseURL of the sidecar, e.g. http://omnilingual:8000.
	BaseURL string
	// HTTPClient defaults to a client with no timeout: jobs are bounded by
	// the request context (the per-job timeout), and a 5-minute chunk on a
	// 7B model can legitimately take minutes.
	HTTPClient *http.Client
}

type Adapter struct {
	cfg Config
}

func New(cfg Config) *Adapter {
	if cfg.ID == "" {
		cfg.ID = "omnilingual"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Adapter{cfg: cfg}
}

func (a *Adapter) ID() string { return a.cfg.ID }

// IgnoresPrompt reports that this model has no prompt conditioning.
func (a *Adapter) IgnoresPrompt() bool { return true }

func (a *Adapter) Name() string {
	if a.cfg.DisplayName != "" {
		return a.cfg.DisplayName
	}
	return "Omnilingual ASR"
}

// langCodes maps the ISO 639-1 codes the API accepts to Omnilingual's
// {iso639-3}_{script} codes. Every target was checked against the model's
// supported_langs. Nynorsk (nn) has no entry upstream, so it falls through to
// "no language hint" rather than being forced to Bokmål.
var langCodes = map[string]string{
	"no": "nob_Latn",
	"nb": "nob_Latn",
	"en": "eng_Latn",
	"de": "deu_Latn",
	"sv": "swe_Latn",
	"da": "dan_Latn",
	"fi": "fin_Latn",
	"fr": "fra_Latn",
	"es": "spa_Latn",
	"pt": "por_Latn",
	"it": "ita_Latn",
	"nl": "nld_Latn",
	"pl": "pol_Latn",
	"ru": "rus_Cyrl",
	"uk": "ukr_Cyrl",
	"ro": "ron_Latn",
	"hu": "hun_Latn",
}

// LangCode returns the Omnilingual language code for an ISO 639-1 code, or ""
// when there is none (including "auto" and ""), meaning "let the model guess".
func LangCode(iso string) string {
	return langCodes[strings.ToLower(iso)]
}

type response struct {
	Language string `json:"language"`
	Segments []struct {
		Start float64 `json:"start"`
		End   float64 `json:"end"`
		Text  string  `json:"text"`
	} `json:"segments"`
}

// Transcribe uploads the input to the sidecar. req.Prompt is ignored: the
// model has no prompt/vocabulary conditioning. There is no intra-request
// progress; the chunked wrapper reports progress per chunk.
func (a *Adapter) Transcribe(ctx context.Context, req transcriber.Request, onProgress transcriber.ProgressFunc) (*transcriber.Result, error) {
	if a.cfg.BaseURL == "" {
		return nil, fmt.Errorf("omnilingual: base URL not configured")
	}
	f, err := os.Open(req.InputPath)
	if err != nil {
		return nil, fmt.Errorf("omnilingual: open input: %w", err)
	}
	defer f.Close()

	lang := LangCode(req.Language)

	// Stream the multipart body so an hour-long file isn't buffered in memory.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := func() error {
			if lang != "" {
				if err := mw.WriteField("lang", lang); err != nil {
					return err
				}
			}
			part, err := mw.CreateFormFile("audio", filepath.Base(req.InputPath))
			if err != nil {
				return err
			}
			if _, err := io.Copy(part, f); err != nil {
				return err
			}
			return mw.Close()
		}()
		pw.CloseWithError(err)
	}()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.BaseURL+"/transcribe", pr)
	if err != nil {
		pr.Close()
		return nil, err
	}
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())

	start := time.Now()
	resp, err := a.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		pr.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("omnilingual: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("omnilingual: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var raw response
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("omnilingual: decode response: %w", err)
	}

	t := &transcriber.Transcription{
		Language: req.Language,
		Segments: make([]transcriber.Segment, 0, len(raw.Segments)),
	}
	texts := make([]string, 0, len(raw.Segments))
	for _, s := range raw.Segments {
		text := strings.TrimSpace(s.Text)
		if text == "" {
			continue
		}
		t.Segments = append(t.Segments, transcriber.Segment{
			ID:    len(t.Segments),
			Start: s.Start,
			End:   s.End,
			Text:  text,
		})
		texts = append(texts, text)
	}
	t.Text = strings.Join(texts, " ")
	if onProgress != nil {
		onProgress(1)
	}
	return &transcriber.Result{
		Transcription: t,
		ModelUsed:     a.cfg.ID,
		Duration:      time.Since(start),
	}, nil
}
