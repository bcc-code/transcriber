// Package whispercpp adapts the whisper.cpp CLI to the Transcriber interface.
package whispercpp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	if err := os.MkdirAll(req.OutputDir, 0o755); err != nil {
		return nil, err
	}
	outPrefix := filepath.Join(req.OutputDir, "whispercpp_out")

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
	if a.cfg.DTWPreset != "" {
		args = append(args, "-dtw", a.cfg.DTWPreset)
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
	cmd := exec.CommandContext(ctx, a.cfg.Binary, args...)
	procutil.KillGroupOnCancel(cmd)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("whispercpp start: %w", err)
	}

	capture := captureStderr(stderr, onProgress)
	go io.Copy(io.Discard, stdout)

	waitErr := cmd.Wait()
	tail := capture.wait()
	if waitErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if tail != "" {
			return nil, fmt.Errorf("whispercpp exit: %w: %s", waitErr, tail)
		}
		return nil, fmt.Errorf("whispercpp exit: %w", waitErr)
	}

	data, err := os.ReadFile(outPrefix + ".json")
	if err != nil {
		// whisper-cli exits 0 even when it rejects an unknown flag (it just
		// prints help to stderr). Surface the stderr tail so the real cause
		// isn't hidden behind a generic "no such file" from os.ReadFile.
		if tail != "" {
			return nil, fmt.Errorf("whispercpp output: %w: %s", err, tail)
		}
		return nil, fmt.Errorf("whispercpp output: %w", err)
	}
	tr, err := parseJSON(data, req.Language)
	if err != nil {
		return nil, err
	}
	return &transcriber.Result{
		Transcription: tr,
		ModelUsed:     a.cfg.ID,
		Duration:      time.Since(start),
	}, nil
}

// stderrCapture dispatches progress lines and buffers the tail for the exit error.
type stderrCapture struct {
	done chan struct{}
	tail []string
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

func parseJSON(data []byte, fallbackLang string) (*transcriber.Transcription, error) {
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
		t.Segments = append(t.Segments, transcriber.Segment{
			ID:    i,
			Start: segStart,
			End:   segEnd,
			Text:  text,
			Words: tokensToWords(s.Tokens, segStart, segEnd),
		})
		sb.WriteString(text)
		sb.WriteByte(' ')
	}
	t.Text = strings.TrimSpace(sb.String())
	return t, nil
}

// tokensToWords groups BPE tokens into words (leading space marks a new word)
// and remaps timestamps. With VAD on, token offsets are in VAD-compressed
// time while segment offsets are in original time — we anchor the first
// token to segStart and apply the same delta to the rest.
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
