// Package whispercpp adapts the whisper.cpp CLI to the Transcriber interface.
package whispercpp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"transcriber/internal/procutil"
	"transcriber/internal/transcriber"
)

type Config struct {
	ID          string
	DisplayName string
	Binary      string
	// ModelFile, when set, wins over ResolveModel.
	ModelFile    string
	ResolveModel func(ctx context.Context) (string, error)
	Threads      int

	// DTWPreset (e.g. "large.v3") aligns per-token timestamps to the audio;
	// without it, timestamps drift across long segments.
	DTWPreset string

	// VAD pre-pass; whisper only sees detected speech, avoiding hallucinated
	// timestamps over music/silence. VADModelFile wins over ResolveVADModel.
	VADModelFile    string
	ResolveVADModel func(ctx context.Context) (string, error)

	// NoGPU passes `-ng` to whisper-cli, forcing the CPU backend even when
	// a GPU backend is compiled in. Use this on hosts without a real NVIDIA
	// GPU (e.g. Docker-on-Mac) where the CUDA backend would fail to find a
	// device.
	NoGPU bool
}

type Adapter struct {
	cfg Config
}

func New(cfg Config) *Adapter {
	if cfg.ID == "" {
		cfg.ID = "whisper-cpp"
	}
	if cfg.Threads <= 0 {
		cfg.Threads = 4
	}
	return &Adapter{cfg: cfg}
}

func (a *Adapter) ID() string { return a.cfg.ID }

func (a *Adapter) Name() string {
	if a.cfg.DisplayName != "" {
		return a.cfg.DisplayName
	}
	if a.cfg.ModelFile != "" {
		return "whisper.cpp (" + filepath.Base(a.cfg.ModelFile) + ")"
	}
	return "whisper.cpp"
}

var progressRe = regexp.MustCompile(`progress\s*=\s*(\d+)`)

func (a *Adapter) Transcribe(ctx context.Context, req transcriber.Request, onProgress transcriber.ProgressFunc) (*transcriber.Result, error) {
	if a.cfg.Binary == "" {
		return nil, fmt.Errorf("whispercpp: binary not configured")
	}
	modelPath := a.cfg.ModelFile
	if modelPath == "" {
		if a.cfg.ResolveModel == nil {
			return nil, fmt.Errorf("whispercpp: no model_file and no resolver configured")
		}
		p, err := a.cfg.ResolveModel(ctx)
		if err != nil {
			return nil, fmt.Errorf("whispercpp resolve model: %w", err)
		}
		modelPath = p
	}
	if err := os.MkdirAll(req.WorkDir, 0o755); err != nil {
		return nil, err
	}
	// Raw whisper output is an intermediate: it goes in the scratch dir, not
	// the caller's output_path.
	outPrefix := filepath.Join(req.WorkDir, "whispercpp_out")

	args := []string{
		"-m", modelPath,
		"-f", req.InputPath,
		"-of", outPrefix,
		"-ojf",
		"-t", strconv.Itoa(a.cfg.Threads),
		"-pp",
	}
	if a.cfg.NoGPU {
		args = append(args, "-ng")
	}
	if req.Language != "" && req.Language != "auto" {
		args = append(args, "--language", req.Language)
	}
	if req.Prompt != "" {
		args = append(args, "--prompt", req.Prompt)
	}
	vadPath := a.cfg.VADModelFile
	if vadPath == "" && a.cfg.ResolveVADModel != nil {
		p, err := a.cfg.ResolveVADModel(ctx)
		if err != nil {
			return nil, fmt.Errorf("whispercpp resolve VAD model: %w", err)
		}
		vadPath = p
	}
	if vadPath != "" {
		args = append(args, "--vad", "--vad-model", vadPath)
	}

	start := time.Now()
	var dtwArgs []string
	if a.cfg.DTWPreset != "" {
		// whisper.cpp >= 1.8 enables flash attention by default, and DTW is
		// incompatible with it: whisper-cli logs "dtw_token_timestamps is not
		// supported with flash_attn - disabling" and silently drops DTW, so
		// word timestamps drift. -nfa keeps DTW on at some speed cost.
		dtwArgs = []string{"-nfa", "-dtw", a.cfg.DTWPreset}
	}
	data, vad, err := a.run(ctx, append(args, dtwArgs...), outPrefix, onProgress)
	if err != nil && dtwArgs != nil && isDTWShortWindowAbort(err) {
		// whisper.cpp bug: see isDTWShortWindowAbort. The run is lost, so do
		// it again without DTW; word timing falls back to whisper's own token
		// timestamps (and is re-timed by the aligner when that is enabled).
		slog.Warn("whisper.cpp DTW aborted on a short final window; retrying without DTW",
			"model", a.cfg.ID, "input", req.InputPath)
		data, vad, err = a.run(ctx, args, outPrefix, onProgress)
	}
	if err != nil {
		return nil, err
	}
	tr, err := parseJSON(data, req.Language, vad)
	if err != nil {
		return nil, err
	}
	return &transcriber.Result{
		Transcription: tr,
		ModelUsed:     a.cfg.ID,
		Duration:      time.Since(start),
	}, nil
}

