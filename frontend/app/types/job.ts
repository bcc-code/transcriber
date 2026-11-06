export type JobStatus =
  | "QUEUED"
  | "RUNNING"
  | "COMPLETED"
  | "FAILED"
  | "CANCELED";

export interface TranscribeJob {
  id: string;
  path: string;
  language: string;
  format: string;
  output_path: string;
  progress: number;
  status: JobStatus;
  result: string;
  results?: string[];
  callback: string;
  model: string;
  prompt?: string;
  subtitle_options?: SubtitleOptions;
  timeout_seconds?: number;
  duration: string;
  priority: number;
  error?: string;
}

export interface TranscribeInput {
  path: string;
  output_path: string;
  language?: string;
  format?: string;
  callback?: string;
  priority?: number;
  model?: string;
  prompt?: string;
  subtitle_options?: SubtitleOptions;
  timeout_seconds?: number;
}

export interface SubtitleOptions {
  max_chars_per_line?: number;
  max_lines_per_cue?: number;
}

export interface ModelInfo {
  id: string;
  name: string;
  default: boolean;
  /** Alternate IDs that resolve to this model. */
  aliases: string[];
  /** Per-model default prompt; empty = server-wide default applies. */
  prompt: string;
  /** False for models that ignore prompts (e.g. omnilingual). */
  supports_prompt: boolean;
}

export interface ServerConfig {
  default_prompt: string;
  default_language: string;
}
