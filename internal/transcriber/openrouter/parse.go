package openrouter

import (
	"encoding/json"
	"fmt"
	"strings"

	"transcriber/internal/transcriber"
)

// rawResponse is the OpenAI-style verbose_json body. Providers that only
// support plain json return just Text.
type rawResponse struct {
	Text     string       `json:"text"`
	Language string       `json:"language"`
	Duration float64      `json:"duration"`
	Segments []rawSegment `json:"segments"`
	Words    []rawWord    `json:"words"`
}

type rawSegment struct {
	ID               int     `json:"id"`
	Seek             int     `json:"seek"`
	Start            float64 `json:"start"`
	End              float64 `json:"end"`
	Text             string  `json:"text"`
	Tokens           []int   `json:"tokens"`
	Temperature      float64 `json:"temperature"`
	AvgLogprob       float64 `json:"avg_logprob"`
	CompressionRatio float64 `json:"compression_ratio"`
	NoSpeechProb     float64 `json:"no_speech_prob"`
}

type rawWord struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

func parseResponse(data []byte, fallbackLang string) (*transcriber.Transcription, error) {
	var raw rawResponse
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("openrouter parse: %w", err)
	}

	lang := languageCode(raw.Language)
	if lang == "" && fallbackLang != "auto" {
		lang = fallbackLang
	}

	words := make([]rawWord, 0, len(raw.Words))
	for _, w := range raw.Words {
		w.Word = strings.TrimSpace(w.Word)
		if w.Word != "" {
			words = append(words, w)
		}
	}

	var segs []transcriber.Segment
	if len(raw.Segments) > 0 {
		segs = fromSegments(raw.Segments, words)
	} else {
		segs = fromWords(words)
	}
	// Plain-json providers give only text; keep it as one untimed segment so
	// the transcript is not lost.
	if len(segs) == 0 && strings.TrimSpace(raw.Text) != "" {
		segs = []transcriber.Segment{{Text: strings.TrimSpace(raw.Text), End: raw.Duration}}
	}
	if segs == nil {
		segs = []transcriber.Segment{}
	}

	text := strings.TrimSpace(raw.Text)
	if text == "" {
		parts := make([]string, len(segs))
		for i, s := range segs {
			parts[i] = s.Text
		}
		text = strings.Join(parts, " ")
	}
	return &transcriber.Transcription{Text: text, Segments: segs, Language: lang}, nil
}

// fromSegments keeps the provider's segmentation and attaches each word to
// the segment containing its midpoint (words come back as a separate list).
func fromSegments(raw []rawSegment, words []rawWord) []transcriber.Segment {
	segs := make([]transcriber.Segment, 0, len(raw))
	wi := 0
	for i, r := range raw {
		text := strings.TrimSpace(r.Text)
		if text == "" {
			continue
		}
		s := transcriber.Segment{
			ID:               len(segs),
			Text:             text,
			Start:            r.Start,
			End:              r.End,
			Seek:             r.Seek,
			Tokens:           r.Tokens,
			Temperature:      r.Temperature,
			AvgLogprob:       r.AvgLogprob,
			CompressionRatio: r.CompressionRatio,
			NoSpeechProb:     r.NoSpeechProb,
		}
		last := i == len(raw)-1
		for wi < len(words) {
			w := words[wi]
			if !last && (w.Start+w.End)/2 >= r.End {
				break
			}
			s.Words = append(s.Words, transcriber.Word{Text: w.Word, Start: w.Start, End: w.End})
			wi++
		}
		segs = append(segs, s)
	}
	return segs
}

const (
	pauseBreakSec = 1.0
	maxSegSec     = 15.0
	maxSegWords   = 40
)

// fromWords builds segments from a flat word list for providers that return
// words but no segments.
func fromWords(words []rawWord) []transcriber.Segment {
	var segs []transcriber.Segment
	var cur []rawWord
	flush := func() {
		if len(cur) == 0 {
			return
		}
		texts := make([]string, len(cur))
		ws := make([]transcriber.Word, len(cur))
		for i, w := range cur {
			texts[i] = w.Word
			ws[i] = transcriber.Word{Text: w.Word, Start: w.Start, End: w.End}
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
	for _, w := range words {
		if len(cur) > 0 {
			prev := cur[len(cur)-1]
			if w.Start-prev.End >= pauseBreakSec || w.End-cur[0].Start > maxSegSec || len(cur) >= maxSegWords {
				flush()
			}
		}
		cur = append(cur, w)
		if strings.ContainsAny(w.Word[len(w.Word)-1:], ".?!") {
			flush()
		}
	}
	flush()
	return segs
}

// languageCode normalises the language field: OpenAI's whisper returns
// lowercase English names ("norwegian"), other providers return codes.
func languageCode(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	if c, ok := languageNames[s]; ok {
		return c
	}
	if i := strings.IndexAny(s, "-_"); i > 0 { // "en-us" -> "en"
		s = s[:i]
	}
	if len(s) <= 3 {
		return s
	}
	return ""
}

// languageNames maps whisper's language names to ISO 639-1 codes.
var languageNames = map[string]string{
	"english": "en", "chinese": "zh", "german": "de", "spanish": "es",
	"russian": "ru", "korean": "ko", "french": "fr", "japanese": "ja",
	"portuguese": "pt", "turkish": "tr", "polish": "pl", "catalan": "ca",
	"dutch": "nl", "arabic": "ar", "swedish": "sv", "italian": "it",
	"indonesian": "id", "hindi": "hi", "finnish": "fi", "vietnamese": "vi",
	"hebrew": "he", "ukrainian": "uk", "greek": "el", "malay": "ms",
	"czech": "cs", "romanian": "ro", "danish": "da", "hungarian": "hu",
	"tamil": "ta", "norwegian": "no", "thai": "th", "urdu": "ur",
	"croatian": "hr", "bulgarian": "bg", "lithuanian": "lt", "latin": "la",
	"maori": "mi", "malayalam": "ml", "welsh": "cy", "slovak": "sk",
	"telugu": "te", "persian": "fa", "latvian": "lv", "bengali": "bn",
	"serbian": "sr", "azerbaijani": "az", "slovenian": "sl", "kannada": "kn",
	"estonian": "et", "macedonian": "mk", "breton": "br", "basque": "eu",
	"icelandic": "is", "armenian": "hy", "nepali": "ne", "mongolian": "mn",
	"bosnian": "bs", "kazakh": "kk", "albanian": "sq", "swahili": "sw",
	"galician": "gl", "marathi": "mr", "punjabi": "pa", "sinhala": "si",
	"khmer": "km", "shona": "sn", "yoruba": "yo", "somali": "so",
	"afrikaans": "af", "occitan": "oc", "georgian": "ka", "belarusian": "be",
	"tajik": "tg", "sindhi": "sd", "gujarati": "gu", "amharic": "am",
	"yiddish": "yi", "lao": "lo", "uzbek": "uz", "faroese": "fo",
	"haitian creole": "ht", "pashto": "ps", "turkmen": "tk", "nynorsk": "nn",
	"maltese": "mt", "sanskrit": "sa", "luxembourgish": "lb", "myanmar": "my",
	"tibetan": "bo", "tagalog": "tl", "malagasy": "mg", "assamese": "as",
	"tatar": "tt", "hawaiian": "haw", "lingala": "ln", "hausa": "ha",
	"bashkir": "ba", "javanese": "jw", "sundanese": "su", "cantonese": "yue",
	"norwegian bokmål": "no", "bokmål": "no", "norwegian nynorsk": "nn",
}