// dtwShortWindowAssert is the assertion whisper.cpp (checked in v1.8.6) hits
// when DTW runs on a decode window shorter than 16 mel frames (160 ms). Whisper
// decodes any trailing window of 10 frames or more, and DTW's median filter
// (width 7) needs more than 7 audio tokens = n_frames/2. With VAD the input is
// the concatenated speech, so this fires whenever the total speech comes to a
// multiple of 30 s plus 100–150 ms. whisper-cli then dies with SIGABRT.
const dtwShortWindowAssert = "filter_width < a->ne[2]"

func isDTWShortWindowAbort(err error) bool {
	return err != nil && strings.Contains(err.Error(), dtwShortWindowAssert)
}

// run executes whisper-cli with args and returns the JSON it wrote to
// outPrefix.json and the VAD time mapping it logged.
func (a *Adapter) run(ctx context.Context, args []string, outPrefix string, onProgress transcriber.ProgressFunc) ([]byte, vadMap, error) {
	// A retry must not read the previous attempt's output.
	_ = os.Remove(outPrefix + ".json")

	cmd := exec.CommandContext(ctx, a.cfg.Binary, args...)
	procutil.KillGroupOnCancel(cmd)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("whispercpp start: %w", err)
	}

	capture := captureStderr(stderr, onProgress)
	go io.Copy(io.Discard, stdout)

	waitErr := cmd.Wait()
	tail := capture.wait()
	if waitErr != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if tail != "" {
			return nil, nil, fmt.Errorf("whispercpp exit: %w: %s", waitErr, tail)
		}
		return nil, nil, fmt.Errorf("whispercpp exit: %w", waitErr)
	}

	data, err := os.ReadFile(outPrefix + ".json")
	if err != nil {
		// whisper-cli exits 0 even when it rejects an unknown flag (it just
		// prints help to stderr). Surface the stderr tail so the real cause
		// isn't hidden behind a generic "no such file" from os.ReadFile.
		if tail != "" {
			return nil, nil, fmt.Errorf("whispercpp output: %w: %s", err, tail)
		}
		return nil, nil, fmt.Errorf("whispercpp output: %w", err)
	}
	return data, capture.vad, nil
}

// stderrCapture dispatches progress lines and buffers the tail for the exit error.
type stderrCapture struct {
	done chan struct{}
	tail []string
	// vad is the VAD time mapping whisper-cli logged; read only after wait().
	vad vadMap
}

func captureStderr(r io.Reader, onProgress transcriber.ProgressFunc) *stderrCapture {
	c := &stderrCapture{done: make(chan struct{})}
	go func() {
		defer close(c.done)
		const maxTail = 20
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := sc.Text()
			if m := progressRe.FindStringSubmatch(line); m != nil {
				if onProgress != nil {
					if pct, err := strconv.Atoi(m[1]); err == nil {
						onProgress(float64(pct) / 100.0)
					}
				}
				continue
			}
			if span, ok := parseVADSpan(line); ok {
				c.vad = append(c.vad, span)
				continue
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			c.tail = append(c.tail, line)
			if len(c.tail) > maxTail {
				c.tail = c.tail[len(c.tail)-maxTail:]
			}
		}
	}()
	return c
}

