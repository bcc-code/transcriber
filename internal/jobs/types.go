package jobs

import (
	"time"

	"transcriber/internal/formats"
)

const (
	StatusPending   = "QUEUED"
	StatusRunning   = "RUNNING"
	StatusCompleted = "COMPLETED"
	StatusFailed    = "FAILED"
	StatusCanceled  = "CANCELED"
)

type Job struct {
	ID             string
	Path           string
	Language       string
	Format         string
	OutputPath     string
	Priority       int
	Callback       string
	Model          string
	Prompt         string
	Subtitle       formats.SubtitleOptions
	Timeout        time.Duration
	IdempotencyKey string

	Status   string
	Progress int
	Result   string
	Results  []string
	Error    string
	// NoSpeech marks a completed job whose audio held nothing to transcribe.
	// The output files are still written, empty.
	NoSpeech bool

	CreatedAt time.Time
	StartedAt time.Time
	EndedAt   time.Time
	Duration  time.Duration
}
