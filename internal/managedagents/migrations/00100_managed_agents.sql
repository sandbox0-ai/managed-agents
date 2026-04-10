-- +goose Up

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ language 'plpgsql';
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS managed_agent_sessions (
    id TEXT PRIMARY KEY,
    team_id TEXT NOT NULL,
    created_by_user_id TEXT,
    vendor TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    working_directory TEXT NOT NULL DEFAULT '/workspace',
    title TEXT,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    agent JSONB NOT NULL,
    resources JSONB NOT NULL DEFAULT '[]'::jsonb,
    vault_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    engine JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL CHECK (status IN ('rescheduling', 'running', 'idle', 'terminated')),
    usage_input_tokens BIGINT NOT NULL DEFAULT 0,
    usage_output_tokens BIGINT NOT NULL DEFAULT 0,
    usage_cache_read_input_tokens BIGINT NOT NULL DEFAULT 0,
    stats_active_seconds DOUBLE PRECISION NOT NULL DEFAULT 0,
    last_status_started_at TIMESTAMPTZ,
    archived_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_managed_agent_sessions_team_id ON managed_agent_sessions(team_id);
CREATE INDEX IF NOT EXISTS idx_managed_agent_sessions_status ON managed_agent_sessions(status);
CREATE INDEX IF NOT EXISTS idx_managed_agent_sessions_created_at ON managed_agent_sessions(created_at DESC);

CREATE TABLE IF NOT EXISTS managed_agent_agents (
    id TEXT PRIMARY KEY,
    team_id TEXT NOT NULL,
    vendor TEXT NOT NULL,
    current_version INTEGER NOT NULL,
    snapshot JSONB NOT NULL,
    archived_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_managed_agent_agents_team_id ON managed_agent_agents(team_id);
CREATE INDEX IF NOT EXISTS idx_managed_agent_agents_created_at ON managed_agent_agents(created_at DESC);

CREATE TABLE IF NOT EXISTS managed_agent_agent_versions (
    agent_id TEXT NOT NULL REFERENCES managed_agent_agents(id) ON DELETE CASCADE,
    version INTEGER NOT NULL,
    snapshot JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (agent_id, version)
);

CREATE TABLE IF NOT EXISTS managed_agent_environments (
    id TEXT PRIMARY KEY,
    team_id TEXT NOT NULL,
    snapshot JSONB NOT NULL,
    archived_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_managed_agent_environments_team_id ON managed_agent_environments(team_id);
CREATE INDEX IF NOT EXISTS idx_managed_agent_environments_created_at ON managed_agent_environments(created_at DESC);

CREATE TABLE IF NOT EXISTS managed_agent_vaults (
    id TEXT PRIMARY KEY,
    team_id TEXT NOT NULL,
    snapshot JSONB NOT NULL,
    archived_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_managed_agent_vaults_team_id ON managed_agent_vaults(team_id);
CREATE INDEX IF NOT EXISTS idx_managed_agent_vaults_created_at ON managed_agent_vaults(created_at DESC);

CREATE TABLE IF NOT EXISTS managed_agent_credentials (
    id TEXT PRIMARY KEY,
    team_id TEXT NOT NULL,
    vault_id TEXT NOT NULL REFERENCES managed_agent_vaults(id) ON DELETE CASCADE,
    snapshot JSONB NOT NULL,
    secret JSONB NOT NULL DEFAULT '{}'::jsonb,
    archived_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_managed_agent_credentials_vault_id ON managed_agent_credentials(vault_id);
CREATE INDEX IF NOT EXISTS idx_managed_agent_credentials_team_id ON managed_agent_credentials(team_id);

CREATE TABLE IF NOT EXISTS managed_agent_files (
    id TEXT PRIMARY KEY,
    team_id TEXT NOT NULL,
    filename TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    downloadable BOOLEAN NOT NULL DEFAULT TRUE,
    scope_type TEXT,
    scope_id TEXT,
    content BYTEA NOT NULL,
    snapshot JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_managed_agent_files_team_id ON managed_agent_files(team_id);
CREATE INDEX IF NOT EXISTS idx_managed_agent_files_scope_id ON managed_agent_files(scope_id);

CREATE TABLE IF NOT EXISTS managed_agent_session_events (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES managed_agent_sessions(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_managed_agent_session_events_session_created_at ON managed_agent_session_events(session_id, created_at);

CREATE TABLE IF NOT EXISTS managed_agent_session_runtimes (
    session_id TEXT PRIMARY KEY REFERENCES managed_agent_sessions(id) ON DELETE CASCADE,
    vendor TEXT NOT NULL,
    region_id TEXT NOT NULL,
    sandbox_id TEXT NOT NULL,
    workspace_volume_id TEXT NOT NULL,
    engine_state_volume_id TEXT NOT NULL,
    callback_token TEXT NOT NULL,
    vendor_session_id TEXT,
    runtime_generation BIGINT NOT NULL DEFAULT 1,
    active_run_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_managed_agent_session_runtimes_region_id ON managed_agent_session_runtimes(region_id);
CREATE INDEX IF NOT EXISTS idx_managed_agent_session_runtimes_sandbox_id ON managed_agent_session_runtimes(sandbox_id);

DROP TRIGGER IF EXISTS update_managed_agent_sessions_updated_at ON managed_agent_sessions;
CREATE TRIGGER update_managed_agent_sessions_updated_at
    BEFORE UPDATE ON managed_agent_sessions
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

DROP TRIGGER IF EXISTS update_managed_agent_agents_updated_at ON managed_agent_agents;
CREATE TRIGGER update_managed_agent_agents_updated_at
    BEFORE UPDATE ON managed_agent_agents
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

DROP TRIGGER IF EXISTS update_managed_agent_environments_updated_at ON managed_agent_environments;
CREATE TRIGGER update_managed_agent_environments_updated_at
    BEFORE UPDATE ON managed_agent_environments
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

DROP TRIGGER IF EXISTS update_managed_agent_vaults_updated_at ON managed_agent_vaults;
CREATE TRIGGER update_managed_agent_vaults_updated_at
    BEFORE UPDATE ON managed_agent_vaults
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

DROP TRIGGER IF EXISTS update_managed_agent_credentials_updated_at ON managed_agent_credentials;
CREATE TRIGGER update_managed_agent_credentials_updated_at
    BEFORE UPDATE ON managed_agent_credentials
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

DROP TRIGGER IF EXISTS update_managed_agent_files_updated_at ON managed_agent_files;
CREATE TRIGGER update_managed_agent_files_updated_at
    BEFORE UPDATE ON managed_agent_files
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

DROP TRIGGER IF EXISTS update_managed_agent_session_runtimes_updated_at ON managed_agent_session_runtimes;
CREATE TRIGGER update_managed_agent_session_runtimes_updated_at
    BEFORE UPDATE ON managed_agent_session_runtimes
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- +goose Down

DROP TRIGGER IF EXISTS update_managed_agent_session_runtimes_updated_at ON managed_agent_session_runtimes;
DROP TRIGGER IF EXISTS update_managed_agent_sessions_updated_at ON managed_agent_sessions;
DROP TRIGGER IF EXISTS update_managed_agent_agents_updated_at ON managed_agent_agents;
DROP TRIGGER IF EXISTS update_managed_agent_environments_updated_at ON managed_agent_environments;
DROP TRIGGER IF EXISTS update_managed_agent_vaults_updated_at ON managed_agent_vaults;
DROP TRIGGER IF EXISTS update_managed_agent_credentials_updated_at ON managed_agent_credentials;
DROP TRIGGER IF EXISTS update_managed_agent_files_updated_at ON managed_agent_files;

DROP INDEX IF EXISTS idx_managed_agent_files_scope_id;
DROP INDEX IF EXISTS idx_managed_agent_files_team_id;
DROP TABLE IF EXISTS managed_agent_files;

DROP INDEX IF EXISTS idx_managed_agent_credentials_team_id;
DROP INDEX IF EXISTS idx_managed_agent_credentials_vault_id;
DROP TABLE IF EXISTS managed_agent_credentials;

DROP INDEX IF EXISTS idx_managed_agent_vaults_created_at;
DROP INDEX IF EXISTS idx_managed_agent_vaults_team_id;
DROP TABLE IF EXISTS managed_agent_vaults;

DROP INDEX IF EXISTS idx_managed_agent_environments_created_at;
DROP INDEX IF EXISTS idx_managed_agent_environments_team_id;
DROP TABLE IF EXISTS managed_agent_environments;

DROP TABLE IF EXISTS managed_agent_agent_versions;
DROP INDEX IF EXISTS idx_managed_agent_agents_created_at;
DROP INDEX IF EXISTS idx_managed_agent_agents_team_id;
DROP TABLE IF EXISTS managed_agent_agents;

DROP INDEX IF EXISTS idx_managed_agent_session_runtimes_sandbox_id;
DROP INDEX IF EXISTS idx_managed_agent_session_runtimes_region_id;
DROP TABLE IF EXISTS managed_agent_session_runtimes;

DROP INDEX IF EXISTS idx_managed_agent_session_events_session_created_at;
DROP TABLE IF EXISTS managed_agent_session_events;

DROP INDEX IF EXISTS idx_managed_agent_sessions_created_at;
DROP INDEX IF EXISTS idx_managed_agent_sessions_status;
DROP INDEX IF EXISTS idx_managed_agent_sessions_team_id;
DROP TABLE IF EXISTS managed_agent_sessions;

DROP FUNCTION IF EXISTS update_updated_at_column();
