// Package modelprompts keeps per-model default prompts, editable at runtime
// from the UI and persisted in SQLite (table model_prompts).
package modelprompts

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"transcriber/internal/db/dbq"
)

type Store struct {
	q *dbq.Queries
}

func New(conn *sql.DB) *Store {
	return &Store{q: dbq.New(conn)}
}

// Get returns the prompt for modelID, or "" if none is set.
func (s *Store) Get(ctx context.Context, modelID string) (string, error) {
	p, err := s.q.GetModelPrompt(ctx, modelID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return p, err
}

// All returns every stored prompt keyed by model ID.
func (s *Store) All(ctx context.Context) (map[string]string, error) {
	rows, err := s.q.ListModelPrompts(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.ModelID] = r.Prompt
	}
	return out, nil
}

// Set stores prompt for modelID; an empty (or whitespace-only) prompt clears it.
func (s *Store) Set(ctx context.Context, modelID, prompt string) error {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return s.q.DeleteModelPrompt(ctx, modelID)
	}
	return s.q.UpsertModelPrompt(ctx, dbq.UpsertModelPromptParams{ModelID: modelID, Prompt: prompt})
}
