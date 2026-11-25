package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"transcriber/internal/hfcache"
	"transcriber/internal/transcriber"
	"transcriber/internal/transcriber/aligned"
	"transcriber/internal/transcriber/chunked"
	"transcriber/internal/transcriber/elevenlabs"
	"transcriber/internal/transcriber/openrouter"
	"transcriber/internal/transcriber/qwenasr"
	"transcriber/internal/transcriber/stub"
	"transcriber/internal/transcriber/whispercpp"
)

func buildRegistry(defaultID string, whisperNoGPU bool) *transcriber.Registry {
	r := transcriber.NewRegistry(defaultID)

	r.Register(stub.New("stub", "Stub Adapter"))

	cache := hfcache.Default()
	whisperBin := envOr("WHISPER_CPP_BIN", "/opt/homebrew/bin/whisper-cli")
	vadModelFile := os.Getenv("WHISPER_VAD_MODEL")
	resolveVAD := func(ctx context.Context) (string, error) {
		return cache.Get(ctx, "ggml-org/whisper-vad", "ggml-silero-v5.1.2.bin")
	}

	// Local whisper models get their words re-timed by the forced aligner
	// service when ALIGNER_URL is set (see aligner/).
	local := func(t transcriber.Transcriber) transcriber.Transcriber { return t }
	if url := os.Getenv("ALIGNER_URL"); url != "" {
		local = func(t transcriber.Transcriber) transcriber.Transcriber {
			return aligned.New(t, aligned.Config{URL: url})
		}
		slog.Info("forced alignment enabled for local whisper models", "url", url)
	} else {
		slog.Info("forced alignment disabled: ALIGNER_URL not set")
	}

	r.Register(local(chunked.New(
		whispercpp.New(whispercpp.Config{
			ID:          "whisper-cpp-large-v3",
			DisplayName: "OpenAI Whisper large-v3",
			Binary:      whisperBin,
			ModelFile:   os.Getenv("WHISPER_CPP_MODEL"),
			ResolveModel: func(ctx context.Context) (string, error) {
				return cache.Get(ctx, "ggerganov/whisper.cpp", "ggml-large-v3.bin")
			},
			Threads:         8,
			DTWPreset:       "large.v3",
			VADModelFile:    vadModelFile,
			ResolveVADModel: resolveVAD,
			NoGPU:           whisperNoGPU,
		}),
		chunked.Config{},
	)))

	r.Register(local(chunked.New(
		whispercpp.New(whispercpp.Config{
			ID:          "nb-whisper-large",
			DisplayName: "NB-Whisper large (Norwegian)",
			Binary:      whisperBin,
			ModelFile:   os.Getenv("NB_WHISPER_MODEL"),
			ResolveModel: func(ctx context.Context) (string, error) {
				return cache.Get(ctx, "NbAiLab/nb-whisper-large", "ggml-model.bin")
			},
			Threads:         8,
			DTWPreset:       "large.v3",
			VADModelFile:    vadModelFile,
			ResolveVADModel: resolveVAD,
			NoGPU:           whisperNoGPU,
		}),
		chunked.Config{},
	)))

	r.Register(local(chunked.New(
		whispercpp.New(whispercpp.Config{
			ID:          "nb-whisper-medium",
			DisplayName: "NB-Whisper medium (Norwegian)",
			Binary:      whisperBin,
			ModelFile:   os.Getenv("NB_WHISPER_MEDIUM_MODEL"),
			ResolveModel: func(ctx context.Context) (string, error) {
				return cache.Get(ctx, "NbAiLab/nb-whisper-medium", "ggml-model.bin")
			},
			Threads:         8,
			DTWPreset:       "medium",
			VADModelFile:    vadModelFile,
			ResolveVADModel: resolveVAD,
			NoGPU:           whisperNoGPU,
		}),
		chunked.Config{},
	)))

	registerQwenASR(r, local)

	// Sub-large sizes upgrade to large — matches the old runner's coercion behavior.
	r.Alias("openai/whisper-large-v3", "whisper-cpp-large-v3")
	r.Alias("openai/whisper-large-v2", "whisper-cpp-large-v3")
	r.Alias("openai/whisper-large", "whisper-cpp-large-v3")
	r.Alias("openai/whisper-medium", "whisper-cpp-large-v3")
	r.Alias("openai/whisper-small", "whisper-cpp-large-v3")
	r.Alias("openai/whisper-tiny", "whisper-cpp-large-v3")
	r.Alias("NbAiLab/nb-whisper-large", "nb-whisper-large")
	r.Alias("NbAiLab/nb-whisper-medium", "nb-whisper-medium")
	r.Alias("NbAiLab/nb-whisper-small", "nb-whisper-large")
	r.Alias("NbAiLab/nb-whisper-tiny", "nb-whisper-large")

	registerElevenLabs(r)
	registerOpenRouter(r)

	return r
}

