package api

import (
	"time"

	"transcriber/internal/formats"
	"transcriber/internal/jobs"
)

type TranscribeInput struct {
	Path            string                   `json:"path"`
	Language        string                   `json:"language"`
	Format          string                   `json:"format"`
	Callback        string                   `json:"callback,omitempty"`
	OutputPath      string                   `json:"output_path"`
	Priority        int                      `json:"priority,omitempty"`
	Model           string                   `json:"model,omitempty"`
	Prompt          string                   `json:"prompt,omitempty"`
	SubtitleOptions *formats.SubtitleOptions `json:"subtitle_options,omitempty"`
	TimeoutSeconds  int                      `json:"timeout_seconds,omitempty"`
}

type TranscribeJob struct {
	ID              string                   `json:"id"`
	Path            string                   `json:"path"`
	Language        string                   `json:"language"`
	OutputFormat    string                   `json:"format"`
	OutputPath      string                   `json:"output_path"`
	Progress        int                      `json:"progress"`
	Status          string                   `json:"status"`
	Result          string                   `json:"result"`
	Results         []string                 `json:"results,omitempty"`
	Callback        string                   `json:"callback"`
	Model           string                   `json:"model"`
	Prompt          string                   `json:"prompt,omitempty"`
	SubtitleOptions *formats.SubtitleOptions `json:"subtitle_options,omitempty"`
	Duration        string                   `json:"duration"`
	Priority        int                      `json:"priority"`
	Error           string                   `json:"error,omitempty"`
	NoSpeech        bool                     `json:"no_speech"`
	TimeoutSeconds  int                      `json:"timeout_seconds,omitempty"`
}

func ToDTO(j jobs.Job) TranscribeJob {
	var sub *formats.SubtitleOptions
	if j.Subtitle != (formats.SubtitleOptions{}) {
		s := j.Subtitle
		sub = &s
	}
	return TranscribeJob{
		ID:              j.ID,
		Path:            j.Path,
		Language:        j.Language,
		OutputFormat:    j.Format,
		OutputPath:      j.OutputPath,
		Progress:        j.Progress,
		Status:          j.Status,
		Result:          j.Result,
		Results:         j.Results,
		Callback:        j.Callback,
		Model:           j.Model,
		Prompt:          j.Prompt,
		SubtitleOptions: sub,
		Duration:        formatDuration(j.Duration),
		Priority:        j.Priority,
		Error:           j.Error,
		NoSpeech:        j.NoSpeech,
		TimeoutSeconds:  int(j.Timeout / time.Second),
	}
}

func formatDuration(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return "duration: " + d.String()
}
