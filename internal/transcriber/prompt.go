package transcriber

import "strings"

// PromptStyle is the shape of prompt a model makes best use of.
type PromptStyle string

const (
	// PromptText is free text in the register of the audio (Whisper's
	// initial prompt). A short sentence works better than a list.
	PromptText PromptStyle = "text"
	// PromptKeyterms is a comma/newline separated list of names and terms
	// (ElevenLabs Scribe and Deepgram keyterms).
	PromptKeyterms PromptStyle = "keyterms"
)

// keytermPrefixes are the adapter IDs whose backends take keyterms rather
// than a free-text prompt.
var keytermPrefixes = []string{
	"elevenlabs-",
	"openrouter/deepgram/",
	"openrouter/elevenlabs/",
	"openrouter/assemblyai/",
	"nordlyd-", // Qwen3-ASR context biases toward listed terms
}

// PromptStyleFor returns the prompt style for a canonical adapter ID.
func PromptStyleFor(id string) PromptStyle {
	for _, p := range keytermPrefixes {
		if strings.HasPrefix(id, p) {
			return PromptKeyterms
		}
	}
	return PromptText
}

// DefaultPrompts are the server-side prompts used when a request omits one.
type DefaultPrompts struct {
	Text     string
	Keyterms string
}

// For returns the default prompt for a canonical adapter ID. Keyterm models
// fall back to the text prompt when no keyterm list is configured.
func (d DefaultPrompts) For(id string) string {
	if PromptStyleFor(id) == PromptKeyterms && d.Keyterms != "" {
		return d.Keyterms
	}
	return d.Text
}