// registerQwenASR adds NbAiLab's Nordlyd Qwen3-ASR model, served by the
// qwen-asr service (qwen-asr/). It runs on the local GPU, so it goes in the
// local pool, and its words are re-timed by the aligner like whisper's (the
// model itself gives no timestamps). Skipped without QWEN_ASR_URL.
func registerQwenASR(r *transcriber.Registry, local func(transcriber.Transcriber) transcriber.Transcriber) {
	url := os.Getenv("QWEN_ASR_URL")
	if url == "" {
		slog.Info("qwen-asr backend disabled: QWEN_ASR_URL not set")
		return
	}
	const id = "nordlyd-asr-qwen-0.6b"
	r.Register(local(chunked.New(
		qwenasr.New(qwenasr.Config{
			ID:              id,
			DisplayName:     "Nordlyd ASR Qwen3 0.6B (Norwegian, Nov 26 preview)",
			URL:             url,
			DefaultLanguage: "no",
		}),
		chunked.Config{},
	)))
	r.Alias("NbAiLab/nordlyd-asr-qwen06B-nov26preview", id)
	slog.Info("qwen-asr backend enabled", "url", url, "model", id)
}

// registerElevenLabs adds one adapter per Scribe model so clients choose the
// model with the request's `model` field (e.g. "elevenlabs-scribe-v1").
// Skipped entirely without an API key, so /models only lists usable backends.
func registerElevenLabs(r *transcriber.Registry) {
	key := os.Getenv("ELEVENLABS_API_KEY")
	if key == "" {
		slog.Info("elevenlabs backends disabled: ELEVENLABS_API_KEY not set")
		return
	}
	models := []struct{ id, modelID, name string }{
		{"elevenlabs-scribe-v3", "scribe_v3", "ElevenLabs Scribe v3"},
		{"elevenlabs-scribe-v2", "scribe_v2", "ElevenLabs Scribe v2"},
		{"elevenlabs-scribe-v1", "scribe_v1", "ElevenLabs Scribe v1"},
	}
	for _, m := range models {
		r.RegisterRemote(elevenlabs.New(elevenlabs.Config{
			ID:          m.id,
			DisplayName: m.name,
			APIKey:      key,
			ModelID:     m.modelID,
		}))
		// ElevenLabs' own model name works too, e.g. "elevenlabs/scribe_v2".
		r.Alias("elevenlabs/"+m.modelID, m.id)
	}
	// Unversioned ID follows ELEVENLABS_DEFAULT_MODEL so the server owner can move
	// clients that don't pin a version.
	def := envOr("ELEVENLABS_DEFAULT_MODEL", "elevenlabs-scribe-v2")
	r.Alias("elevenlabs-scribe", def)
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.id
	}
	slog.Info("elevenlabs backends enabled", "models", ids, "elevenlabs-scribe", def)
}

const openRouterFallbackModel = "openai/whisper-large-v3"

