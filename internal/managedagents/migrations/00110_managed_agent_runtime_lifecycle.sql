-- +goose Up

ALTER TABLE managed_agent_session_runtimes
    ALTER COLUMN sandbox_id DROP NOT NULL;

ALTER TABLE managed_agent_session_runtimes
    ADD COLUMN IF NOT EXISTS sandbox_deleted_at TIMESTAMPTZ;

-- +goose Down

ALTER TABLE managed_agent_session_runtimes
    DROP COLUMN IF EXISTS sandbox_deleted_at;

UPDATE managed_agent_session_runtimes
SET sandbox_id = ''
WHERE sandbox_id IS NULL;

ALTER TABLE managed_agent_session_runtimes
    ALTER COLUMN sandbox_id SET NOT NULL;
