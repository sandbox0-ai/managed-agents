-- +goose Up

ALTER TABLE managed_agent_sessions
    ADD COLUMN IF NOT EXISTS environment_artifact_id TEXT;

CREATE INDEX IF NOT EXISTS idx_managed_agent_sessions_environment_artifact_id
    ON managed_agent_sessions(environment_artifact_id);

CREATE UNIQUE INDEX IF NOT EXISTS uq_managed_agent_environments_team_name
    ON managed_agent_environments(team_id, LOWER(BTRIM(snapshot->>'name')));

CREATE TABLE IF NOT EXISTS managed_agent_environment_artifacts (
    id TEXT PRIMARY KEY,
    team_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    digest TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'building', 'ready', 'failed', 'archived')),
    config_snapshot JSONB NOT NULL,
    compatibility JSONB NOT NULL,
    assets JSONB NOT NULL DEFAULT '{}'::jsonb,
    build_log TEXT NOT NULL DEFAULT '',
    failure_reason TEXT,
    archived_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_managed_agent_environment_artifacts_team_env_digest
    ON managed_agent_environment_artifacts(team_id, environment_id, digest);

CREATE INDEX IF NOT EXISTS idx_managed_agent_environment_artifacts_environment_id
    ON managed_agent_environment_artifacts(team_id, environment_id, created_at DESC);

DROP TRIGGER IF EXISTS update_managed_agent_environment_artifacts_updated_at ON managed_agent_environment_artifacts;
CREATE TRIGGER update_managed_agent_environment_artifacts_updated_at
    BEFORE UPDATE ON managed_agent_environment_artifacts
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- +goose Down

DROP TRIGGER IF EXISTS update_managed_agent_environment_artifacts_updated_at ON managed_agent_environment_artifacts;

DROP INDEX IF EXISTS idx_managed_agent_environment_artifacts_environment_id;
DROP INDEX IF EXISTS uq_managed_agent_environment_artifacts_team_env_digest;
DROP TABLE IF EXISTS managed_agent_environment_artifacts;

DROP INDEX IF EXISTS uq_managed_agent_environments_team_name;

DROP INDEX IF EXISTS idx_managed_agent_sessions_environment_artifact_id;

ALTER TABLE managed_agent_sessions
    DROP COLUMN IF EXISTS environment_artifact_id;
