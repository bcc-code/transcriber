-- +goose Up
-- 1 when a completed job found no speech in its audio.
ALTER TABLE jobs ADD COLUMN no_speech INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE jobs DROP COLUMN no_speech;
