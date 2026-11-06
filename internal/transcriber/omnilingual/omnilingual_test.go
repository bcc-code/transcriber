package omnilingual

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"transcriber/internal/transcriber"
)

func writeInput(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "in.wav")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTranscribeSendsAudioAndLang(t *testing.T) {
	var gotLang, gotAudio, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		gotLang = r.FormValue("lang")
		f, _, err := r.FormFile("audio")
		if err != nil {
			t.Errorf("audio field: %v", err)
		} else {
			b, _ := io.ReadAll(f)
			gotAudio = string(b)
		}
		io.WriteString(w, `{"language":"nob_Latn","segments":[
			{"start":0.5,"end":4.25,"text":" hei og velkommen "},
			{"start":5,"end":6,"text":"   "},
			{"start":6.1,"end":9.9,"text":"til møtet"}]}`)
	}))
	defer srv.Close()

	var progress float64
	a := New(Config{ID: "omni", BaseURL: srv.URL + "/"})
	res, err := a.Transcribe(context.Background(), transcriber.Request{
		InputPath: writeInput(t, "RIFFfake"),
		Language:  "no",
	}, func(p float64) { progress = p })
	if err != nil {
		t.Fatal(err)
	}

	if gotPath != "/transcribe" {
		t.Errorf("path = %q", gotPath)
	}
	if gotLang != "nob_Latn" {
		t.Errorf("lang = %q, want nob_Latn", gotLang)
	}
	if gotAudio != "RIFFfake" {
		t.Errorf("audio = %q", gotAudio)
	}
	if progress != 1 {
		t.Errorf("progress = %v, want 1", progress)
	}

	tr := res.Transcription
	if res.ModelUsed != "omni" {
		t.Errorf("ModelUsed = %q", res.ModelUsed)
	}
	if tr.Language != "no" {
		t.Errorf("Language = %q, want request language", tr.Language)
	}
	if len(tr.Segments) != 2 {
		t.Fatalf("segments = %d, want 2 (blank dropped): %+v", len(tr.Segments), tr.Segments)
	}
	s0, s1 := tr.Segments[0], tr.Segments[1]
	if s0.ID != 0 || s0.Start != 0.5 || s0.End != 4.25 || s0.Text != "hei og velkommen" {
		t.Errorf("seg0 = %+v", s0)
	}
	if s1.ID != 1 || s1.Start != 6.1 || s1.Text != "til møtet" {
		t.Errorf("seg1 = %+v", s1)
	}
	if tr.Text != "hei og velkommen til møtet" {
		t.Errorf("Text = %q", tr.Text)
	}
}

func TestTranscribeOmitsUnknownLang(t *testing.T) {
	for _, lang := range []string{"", "auto", "nn", "xx"} {
		t.Run(lang, func(t *testing.T) {
			var hasLang bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.ParseMultipartForm(1 << 20)
				_, hasLang = r.MultipartForm.Value["lang"]
				io.WriteString(w, `{"segments":[]}`)
			}))
			defer srv.Close()
			_, err := New(Config{BaseURL: srv.URL}).Transcribe(context.Background(), transcriber.Request{
				InputPath: writeInput(t, "x"), Language: lang,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if hasLang {
				t.Errorf("lang field sent for %q", lang)
			}
		})
	}
}

func TestTranscribeErrorIncludesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not loaded", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	_, err := New(Config{BaseURL: srv.URL}).Transcribe(context.Background(), transcriber.Request{
		InputPath: writeInput(t, "x"),
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "model not loaded") {
		t.Fatalf("err = %v", err)
	}
}

func TestTranscribeCancel(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := New(Config{BaseURL: srv.URL}).Transcribe(ctx, transcriber.Request{
		InputPath: writeInput(t, "x"),
	}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestTranscribeMissingInput(t *testing.T) {
	_, err := New(Config{BaseURL: "http://127.0.0.1:1"}).Transcribe(context.Background(), transcriber.Request{
		InputPath: filepath.Join(t.TempDir(), "nope.wav"),
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "open input") {
		t.Fatalf("err = %v", err)
	}
}
