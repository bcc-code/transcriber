package transcriber

import "testing"

func TestDefaultPromptsFor(t *testing.T) {
	d := DefaultPrompts{Text: "text", Keyterms: "keyterms"}
	cases := map[string]string{
		"whisper-cpp-large-v3":                    "text",
		"nb-whisper-large":                        "text",
		"openrouter/openai/whisper-large-v3":      "text",
		"elevenlabs-scribe-v2":                    "keyterms",
		"openrouter/deepgram/nova-3":              "keyterms",
		"openrouter/elevenlabs/scribe-v2":         "keyterms",
		"openrouter/assemblyai/universal-3-6-pro": "keyterms",
	}
	for id, want := range cases {
		if got := d.For(id); got != want {
			t.Errorf("For(%q) = %q, want %q", id, got, want)
		}
	}
	if got := (DefaultPrompts{Text: "text"}).For("elevenlabs-scribe-v2"); got != "text" {
		t.Errorf("keyterm model without keyterms = %q, want text fallback", got)
	}
}
