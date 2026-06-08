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
}

export interface ServerConfig {
  default_prompt: string;
  default_language: string;
}
