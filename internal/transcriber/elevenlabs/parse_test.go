package elevenlabs

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"transcriber/internal/formats"
)

var goldenDir = filepath.Join("..", "..", "formats", "testdata", "golden")

func TestParseResponseMatchesGoldens(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "raw.json"))
	if err != nil {
		t.Fatal(err)
	}
	tr, err := parseResponse(raw, "")
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	cases := []struct{ format, golden string }{
		{formats.JSON, "transcript.json"},
		{formats.SRT, "transcript.srt"},
		{formats.VTT, "transcript.vtt"},
		{formats.TXT, "transcript.txt"},
		{formats.WordsSRT, "transcript.words.srt"},
	}
	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			path, err := formats.Write(tc.format, tr, t.TempDir(), "transcript", formats.SubtitleOptions{})
			if err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(path)
			want, err := os.ReadFile(filepath.Join(goldenDir, tc.golden))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

func w(text string, start, end float64) rawWord {
	return rawWord{Text: text, Start: start, End: end, Type: "word"}
}

func segTexts(t *testing.T, words []rawWord) []string {
	t.Helper()
	var out []string
	for _, s := range segment(words) {
		out = append(out, s.Text)
	}
	return out
}

func TestSegmentPauseBreak(t *testing.T) {
	got := segTexts(t, []rawWord{w("one", 0, 0.5), w("two", 0.5, 1), w("three", 2.5, 3)})
	if strings.Join(got, "|") != "one two|three" {
		t.Errorf("got %q", got)
	}
}

func TestSegmentSpeakerChange(t *testing.T) {
	a, b := w("hi", 0, 0.3), w("there", 0.3, 0.6)
	a.SpeakerID, b.SpeakerID = "speaker_0", "speaker_1"
	got := segTexts(t, []rawWord{a, b})
	if strings.Join(got, "|") != "hi|there" {
		t.Errorf("got %q", got)
	}
}

func TestSegmentShortSentenceNotSplit(t *testing.T) {
	// "Ja." is under minSentenceSegSec, so it stays with the next words.
	got := segTexts(t, []rawWord{w("Ja.", 0, 0.3), w("Det", 0.3, 0.5), w("stemmer.", 0.5, 1.2)})
	if strings.Join(got, "|") != "Ja. Det stemmer." {
		t.Errorf("got %q", got)
	}
}

func TestSegmentMaxWords(t *testing.T) {
	var words []rawWord
	for i := 0; i < maxSegWords+5; i++ {
		words = append(words, w("x", float64(i)*0.1, float64(i)*0.1+0.1))
	}
	segs := segment(words)
	if len(segs) != 2 || len(segs[0].Words) != maxSegWords {
		t.Fatalf("got %d segs, first has %d words", len(segs), len(segs[0].Words))
	}
}

func TestSegmentMaxDuration(t *testing.T) {
	var words []rawWord
	for i := 0; i < 20; i++ {
		words = append(words, w("x", float64(i), float64(i)+0.9))
	}
	for _, s := range segment(words) {
		if s.End-s.Start > maxSegSec {
			t.Errorf("segment %d spans %.1fs", s.ID, s.End-s.Start)
		}
	}
}

func TestParseDropsAudioEventsAndHandlesEmpty(t *testing.T) {
	tr, err := parseResponse([]byte(`{"language_code":"nor","text":"","words":[
		{"text":"(music)","start":0,"end":5,"type":"audio_event"},
		{"text":"Hei.","start":5,"end":6,"type":"word"}]}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Language != "no" || len(tr.Segments) != 1 || tr.Segments[0].Text != "Hei." || tr.Text != "Hei." {
		t.Errorf("got %+v", tr)
	}

	tr, err = parseResponse([]byte(`{"language_code":"","text":"","words":[]}`), "no")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Language != "no" || len(tr.Segments) != 0 {
		t.Errorf("got %+v", tr)
	}
}

func TestKeyterms(t *testing.T) {
	got := keyterms("BCC, Brunstad\nSamvirk; bcc,  ,This is a long prose sentence that should not become a keyterm at all.")
	if strings.Join(got, "|") != "BCC|Brunstad|Samvirk" {
		t.Errorf("got %q", got)
	}
}
