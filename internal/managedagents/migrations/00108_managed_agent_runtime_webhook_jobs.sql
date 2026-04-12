-- +goose Up

CREATE TABLE IF NOT EXISTS managed_agent_runtime_webhook_jobs (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES managed_agent_sessions(id) ON DELETE CASCADE,
    sandbox_id TEXT NOT NULL,
    runtime_generation BIGINT NOT NULL,
    run_id TEXT,
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'done', 'failed')),
    attempts INTEGER NOT NULL DEFAULT 0,
    lease_owner TEXT,
    lease_expires_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_managed_agent_runtime_webhook_jobs_status_created
    ON managed_agent_runtime_webhook_jobs(status, created_at, id);

CREATE INDEX IF NOT EXISTS idx_managed_agent_runtime_webhook_jobs_session_status_created
    ON managed_agent_runtime_webhook_jobs(session_id, status, created_at, id);

CREATE TABLE IF NOT EXISTS managed_agent_runtime_input_event_batches (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES managed_agent_sessions(id) ON DELETE CASCADE,
    event_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    runtime_input_events JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_managed_agent_runtime_input_event_batches_session_created
    ON managed_agent_runtime_input_event_batches(session_id, created_at, id);

DROP TRIGGER IF EXISTS update_managed_agent_runtime_webhook_jobs_updated_at ON managed_agent_runtime_webhook_jobs;
CREATE TRIGGER update_managed_agent_runtime_webhook_jobs_updated_at
    BEFORE UPDATE ON managed_agent_runtime_webhook_jobs
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

DROP TRIGGER IF EXISTS update_managed_agent_runtime_input_event_batches_updated_at ON managed_agent_runtime_input_event_batches;
CREATE TRIGGER update_managed_agent_runtime_input_event_batches_updated_at
    BEFORE UPDATE ON managed_agent_runtime_input_event_batches
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- +goose Down

DROP TRIGGER IF EXISTS update_managed_agent_runtime_input_event_batches_updated_at ON managed_agent_runtime_input_event_batches;
DROP TRIGGER IF EXISTS update_managed_agent_runtime_webhook_jobs_updated_at ON managed_agent_runtime_webhook_jobs;
DROP INDEX IF EXISTS idx_managed_agent_runtime_input_event_batches_session_created;
DROP TABLE IF EXISTS managed_agent_runtime_input_event_batches;
DROP INDEX IF EXISTS idx_managed_agent_runtime_webhook_jobs_session_status_created;
DROP INDEX IF EXISTS idx_managed_agent_runtime_webhook_jobs_status_created;
DROP TABLE IF EXISTS managed_agent_runtime_webhook_jobs;
