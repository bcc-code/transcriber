package elevenlabs

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

func newTestAdapter(t *testing.T, modelID string, h http.HandlerFunc) *Adapter {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(Config{
		APIKey:       "secret",
		ModelID:      modelID,
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

func TestTranscribeSendsExpectedRequest(t *testing.T) {
	raw, _ := os.ReadFile(filepath.Join("testdata", "raw.json"))
	a := newTestAdapter(t, "scribe_v2", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/speech-to-text" || r.Header.Get("xi-api-key") != "secret" {
			t.Errorf("path=%s key=%q", r.URL.Path, r.Header.Get("xi-api-key"))
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		f := r.MultipartForm.Value
		if f["model_id"][0] != "scribe_v2" || f["timestamps_granularity"][0] != "word" || f["language_code"][0] != "no" {
			t.Errorf("fields: %v", f)
		}
		if strings.Join(f["keyterms"], "|") != "BCC|Brunstad" {
			t.Errorf("keyterms: %v", f["keyterms"])
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := io.ReadAll(file); string(b) != "fake-audio" {
			t.Errorf("file body %q", b)
		}
		w.Write(raw)
	})
	var last float64
	res, err := a.Transcribe(context.Background(), testRequest(t, "no", "BCC, Brunstad"), func(p float64) { last = p })
	if err != nil {
		t.Fatal(err)
	}
	if res.Transcription.Language != "en" || len(res.Transcription.Segments) != 3 || last != 1 {
		t.Errorf("lang=%s segs=%d progress=%v", res.Transcription.Language, len(res.Transcription.Segments), last)
	}
}

func TestTranscribeAutoLanguageAndV1OmitFields(t *testing.T) {
	a := newTestAdapter(t, "scribe_v1", func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(1 << 20)
		f := r.MultipartForm.Value
		if _, ok := f["language_code"]; ok {
			t.Errorf("language_code sent for auto")
		}
		if _, ok := f["keyterms"]; ok {
			t.Errorf("keyterms sent to scribe_v1")
		}
		if f["model_id"][0] != "scribe_v1" {
			t.Errorf("model_id %v", f["model_id"])
		}
		w.Write([]byte(`{"language_code":"nor","text":"Hei.","words":[{"text":"Hei.","start":0,"end":1,"type":"word"}]}`))
	})
	if _, err := a.Transcribe(context.Background(), testRequest(t, "auto", "BCC"), nil); err != nil {
		t.Fatal(err)
	}
}

func TestTranscribeClientErrorNotRetried(t *testing.T) {
	var calls atomic.Int32
	a := newTestAdapter(t, "", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		http.Error(w, `{"detail":"invalid api key"}`, http.StatusUnauthorized)
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
	a := newTestAdapter(t, "", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if calls.Add(1) < 3 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"language_code":"eng","text":"ok","words":[{"text":"ok","start":0,"end":1,"type":"word"}]}`))
	})
	if _, err := a.Transcribe(context.Background(), testRequest(t, "", ""), nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestTranscodeFFmpeg(t *testing.T) {
	a := New(Config{APIKey: "x"})
	if _, err := exec.LookPath(a.cfg.FFmpegBin); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "in.wav")
	gen := exec.Command(a.cfg.FFmpegBin, "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=1", in)
	if err := gen.Run(); err != nil {
		t.Skipf("cannot generate test audio: %v", err)
	}
	out := filepath.Join(dir, "out.ogg")
	if err := a.transcodeFFmpeg(context.Background(), in, out); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		t.Fatalf("output missing: %v", err)
	}
}