func (c *stderrCapture) wait() string {
	<-c.done
	return strings.Join(c.tail, "\n")
}

// whisper.cpp `--output-json-full` shape; offsets are in ms.
type rawOutput struct {
	Result struct {
		Language string `json:"language"`
	} `json:"result"`
	Transcription []rawSegment `json:"transcription"`
}

type rawSegment struct {
	Offsets struct {
		From int `json:"from"`
		To   int `json:"to"`
	} `json:"offsets"`
	Text   string     `json:"text"`
	Tokens []rawToken `json:"tokens"`
}

type rawToken struct {
	Text    rawString `json:"text"`
	Offsets struct {
		From int `json:"from"`
		To   int `json:"to"`
	} `json:"offsets"`
	ID int `json:"id"`
}

// rawString keeps raw bytes through JSON decode. Whisper.cpp BPE tokens
// often split a multi-byte codepoint (e.g. "å" = 0xC3 0xA5) across two
// tokens; the stdlib decoder replaces each lone byte with U+FFFD, which
// is unrecoverable. Raw bytes survive concatenation.
type rawString string

func (r *rawString) UnmarshalJSON(data []byte) error {
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return fmt.Errorf("rawString: not a JSON string: %s", data)
	}
	*r = rawString(decodeJSONStringBody(data[1 : len(data)-1]))
	return nil
}

func decodeJSONStringBody(body []byte) []byte {
	if bytes.IndexByte(body, '\\') < 0 {
		out := make([]byte, len(body))
		copy(out, body)
		return out
	}
	out := make([]byte, 0, len(body))
	for i := 0; i < len(body); {
		b := body[i]
		if b != '\\' || i+1 >= len(body) {
			out = append(out, b)
			i++
			continue
		}
		switch body[i+1] {
		case '"', '\\', '/':
			out = append(out, body[i+1])
			i += 2
		case 'b':
			out = append(out, '\b')
			i += 2
		case 'f':
			out = append(out, '\f')
			i += 2
		case 'n':
			out = append(out, '\n')
			i += 2
		case 'r':
			out = append(out, '\r')
			i += 2
		case 't':
			out = append(out, '\t')
			i += 2
		case 'u':
			if i+6 > len(body) {
				out = append(out, b)
				i++
				continue
			}
			r1, ok := parseHex4(body[i+2 : i+6])
			if !ok {
				out = append(out, b)
				i++
				continue
			}
			i += 6
			if utf16.IsSurrogate(r1) && i+6 <= len(body) && body[i] == '\\' && body[i+1] == 'u' {
				if r2, ok := parseHex4(body[i+2 : i+6]); ok && utf16.IsSurrogate(r2) {
					if r := utf16.DecodeRune(r1, r2); r != utf8.RuneError {
						out = utf8.AppendRune(out, r)
						i += 6
						continue
					}
				}
			}
			out = utf8.AppendRune(out, r1)
		default:
			out = append(out, b)
			i++
		}
	}
	return out
}

func parseHex4(b []byte) (rune, bool) {
	var r rune
	for _, c := range b {
		r <<= 4
		switch {
		case '0' <= c && c <= '9':
			r |= rune(c - '0')
		case 'a' <= c && c <= 'f':
			r |= rune(c - 'a' + 10)
		case 'A' <= c && c <= 'F':
			r |= rune(c - 'A' + 10)
		default:
			return 0, false
		}
	}
	return r, true
}

// Whisper vocab: id >= 50256 is a control token, not a word piece.
const firstSpecialTokenID = 50256

