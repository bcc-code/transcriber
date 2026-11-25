package jobs

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"transcriber/internal/formats"
	"transcriber/internal/jobs/jobsdb"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is a goroutine-safe job store. Reads return copies.
//
// All jobs live in memory; when the store was opened with Open, every mutation
// is also written through to SQLite so state survives a restart.
//
// When retention > 0, Prune drops jobs in a terminal status
// (completed/failed/canceled) that ended longer ago than retention. Active jobs
// (queued/running) are never pruned.
type Store struct {
	mu        sync.RWMutex
	jobs      map[string]*Job
	cancels   map[string]context.CancelFunc
	byKey     map[string]string // IdempotencyKey -> job ID; only populated when the job has a key
	retention time.Duration

	db *sql.DB         // nil for a memory-only store
	q  *jobsdb.Queries // nil for a memory-only store
}

// NewStore creates a memory-only store. retention <= 0 disables pruning.
func NewStore(retention time.Duration) *Store {
	return &Store{
		jobs:      map[string]*Job{},
		cancels:   map[string]context.CancelFunc{},
		byKey:     map[string]string{},
		retention: retention,
	}
}

// Open creates a store backed by the SQLite database at path, applying any
// pending migrations and loading the jobs it holds. Jobs already past
// retention are pruned. Call Recover to requeue interrupted work, and Close
// when done.
func Open(ctx context.Context, path string, retention time.Duration) (*Store, error) {
	// WAL lets readers proceed during a write; synchronous=NORMAL is durable
	// across a process crash, which is the failure mode a redeploy produces.
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// Writes are already serialized by Store.mu; one connection avoids
	// SQLITE_BUSY between pooled connections.
	db.SetMaxOpenConns(1)

	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}

	s := NewStore(retention)
	s.db = db
	s.q = jobsdb.New(db)

	rows, err := s.q.ListJobs(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("load jobs: %w", err)
	}
	for _, r := range rows {
		j, err := fromRow(r)
		if err != nil {
			slog.Warn("skipping unreadable job row", "id", r.ID, "err", err)
			continue
		}
		s.jobs[j.ID] = &j
		if j.IdempotencyKey != "" {
			s.byKey[j.IdempotencyKey] = j.ID
		}
	}
	pruned := s.Prune(time.Now())
	slog.Info("job store opened", "path", path, "jobs", len(s.jobs), "pruned", pruned)
	return s, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, sub)
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	for _, r := range results {
		slog.Info("applied migration", "source", r.Source.Path, "duration", r.Duration)
	}
	return nil
}

// Close releases the database. It is a no-op for a memory-only store.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Recover prepares jobs left unfinished by a previous process to run again
// and returns them, oldest first, for the caller to push onto a queue. A job
// that was RUNNING when the process stopped is reset to QUEUED: its subprocess
// or remote request died with the process, so it starts over.
func (s *Store) Recover() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Job
	for _, j := range s.jobs {
		switch j.Status {
		case StatusRunning:
			j.Status = StatusPending
			j.Progress = 0
			j.StartedAt = time.Time{}
			s.persistLocked(j)
		case StatusPending:
		default:
			continue
		}
		out = append(out, *j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].CreatedAt.Before(out[k].CreatedAt) })
	return out
}

func (s *Store) Create(j Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createLocked(j)
}

// CreateOrGet atomically stores j or returns the existing job for j.IdempotencyKey.
// The bool reports whether j was newly inserted. An empty IdempotencyKey skips the
// index lookup and always inserts.
func (s *Store) CreateOrGet(j Job) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j.IdempotencyKey != "" {
		if id, ok := s.byKey[j.IdempotencyKey]; ok {
			if existing, ok := s.jobs[id]; ok {
				return *existing, false
			}
		}
	}
	stored := s.createLocked(j)
	return stored, true
}

func (s *Store) createLocked(j Job) Job {
	cp := j
	s.jobs[j.ID] = &cp
	if j.IdempotencyKey != "" {
		s.byKey[j.IdempotencyKey] = j.ID
	}
	s.persistLocked(&cp)
	return cp
}

func (s *Store) Get(id string) (Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

// Update applies fn under the write lock; returns false if the job does not exist.
func (s *Store) Update(id string, fn func(*Job)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return false
	}
	fn(j)
	s.persistLocked(j)
	return true
}

func (s *Store) List() []Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, *j)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// SetCancel registers a cancel func so a later DELETE can kill the subprocess.
func (s *Store) SetCancel(id string, cancel context.CancelFunc) {
	s.mu.Lock()
	s.cancels[id] = cancel
	s.mu.Unlock()
}

func (s *Store) ClearCancel(id string) {
	s.mu.Lock()
	delete(s.cancels, id)
	s.mu.Unlock()
}

