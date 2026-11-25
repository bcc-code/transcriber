-- name: UpsertJob :exec
INSERT INTO jobs (
    id, path, language, format, output_path, priority, callback, model, prompt,
    subtitle_max_chars, subtitle_max_lines, timeout_ns, idempotency_key,
    status, progress, result, results, error,
    created_at, started_at, ended_at, duration_ns
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?,
    ?, ?, ?, ?, ?,
    ?, ?, ?, ?
)
ON CONFLICT (id) DO UPDATE SET
    path               = excluded.path,
    language           = excluded.language,
    format             = excluded.format,
    output_path        = excluded.output_path,
    priority           = excluded.priority,
    callback           = excluded.callback,
    model              = excluded.model,
    prompt             = excluded.prompt,
    subtitle_max_chars = excluded.subtitle_max_chars,
    subtitle_max_lines = excluded.subtitle_max_lines,
    timeout_ns         = excluded.timeout_ns,
    idempotency_key    = excluded.idempotency_key,
    status             = excluded.status,
    progress           = excluded.progress,
    result             = excluded.result,
    results            = excluded.results,
    error              = excluded.error,
    created_at         = excluded.created_at,
    started_at         = excluded.started_at,
    ended_at           = excluded.ended_at,
    duration_ns        = excluded.duration_ns;

-- name: ListJobs :many
SELECT * FROM jobs ORDER BY created_at;

-- name: DeleteJob :exec
DELETE FROM jobs WHERE id = ?;
