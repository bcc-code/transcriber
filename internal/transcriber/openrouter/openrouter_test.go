package openrouter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"transcriber/internal/transcriber"
)

func copyTranscode(_ context.Context, in, out string) error {
	b, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	return os.WriteFile(out, b, 0o644)
}

func newTestAdapter(t *testing.T, h http.HandlerFunc) *Adapter {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(Config{
		APIKey:       "secret",
		BaseURL:      srv.URL,
		Transcode:    copyTranscode,
		RetryBackoff: time.Millisecond,
	})
}

func testRequest(t *testing.T, lang, prompt string) transcriber.Request {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.wav")
	if err := os.WriteFile(in, []byte("fake-audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	return transcriber.Request{InputPath: in, Language: lang, Prompt: prompt, WorkDir: filepath.Join(dir, "work")}
}

const verboseJSON = `{
  "task": "transcribe", "language": "norwegian", "duration": 4.2,
  "text": "Hei alle sammen. Velkommen til BCC.",
  "segments": [
    {"id": 0, "start": 0.0, "end": 1.8, "text": " Hei alle sammen.", "avg_logprob": -0.2},
    {"id": 1, "start": 2.0, "end": 4.1, "text": " Velkommen til BCC."}
  ],
  "words": [
    {"word": "Hei", "start": 0.0, "end": 0.4},
    {"word": "alle", "start": 0.5, "end": 0.9},
    {"word": "sammen.", "start": 1.0, "end": 1.8},
    {"word": "Velkommen", "start": 2.0, "end": 2.7},
    {"word": "til", "start": 2.8, "end": 3.0},
    {"word": "BCC.", "start": 3.2, "end": 4.1}
  ]
}`

func TestTranscribeSendsExpectedRequest(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/transcriptions" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("path=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		f := r.MultipartForm.Value
		if f["model"][0] != "openai/whisper-large-v3" || f["response_format"][0] != "verbose_json" ||
			f["language"][0] != "no" || f["prompt"][0] != "BCC, Brunstad" {
			t.Errorf("fields: %v", f)
		}
		if strings.Join(f["timestamp_granularities[]"], ",") != "segment,word" {
			t.Errorf("granularities: %v", f["timestamp_granularities[]"])
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := io.ReadAll(file); string(b) != "fake-audio" {
			t.Errorf("file body %q", b)
		}
		w.Write([]byte(verboseJSON))
	})
	var last float64
	res, err := a.Transcribe(context.Background(), testRequest(t, "no", "BCC, Brunstad"), func(p float64) { last = p })
	if err != nil {
		t.Fatal(err)
	}
	tr := res.Transcription
	if tr.Language != "no" || len(tr.Segments) != 2 || last != 1 || res.ModelUsed != "openrouter/openai/whisper-large-v3" {
		t.Fatalf("lang=%s segs=%d progress=%v model=%s", tr.Language, len(tr.Segments), last, res.ModelUsed)
	}
	if tr.Segments[0].Text != "Hei alle sammen." || len(tr.Segments[0].Words) != 3 || len(tr.Segments[1].Words) != 3 {
		t.Errorf("segments: %+v", tr.Segments)
	}
	if tr.Segments[1].Words[2].Text != "BCC." || tr.Segments[1].Words[2].End != 4.1 {
		t.Errorf("last word: %+v", tr.Segments[1].Words[2])
	}
}

func TestTranscribeAutoLanguageOmitsFields(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(1 << 20)
		f := r.MultipartForm.Value
		if _, ok := f["language"]; ok {
			t.Errorf("language sent for auto")
		}
		if _, ok := f["prompt"]; ok {
			t.Errorf("empty prompt sent")
		}
		w.Write([]byte(`{"text":"Hello there.","language":"en","words":[{"word":"Hello","start":0,"end":0.5},{"word":"there.","start":0.6,"end":1}]}`))
	})
	res, err := a.Transcribe(context.Background(), testRequest(t, "auto", ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	if tr := res.Transcription; tr.Language != "en" || len(tr.Segments) != 1 || len(tr.Segments[0].Words) != 2 {
		t.Errorf("got %+v", tr)
	}
}

func TestParseTextOnly(t *testing.T) {
	tr, err := parseResponse([]byte(`{"text":" Just text. "}`), "de")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Text != "Just text." || tr.Language != "de" || len(tr.Segments) != 1 || tr.Segments[0].Text != "Just text." {
		t.Errorf("got %+v", tr)
	}
}

func TestTranscribeClientErrorNotRetried(t *testing.T) {
	var calls atomic.Int32
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		http.Error(w, `{"error":{"code":401,"message":"No auth credentials found"}}`, http.StatusUnauthorized)
	})
	_, err := a.Transcribe(context.Background(), testRequest(t, "", ""), nil)
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

func TestTranscribeRetriesServerErrors(t *testing.T) {
	var calls atomic.Int32
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if calls.Add(1) < 3 {
			http.Error(w, "busy", http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"text":"ok"}`))
	})
	if _, err := a.Transcribe(context.Background(), testRequest(t, "", ""), nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}
