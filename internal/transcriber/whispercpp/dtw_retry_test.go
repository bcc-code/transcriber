package whispercpp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"transcriber/internal/transcriber"
)

// fakeWhisper writes a whisper-cli stand-in that behaves like v1.8.6 on a VAD
// output just over 30 s: with -dtw it aborts on the median-filter assertion,
// without it it writes testdata/raw.json to the -of prefix. Each call's
// arguments are appended to calls.log.
func fakeWhisper(t *testing.T, dir string) string {
	t.Helper()
	raw, err := filepath.Abs(filepath.Join("testdata", "raw.json"))
	if err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
echo "$@" >> "` + dir + `/calls.log"
out=""
while [ $# -gt 0 ]; do
  case "$1" in
    -of) out="$2"; shift ;;
    -dtw) echo "WHISPER_ASSERT: /src/src/whisper.cpp:8793: filter_width < a->ne[2]" >&2; kill -ABRT $$ ;;
  esac
  shift
done
cp "` + raw + `" "$out.json"
`
	bin := filepath.Join(dir, "whisper-cli")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestTranscribeRetriesWithoutDTWOnShortWindowAbort(t *testing.T) {
	dir := t.TempDir()
	a := New(Config{Binary: fakeWhisper(t, dir), ModelFile: "model.bin", DTWPreset: "large.v3"})

	res, err := a.Transcribe(t.Context(), transcriber.Request{
		InputPath: "in.wav",
		WorkDir:   filepath.Join(dir, "work"),
	}, func(float64) {})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if res.Transcription == nil || len(res.Transcription.Segments) == 0 {
		t.Fatal("no transcription from the retry")
	}

	log, err := os.ReadFile(filepath.Join(dir, "calls.log"))
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(calls) != 2 {
		t.Fatalf("whisper-cli ran %d times, want 2: %q", len(calls), calls)
	}
	if !strings.Contains(calls[0], "-dtw large.v3") {
		t.Errorf("first run lacked DTW: %s", calls[0])
	}
	if strings.Contains(calls[1], "-dtw") || strings.Contains(calls[1], "-nfa") {
		t.Errorf("retry still passed DTW flags: %s", calls[1])
	}
}

func TestTranscribeDoesNotRetryOtherFailures(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "whisper-cli")
	script := "#!/bin/sh\necho run >> \"" + dir + "/calls.log\"\necho 'some other failure' >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	a := New(Config{Binary: bin, ModelFile: "model.bin", DTWPreset: "large.v3"})

	_, err := a.Transcribe(t.Context(), transcriber.Request{InputPath: "in.wav", WorkDir: filepath.Join(dir, "work")}, func(float64) {})
	if err == nil || !strings.Contains(err.Error(), "some other failure") {
		t.Fatalf("err = %v, want the whisper-cli failure", err)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	if n := strings.Count(string(log), "run"); n != 1 {
		t.Fatalf("whisper-cli ran %d times, want 1", n)
	}
}
