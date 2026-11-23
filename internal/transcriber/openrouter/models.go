package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
)

// Model is a speech-to-text model listed by OpenRouter.
type Model struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListTranscriptionModels returns the models OpenRouter serves on the
// transcription endpoint, i.e. those whose output modality is
// "transcription". baseURL defaults to https://openrouter.ai/api/v1; apiKey
// may be empty (the listing is public).
func ListTranscriptionModels(ctx context.Context, client *http.Client, baseURL, apiKey string) ([]Model, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	url := strings.TrimRight(baseURL, "/") + "/models?input_modalities=audio&output_modalities=transcription"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-OpenRouter-Title", appTitle)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openrouter list models: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openrouter list models: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > maxErrBodySize {
			msg = msg[:maxErrBodySize] + "…"
		}
		return nil, &apiError{Status: resp.StatusCode, Body: msg}
	}

	var out struct {
		Data []struct {
			Model
			Architecture struct {
				OutputModalities []string `json:"output_modalities"`
			} `json:"architecture"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("openrouter list models: %w", err)
	}
	models := []Model{}
	for _, d := range out.Data {
		// Filter client-side too, in case the query parameter is ignored.
		if d.ID == "" || !slices.Contains(d.Architecture.OutputModalities, "transcription") {
			continue
		}
		models = append(models, d.Model)
	}
	slices.SortFunc(models, func(a, b Model) int { return strings.Compare(a.ID, b.ID) })
	return models, nil
}
