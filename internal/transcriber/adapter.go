package transcriber

import (
	"context"
	"time"
	"unicode"
)

// Transcription mirrors the OpenAI Whisper JSON layout.
type Transcription struct {
	Text     string    `json:"text"`
	Segments []Segment `json:"segments"`
	Language string    `json:"language"`
}

// HasSpeech reports whether any segment carries spoken text. Segments that
// are empty or only a non-speech marker ("[BLANK_AUDIO]", "(music)", "♪")
// don't count: models emit those for silence and music.
func (t *Transcription) HasSpeech() bool {
	if t == nil {
		return false
	}
	for _, s := range t.Segments {
		if !isNonSpeech(s.Text) {
			return true
		}
	}
	// Some backends return text without segments.
	return len(t.Segments) == 0 && !isNonSpeech(t.Text)
}

// isNonSpeech is true for text with no letters or digits once bracketed and
// parenthesized markers are removed.
func isNonSpeech(text string) bool {
	depth := 0
	for _, r := range text {
		switch r {
		case '[', '(':
			depth++
		case ']', ')':
			depth = max(0, depth-1)
		default:
			if depth == 0 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
				return false
			}
		}
	}
	return true
}

type Segment struct {
	Text             string  `json:"text"`
	Start            float64 `json:"start"`
	End              float64 `json:"end"`
	Words            []Word  `json:"words,omitempty"`
	ID               int     `json:"id"`
	Seek             int     `json:"seek,omitempty"`
	Tokens           []int   `json:"tokens,omitempty"`
	Temperature      float64 `json:"temperature,omitempty"`
	AvgLogprob       float64 `json:"avg_logprob,omitempty"`
	CompressionRatio float64 `json:"compression_ratio,omitempty"`
	NoSpeechProb     float64 `json:"no_speech_prob,omitempty"`
}

type Word struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type Request struct {
	InputPath string
	Language  string
	// WorkDir is scratch space for intermediate files (extracted chunk wavs,
	// raw model output). It is owned by the caller, which deletes it once the
	// job finishes, so adapters must not treat anything written here as a
	// deliverable.
	//
	// It is deliberately NOT the job's output_path: the final transcripts are
	// written by the worker via internal/formats, so nothing an adapter leaves
	// behind lands in the directory the API caller reads.
	WorkDir string
	// Prompt biases the decoder toward names and terms it might otherwise mishear.
	// whisper.cpp truncates to ~224 tokens.
	Prompt  string
	Options map[string]any
}

type Result struct {
	Transcription *Transcription
	ModelUsed     string
	Duration      time.Duration
}

type ProgressFunc func(progress float64)

type Transcriber interface {
	ID() string
	Name() string
	Transcribe(ctx context.Context, req Request, onProgress ProgressFunc) (*Result, error)
}

type Registry struct {
	adapters map[string]Transcriber
	aliases  map[string]string // alternate ID -> canonical adapter ID
	remote   map[string]bool   // canonical IDs of adapters that run on a remote service
	def      string
}

func NewRegistry(defaultID string) *Registry {
	return &Registry{
		adapters: map[string]Transcriber{},
		aliases:  map[string]string{},
		remote:   map[string]bool{},
		def:      defaultID,
	}
}

func (r *Registry) Register(t Transcriber) {
	r.adapters[t.ID()] = t
}

// RegisterRemote registers an adapter whose work happens on a remote service
// (a cloud API). Its jobs run in the remote worker pool, which is sized
// independently of the local, GPU-bound pool.
func (r *Registry) RegisterRemote(t Transcriber) {
	r.Register(t)
	r.remote[t.ID()] = true
}

// IsRemote reports whether id (or the adapter an alias points to) was
// registered with RegisterRemote. Unknown IDs are not remote.
func (r *Registry) IsRemote(id string) bool {
	t, ok := r.Get(id)
	return ok && r.remote[t.ID()]
}

// Alias makes `from` resolve to the same adapter as `to`. Aliases are not listed.
func (r *Registry) Alias(from, to string) {
	r.aliases[from] = to
}

func (r *Registry) Get(id string) (Transcriber, bool) {
	if t, ok := r.adapters[id]; ok {
		return t, true
	}
	if target, ok := r.aliases[id]; ok {
		if t, ok := r.adapters[target]; ok {
			return t, true
		}
	}
	return nil, false
}

func (r *Registry) Default() (Transcriber, bool) {
	return r.Get(r.def)
}

func (r *Registry) DefaultID() string {
	return r.def
}

func (r *Registry) List() []Transcriber {
	out := make([]Transcriber, 0, len(r.adapters))
	for _, t := range r.adapters {
		out = append(out, t)
	}
	return out
}
