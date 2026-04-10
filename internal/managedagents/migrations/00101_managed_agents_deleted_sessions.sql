-- +goose Up

ALTER TABLE managed_agent_sessions
	ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_managed_agent_sessions_deleted_at ON managed_agent_sessions(deleted_at);

-- +goose Down

DROP INDEX IF EXISTS idx_managed_agent_sessions_deleted_at;

ALTER TABLE managed_agent_sessions
	DROP COLUMN IF EXISTS deleted_at;
