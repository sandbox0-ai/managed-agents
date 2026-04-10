-- +goose Up

ALTER TABLE managed_agent_skill_versions
    ADD COLUMN IF NOT EXISTS files JSONB NOT NULL DEFAULT '[]'::jsonb;

-- +goose Down

ALTER TABLE managed_agent_skill_versions
    DROP COLUMN IF EXISTS files;
