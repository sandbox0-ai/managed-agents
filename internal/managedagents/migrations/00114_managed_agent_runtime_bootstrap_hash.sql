-- +goose Up

ALTER TABLE managed_agent_session_runtimes
    ADD COLUMN IF NOT EXISTS bootstrap_hash TEXT NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE managed_agent_session_runtimes
    DROP COLUMN IF EXISTS bootstrap_hash;
