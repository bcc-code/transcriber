package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"transcriber/internal/callback"
	"transcriber/internal/formats"
	"transcriber/internal/jobs"
	"transcriber/internal/transcriber"
)

// Config holds the pool's tunables.
type Config struct {
	// DefaultTimeout <= 0 disables the wall-clock cap; a job's own Timeout
	// takes precedence when non-zero.
	DefaultTimeout time.Duration

	// ScratchRoot is where per-job working directories are created; empty uses
	// os.TempDir(). Keep it on local disk — chunk extraction writes ~115 MB per
	// hour of audio, and routing that through network storage is wasted I/O.
	ScratchRoot string

	// KeepWorkDirs retains per-job scratch directories after the job finishes,
	// for inspecting a bad transcription. Off by default: they are large.
	KeepWorkDirs bool
}

// Pool is a fixed-size worker pool that runs jobs through the configured adapter.
type Pool struct {
	workers  int
	store    *jobs.Store
	queue    *jobs.Queue
	registry *transcriber.Registry
	notifier *callback.Notifier
	dtoFn    func(jobs.Job) any
	cfg      Config
	wg       sync.WaitGroup
}

// New builds a pool.
func New(
	workers int,
	store *jobs.Store,
	queue *jobs.Queue,
	registry *transcriber.Registry,
	notifier *callback.Notifier,
	dtoFn func(jobs.Job) any,
	cfg Config,
) *Pool {
	if workers < 1 {
		workers = 1
	}
	return &Pool{
		workers:  workers,
		store:    store,
		queue:    queue,
		registry: registry,
		notifier: notifier,
		dtoFn:    dtoFn,
		cfg:      cfg,
	}
}

func (p *Pool) Start(ctx context.Context) {
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.worker(ctx, i)
	}
}

func (p *Pool) Wait() { p.wg.Wait() }

func (p *Pool) worker(ctx context.Context, n int) {
	defer p.wg.Done()
	log := slog.With("worker", n)
	for {
		id, ok := p.queue.Pop(ctx)
		if !ok {
			log.Debug("worker exiting")
			return
		}
		p.runJob(ctx, id, log)
	}
}

func (p *Pool) runJob(parent context.Context, id string, log *slog.Logger) {
	job, ok := p.store.Get(id)
	if !ok {
		log.Warn("job vanished from store", "id", id)
		return
	}
	if job.Status == jobs.StatusCanceled {
		return
	}

	adapter, err := p.pickAdapter(job)
	if err != nil {
		p.markFailed(id, err)
		p.fireCallback(id)
		return
	}

	// Scratch space for the adapter's intermediates, removed when the job ends
	// (including on timeout, cancel, and failure) so nothing accumulates in the
	// caller's output_path.
	workDir, cleanupWorkDir, err := p.newWorkDir(id)
	if err != nil {
		p.markFailed(id, fmt.Errorf("create work dir: %w", err))
		p.fireCallback(id)
		return
	}
	defer cleanupWorkDir()

	timeout := job.Timeout
	if timeout <= 0 {
		timeout = p.cfg.DefaultTimeout
	}
	var ctx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, timeout)
	} else {
		ctx, cancel = context.WithCancel(parent)
	}
	defer cancel()
	p.store.SetCancel(id, cancel)
	defer p.store.ClearCancel(id)

	startedAt := time.Now()
	p.store.Update(id, func(j *jobs.Job) {
		j.Status = jobs.StatusRunning
		j.StartedAt = startedAt
	})
	log.Info("job started", "id", id, "model", adapter.ID(), "path", job.Path)

	onProgress := func(progress float64) {
		pct := max(0, min(100, int(progress*100)))
		p.store.Update(id, func(j *jobs.Job) {
			j.Progress = max(j.Progress, pct)
		})
	}

	req := transcriber.Request{
		InputPath: job.Path,
		Language:  job.Language,
		WorkDir:   workDir,
		Prompt:    job.Prompt,
	}

	res, err := adapter.Transcribe(ctx, req, onProgress)
	if err != nil {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			p.markFailed(id, errors.New("timeout"))
			log.Warn("job timed out", "id", id, "timeout", timeout)
		case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
			p.markCanceled(id)
			log.Info("job canceled", "id", id)
		default:
			p.markFailed(id, err)
			log.Error("job failed", "id", id, "err", err)
		}
		p.fireCallback(id)
		return
	}

	wantFormats := formats.Parse(job.Format)
	basename := filepath.Base(job.Path)
	primary := ""
	written := make([]string, 0, len(wantFormats))
	for _, f := range wantFormats {
		path, err := formats.Write(f, res.Transcription, job.OutputPath, basename, job.Subtitle)
		if err != nil {
			p.markFailed(id, fmt.Errorf("write %s: %w", f, err))
			p.fireCallback(id)
			return
		}
		written = append(written, path)
		if f == formats.JSON || primary == "" {
			primary = path
		}
	}

	endedAt := time.Now()
	p.store.Update(id, func(j *jobs.Job) {
		j.Status = jobs.StatusCompleted
		j.Progress = 100
		j.EndedAt = endedAt
		j.Duration = endedAt.Sub(startedAt)
		j.Result = primary
		j.Results = written
	})
	log.Info("job completed", "id", id, "duration", endedAt.Sub(startedAt))
	p.fireCallback(id)
}

