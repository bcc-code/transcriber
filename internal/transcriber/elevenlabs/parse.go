package elevenlabs

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"transcriber/internal/transcriber"
)

// Segmentation thresholds. Scribe returns a flat word list, so segments are
// built here: break at sentence ends, long pauses, speaker changes, or when a
// segment grows too long for a readable subtitle source.
const (
	minSentenceSegSec = 1.0
	pauseBreakSec     = 1.0
	maxSegSec         = 15.0
	maxSegWords       = 40
)

type rawResponse struct {
	LanguageCode string    `json:"language_code"`
	Text         string    `json:"text"`
	Words        []rawWord `json:"words"`
}

type rawWord struct {
	Text      string  `json:"text"`
	Start     float64 `json:"start"`
	End       float64 `json:"end"`
	Type      string  `json:"type"`
	SpeakerID string  `json:"speaker_id"`
}

func parseResponse(data []byte, fallbackLang string) (*transcriber.Transcription, error) {
	var raw rawResponse
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("elevenlabs parse: %w", err)
	}

	lang := toISO6391(raw.LanguageCode)
	if lang == "" && fallbackLang != "auto" {
		lang = fallbackLang
	}

	words := make([]rawWord, 0, len(raw.Words))
	for _, w := range raw.Words {
		if w.Type != "" && w.Type != "word" {
			continue // spacing, audio_event
		}
		w.Text = strings.TrimSpace(w.Text)
		if w.Text == "" {
			continue
		}
		words = append(words, w)
	}

	segs := segment(words)
	text := strings.TrimSpace(raw.Text)
	if text == "" {
		parts := make([]string, len(segs))
		for i, s := range segs {
			parts[i] = s.Text
		}
		text = strings.Join(parts, " ")
	}
	return &transcriber.Transcription{
		Text:     text,
		Segments: segs,
		Language: lang,
	}, nil
}

func segment(words []rawWord) []transcriber.Segment {
	segs := []transcriber.Segment{}
	var cur []rawWord
	flush := func() {
		if len(cur) == 0 {
			return
		}
		texts := make([]string, len(cur))
		ws := make([]transcriber.Word, len(cur))
		for i, w := range cur {
			texts[i] = w.Text
			ws[i] = transcriber.Word{Text: w.Text, Start: w.Start, End: w.End}
		}
		segs = append(segs, transcriber.Segment{
			ID:    len(segs),
			Text:  strings.Join(texts, " "),
			Start: cur[0].Start,
			End:   cur[len(cur)-1].End,
			Words: ws,
		})
		cur = nil
	}

	for i, w := range words {
		if len(cur) > 0 {
			prev := cur[len(cur)-1]
			if w.Start-prev.End >= pauseBreakSec ||
				(w.SpeakerID != "" && prev.SpeakerID != "" && w.SpeakerID != prev.SpeakerID) ||
				w.End-cur[0].Start > maxSegSec ||
				len(cur) >= maxSegWords {
				flush()
			}
		}
		cur = append(cur, w)
		if endsSentence(w.Text) && w.End-cur[0].Start >= minSentenceSegSec && i < len(words)-1 {
			flush()
		}
	}
	flush()
	return segs
}

func endsSentence(s string) bool {
	s = strings.TrimRight(s, `"'”’»)]`)
	r, _ := utf8.DecodeLastRuneInString(s)
	switch r {
	case '.', '?', '!', '…':
		return true
	}
	return false
}
