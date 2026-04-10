-- +goose Up

ALTER TABLE managed_agent_sessions
    ADD COLUMN IF NOT EXISTS usage_cache_creation_ephemeral_1h_input_tokens BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS usage_cache_creation_ephemeral_5m_input_tokens BIGINT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS managed_agent_session_resource_secrets (
    session_id TEXT NOT NULL REFERENCES managed_agent_sessions(id) ON DELETE CASCADE,
    resource_id TEXT NOT NULL,
    secret JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (session_id, resource_id)
);

CREATE INDEX IF NOT EXISTS idx_managed_agent_session_resource_secrets_resource_id
    ON managed_agent_session_resource_secrets(resource_id);

DROP TRIGGER IF EXISTS update_managed_agent_session_resource_secrets_updated_at ON managed_agent_session_resource_secrets;
CREATE TRIGGER update_managed_agent_session_resource_secrets_updated_at
    BEFORE UPDATE ON managed_agent_session_resource_secrets
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- +goose Down

DROP TRIGGER IF EXISTS update_managed_agent_session_resource_secrets_updated_at ON managed_agent_session_resource_secrets;
DROP INDEX IF EXISTS idx_managed_agent_session_resource_secrets_resource_id;
DROP TABLE IF EXISTS managed_agent_session_resource_secrets;

ALTER TABLE managed_agent_sessions
    DROP COLUMN IF EXISTS usage_cache_creation_ephemeral_5m_input_tokens,
    DROP COLUMN IF EXISTS usage_cache_creation_ephemeral_1h_input_tokens;
