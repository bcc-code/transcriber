package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"transcriber/internal/jobs"
	"transcriber/internal/modelprompts"
	"transcriber/internal/transcriber"
)

// maxModelPromptLen bounds per-model prompts. whisper.cpp only keeps ~224
// tokens anyway; this just stops accidental megabyte pastes into the file.
const maxModelPromptLen = 4000

type Server struct {
	store           *jobs.Store
	queue           *jobs.Queue
	registry        *transcriber.Registry
	modelPrompts    *modelprompts.Store
	defaultPrompt   string
	defaultLanguage string
}

func NewServer(store *jobs.Store, queue *jobs.Queue, registry *transcriber.Registry, modelPrompts *modelprompts.Store, defaultPrompt, defaultLanguage string) *Server {
	return &Server{store: store, queue: queue, registry: registry, modelPrompts: modelPrompts, defaultPrompt: defaultPrompt, defaultLanguage: defaultLanguage}
}

// Routes mounts staticHandler at "/" when non-nil; API patterns take precedence.
func (s *Server) Routes(staticHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /transcription/job", s.createJob)
	mux.HandleFunc("GET /transcription/job/{id}", s.getJob)
	mux.HandleFunc("DELETE /transcription/job/{id}", s.cancelJob)
	mux.HandleFunc("GET /transcription/jobs", s.listJobs)
	mux.HandleFunc("GET /models", s.listModels)
	mux.HandleFunc("PUT /models/{id}/prompt", s.setModelPrompt)
	mux.HandleFunc("GET /config", s.config)
	mux.HandleFunc("GET /stats", s.stats)
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /openapi.yaml", s.openapi)
	mux.HandleFunc("GET /docs", s.docs)
	mux.HandleFunc("GET /docs/redoc.standalone.js", s.docsRedocJS)
	if staticHandler != nil {
		mux.Handle("/", staticHandler)
	}
	return mux
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	var in TranscribeInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if in.Path == "" {
		writeError(w, http.StatusBadRequest, "missing path")
		return
	}
	if in.OutputPath == "" {
		writeError(w, http.StatusBadRequest, "missing output_path")
		return
	}
	if in.TimeoutSeconds < 0 {
		writeError(w, http.StatusBadRequest, "timeout_seconds must be >= 0")
		return
	}
	if in.Priority <= 0 {
		in.Priority = 1000
	}

	model := in.Model
	var adapter transcriber.Transcriber
	if model == "" {
		def, ok := s.registry.Default()
		if !ok {
			writeError(w, http.StatusInternalServerError, "no default model configured")
			return
		}
		adapter = def
		model = def.ID()
	} else {
		t, ok := s.registry.Get(model)
		if !ok {
			writeError(w, http.StatusBadRequest, "unknown model: "+model)
			return
		}
		adapter = t
	}

	// Request prompt > per-model prompt > server-wide default prompt.
	prompt := in.Prompt
	if prompt == "" {
		p, err := s.modelPrompts.Get(r.Context(), adapter.ID())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "loading model prompt: "+err.Error())
			return
		}
		prompt = p
	}
	if prompt == "" {
		prompt = s.defaultPrompt
	}

	language := in.Language
	if language == "" {
		language = s.defaultLanguage
	}

	now := time.Now()
	job := jobs.Job{
		ID:             newID(),
		Path:           in.Path,
		Language:       transcriber.NormalizeLanguage(language),
		Format:         in.Format,
		OutputPath:     in.OutputPath,
		Priority:       in.Priority,
		Callback:       in.Callback,
		Model:          model,
		Prompt:         prompt,
		Timeout:        time.Duration(in.TimeoutSeconds) * time.Second,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		Status:         jobs.StatusPending,
		CreatedAt:      now,
	}
	if in.SubtitleOptions != nil {
		job.Subtitle = *in.SubtitleOptions
	}
	stored, created := s.store.CreateOrGet(job)
	if !created {
		writeJSON(w, http.StatusOK, ToDTO(stored))
		return
	}
	s.queue.Push(stored.ID, stored.Priority, stored.CreatedAt)
	writeJSON(w, http.StatusAccepted, ToDTO(stored))
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.store.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, ToDTO(job))
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.store.Get(id); !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if !s.store.Cancel(id) {
		s.store.Update(id, func(j *jobs.Job) {
			if j.Status == jobs.StatusPending {
				j.Status = jobs.StatusCanceled
				j.EndedAt = time.Now()
			}
		})
	}
	job, _ := s.store.Get(id)
	writeJSON(w, http.StatusOK, ToDTO(job))
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	all := s.store.List()
	out := make([]TranscribeJob, 0, len(all))
	for _, j := range all {
		out = append(out, ToDTO(j))
	}
	writeJSON(w, http.StatusOK, out)
}

type modelInfo struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Default bool     `json:"default"`
	Aliases []string `json:"aliases"`
	// Prompt is the per-model default prompt; empty = falls back to the
	// server-wide default prompt.
	Prompt string `json:"prompt"`
	// SupportsPrompt is false for models that ignore prompts entirely.
	SupportsPrompt bool `json:"supports_prompt"`
}

func (s *Server) modelInfo(m transcriber.Transcriber, prompt string) modelInfo {
	aliases := s.registry.AliasesFor(m.ID())
	if aliases == nil {
		aliases = []string{}
	}
	return modelInfo{
		ID:      m.ID(),
		Name:    m.Name(),
		Default: m.ID() == s.registry.DefaultID(),
		Aliases: aliases,
		Prompt:  prompt,

		SupportsPrompt: transcriber.SupportsPrompt(m),
	}
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	prompts, err := s.modelPrompts.All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading model prompts: "+err.Error())
		return
	}
	models := s.registry.List()
	out := make([]modelInfo, 0, len(models))
	for _, m := range models {
		out = append(out, s.modelInfo(m, prompts[m.ID()]))
	}
	writeJSON(w, http.StatusOK, out)
}

// setModelPrompt replaces a model's default prompt; an empty prompt clears it.
// Aliases resolve to their canonical model.
func (s *Server) setModelPrompt(w http.ResponseWriter, r *http.Request) {
	m, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown model")
		return
	}
	var in struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if !transcriber.SupportsPrompt(m) && in.Prompt != "" {
		writeError(w, http.StatusBadRequest, "model does not support prompts")
		return
	}
	if len(in.Prompt) > maxModelPromptLen {
		writeError(w, http.StatusBadRequest, "prompt too long")
		return
	}
	if err := s.modelPrompts.Set(r.Context(), m.ID(), in.Prompt); err != nil {
		writeError(w, http.StatusInternalServerError, "saving prompt: "+err.Error())
		return
	}
	prompt, err := s.modelPrompts.Get(r.Context(), m.ID())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading prompt: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.modelInfo(m, prompt))
}

func (s *Server) config(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"default_prompt":   s.defaultPrompt,
		"default_language": s.defaultLanguage,
	})
}

// stats reports counts with capitalized JSON keys; Processed = any terminal status.
func (s *Server) stats(w http.ResponseWriter, _ *http.Request) {
	var out struct {
		Queued    int `json:"Queued"`
		Running   int `json:"Running"`
		Processed int `json:"Processed"`
	}
	for _, j := range s.store.List() {
		switch j.Status {
		case jobs.StatusPending:
			out.Queued++
		case jobs.StatusRunning:
			out.Running++
		case jobs.StatusCompleted, jobs.StatusFailed, jobs.StatusCanceled:
			out.Processed++
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) ready(w http.ResponseWriter, _ *http.Request) {
	if _, ok := s.registry.Default(); !ok {
		writeError(w, http.StatusServiceUnavailable, "no default model")
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
