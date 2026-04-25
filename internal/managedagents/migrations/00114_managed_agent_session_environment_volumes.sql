-- +goose Up

ALTER TABLE managed_agent_session_runtimes
    ADD COLUMN IF NOT EXISTS environment_volume_ids JSONB NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down

ALTER TABLE managed_agent_session_runtimes
    DROP COLUMN IF EXISTS environment_volume_ids;
