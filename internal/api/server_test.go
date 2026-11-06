package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"transcriber/internal/db"
	"transcriber/internal/jobs"
	"transcriber/internal/modelprompts"
	"transcriber/internal/transcriber"
	"transcriber/internal/transcriber/stub"
)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	conn, err := db.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	reg := transcriber.NewRegistry("a")
	reg.Register(stub.New("a", "Model A"))
	reg.Register(stub.New("b", "Model B"))
	reg.Alias("old-a", "a")
	srv := NewServer(jobs.NewStore(0), jobs.NewQueue(), reg, modelprompts.New(conn), "global prompt", "")
	return srv.Routes(nil)
}

func do(t *testing.T, h http.Handler, method, path string, body any, out any) int {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, &buf))
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s %s: decode %q: %v", method, path, rec.Body.String(), err)
		}
	}
	return rec.Code
}

func TestModelPromptPrecedence(t *testing.T) {
	h := newTestServer(t)

	// Setting via an alias stores it on the canonical model.
	var mi modelInfo
	if code := do(t, h, "PUT", "/models/old-a/prompt", map[string]string{"prompt": "model A prompt"}, &mi); code != 200 {
		t.Fatalf("PUT code %d", code)
	}
	if mi.ID != "a" || mi.Prompt != "model A prompt" {
		t.Fatalf("PUT resp %+v", mi)
	}

	var list []modelInfo
	do(t, h, "GET", "/models", nil, &list)
	if len(list) != 2 || list[0].ID != "a" || list[0].Prompt != "model A prompt" || list[1].Prompt != "" {
		t.Fatalf("list %+v", list)
	}
	if len(list[0].Aliases) != 1 || list[0].Aliases[0] != "old-a" || !list[0].Default {
		t.Fatalf("aliases/default %+v", list[0])
	}

	cases := []struct {
		name string
		in   map[string]any
		want string
	}{
		{"model prompt", map[string]any{"model": "a"}, "model A prompt"},
		{"model prompt via default", map[string]any{}, "model A prompt"},
		{"model prompt via alias", map[string]any{"model": "old-a"}, "model A prompt"},
		{"global fallback", map[string]any{"model": "b"}, "global prompt"},
		{"request wins", map[string]any{"model": "a", "prompt": "mine"}, "mine"},
	}
	for _, c := range cases {
		c.in["path"] = "/in.wav"
		c.in["output_path"] = "/out"
		var job TranscribeJob
		if code := do(t, h, "POST", "/transcription/job", c.in, &job); code != http.StatusAccepted {
			t.Fatalf("%s: code %d", c.name, code)
		}
		if job.Prompt != c.want {
			t.Errorf("%s: prompt %q, want %q", c.name, job.Prompt, c.want)
		}
	}

	// Clearing falls back to the global prompt.
	do(t, h, "PUT", "/models/a/prompt", map[string]string{"prompt": ""}, &mi)
	var job TranscribeJob
	do(t, h, "POST", "/transcription/job", map[string]any{"path": "/x", "output_path": "/o", "model": "a"}, &job)
	if job.Prompt != "global prompt" {
		t.Fatalf("after clear: %q", job.Prompt)
	}
}

func TestSetModelPromptUnknown(t *testing.T) {
	h := newTestServer(t)
	if code := do(t, h, "PUT", "/models/nope/prompt", map[string]string{"prompt": "x"}, nil); code != 404 {
		t.Fatalf("code %d", code)
	}
}

type promptless struct{ transcriber.Transcriber }

func (promptless) IgnoresPrompt() bool { return true }

func TestSupportsPrompt(t *testing.T) {
	if !transcriber.SupportsPrompt(stub.New("a", "A")) {
		t.Fatal("stub should support prompts")
	}
	if transcriber.SupportsPrompt(promptless{stub.New("b", "B")}) {
		t.Fatal("promptless should not")
	}
}
