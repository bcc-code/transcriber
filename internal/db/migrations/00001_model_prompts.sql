-- +goose Up
CREATE TABLE model_prompts (
    model_id   TEXT PRIMARY KEY,
    prompt     TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE model_prompts;