// newWorkDir allocates per-job scratch space and returns a cleanup func that
// the caller must defer. The cleanup is a no-op (beyond a log line) when
// KeepWorkDirs is set.
func (p *Pool) newWorkDir(id string) (string, func(), error) {
	root := p.cfg.ScratchRoot
	if root == "" {
		root = os.TempDir()
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(root, "transcriber-"+safeName(id)+"-")
	if err != nil {
		return "", nil, err
	}
	if p.cfg.KeepWorkDirs {
		return dir, func() {
			slog.Info("retaining job work dir", "id", id, "dir", dir)
		}, nil
	}
	return dir, func() {
		if err := os.RemoveAll(dir); err != nil {
			slog.Warn("work dir cleanup failed", "id", id, "dir", dir, "err", err)
		}
	}, nil
}

// safeName reduces s to characters safe in a single filename component. The ID
// goes into the work dir name so `-keep-work-dirs` output is traceable back to
// a job, but os.MkdirTemp rejects patterns containing a path separator and IDs
// are only hex by current convention.
func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, s)
}

func (p *Pool) pickAdapter(j jobs.Job) (transcriber.Transcriber, error) {
	if j.Model != "" {
		a, ok := p.registry.Get(j.Model)
		if !ok {
			return nil, fmt.Errorf("unknown model: %s", j.Model)
		}
		return a, nil
	}
	a, ok := p.registry.Default()
	if !ok {
		return nil, errors.New("no default model configured")
	}
	return a, nil
}

func (p *Pool) markFailed(id string, err error) {
	p.store.Update(id, func(j *jobs.Job) {
		j.Status = jobs.StatusFailed
		j.Error = err.Error()
		j.EndedAt = time.Now()
		if !j.StartedAt.IsZero() {
			j.Duration = j.EndedAt.Sub(j.StartedAt)
		}
	})
}

func (p *Pool) markCanceled(id string) {
	p.store.Update(id, func(j *jobs.Job) {
		j.Status = jobs.StatusCanceled
		j.EndedAt = time.Now()
		if !j.StartedAt.IsZero() {
			j.Duration = j.EndedAt.Sub(j.StartedAt)
		}
	})
}

func (p *Pool) fireCallback(id string) {
	if p.notifier == nil {
		return
	}
	job, ok := p.store.Get(id)
	if !ok || job.Callback == "" {
		return
	}
	body, err := json.Marshal(p.dtoFn(job))
	if err != nil {
		slog.Warn("callback marshal failed", "id", id, "err", err)
		return
	}
	if err := p.notifier.Enqueue(job.Callback, body); err != nil {
		slog.Warn("callback enqueue failed", "id", id, "err", err)
	}
}
