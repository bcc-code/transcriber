package main

import (
	"context"
	"os"

	"transcriber/internal/hfcache"
	"transcriber/internal/transcriber"
	"transcriber/internal/transcriber/chunked"
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

	r.Register(chunked.New(
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
	))

	r.Register(chunked.New(
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
	))

	// Sub-large sizes upgrade to large — matches the old runner's coercion behavior.
	r.Alias("openai/whisper-large-v3", "whisper-cpp-large-v3")
	r.Alias("openai/whisper-large-v2", "whisper-cpp-large-v3")
	r.Alias("openai/whisper-large", "whisper-cpp-large-v3")
	r.Alias("openai/whisper-medium", "whisper-cpp-large-v3")
	r.Alias("openai/whisper-small", "whisper-cpp-large-v3")
	r.Alias("openai/whisper-tiny", "whisper-cpp-large-v3")
	r.Alias("NbAiLab/nb-whisper-large", "nb-whisper-large")
	r.Alias("NbAiLab/nb-whisper-medium", "nb-whisper-large")
	r.Alias("NbAiLab/nb-whisper-small", "nb-whisper-large")
	r.Alias("NbAiLab/nb-whisper-tiny", "nb-whisper-large")

	return r
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
