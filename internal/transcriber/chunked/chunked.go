// Package chunked wraps a Transcriber to split long audio into overlapping chunks and stitch results.
package chunked

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"transcriber/internal/transcriber"
)

type Config struct {
	ChunkLengthSec float64
	OverlapSec     float64
	// Parallelism only helps when the inner adapter doesn't serialize on a single GPU.
	Parallelism int
	FFmpegBin   string
	FFprobeBin  string
}

type Adapter struct {
	Inner transcriber.Transcriber
	cfg   Config
}

func New(inner transcriber.Transcriber, cfg Config) *Adapter {
	if cfg.ChunkLengthSec <= 0 {
		cfg.ChunkLengthSec = 300
	}
	if cfg.OverlapSec <= 0 {
		cfg.OverlapSec = 3
	}
	if cfg.Parallelism < 1 {
		cfg.Parallelism = 1
	}
	return &Adapter{Inner: inner, cfg: cfg}
}

func (a *Adapter) ID() string   { return a.Inner.ID() }
func (a *Adapter) Name() string { return a.Inner.Name() }

func (a *Adapter) Transcribe(ctx context.Context, req transcriber.Request, onProgress transcriber.ProgressFunc) (*transcriber.Result, error) {
	duration, err := ProbeDuration(ctx, a.cfg.FFprobeBin, req.InputPath)
	if err != nil {
		return nil, fmt.Errorf("chunked: probe duration: %w", err)
	}
	if duration <= a.cfg.ChunkLengthSec {
		return a.Inner.Transcribe(ctx, req, onProgress)
	}

	plan := Plan(duration, a.cfg.ChunkLengthSec, a.cfg.OverlapSec)
	if len(plan) <= 1 {
		return a.Inner.Transcribe(ctx, req, onProgress)
	}

	if err := os.MkdirAll(req.WorkDir, 0o755); err != nil {
		return nil, err
	}
	// Extracted chunk wavs are ~32 kB/s of audio (16 kHz mono s16le), i.e.
	// ~115 MB per hour. They live in scratch and are deleted with it.
	chunksDir := filepath.Join(req.WorkDir, "chunks")
	if err := os.MkdirAll(chunksDir, 0o755); err != nil {
		return nil, err
	}

	parts := make([]*transcriber.Transcription, len(plan))
	model := a.Inner.ID()
	progress := newProgressTracker(len(plan), onProgress)

	sem := make(chan struct{}, a.cfg.Parallelism)
	var wg sync.WaitGroup
	errCh := make(chan error, len(plan))

	for i, ch := range plan {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(i int, ch Chunk) {
			defer wg.Done()
			defer func() { <-sem }()
			part, err := a.transcribeChunk(ctx, req, chunksDir, ch, progress.reporter(i))
			if err != nil {
				errCh <- fmt.Errorf("chunk %d (%.1fs–%.1fs): %w", ch.Index, ch.Start, ch.End, err)
				return
			}
			parts[i] = part
			progress.done(i)
		}(i, ch)
	}

	wg.Wait()
	close(errCh)
	if err := <-errCh; err != nil {
		return nil, err
	}

	merged := stitch(plan, parts)
	return &transcriber.Result{
		Transcription: merged,
		ModelUsed:     model,
	}, nil
}

func (a *Adapter) transcribeChunk(ctx context.Context, req transcriber.Request, chunksDir string, ch Chunk, onProgress transcriber.ProgressFunc) (*transcriber.Transcription, error) {
	wavPath := filepath.Join(chunksDir, fmt.Sprintf("%03d.wav", ch.Index))
	if err := ExtractChunk(ctx, a.cfg.FFmpegBin, req.InputPath, wavPath, ch.Start, ch.Duration()); err != nil {
		return nil, err
	}
	chunkWorkDir := filepath.Join(chunksDir, fmt.Sprintf("%03d", ch.Index))
	if err := os.MkdirAll(chunkWorkDir, 0o755); err != nil {
		return nil, err
	}
	subReq := transcriber.Request{
		InputPath: wavPath,
		Language:  req.Language,
		WorkDir:   chunkWorkDir,
		Prompt:    req.Prompt,
		Options:   req.Options,
	}
	res, err := a.Inner.Transcribe(ctx, subReq, onProgress)
	if err != nil {
		return nil, err
	}
	return res.Transcription, nil
}

// progressTracker aggregates per-chunk progress into an overall fraction.
type progressTracker struct {
	total    int
	onUpdate transcriber.ProgressFunc
	mu       sync.Mutex
	frac     []float64
}

func newProgressTracker(total int, onUpdate transcriber.ProgressFunc) *progressTracker {
	return &progressTracker{total: total, onUpdate: onUpdate, frac: make([]float64, total)}
}

func (p *progressTracker) reporter(i int) transcriber.ProgressFunc {
	if p.onUpdate == nil {
		return nil
	}
	return func(f float64) {
		p.mu.Lock()
		if f > p.frac[i] {
			p.frac[i] = f
		}
		sum := 0.0
		for _, v := range p.frac {
			sum += v
		}
		overall := sum / float64(p.total)
		p.mu.Unlock()
		p.onUpdate(overall)
	}
}

func (p *progressTracker) done(i int) {
	if p.onUpdate == nil {
		return
	}
	p.mu.Lock()
	p.frac[i] = 1.0
	sum := 0.0
	for _, v := range p.frac {
		sum += v
	}
	overall := sum / float64(p.total)
	p.mu.Unlock()
	p.onUpdate(overall)
}
