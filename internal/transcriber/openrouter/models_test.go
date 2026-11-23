package openrouter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListTranscriptionModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/models" || q.Get("output_modalities") != "transcription" || q.Get("input_modalities") != "audio" {
			t.Errorf("url = %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"data":[
			{"id":"openai/whisper-large-v3","name":"OpenAI: Whisper Large V3","architecture":{"output_modalities":["transcription"]}},
			{"id":"deepgram/aura-2","name":"Deepgram: Aura-2","architecture":{"output_modalities":["speech"]}},
			{"id":"elevenlabs/scribe-v2","name":"ElevenLabs: Scribe v2","architecture":{"output_modalities":["transcription"]}}
		]}`))
	}))
	defer srv.Close()

	models, err := ListTranscriptionModels(context.Background(), nil, srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "elevenlabs/scribe-v2" || models[1].Name != "OpenAI: Whisper Large V3" {
		t.Errorf("models = %+v", models)
	}
}

func TestListTranscriptionModelsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	if _, err := ListTranscriptionModels(context.Background(), nil, srv.URL, ""); err == nil {
		t.Fatal("expected error")
	}
}
