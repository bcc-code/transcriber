import type {
  ModelInfo,
  ServerConfig,
  TranscribeInput,
  TranscribeJob,
} from "~/types/job";

// Same-origin paths: dev proxies via routeRules, prod is served by the embedded Go binary.
export function useApi() {
  return {
    listJobs: () => $fetch<TranscribeJob[]>(`/transcription/jobs`),
    getJob: (id: string) => $fetch<TranscribeJob>(`/transcription/job/${id}`),
    createJob: (input: TranscribeInput) =>
      $fetch<TranscribeJob>(`/transcription/job`, {
        method: "POST",
        body: input,
      }),
    cancelJob: (id: string) =>
      $fetch<TranscribeJob>(`/transcription/job/${id}`, {
        method: "DELETE",
      }),
    listModels: () => $fetch<ModelInfo[]>(`/models`),
    setModelPrompt: (id: string, prompt: string) =>
      $fetch<ModelInfo>(`/models/${encodeURIComponent(id)}/prompt`, {
        method: "PUT",
        body: { prompt },
      }),
    getConfig: () => $fetch<ServerConfig>(`/config`),
  };
}
