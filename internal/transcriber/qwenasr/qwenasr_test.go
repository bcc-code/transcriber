package qwenasr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"transcriber/internal/transcriber"
)

func TestTranscribe(t *testing.T) {
	var got request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transcribe" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(`{"model":"m","language":"","segments":[
			{"start":1,"end":5,"text":" Hei  på deg "},
			{"start":6,"end":7,"text":"   "},
			{"start":8,"end":10,"text":"Brunstad."}]}`))
	}))
	defer srv.Close()

	a := New(Config{ID: "q", URL: srv.URL + "/", DefaultLanguage: "no"})
	res, err := a.Transcribe(context.Background(), transcriber.Request{InputPath: "/a.wav", Prompt: "Brunstad"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.AudioPath != "/a.wav" || got.Language != "auto" || got.Context != "Brunstad" {
		t.Errorf("request = %+v", got)
	}
	tr := res.Transcription
	if tr.Text != "Hei på deg Brunstad." {
		t.Errorf("text = %q", tr.Text)
	}
	if tr.Language != "no" {
		t.Errorf("language = %q, want default no", tr.Language)
	}
	if len(tr.Segments) != 2 {
		t.Fatalf("segments = %d, want 2 (blank one dropped)", len(tr.Segments))
	}
	ws := tr.Segments[0].Words
	if len(ws) != 3 || ws[0].Start != 1 || ws[2].End != 5 {
		t.Fatalf("words = %+v", ws)
	}
	for i := 1; i < len(ws); i++ {
		if ws[i].Start < ws[i-1].End || ws[i].End <= ws[i].Start {
			t.Errorf("word %d not ordered: %+v", i, ws)
		}
	}
}

func TestTranscribeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()
	_, err := New(Config{ID: "q", URL: srv.URL}).Transcribe(context.Background(), transcriber.Request{Language: "no"}, nil)
	if err == nil || err.Error() != "qwen-asr: status 500: boom" {
		t.Fatalf("err = %v", err)
	}
}
