package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"transcriber/internal/jobs"
	"transcriber/internal/transcriber"
	"transcriber/internal/transcriber/stub"
)

// stub.Adapter sleeps ~2 s before returning, so any timeout under that should
// reliably mark the job FAILED with error "timeout".
func TestPool_Timeouts(t *testing.T) {
	cases := []struct {
		name           string
		defaultTimeout time.Duration
		jobTimeout     time.Duration
	}{
		{"per-job override applies", 0, 300 * time.Millisecond},
		{"pool default applies when job has none", 200 * time.Millisecond, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runJobAndWait(t, tc.defaultTimeout, tc.jobTimeout)
			if got.Status != jobs.StatusFailed {
				t.Fatalf("status: got %q, want FAILED", got.Status)
			}
			if got.Error != "timeout" {
				t.Fatalf("error: got %q, want %q", got.Error, "timeout")
			}
		})
	}
}

// scratchAdapter writes intermediates into req.WorkDir the way the real
// whisper.cpp adapters do, so the test can assert none of it reaches the
// caller's output_path and that the scratch dir is reclaimed.
type scratchAdapter struct {
	workDirs chan string
}

func (a *scratchAdapter) ID() string   { return "scratch" }
func (a *scratchAdapter) Name() string { return "Scratch" }

func (a *scratchAdapter) Transcribe(ctx context.Context, req transcriber.Request, _ transcriber.ProgressFunc) (*transcriber.Result, error) {
	if req.WorkDir == "" {
		return nil, errors.New("WorkDir not set")
	}
	if err := os.MkdirAll(filepath.Join(req.WorkDir, "chunks"), 0o755); err != nil {
		return nil, err
	}
	for _, name := range []string{"whispercpp_out.json", filepath.Join("chunks", "000.wav")} {
		if err := os.WriteFile(filepath.Join(req.WorkDir, name), []byte("junk"), 0o644); err != nil {
			return nil, err
		}
	}
	a.workDirs <- req.WorkDir
	return &transcriber.Result{
		Transcription: &transcriber.Transcription{
			Language: "en",
			Text:     "hello",
			Segments: []transcriber.Segment{{ID: 0, Start: 0, End: 1, Text: "hello"}},
		},
		ModelUsed: "scratch",
	}, nil
}

func TestPool_IntermediatesStayOutOfOutputPath(t *testing.T) {
	tmp := t.TempDir()
	outPath := filepath.Join(tmp, "out")

	store := jobs.NewStore(0)
	queue := jobs.NewQueue()
	t.Cleanup(queue.Close)

	adapter := &scratchAdapter{workDirs: make(chan string, 1)}
	registry := transcriber.NewRegistry("scratch")
	registry.Register(adapter)

	// ScratchRoot under tmp keeps the test off the real temp dir.
	pool := New(1, store, queue, registry, nil, func(j jobs.Job) any { return j },
		Config{ScratchRoot: filepath.Join(tmp, "scratch")})
	pool.Start(t.Context())

	now := time.Now()
	job := jobs.Job{
		ID:         "clean-output",
		Path:       filepath.Join(tmp, "input.wav"),
		OutputPath: outPath,
		Format:     "json,txt",
		Model:      "scratch",
		Status:     jobs.StatusPending,
		CreatedAt:  now,
	}
	store.Create(job)
	queue.Push(job.ID, 1, now)

	var final jobs.Job
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := store.Get(job.ID); ok && isTerminal(j.Status) {
			final = j
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.Status != jobs.StatusCompleted {
		t.Fatalf("status = %q (%s), want COMPLETED", final.Status, final.Error)
	}

	// output_path holds deliverables and nothing else.
	entries, err := os.ReadDir(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := map[string]bool{"input.wav.json": true, "input.wav.txt": true}
	if len(names) != len(want) {
		t.Errorf("output_path contains %v, want exactly %v", names, want)
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("intermediate %q leaked into output_path", n)
		}
	}

	// And the scratch dir the adapter wrote into is reclaimed. Cleanup is a
	// deferred call that runs *after* the status is published — marking the job
	// complete deliberately isn't blocked on removing a potentially large
	// directory — so poll rather than checking once.
	var wd string
	select {
	case wd = <-adapter.workDirs:
	default:
		t.Fatal("adapter never reported a work dir")
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(wd); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("work dir %s still exists after job", wd)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func runJobAndWait(t *testing.T, defaultTimeout, jobTimeout time.Duration) jobs.Job {
	t.Helper()
	tmp := t.TempDir()

	store := jobs.NewStore(0)
	queue := jobs.NewQueue()
	t.Cleanup(queue.Close)

	registry := transcriber.NewRegistry("stub")
	registry.Register(stub.New("stub", "Stub"))

	pool := New(1, store, queue, registry, nil, func(j jobs.Job) any { return j }, Config{DefaultTimeout: defaultTimeout})
	pool.Start(t.Context())

	now := time.Now()
	job := jobs.Job{
		ID:         t.Name(),
		Path:       "/dev/null",
		OutputPath: filepath.Join(tmp, "out"),
		Model:      "stub",
		Timeout:    jobTimeout,
		Status:     jobs.StatusPending,
		CreatedAt:  now,
	}
	if err := os.MkdirAll(job.OutputPath, 0o755); err != nil {
		t.Fatal(err)
	}
	store.Create(job)
	queue.Push(job.ID, 1, now)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := store.Get(job.ID); ok && isTerminal(j.Status) {
			return j
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("job did not reach a terminal state")
	return jobs.Job{}
}

func isTerminal(s string) bool {
	return s == jobs.StatusCompleted || s == jobs.StatusFailed || s == jobs.StatusCanceled
}
