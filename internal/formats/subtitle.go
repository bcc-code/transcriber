package formats

import (
	"math"
	"strings"

	"transcriber/internal/transcriber"
)

// SubtitleOptions tunes cue splitting; zero values use broadcast defaults
// (≤42 chars, ≤2 lines).
type SubtitleOptions struct {
	MaxCharsPerLine int `json:"max_chars_per_line,omitempty"`
	MaxLinesPerCue  int `json:"max_lines_per_cue,omitempty"`
}

func (o SubtitleOptions) normalized() SubtitleOptions {
	if o.MaxCharsPerLine <= 0 {
		o.MaxCharsPerLine = 42
	}
	if o.MaxLinesPerCue <= 0 {
		o.MaxLinesPerCue = 2
	}
	return o
}

type Cue struct {
	Start float64
	End   float64
	Lines []string
}

type cueWord struct {
	text       string
	start, end float64
}

// BuildCues splits segments into cues using word timestamps; segments
// without words emit one cue each.
func BuildCues(t *transcriber.Transcription, opts SubtitleOptions) []Cue {
	opts = opts.normalized()

	var cues []Cue
	for _, seg := range t.Segments {
		if len(seg.Words) == 0 {
			cues = append(cues, Cue{
				Start: seg.Start,
				End:   seg.End,
				Lines: []string{strings.TrimSpace(seg.Text)},
			})
			continue
		}
		cues = append(cues, splitSegment(seg, opts)...)
	}
	extendCueEnds(cues, cueTailPad)
	return cues
}

// cueTailPad is how long a cue may linger after its last word. Forced
// alignment puts word ends right on the acoustic offset, which reads as the
// subtitle vanishing too early.
const cueTailPad = 0.8

// extendCueEnds pushes each cue's end out by up to pad seconds, never past
// the start of the next cue.
func extendCueEnds(cues []Cue, pad float64) {
	for i := range cues {
		end := cues[i].End + pad
		if i+1 < len(cues) {
			end = math.Min(end, cues[i+1].Start)
		}
		cues[i].End = math.Max(cues[i].End, end)
	}
}

func splitSegment(seg transcriber.Segment, opts SubtitleOptions) []Cue {
	ws := make([]cueWord, 0, len(seg.Words))
	for _, w := range seg.Words {
		text := strings.TrimSpace(w.Text)
		if text == "" {
			continue
		}
		ws = append(ws, cueWord{text: text, start: w.Start, end: w.End})
	}
	if len(ws) == 0 {
		return nil
	}

	var cues []Cue
	i := 0
	for i < len(ws) {
		maxJ := i
		for j := i; j < len(ws); j++ {
			if tryWrap(ws[i:j+1], opts.MaxLinesPerCue, opts.MaxCharsPerLine) != nil {
				maxJ = j
			} else {
				break
			}
		}
		end := maxJ

		// Prefer breaking at a sentence end; if none, fall back to clause
		// only when the tail would orphan. Either path keeps the cue substantial.
		if maxJ+1 < len(ws) {
			if k := lastTerminalIdx(ws[i : maxJ+1]); k > 0 && cueSubstantial(ws[i:i+k+1]) {
				end = i + k
			} else if isOrphan(ws[maxJ+1:]) {
				if k := lastClauseIdx(ws[i : maxJ+1]); k > 0 && cueSubstantial(ws[i:i+k+1]) {
					end = i + k
				}
			}
		}

		cueWords := ws[i : end+1]
		cues = append(cues, Cue{
			Start: cueWords[0].start,
			End:   cueWords[len(cueWords)-1].end,
			Lines: wrapCue(cueWords, opts.MaxLinesPerCue, opts.MaxCharsPerLine),
		})
		i = end + 1
	}
	return cues
}

