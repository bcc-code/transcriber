// Package aligned wraps a Transcriber and re-times its words with the forced
// aligner service (aligner/ in this repo). The text is never changed; only
// word and segment timestamps move. Alignment is best effort: when the
// aligner is down, has no model for the language, or errors, the inner
// result is returned unchanged.
package aligned

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"transcriber/internal/transcriber"
)

type Config struct {
	// URL is the aligner base URL, e.g. http://aligner:8000.
	URL    string
	Client *http.Client
}

type Adapter struct {
	inner transcriber.Transcriber
	cfg   Config
}

func New(inner transcriber.Transcriber, cfg Config) *Adapter {
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	return &Adapter{inner: inner, cfg: cfg}
}

func (a *Adapter) ID() string   { return a.inner.ID() }
func (a *Adapter) Name() string { return a.inner.Name() }

// alignShare is the slice of the progress bar reserved for alignment.
const alignShare = 0.05

func (a *Adapter) Transcribe(ctx context.Context, req transcriber.Request, onProgress transcriber.ProgressFunc) (*transcriber.Result, error) {
	res, err := a.inner.Transcribe(ctx, req, func(p float64) {
		if onProgress != nil {
			onProgress(p * (1 - alignShare))
		}
	})
	if err != nil || res == nil || res.Transcription == nil {
		return res, err
	}

	lang := req.Language
	if lang == "" || lang == "auto" {
		lang = res.Transcription.Language
	}
	start := time.Now()
	n, err := a.align(ctx, req.InputPath, lang, res.Transcription)
	switch {
	case err == nil:
		slog.Info("aligned words", "model", a.ID(), "language", lang, "aligned", n, "took", time.Since(start).Round(time.Millisecond))
	case ctx.Err() != nil:
		return nil, ctx.Err()
	default:
		slog.Warn("alignment skipped; keeping model timestamps", "model", a.ID(), "language", lang, "err", err)
	}
	if onProgress != nil {
		onProgress(1)
	}
	return res, nil
}

type alignWord struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type alignRequest struct {
	AudioPath string `json:"audio_path"`
	Language  string `json:"language"`
	Segments  []struct {
		Words []alignWord `json:"words"`
	} `json:"segments"`
}

type alignTime struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type alignResponse struct {
	Segments []struct {
		Words []*alignTime `json:"words"`
	} `json:"segments"`
	Error string `json:"error"`
}

// align updates tr in place and returns how many words got new times.
func (a *Adapter) align(ctx context.Context, audioPath, lang string, tr *transcriber.Transcription) (int, error) {
	in := alignRequest{AudioPath: audioPath, Language: lang}
	in.Segments = make([]struct {
		Words []alignWord `json:"words"`
	}, len(tr.Segments))
	for i, s := range tr.Segments {
		ws := make([]alignWord, len(s.Words))
		for j, w := range s.Words {
			ws[j] = alignWord{Text: w.Text, Start: w.Start, End: w.End}
		}
		in.Segments[i].Words = ws
	}
	body, err := json.Marshal(in)
	if err != nil {
		return 0, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.URL+"/align", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := a.cfg.Client.Do(httpReq)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	var out alignResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, fmt.Errorf("aligner: status %d: %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("aligner: status %d: %s", resp.StatusCode, out.Error)
	}
	if len(out.Segments) != len(tr.Segments) {
		return 0, fmt.Errorf("aligner: %d segments back for %d sent", len(out.Segments), len(tr.Segments))
	}
	for i, s := range out.Segments {
		if len(s.Words) != len(tr.Segments[i].Words) {
			return 0, fmt.Errorf("aligner: segment %d: %d words back for %d sent", i, len(s.Words), len(tr.Segments[i].Words))
		}
	}
	return apply(tr, out), nil
}

// apply copies aligned times into tr. Words the aligner could not place
// (null, e.g. bare numbers) keep the model's time, clamped between their
// aligned neighbours so the word order stays monotonic. Segment bounds are
// tightened to their words.
func apply(tr *transcriber.Transcription, out alignResponse) int {
	n := 0
	for i := range tr.Segments {
		seg := &tr.Segments[i]
		times := out.Segments[i].Words
		for j := range seg.Words {
			if t := times[j]; t != nil {
				seg.Words[j].Start, seg.Words[j].End = t.Start, t.End
				n++
			}
		}
		for j := range seg.Words {
			if times[j] != nil {
				continue
			}
			w := &seg.Words[j]
			lo, hi := w.Start, w.End
			if j > 0 {
				lo = max(lo, seg.Words[j-1].End)
			}
			for k := j + 1; k < len(seg.Words); k++ {
				if times[k] != nil {
					hi = min(hi, seg.Words[k].Start)
					break
				}
			}
			if hi < lo {
				hi = lo
			}
			w.Start, w.End = lo, hi
		}
		if len(seg.Words) > 0 {
			seg.Start = seg.Words[0].Start
			seg.End = seg.Words[len(seg.Words)-1].End
		}
	}
	return n
}
