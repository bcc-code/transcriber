package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"transcriber/internal/jobs"
	"transcriber/internal/transcriber"
	"transcriber/internal/transcriber/stub"
)

func TestCreateJobRoutesRemoteModelsToRemoteQueue(t *testing.T) {
	registry := transcriber.NewRegistry("local")
	registry.Register(stub.New("local", "Local"))
	registry.RegisterRemote(stub.New("cloud", "Cloud"))
	registry.Alias("cloud-alias", "cloud")

	local, remote := jobs.NewQueue(), jobs.NewQueue()
	srv := NewServer(jobs.NewStore(0), local, remote, registry, transcriber.DefaultPrompts{}, "")
	h := srv.Routes(nil)

	for _, model := range []string{"", "local", "cloud", "cloud-alias"} {
		body := `{"path":"/in.wav","output_path":"/out","model":"` + model + `"}`
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/transcription/job", strings.NewReader(body)))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("model %q: status %d: %s", model, rec.Code, rec.Body)
		}
	}
	// "" (default) and "local" go local; "cloud" and its alias go remote.
	if local.Len() != 2 || remote.Len() != 2 {
		t.Errorf("local=%d remote=%d, want 2 and 2", local.Len(), remote.Len())
	}
}
