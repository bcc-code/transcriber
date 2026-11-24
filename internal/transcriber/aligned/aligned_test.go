package aligned

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"transcriber/internal/transcriber"
)

type fakeInner struct{ tr *transcriber.Transcription }

func (f fakeInner) ID() string   { return "fake" }
func (f fakeInner) Name() string { return "Fake" }
func (f fakeInner) Transcribe(_ context.Context, _ transcriber.Request, p transcriber.ProgressFunc) (*transcriber.Result, error) {
	p(1)
	return &transcriber.Result{Transcription: f.tr}, nil
}

func sample() *transcriber.Transcription {
	return &transcriber.Transcription{
		Language: "no",
		Segments: []transcriber.Segment{{
			Start: 9.25, End: 16.87, Text: "kapittel 6: Vær nidkjær",
			Words: []transcriber.Word{
				{Text: "kapittel", Start: 9.25, End: 9.77},
				{Text: "6:", Start: 9.77, End: 10.01},
				{Text: "Vær", Start: 10.09, End: 10.33},
				{Text: "nidkjær", Start: 10.33, End: 16.87},
			},
		}},
	}
}

func TestTranscribeAppliesAlignment(t *testing.T) {
	var got alignRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/align" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"model":"m","segments":[{"words":[
			{"start":9.26,"end":9.72}, null, {"start":16.64,"end":16.75}, {"start":16.80,"end":17.36}]}]}`))
	}))
	defer srv.Close()

	a := New(fakeInner{sample()}, Config{URL: srv.URL + "/"})
	res, err := a.Transcribe(context.Background(), transcriber.Request{InputPath: "/in.wav", Language: "auto"}, func(float64) {})
	if err != nil {
		t.Fatal(err)
	}
	if got.AudioPath != "/in.wav" || got.Language != "no" || len(got.Segments[0].Words) != 4 {
		t.Errorf("request = %+v (language should fall back to the detected one)", got)
	}
	seg := res.Transcription.Segments[0]
	want := []transcriber.Word{
		{Text: "kapittel", Start: 9.26, End: 9.72},
		{Text: "6:", Start: 9.77, End: 10.01}, // unaligned: keeps model time
		{Text: "Vær", Start: 16.64, End: 16.75},
		{Text: "nidkjær", Start: 16.80, End: 17.36},
	}
	for i, w := range want {
		if seg.Words[i] != w {
			t.Errorf("word %d = %+v, want %+v", i, seg.Words[i], w)
		}
	}
	if seg.Start != 9.26 || seg.End != 17.36 {
		t.Errorf("segment = %.2f–%.2f, want 9.26–17.36", seg.Start, seg.End)
	}
}

func TestUnalignedWordClampedBetweenNeighbours(t *testing.T) {
	tr := sample()
	out := alignResponse{}
	out.Segments = []struct {
		Words []*alignTime `json:"words"`
	}{{Words: []*alignTime{{9.26, 9.90}, nil, {9.95, 10.2}, {10.3, 10.9}}}}
	apply(tr, out)
	w := tr.Segments[0].Words[1]
	if w.Start != 9.90 || w.End != 9.95 {
		t.Errorf("unaligned word = %.2f–%.2f, want 9.90–9.95", w.Start, w.End)
	}
}

func TestAlignerFailureKeepsResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"no alignment model for language 'xx'"}`))
	}))
	defer srv.Close()

	a := New(fakeInner{sample()}, Config{URL: srv.URL})
	res, err := a.Transcribe(context.Background(), transcriber.Request{Language: "xx"}, func(float64) {})
	if err != nil {
		t.Fatal(err)
	}
	if w := res.Transcription.Segments[0].Words[2]; w.Start != 10.09 {
		t.Errorf("word moved despite aligner failure: %+v", w)
	}
}
