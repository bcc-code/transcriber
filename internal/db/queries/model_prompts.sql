-- name: GetModelPrompt :one
SELECT prompt FROM model_prompts WHERE model_id = ?;

-- name: ListModelPrompts :many
SELECT model_id, prompt, updated_at FROM model_prompts ORDER BY model_id;

-- name: UpsertModelPrompt :exec
INSERT INTO model_prompts (model_id, prompt, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT (model_id) DO UPDATE SET
    prompt = excluded.prompt,
    updated_at = excluded.updated_at;

-- name: DeleteModelPrompt :exec
DELETE FROM model_prompts WHERE model_id = ?;
