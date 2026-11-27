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
  duration: string;
  priority: number;
  error?: string;
  /** True on a COMPLETED job whose audio held no speech; its files are empty. */
  no_speech?: boolean;
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
}

export interface ModelInfo {
  id: string;
  name: string;
  default: boolean;
  prompt_style: "text" | "keyterms";
  default_prompt: string;
}

export interface VersionInfo {
  commit: string;
  build_time: string;
  modified: boolean;
}

export interface ServerConfig {
  default_prompt: string;
  default_keyterms: string;
  default_language: string;
}
