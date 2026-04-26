-- +goose Up

ALTER TABLE managed_agent_session_runtimes
    ADD COLUMN IF NOT EXISTS workspace_base_digest TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS workspace_base_volume_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS bootstrap_state_digest TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS bootstrap_synced_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS managed_agent_workspace_bases (
    id TEXT PRIMARY KEY,
    team_id TEXT NOT NULL,
    digest TEXT NOT NULL,
    status TEXT NOT NULL,
    volume_id TEXT NOT NULL DEFAULT '',
    input_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    failure_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT managed_agent_workspace_bases_status_check CHECK (status IN ('building', 'ready', 'failed')),
    CONSTRAINT managed_agent_workspace_bases_team_digest_unique UNIQUE (team_id, digest)
);

CREATE INDEX IF NOT EXISTS idx_managed_agent_workspace_bases_team_status
    ON managed_agent_workspace_bases(team_id, status);

DROP TRIGGER IF EXISTS update_managed_agent_workspace_bases_updated_at ON managed_agent_workspace_bases;
CREATE TRIGGER update_managed_agent_workspace_bases_updated_at
    BEFORE UPDATE ON managed_agent_workspace_bases
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- +goose Down

DROP TRIGGER IF EXISTS update_managed_agent_workspace_bases_updated_at ON managed_agent_workspace_bases;
DROP INDEX IF EXISTS idx_managed_agent_workspace_bases_team_status;
DROP TABLE IF EXISTS managed_agent_workspace_bases;

ALTER TABLE managed_agent_session_runtimes
    DROP COLUMN IF EXISTS bootstrap_synced_at,
    DROP COLUMN IF EXISTS bootstrap_state_digest,
    DROP COLUMN IF EXISTS workspace_base_volume_id,
    DROP COLUMN IF EXISTS workspace_base_digest;