// tryWrap returns wrapped lines or nil if the words don't fit. Single-word
// lines may exceed maxChars (no way to break a single word cleanly).
func tryWrap(words []cueWord, maxLines, maxChars int) []string {
	if len(words) == 0 {
		return nil
	}
	text := joinWords(words)
	if len(text) <= maxChars || len(words) == 1 {
		return []string{text}
	}
	if maxLines >= 2 {
		if lines := tryWrapTwoLines(words, maxChars); lines != nil {
			return lines
		}
	}
	if maxLines > 2 {
		lines := greedyWrap(words, maxChars)
		if len(lines) <= maxLines {
			return lines
		}
	}
	return nil
}

// tryWrapTwoLines picks the most balanced 2-line split, biased toward
// punctuation breaks. Returns nil if no split fits.
func tryWrapTwoLines(words []cueWord, maxChars int) []string {
	bestI := -1
	bestScore := math.MaxInt
	for i := 1; i < len(words); i++ {
		l1 := joinWords(words[:i])
		l2 := joinWords(words[i:])
		if i > 1 && len(l1) > maxChars {
			continue
		}
		if len(words)-i > 1 && len(l2) > maxChars {
			continue
		}
		score := abs(len(l1) - len(l2))
		switch {
		case endsWithSentenceEnd(words[i-1].text):
			score -= 200
		case endsWithClauseEnd(words[i-1].text):
			score -= 100
		}
		if score < bestScore {
			bestScore = score
			bestI = i
		}
	}
	if bestI < 0 {
		return nil
	}
	return []string{joinWords(words[:bestI]), joinWords(words[bestI:])}
}

func wrapCue(words []cueWord, maxLines, maxChars int) []string {
	if lines := tryWrap(words, maxLines, maxChars); lines != nil {
		return lines
	}
	return greedyWrap(words, maxChars)
}

func greedyWrap(words []cueWord, maxChars int) []string {
	var lines []string
	var cur string
	for _, w := range words {
		switch {
		case cur == "":
			cur = w.text
		case len(cur)+1+len(w.text) > maxChars:
			lines = append(lines, cur)
			cur = w.text
		default:
			cur += " " + w.text
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func isOrphan(tail []cueWord) bool {
	if len(tail) > 2 {
		return false
	}
	total := 0
	for _, w := range tail {
		total += len(w.text)
	}
	if len(tail) > 1 {
		total += len(tail) - 1
	}
	return total <= 20
}

func cueSubstantial(words []cueWord) bool {
	if len(words) < 2 {
		return false
	}
	total := len(words) - 1
	for _, w := range words {
		total += len(w.text)
	}
	return total >= 12
}

func lastTerminalIdx(words []cueWord) int {
	for i := len(words) - 2; i >= 0; i-- {
		if endsWithSentenceEnd(words[i].text) {
			return i
		}
	}
	return -1
}

func lastClauseIdx(words []cueWord) int {
	for i := len(words) - 2; i >= 0; i-- {
		if endsWithClauseEnd(words[i].text) {
			return i
		}
	}
	return -1
}

// endsWithSentenceEnd ignores 1–2 letter abbreviation periods ("J.", "Mr.")
// so they don't get treated as sentence boundaries.
func endsWithSentenceEnd(s string) bool {
	if s == "" {
		return false
	}
	rs := []rune(s)
	switch rs[len(rs)-1] {
	case '!', '?':
		return true
	case '.':
		body := rs[:len(rs)-1]
		if len(body) > 0 && len(body) <= 2 && allLetters(body) {
			return false
		}
		return true
	}
	return false
}

func endsWithClauseEnd(s string) bool {
	if s == "" {
		return false
	}
	rs := []rune(s)
	switch rs[len(rs)-1] {
	case ',', ';', ':':
		return true
	}
	return false
}

func allLetters(rs []rune) bool {
	for _, r := range rs {
		switch {
		case 'a' <= r && r <= 'z':
		case 'A' <= r && r <= 'Z':
		case r >= 0x80: // non-ASCII letters (å, ø, etc.)
		default:
			return false
		}
	}
	return true
}

func joinWords(words []cueWord) string {
	parts := make([]string, len(words))
	for i, w := range words {
		parts[i] = w.text
	}
	return strings.Join(parts, " ")
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
