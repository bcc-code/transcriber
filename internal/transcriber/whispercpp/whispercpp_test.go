package whispercpp

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"transcriber/internal/formats"
)

// goldenDir is shared with internal/formats; parsed whisper-cli output must round-trip byte-identical.
var goldenDir = filepath.Join("..", "..", "formats", "testdata", "golden")

func TestParseJSONMatchesGoldens(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "raw.json"))
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	tr, err := parseJSON(raw, "", nil)
	if err != nil {
		t.Fatalf("parseJSON: %v", err)
	}

	cases := []struct {
		name   string
		format string
		golden string
	}{
		{"json", formats.JSON, "transcript.json"},
		{"srt", formats.SRT, "transcript.srt"},
		{"vtt", formats.VTT, "transcript.vtt"},
		{"txt", formats.TXT, "transcript.txt"},
		{"words.srt", formats.WordsSRT, "transcript.words.srt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			path, err := formats.Write(tc.format, tr, tmp, "transcript", formats.SubtitleOptions{})
			if err != nil {
				t.Fatalf("Write(%s): %v", tc.format, err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read output: %v", err)
			}
			want, err := os.ReadFile(filepath.Join(goldenDir, tc.golden))
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("output mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", tc.format, got, want)
			}
		})
	}
}

func TestParseJSONLanguageFallback(t *testing.T) {
	raw := `{"result": {"language": ""}, "transcription": []}`
	tr, err := parseJSON([]byte(raw), "no", nil)
	if err != nil {
		t.Fatalf("parseJSON: %v", err)
	}
	if tr.Language != "no" {
		t.Errorf("language = %q, want fallback %q", tr.Language, "no")
	}
}

func TestTokensToWordsEmpty(t *testing.T) {
	if got := tokensToWords(nil, 0, 0); got != nil {
		t.Errorf("tokensToWords(nil) = %v, want nil", got)
	}
}

// With VAD on, segment offsets are in original-audio time but token offsets
// are in VAD-compressed time. tokensToWords must remap them.
func TestTokensToWordsRemapsVADCompressedOffsets(t *testing.T) {
	mkTok := func(text string, fromMS, toMS int) rawToken {
		return rawToken{Text: rawString(text), ID: 1, Offsets: struct {
			From int `json:"from"`
			To   int `json:"to"`
		}{From: fromMS, To: toMS}}
	}
	// Segment 19.65–23.05 (original); first content token at 30ms (VAD).
	tokens := []rawToken{
		mkTok("[_BEG_]", 0, 0),
		mkTok(" Det", 30, 200),
		mkTok(" er", 200, 350),
		mkTok(" blitt", 350, 700),
	}
	tokens[0].ID = firstSpecialTokenID + 1 // skip the special begin token
	words := tokensToWords(tokens, 19.65, 23.05)
	if len(words) != 3 {
		t.Fatalf("words = %d, want 3 (%+v)", len(words), words)
	}
	// delta = 19.65 - 0.030 = 19.62; applied to every token.
	const eps = 0.001
	approx := func(a, b float64) bool { return a-b < eps && b-a < eps }
	if !approx(words[0].Start, 19.65) {
		t.Errorf("words[0].Start = %.3f, want 19.650", words[0].Start)
	}
	if !approx(words[1].Start, 19.82) {
		t.Errorf("words[1].Start = %.3f, want 19.820 (0.200 + 19.62)", words[1].Start)
	}
	if !approx(words[2].End, 20.32) {
		t.Errorf("words[2].End = %.3f, want 20.320 (0.700 + 19.62)", words[2].End)
	}
}

// BPE may split a codepoint across tokens ("å" = 0xC3 0xA5); rawString
// must keep the raw bytes so concatenation reassembles it.
func TestParseJSONReconstructsSplitUTF8Tokens(t *testing.T) {
	raw := []byte("{" +
		"\"result\":{\"language\":\"no\"}," +
		"\"transcription\":[{" +
		"\"offsets\":{\"from\":0,\"to\":1000}," +
		"\"text\":\" Så\"," +
		"\"tokens\":[" +
		"{\"text\":\" S\",\"offsets\":{\"from\":0,\"to\":200},\"id\":1}," +
		"{\"text\":\"\xC3\",\"offsets\":{\"from\":200,\"to\":400},\"id\":2}," +
		"{\"text\":\"\xA5\",\"offsets\":{\"from\":400,\"to\":600},\"id\":3}" +
		"]" +
		"}]" +
		"}")

	tr, err := parseJSON(raw, "no", nil)
	if err != nil {
		t.Fatalf("parseJSON: %v", err)
	}
	if len(tr.Segments) != 1 {
		t.Fatalf("segments = %d, want 1", len(tr.Segments))
	}
	if got := tr.Segments[0].Text; got != "Så" {
		t.Errorf("segment text = %q, want %q", got, "Så")
	}
	words := tr.Segments[0].Words
	if len(words) != 1 {
		t.Fatalf("words = %d (%+v), want 1", len(words), words)
	}
	if words[0].Text != "Så" {
		t.Errorf("word text = %q (bytes %x), want %q", words[0].Text, []byte(words[0].Text), "Så")
	}
}
