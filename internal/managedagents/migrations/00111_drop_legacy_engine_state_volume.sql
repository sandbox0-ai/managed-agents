-- +goose Up

ALTER TABLE managed_agent_session_runtimes
    DROP COLUMN IF EXISTS engine_state_volume_id;

-- +goose Down

ALTER TABLE managed_agent_session_runtimes
    ADD COLUMN IF NOT EXISTS engine_state_volume_id TEXT;

UPDATE managed_agent_session_runtimes
SET engine_state_volume_id = workspace_volume_id
WHERE engine_state_volume_id IS NULL;

ALTER TABLE managed_agent_session_runtimes
    ALTER COLUMN engine_state_volume_id SET NOT NULL;
