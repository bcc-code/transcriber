package whispercpp

import (
	"math"
	"strings"
	"testing"
)

// Log lines and token offsets from a real whisper-cli 1.9.6 run with --vad on
// synthetic Norwegian speech with pauses at 2.6–6.7s, 10.4–16.5s and 20.1–23.3s.
const vadLog = `whisper_vad: vad_segment_info: orig_start: 0.00, orig_end: 2.62, vad_start: 0.00, vad_end: 2.62
whisper_vad: vad_segment_info: orig_start: 6.75, orig_end: 9.05, vad_start: 2.82, vad_end: 5.12
whisper_vad: vad_segment_info: orig_start: 9.25, orig_end: 10.40, vad_start: 5.32, vad_end: 6.47
whisper_vad: vad_segment_info: orig_start: 16.58, orig_end: 18.46, vad_start: 6.67, vad_end: 8.55
whisper_vad: vad_segment_info: orig_start: 18.69, orig_end: 19.77, vad_start: 8.75, vad_end: 9.83
whisper_vad: vad_segment_info: orig_start: 23.62, orig_end: 24.93, vad_start: 10.03, vad_end: 11.31`

func testVADMap(t *testing.T) vadMap {
	t.Helper()
	var m vadMap
	for _, line := range strings.Split(vadLog, "\n") {
		s, ok := parseVADSpan(line)
		if !ok {
			t.Fatalf("parseVADSpan(%q) failed", line)
		}
		m = append(m, s)
	}
	return m
}

func TestVADMapToOriginal(t *testing.T) {
	m := testVADMap(t)
	cases := []struct{ vad, want float64 }{
		{0.02, 0.02},   // first segment: no shift
		{2.72, 2.72},   // overlap tail of real audio after segment 0
		{2.80, 6.75},   // late in the inserted silence: next segment start
		{2.83, 6.76},   // " Vi"
		{6.16, 10.09},  // " V(ær)" in segment 2
		{6.40, 10.33},  // still real audio in segment 2
		{6.65, 16.58},  // late in the gap after segment 2 → segment 3 start
		{6.58, 10.50},  // early in that gap → end of real audio before it
		{6.96, 16.87},  // " i" (helliggjørelsen)
		{10.06, 23.65}, // " dere"
		{12.00, 25.59}, // past the last segment: plain shift
	}
	for _, c := range cases {
		if got := m.toOriginal(c.vad); math.Abs(got-c.want) > 0.005 {
			t.Errorf("toOriginal(%.2f) = %.2f, want %.2f", c.vad, got, c.want)
		}
	}
	if got := vadMap(nil).toOriginal(3.5); got != 3.5 {
		t.Errorf("empty map toOriginal = %v, want identity", got)
	}
}

func TestParseJSONMapsVADTokens(t *testing.T) {
	// One whisper segment spanning all pauses, as whisper.cpp emits with VAD.
	raw := []byte(`{"result":{"language":"no"},"transcription":[{
		"offsets":{"from":0,"to":24930},
		"text":" i dag. Vi leser",
		"tokens":[
			{"text":"[_BEG_]","offsets":{"from":0,"to":0},"id":50365},
			{"text":" i","offsets":{"from":2210,"to":2280},"id":1},
			{"text":" dag","offsets":{"from":2280,"to":2500},"id":2},
			{"text":".","offsets":{"from":2500,"to":2620},"id":3},
			{"text":" Vi","offsets":{"from":2830,"to":2960},"id":4},
			{"text":" les","offsets":{"from":2960,"to":3200},"id":5},
			{"text":"er","offsets":{"from":3200,"to":3360},"id":6}
		]}]}`)
	tr, err := parseJSON(raw, "no", testVADMap(t))
	if err != nil {
		t.Fatal(err)
	}
	seg := tr.Segments[0]
	want := []struct {
		text       string
		start, end float64
	}{
		{"i", 2.21, 2.28},
		{"dag.", 2.28, 2.62},
		{"Vi", 6.76, 6.89},
		{"leser", 6.89, 7.29},
	}
	if len(seg.Words) != len(want) {
		t.Fatalf("words = %+v", seg.Words)
	}
	for i, w := range want {
		g := seg.Words[i]
		if g.Text != w.text || math.Abs(g.Start-w.start) > 0.005 || math.Abs(g.End-w.end) > 0.005 {
			t.Errorf("word %d = %q %.2f–%.2f, want %q %.2f–%.2f", i, g.Text, g.Start, g.End, w.text, w.start, w.end)
		}
	}
	if math.Abs(seg.Start-2.21) > 0.005 || math.Abs(seg.End-7.29) > 0.005 {
		t.Errorf("segment = %.2f–%.2f, want 2.21–7.29", seg.Start, seg.End)
	}
}