// registerOpenRouter adds one adapter per OpenRouter speech-to-text model,
// under the ID "openrouter/<slug>" (e.g. "openrouter/openai/whisper-large-v3"),
// so it never collides with the local "openai/whisper-*" aliases. Each adapter
// is wrapped in chunked to stay far below the endpoint's 25 MB upload cap.
//
// Which models: see openRouterModels. Skipped entirely without an API key.
func registerOpenRouter(r *transcriber.Registry) {
	key := os.Getenv("OPENROUTER_KEY")
	if key == "" {
		slog.Info("openrouter backends disabled: OPENROUTER_KEY not set")
		return
	}

	models, source := openRouterModels(key)
	if len(models) == 0 {
		slog.Warn("openrouter backends disabled: no models", "source", source)
		return
	}

	ids := make([]string, 0, len(models))
	for _, m := range models {
		id := "openrouter/" + m.ID
		name := m.Name
		if name == "" {
			name = m.ID
		}
		r.RegisterRemote(chunked.New(
			openrouter.New(openrouter.Config{
				ID:          id,
				DisplayName: "OpenRouter " + name,
				APIKey:      key,
				Model:       m.ID,
			}),
			// 10-minute chunks are ~2.4 MB of Opus; chunks run concurrently
			// since the work happens remotely.
			chunked.Config{ChunkLengthSec: 600, Parallelism: 4},
		))
		ids = append(ids, id)
	}

	// "openrouter" resolves to OPENROUTER_DEFAULT_MODEL when that model is
	// registered, otherwise to the first one.
	def := ids[0]
	want := "openrouter/" + strings.TrimPrefix(envOr("OPENROUTER_DEFAULT_MODEL", openRouterFallbackModel), "openrouter/")
	if _, ok := r.Get(want); ok {
		def = want
	} else if os.Getenv("OPENROUTER_DEFAULT_MODEL") != "" {
		slog.Warn("OPENROUTER_DEFAULT_MODEL not available; using first model", "requested", want, "openrouter", def)
	}
	r.Alias("openrouter", def)
	slog.Info("openrouter backends enabled", "source", source, "count", len(ids), "models", ids, "openrouter", def)
}

// openRouterNorwegianModels are the OpenRouter transcription models whose
// vendor documents Norwegian support (checked 2026-11-23). Only these are
// registered. Deliberately left out although they list Norwegian:
// elevenlabs/scribe-v2-medical (clinical vocabulary), assemblyai/universal-3-5-pro
// (deprecated), nvidia/nemotron-3.5-transcribe and
// nvidia/nemotron-3.5-asr-streaming-multilingual-0.6b (no Norwegian ASR in the
// model card's support matrix; in testing the 30B model transcribed Norwegian
// as a mix of Swedish and English).
var openRouterNorwegianModels = []string{
	"assemblyai/universal-3-6-pro",
	"deepgram/nova-3",
	"elevenlabs/scribe-v2",
	"google/chirp-3", // Norwegian is Preview, not GA
	"google/gemini-3.5-transcribe",
	"microsoft/mai-transcribe-1.5",
	"microsoft/mai-transcribe-2",
	"mistralai/voxtral-mini-transcribe-3-0",
	"mistralai/voxtral-small-transcribe-3-0",
	"openai/whisper-1",
	"openai/whisper-large-v3",
	"openai/whisper-large-v3-turbo",
	"x-ai/grok-stt-2.0",
}

// openRouterModels returns the models to register and where they came from.
// OPENROUTER_MODELS overrides everything. Otherwise the hardcoded Norwegian
// list is intersected with what OpenRouter lists at boot, so models that
// were withdrawn are dropped; if the fetch fails the list is used as is.
func openRouterModels(key string) ([]openrouter.Model, string) {
	if v := os.Getenv("OPENROUTER_MODELS"); v != "" {
		var models []openrouter.Model
		for _, slug := range strings.Split(v, ",") {
			if slug = strings.TrimSpace(slug); slug != "" {
				models = append(models, openrouter.Model{ID: slug})
			}
		}
		return models, "OPENROUTER_MODELS"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	listed, err := openrouter.ListTranscriptionModels(ctx, nil, "", key)
	if err != nil || len(listed) == 0 {
		slog.Warn("openrouter model list fetch failed; registering hardcoded list unchecked", "err", err)
		models := make([]openrouter.Model, len(openRouterNorwegianModels))
		for i, id := range openRouterNorwegianModels {
			models[i] = openrouter.Model{ID: id}
		}
		return models, "hardcoded"
	}

	byID := make(map[string]openrouter.Model, len(listed))
	for _, m := range listed {
		byID[m.ID] = m
	}
	var models []openrouter.Model
	var missing []string
	for _, id := range openRouterNorwegianModels {
		if m, ok := byID[id]; ok {
			models = append(models, m)
		} else {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		slog.Warn("openrouter models no longer listed; skipped", "models", missing)
	}
	return models, "api"
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
