-- +goose Up

ALTER TABLE managed_agent_session_runtimes
    ADD COLUMN IF NOT EXISTS bootstrapped_runtime_generation BIGINT NOT NULL DEFAULT 0;

ALTER TABLE managed_agent_session_runtimes
    ADD COLUMN IF NOT EXISTS bootstrap_state_hash TEXT NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE managed_agent_session_runtimes
    DROP COLUMN IF EXISTS bootstrap_state_hash;

ALTER TABLE managed_agent_session_runtimes
    DROP COLUMN IF EXISTS bootstrapped_runtime_generation;
