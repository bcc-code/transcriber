package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"transcriber/internal/api"
	"transcriber/internal/callback"
	"transcriber/internal/jobs"
	"transcriber/internal/transcriber"
	"transcriber/internal/version"
	"transcriber/internal/web"
	"transcriber/internal/worker"
)

func main() {
	port := flag.Int("port", 8888, "HTTP listen port")
	workers := flag.Int("workers", 2, "concurrent jobs on local models (GPU-bound)")
	remoteWorkers := flag.Int("remote-workers", 10, "concurrent jobs on remote (cloud API) models, independent of -workers")
	callbackWorkers := flag.Int("callback-workers", 2, "number of webhook delivery goroutines")
	defaultModel := flag.String("default-model", "stub", "model adapter ID to use when the request omits `model`")
	defaultLanguage := flag.String("default-language", "", "ISO 639-1 language hint applied when the request omits `language` (empty or `auto` = let the model detect)")
	defaultPromptFile := flag.String("default-prompt-file", "prompts/whisper.txt", "free-text prompt used when the request omits one, for Whisper-style models (missing file = no default prompt)")
	defaultKeytermsFile := flag.String("default-keyterms-file", "prompts/keyterms.txt", "comma/newline separated keyterms used when the request omits a prompt, for keyterm models such as ElevenLabs Scribe and Deepgram (missing file = fall back to -default-prompt-file)")
	stateDB := flag.String("state-db", "transcriber.db", "SQLite file holding job state, so jobs survive a restart; empty keeps state in memory only")
	retention := flag.Duration("retention", 7*24*time.Hour, "how long finished jobs (completed/failed/canceled) are kept after they end; <= 0 keeps them forever")
	jobTimeout := flag.Duration("job-timeout", 30*time.Minute, "default wall-clock timeout per job; per-request timeout_seconds overrides this; <= 0 disables")
	logFormat := flag.String("log-format", "text", "log handler: text (human-readable, dev) or json (structured, prod)")
	scratchDir := flag.String("scratch-dir", "", "directory for per-job scratch space (extracted audio chunks, raw model output); empty uses the OS temp dir. Keep this on local disk, not network storage")
	keepWorkDirs := flag.Bool("keep-work-dirs", false, "retain per-job scratch directories after completion for debugging; they are large (~115 MB per hour of audio)")
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

	defaultPrompts := transcriber.DefaultPrompts{
		Text:     readPromptFile(*defaultPromptFile, "prompt"),
		Keyterms: readPromptFile(*defaultKeytermsFile, "keyterms"),
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := jobs.NewStore(*retention)
	if *stateDB != "" {
		var err error
		store, err = jobs.Open(ctx, *stateDB, *retention)
		if err != nil {
			slog.Error("open job store", "path", *stateDB, "err", err)
			os.Exit(1)
		}
	}
	defer store.Close()
	go store.RunPruner(ctx, time.Hour)

	queue := jobs.NewQueue()
	remoteQueue := jobs.NewQueue()
	notifier := callback.NewNotifier(*callbackWorkers, 256)

	notifier.Start(ctx)

	toDTO := func(j jobs.Job) any { return api.ToDTO(j) }
	poolCfg := func(name string) worker.Config {
		return worker.Config{
			Name:           name,
			DefaultTimeout: *jobTimeout,
			ScratchRoot:    *scratchDir,
			KeepWorkDirs:   *keepWorkDirs,
		}
	}
	// Local models compete for the GPU; cloud models only wait on a remote API,
	// so they get their own, larger pool and queue.
	pool := worker.New(*workers, store, queue, registry, notifier, toDTO, poolCfg("local"))
	remotePool := worker.New(*remoteWorkers, store, remoteQueue, registry, notifier, toDTO, poolCfg("remote"))
	// Jobs a previous process accepted but didn't finish. Pushed before the
	// pools start so they keep their place ahead of new submissions of equal
	// priority (the queue is FIFO by CreatedAt within a priority).
	for _, j := range store.Recover() {
		q := queue
		if registry.IsRemote(j.Model) {
			q = remoteQueue
		}
		q.Push(j.ID, j.Priority, j.CreatedAt)
		slog.Info("requeued job from previous run", "id", j.ID, "model", j.Model)
	}
	pool.Start(ctx)
	remotePool.Start(ctx)

	srv := api.NewServer(store, queue, remoteQueue, registry, defaultPrompts, normalizedDefaultLang)
	addr := fmt.Sprintf(":%d", *port)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Routes(web.Handler()),
		ReadHeaderTimeout: 10 * time.Second,
	}

	modelIDs := make([]string, 0, len(registry.List()))
	for _, m := range registry.List() {
		modelIDs = append(modelIDs, m.ID())
	}
	slices.Sort(modelIDs)

	go func() {
		v := version.Get()
		slog.Info("transcriber api listening",
			"addr", addr,
			"commit", v.Commit,
			"build_time", v.BuildTime,
			"workers", *workers,
			"remote_workers", *remoteWorkers,
			"default_model", *defaultModel,
			"default_language", normalizedDefaultLang,
			"models", modelIDs,
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
	remoteQueue.Close()
	cancel()
	pool.Wait()
	remotePool.Wait()
	notifier.Shutdown()
	slog.Info("shutdown complete")
}

// readPromptFile returns the trimmed contents of path, or "" when path is
// empty or the file does not exist. Any other read error is fatal.
func readPromptFile(path, kind string) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		p := strings.TrimSpace(string(b))
		slog.Info("loaded default "+kind, "path", path, "bytes", len(p))
		return p
	case os.IsNotExist(err):
		slog.Info("no default "+kind+" file found", "path", path)
		return ""
	default:
		slog.Error("reading default "+kind+" file", "path", path, "err", err)
		os.Exit(1)
		return ""
	}
}