// Cancel invokes the registered cancel func; returns false if none is registered.
func (s *Store) Cancel(id string) bool {
	s.mu.Lock()
	cancel := s.cancels[id]
	s.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// Prune removes terminal jobs that ended before now minus the retention
// period, from memory and the database, and returns how many it removed.
func (s *Store) Prune(now time.Time) int {
	if s.retention <= 0 {
		return 0
	}
	cutoff := now.Add(-s.retention)
	s.mu.Lock()
	defer s.mu.Unlock()
	var doomed []string
	for id, j := range s.jobs {
		if isTerminal(j.Status) && j.EndedAt.Before(cutoff) {
			doomed = append(doomed, id)
		}
	}
	if len(doomed) == 0 {
		return 0
	}
	if s.db != nil {
		if err := s.deleteRows(doomed); err != nil {
			// Keep memory and disk in step: if the rows are still on disk they
			// would come back on the next start, so try again next round.
			slog.Error("prune jobs", "err", err)
			return 0
		}
	}
	for _, id := range doomed {
		j := s.jobs[id]
		if j.IdempotencyKey != "" && s.byKey[j.IdempotencyKey] == id {
			delete(s.byKey, j.IdempotencyKey)
		}
		delete(s.jobs, id)
		delete(s.cancels, id)
	}
	return len(doomed)
}

// RunPruner calls Prune every interval until ctx is done.
func (s *Store) RunPruner(ctx context.Context, interval time.Duration) {
	if s.retention <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if n := s.Prune(now); n > 0 {
				slog.Info("pruned old jobs", "count", n, "retention", s.retention)
			}
		}
	}
}

func (s *Store) deleteRows(ids []string) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	for _, id := range ids {
		if err := q.DeleteJob(ctx, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// persistLocked writes j through to the database. A failed write is logged
// rather than returned: the in-memory state stays authoritative for this
// process, and the next mutation of the job writes it again.
func (s *Store) persistLocked(j *Job) {
	if s.q == nil {
		return
	}
	params, err := toParams(*j)
	if err == nil {
		err = s.q.UpsertJob(context.Background(), params)
	}
	if err != nil {
		slog.Error("persist job", "id", j.ID, "err", err)
	}
}

func isTerminal(status string) bool {
	return status == StatusCompleted || status == StatusFailed || status == StatusCanceled
}

func toParams(j Job) (jobsdb.UpsertJobParams, error) {
	results, err := json.Marshal(j.Results)
	if err != nil {
		return jobsdb.UpsertJobParams{}, err
	}
	return jobsdb.UpsertJobParams{
		ID:               j.ID,
		Path:             j.Path,
		Language:         j.Language,
		Format:           j.Format,
		OutputPath:       j.OutputPath,
		Priority:         int64(j.Priority),
		Callback:         j.Callback,
		Model:            j.Model,
		Prompt:           j.Prompt,
		SubtitleMaxChars: int64(j.Subtitle.MaxCharsPerLine),
		SubtitleMaxLines: int64(j.Subtitle.MaxLinesPerCue),
		TimeoutNs:        int64(j.Timeout),
		IdempotencyKey:   j.IdempotencyKey,
		Status:           j.Status,
		Progress:         int64(j.Progress),
		Result:           j.Result,
		Results:          string(results),
		Error:            j.Error,
		CreatedAt:        toMillis(j.CreatedAt),
		StartedAt:        toMillis(j.StartedAt),
		EndedAt:          toMillis(j.EndedAt),
		DurationNs:       int64(j.Duration),
	}, nil
}

func fromRow(r jobsdb.Job) (Job, error) {
	var results []string
	if err := json.Unmarshal([]byte(r.Results), &results); err != nil {
		return Job{}, fmt.Errorf("results: %w", err)
	}
	return Job{
		ID:         r.ID,
		Path:       r.Path,
		Language:   r.Language,
		Format:     r.Format,
		OutputPath: r.OutputPath,
		Priority:   int(r.Priority),
		Callback:   r.Callback,
		Model:      r.Model,
		Prompt:     r.Prompt,
		Subtitle: formats.SubtitleOptions{
			MaxCharsPerLine: int(r.SubtitleMaxChars),
			MaxLinesPerCue:  int(r.SubtitleMaxLines),
		},
		Timeout:        time.Duration(r.TimeoutNs),
		IdempotencyKey: r.IdempotencyKey,
		Status:         r.Status,
		Progress:       int(r.Progress),
		Result:         r.Result,
		Results:        results,
		Error:          r.Error,
		CreatedAt:      fromMillis(r.CreatedAt),
		StartedAt:      fromMillis(r.StartedAt),
		EndedAt:        fromMillis(r.EndedAt),
		Duration:       time.Duration(r.DurationNs),
	}, nil
}

// Times are stored as Unix milliseconds; 0 stands for the zero time.
func toMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
