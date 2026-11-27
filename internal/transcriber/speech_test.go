package transcriber

import "testing"

func TestHasSpeech(t *testing.T) {
	seg := func(texts ...string) *Transcription {
		tr := &Transcription{}
		for _, s := range texts {
			tr.Segments = append(tr.Segments, Segment{Text: s})
		}
		return tr
	}
	cases := []struct {
		name string
		tr   *Transcription
		want bool
	}{
		{"nil", nil, false},
		{"no segments, no text", &Transcription{}, false},
		{"no segments, text only", &Transcription{Text: "Hei"}, true},
		{"blank segments", seg("", "  "), false},
		{"markers only", seg("[BLANK_AUDIO]", "(music)", " ♪ ♪ ", "[Musikk]"), false},
		{"punctuation only", seg("...", "-"), false},
		{"real speech", seg("[MUSIC]", "Velkommen til møtet."), true},
		{"speech around a marker", seg("(laughs) Ja."), true},
		{"digits count", seg("2024"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tr.HasSpeech(); got != tc.want {
				t.Errorf("HasSpeech() = %v, want %v", got, tc.want)
			}
		})
	}
}
