-- +goose Up

ALTER TABLE managed_agent_session_runtimes
    ADD COLUMN IF NOT EXISTS wrapper_url TEXT NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE managed_agent_session_runtimes
    DROP COLUMN IF EXISTS wrapper_url;
