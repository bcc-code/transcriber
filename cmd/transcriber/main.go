package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"transcriber/internal/api"
	"transcriber/internal/callback"
	"transcriber/internal/jobs"
	"transcriber/internal/transcriber"
	"transcriber/internal/web"
	"transcriber/internal/worker"
)

func main() {
	port := flag.Int("port", 8888, "HTTP listen port")
	workers := flag.Int("workers", 2, "number of transcription worker goroutines")
	callbackWorkers := flag.Int("callback-workers", 2, "number of webhook delivery goroutines")
	defaultModel := flag.String("default-model", "stub", "model adapter ID to use when the request omits `model`")
	defaultLanguage := flag.String("default-language", "", "ISO 639-1 language hint applied when the request omits `language` (empty or `auto` = let the model detect)")
	defaultPromptFile := flag.String("default-prompt-file", "prompt.txt", "path to a file whose contents are used as the prompt when the request omits one (missing file = no default prompt)")
	maxTerminalJobs := flag.Int("max-terminal-jobs", 20, "how many finished jobs (completed/failed/canceled) to retain in memory; <= 0 disables the cap")
	jobTimeout := flag.Duration("job-timeout", 30*time.Minute, "default wall-clock timeout per job; per-request timeout_seconds overrides this; <= 0 disables")
	logFormat := flag.String("log-format", "text", "log handler: text (human-readable, dev) or json (structured, prod)")
	whisperNoGPU := flag.Bool("whispercpp-no-gpu", false, "pass `-ng` to whisper-cli, forcing the CPU backend (use on hosts without a real GPU, e.g. Docker-on-Mac)")
	flag.Parse()

	handlerOpts := &slog.HandlerOptions{Level: slog.LevelInfo}
	var handler slog.Handler
	switch *logFormat {
	case "json":
		handler = slog.NewJSONHandler(os.Stderr, handlerOpts)
	case "text":
		handler = slog.NewTextHandler(os.Stderr, handlerOpts)
	default:
		fmt.Fprintf(os.Stderr, "invalid -log-format %q (want text or json)\n", *logFormat)
		os.Exit(2)
	}
	slog.SetDefault(slog.New(handler))

	var defaultPrompt string
	if *defaultPromptFile != "" {
		b, err := os.ReadFile(*defaultPromptFile)
		switch {
		case err == nil:
			defaultPrompt = strings.TrimSpace(string(b))
			slog.Info("loaded default prompt", "path", *defaultPromptFile, "bytes", len(defaultPrompt))
		case os.IsNotExist(err):
			slog.Info("no default prompt file found", "path", *defaultPromptFile)
		default:
			slog.Error("reading default prompt file", "path", *defaultPromptFile, "err", err)
			os.Exit(1)
		}
	}

	registry := buildRegistry(*defaultModel, *whisperNoGPU)
	if _, ok := registry.Default(); !ok {
		slog.Error("default model not registered", "id", *defaultModel)
		os.Exit(1)
	}

	// Validate up-front so a typo like `-default-language=nb` doesn't silently
	// turn into auto-detect at request time. NormalizeLanguage already accepts
	// empty and "auto", so we only need to flag everything else.
	normalizedDefaultLang := transcriber.NormalizeLanguage(*defaultLanguage)
	if *defaultLanguage != "" && *defaultLanguage != "auto" && normalizedDefaultLang == "auto" {
		slog.Error("unsupported -default-language", "value", *defaultLanguage)
		os.Exit(1)
	}

	store := jobs.NewStore(*maxTerminalJobs)
	queue := jobs.NewQueue()
	notifier := callback.NewNotifier(*callbackWorkers, 256)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	notifier.Start(ctx)

	pool := worker.New(*workers, store, queue, registry, notifier, func(j jobs.Job) any {
		return api.ToDTO(j)
	}, *jobTimeout)
	pool.Start(ctx)

	srv := api.NewServer(store, queue, registry, defaultPrompt, normalizedDefaultLang)
	addr := fmt.Sprintf(":%d", *port)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Routes(web.Handler()),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("transcriber api listening",
			"addr", addr,
			"workers", *workers,
			"default_model", *defaultModel,
			"default_language", normalizedDefaultLang,
			"models", len(registry.List()),
		)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server failed", "err", err)
			os.Exit(1)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	slog.Info("shutdown signal received")

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	_ = httpSrv.Shutdown(shutdownCtx)

	queue.Close()
	cancel()
	pool.Wait()
	notifier.Shutdown()
	slog.Info("shutdown complete")
}
