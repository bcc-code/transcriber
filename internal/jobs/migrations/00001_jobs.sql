-- +goose Up
-- One row per job. Times are Unix milliseconds, 0 when unset; durations are
-- nanoseconds (time.Duration). results is a JSON array of output paths.
CREATE TABLE jobs (
    id                 TEXT    PRIMARY KEY,
    path               TEXT    NOT NULL,
    language           TEXT    NOT NULL,
    format             TEXT    NOT NULL,
    output_path        TEXT    NOT NULL,
    priority           INTEGER NOT NULL,
    callback           TEXT    NOT NULL,
    model              TEXT    NOT NULL,
    prompt             TEXT    NOT NULL,
    subtitle_max_chars INTEGER NOT NULL,
    subtitle_max_lines INTEGER NOT NULL,
    timeout_ns         INTEGER NOT NULL,
    idempotency_key    TEXT    NOT NULL,
    status             TEXT    NOT NULL,
    progress           INTEGER NOT NULL,
    result             TEXT    NOT NULL,
    results            TEXT    NOT NULL,
    error              TEXT    NOT NULL,
    created_at         INTEGER NOT NULL,
    started_at         INTEGER NOT NULL,
    ended_at           INTEGER NOT NULL,
    duration_ns        INTEGER NOT NULL
);

CREATE INDEX jobs_status_ended_at ON jobs (status, ended_at);

-- +goose Down
DROP TABLE jobs;
