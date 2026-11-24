package whispercpp

import (
	"regexp"
	"strconv"
)

// With --vad, whisper.cpp transcribes a shortened buffer: each speech
// segment, followed by vadOverlapSec of the real audio after it, then
// vadSilenceSec of zeros before the next segment. Segment offsets in the
// JSON are mapped back to the original timeline, but token offsets are not:
// they stay in the shortened timeline. whisper-cli logs every segment's
// position in both timelines, which is enough to map tokens back exactly.
//
// Buffer layout from whisper.cpp v1.8.6 src/whisper.cpp (whisper_vad).
const (
	vadOverlapSec = 0.10 // -vo / vad_samples_overlap default; we never pass -vo
	vadSilenceSec = 0.10 // hardcoded in whisper.cpp
)

// vadSpan is one speech segment's position in both timelines, in seconds.
type vadSpan struct {
	OrigStart, OrigEnd float64
	VADStart, VADEnd   float64
}

// vadMap maps times in the shortened VAD buffer back to the original audio.
// A nil or empty map means VAD was off (or not logged), and nothing is mapped.
type vadMap []vadSpan

var vadSpanRe = regexp.MustCompile(`vad_segment_info: orig_start: ([\d.]+), orig_end: ([\d.]+), vad_start: ([\d.]+), vad_end: ([\d.]+)`)

// parseVADSpan reads one "vad_segment_info" log line from whisper-cli.
func parseVADSpan(line string) (vadSpan, bool) {
	m := vadSpanRe.FindStringSubmatch(line)
	if m == nil {
		return vadSpan{}, false
	}
	var f [4]float64
	for i := range f {
		v, err := strconv.ParseFloat(m[i+1], 64)
		if err != nil {
			return vadSpan{}, false
		}
		f[i] = v
	}
	return vadSpan{OrigStart: f[0], OrigEnd: f[1], VADStart: f[2], VADEnd: f[3]}, true
}

// toOriginal maps a time in the VAD buffer to the original timeline.
// Inside a segment (and its real-audio overlap tail) the mapping is a plain
// shift. A time inside the inserted silence snaps to whichever side is nearer:
// the end of the real audio before it, or the start of the next segment.
func (m vadMap) toOriginal(t float64) float64 {
	if len(m) == 0 {
		return t
	}
	i := 0
	for i+1 < len(m) && m[i+1].VADStart <= t {
		i++
	}
	s := m[i]
	if t <= s.VADStart {
		return s.OrigStart
	}
	d := t - s.VADStart
	realLen := s.VADEnd - s.VADStart
	if i+1 < len(m) {
		realLen += vadOverlapSec
	}
	if d <= realLen || i+1 == len(m) {
		return s.OrigStart + d
	}
	// In the zero-filled gap between this segment and the next.
	gapStart := s.VADStart + realLen
	if t-gapStart < (m[i+1].VADStart-gapStart)/2 {
		return s.OrigStart + realLen
	}
	return m[i+1].OrigStart
}