// vad maps token offsets back to the original timeline when VAD was on.
func parseJSON(data []byte, fallbackLang string, vad vadMap) (*transcriber.Transcription, error) {
	var raw rawOutput
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("whispercpp parse: %w", err)
	}
	t := &transcriber.Transcription{
		Language: raw.Result.Language,
		Segments: make([]transcriber.Segment, 0, len(raw.Transcription)),
	}
	if t.Language == "" {
		t.Language = fallbackLang
	}
	var sb strings.Builder
	for i, s := range raw.Transcription {
		text := strings.TrimSpace(s.Text)
		segStart := float64(s.Offsets.From) / 1000.0
		segEnd := float64(s.Offsets.To) / 1000.0
		var words []transcriber.Word
		if len(vad) > 0 {
			words = mappedTokensToWords(s.Tokens, vad)
			// whisper.cpp interpolates segment bounds across removed silence;
			// the mapped words are exact, so tighten the segment to them.
			if len(words) > 0 {
				segStart = words[0].Start
				segEnd = words[len(words)-1].End
			}
		} else {
			words = tokensToWords(s.Tokens, segStart, segEnd)
		}
		t.Segments = append(t.Segments, transcriber.Segment{
			ID:    i,
			Start: segStart,
			End:   segEnd,
			Text:  text,
			Words: words,
		})
		sb.WriteString(text)
		sb.WriteByte(' ')
	}
	t.Text = strings.TrimSpace(sb.String())
	return t, nil
}

// mappedTokensToWords groups BPE tokens into words (leading space marks a
// new word), mapping each token's VAD-buffer offsets to the original timeline.
func mappedTokensToWords(tokens []rawToken, vad vadMap) []transcriber.Word {
	var words []transcriber.Word
	var cur *transcriber.Word
	flush := func() {
		if cur == nil {
			return
		}
		cur.Text = strings.TrimSpace(cur.Text)
		if cur.Text != "" {
			words = append(words, *cur)
		}
		cur = nil
	}
	for _, tok := range tokens {
		if tok.ID >= firstSpecialTokenID {
			continue
		}
		text := string(tok.Text)
		start := vad.toOriginal(float64(tok.Offsets.From) / 1000.0)
		end := max(start, vad.toOriginal(float64(tok.Offsets.To)/1000.0))
		if cur == nil || strings.HasPrefix(text, " ") {
			flush()
			cur = &transcriber.Word{Text: text, Start: start, End: end}
			continue
		}
		cur.Text += text
		cur.End = max(cur.End, end)
	}
	flush()
	return words
}

// tokensToWords groups BPE tokens into words when no VAD mapping is known.
// If VAD was on but its mapping wasn't logged, token offsets are in
// VAD-compressed time while segment offsets are in original time; anchoring
// the first token to segStart is then only right up to the first pause.
func tokensToWords(tokens []rawToken, segStart, segEnd float64) []transcriber.Word {
	if len(tokens) == 0 {
		return nil
	}
	delta := 0.0
	foundFirst := false
	for _, tok := range tokens {
		if tok.ID >= firstSpecialTokenID {
			continue
		}
		delta = segStart - float64(tok.Offsets.From)/1000.0
		foundFirst = true
		break
	}
	if !foundFirst {
		return nil
	}
	clamp := func(t float64) float64 {
		if t < segStart {
			return segStart
		}
		if t > segEnd {
			return segEnd
		}
		return t
	}
	var words []transcriber.Word
	var cur *transcriber.Word
	flush := func() {
		if cur == nil {
			return
		}
		cur.Text = strings.TrimSpace(cur.Text)
		if cur.Text != "" {
			words = append(words, *cur)
		}
		cur = nil
	}
	for _, tok := range tokens {
		if tok.ID >= firstSpecialTokenID {
			continue
		}
		text := string(tok.Text)
		startsWord := cur == nil || strings.HasPrefix(text, " ")
		if startsWord {
			flush()
			start := clamp(float64(tok.Offsets.From)/1000.0 + delta)
			end := clamp(float64(tok.Offsets.To)/1000.0 + delta)
			cur = &transcriber.Word{Text: text, Start: start, End: end}
		} else {
			cur.Text += text
			if end := clamp(float64(tok.Offsets.To)/1000.0 + delta); end > cur.End {
				cur.End = end
			}
		}
	}
	flush()
	return words
}
