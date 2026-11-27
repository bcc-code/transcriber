package jobs

import (
	"path/filepath"
	"testing"
	"time"

	"transcriber/internal/formats"
)

const week = 7 * 24 * time.Hour

func openTemp(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(t.Context(), path, week)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPrune_DropsOnlyOldTerminalJobs(t *testing.T) {
	s := NewStore(week)
	now := time.Now()
	old := now.Add(-week - time.Hour)
	recent := now.Add(-time.Hour)

	s.Create(Job{ID: "old-done", Status: StatusCompleted, CreatedAt: old, EndedAt: old})
	s.Create(Job{ID: "old-failed", Status: StatusFailed, CreatedAt: old, EndedAt: old})
	s.Create(Job{ID: "old-canceled", Status: StatusCanceled, CreatedAt: old, EndedAt: old})
	s.Create(Job{ID: "recent-done", Status: StatusCompleted, CreatedAt: recent, EndedAt: recent})
	// Active jobs are kept however old they are.
	s.Create(Job{ID: "old-queued", Status: StatusPending, CreatedAt: old})
	s.Create(Job{ID: "old-running", Status: StatusRunning, CreatedAt: old})
	s.SetCancel("old-done", func() {})

	if n := s.Prune(now); n != 3 {
		t.Fatalf("pruned %d, want 3", n)
	}
	for _, id := range []string{"recent-done", "old-queued", "old-running"} {
		if _, ok := s.Get(id); !ok {
			t.Errorf("%s was pruned", id)
		}
	}
	for _, id := range []string{"old-done", "old-failed", "old-canceled"} {
		if _, ok := s.Get(id); ok {
			t.Errorf("%s survived", id)
		}
	}
	if s.Cancel("old-done") {
		t.Error("cancel entry of pruned job was kept")
	}
}

func TestPrune_DisabledWithoutRetention(t *testing.T) {
	s := NewStore(0)
	ancient := time.Now().Add(-100 * week)
	s.Create(Job{ID: "a", Status: StatusCompleted, EndedAt: ancient})
	if n := s.Prune(time.Now()); n != 0 {
		t.Fatalf("pruned %d with retention disabled", n)
	}
}

func TestPrune_FreesIdempotencyKey(t *testing.T) {
	s := NewStore(week)
	old := time.Now().Add(-2 * week)
	s.CreateOrGet(Job{ID: "old", IdempotencyKey: "k1", Status: StatusCompleted, EndedAt: old})
	s.Prune(time.Now())

	got, created := s.CreateOrGet(Job{ID: "fresh", IdempotencyKey: "k1", Status: StatusPending})
	if !created || got.ID != "fresh" {
		t.Fatalf("key not reusable after prune: created=%v id=%q", created, got.ID)
	}
}

func TestOpen_RoundTripsJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	created := time.UnixMilli(time.Now().UnixMilli())
	in := Job{
		ID:             "a",
		Path:           "/in/a.wav",
		Language:       "no",
		Format:         "json,srt",
		OutputPath:     "/out",
		Priority:       7,
		Callback:       "http://cb",
		Model:          "stub",
		Prompt:         "BCC",
		Subtitle:       formats.SubtitleOptions{MaxCharsPerLine: 37, MaxLinesPerCue: 1},
		Timeout:        90 * time.Second,
		IdempotencyKey: "key",
		Status:         StatusPending,
		CreatedAt:      created,
	}

	s := openTemp(t, path)
	s.Create(in)
	s.Update("a", func(j *Job) {
		j.Status = StatusCompleted
		j.Progress = 100
		j.Result = "/out/a.wav.json"
		j.Results = []string{"/out/a.wav.json", "/out/a.wav.srt"}
		j.StartedAt = created.Add(time.Second)
		j.EndedAt = created.Add(3 * time.Second)
		j.Duration = 2 * time.Second
		j.NoSpeech = true
	})
	want, _ := s.Get("a")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s = openTemp(t, path)
	defer s.Close()
	got, ok := s.Get("a")
	if !ok {
		t.Fatal("job lost across reopen")
	}
	if got.ID != want.ID || got.Path != want.Path || got.Language != want.Language ||
		got.Format != want.Format || got.OutputPath != want.OutputPath ||
		got.Priority != want.Priority || got.Callback != want.Callback ||
		got.Model != want.Model || got.Prompt != want.Prompt ||
		got.Subtitle != want.Subtitle || got.Timeout != want.Timeout ||
		got.IdempotencyKey != want.IdempotencyKey || got.Status != want.Status ||
		got.Progress != want.Progress || got.Result != want.Result ||
		len(got.Results) != 2 || got.Results[1] != want.Results[1] ||
		!got.CreatedAt.Equal(want.CreatedAt) || !got.StartedAt.Equal(want.StartedAt) ||
		!got.EndedAt.Equal(want.EndedAt) || got.Duration != want.Duration ||
		got.NoSpeech != want.NoSpeech {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
	if _, created := s.CreateOrGet(Job{ID: "b", IdempotencyKey: "key"}); created {
		t.Fatal("idempotency index not rebuilt on reopen")
	}
}

func TestOpen_PrunesExpiredRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	old := time.Now().Add(-week - time.Minute)

	s := openTemp(t, path)
	s.Create(Job{ID: "old", Status: StatusCompleted, CreatedAt: old, EndedAt: old})
	s.Create(Job{ID: "new", Status: StatusCompleted, CreatedAt: time.Now(), EndedAt: time.Now()})
	_ = s.Close()

	s = openTemp(t, path)
	_ = s.Close()
	// Reopen once more: the prune must have reached the database, not just memory.
	s = openTemp(t, path)
	defer s.Close()
	if _, ok := s.Get("old"); ok {
		t.Fatal("expired job survived reopen")
	}
	if _, ok := s.Get("new"); !ok {
		t.Fatal("recent job lost")
	}
}

func TestRecover_RequeuesInterruptedJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	base := time.Now()

	s := openTemp(t, path)
	s.Create(Job{ID: "queued", Status: StatusPending, CreatedAt: base.Add(2 * time.Second)})
	s.Create(Job{ID: "running", Status: StatusPending, CreatedAt: base.Add(time.Second)})
	s.Update("running", func(j *Job) {
		j.Status = StatusRunning
		j.Progress = 40
		j.StartedAt = base
	})
	s.Create(Job{ID: "done", Status: StatusCompleted, CreatedAt: base, EndedAt: base})
	_ = s.Close() // simulated redeploy

	s = openTemp(t, path)
	defer s.Close()
	got := s.Recover()
	if len(got) != 2 || got[0].ID != "running" || got[1].ID != "queued" {
		t.Fatalf("recovered %+v, want [running queued] oldest first", got)
	}
	r, _ := s.Get("running")
	if r.Status != StatusPending || r.Progress != 0 || !r.StartedAt.IsZero() {
		t.Fatalf("interrupted job not reset: %+v", r)
	}
	if d, _ := s.Get("done"); d.Status != StatusCompleted {
		t.Fatalf("finished job touched: %+v", d)
	}
}
