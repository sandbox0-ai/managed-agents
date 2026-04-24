-- +goose Up

ALTER TABLE managed_agent_files
    ALTER COLUMN store_path SET NOT NULL,
    ALTER COLUMN sha256 SET NOT NULL,
    DROP COLUMN IF EXISTS content,
    DROP COLUMN IF EXISTS file_store_volume_id,
    DROP COLUMN IF EXISTS file_store_path;

ALTER TABLE managed_agent_skill_versions
    ALTER COLUMN bundle_path SET NOT NULL,
    ALTER COLUMN bundle_sha256 SET NOT NULL,
    ALTER COLUMN bundle_size_bytes SET NOT NULL,
    DROP COLUMN IF EXISTS files;

-- +goose Down

ALTER TABLE managed_agent_skill_versions
    ADD COLUMN IF NOT EXISTS files JSONB NOT NULL DEFAULT '[]'::jsonb,
    ALTER COLUMN bundle_path DROP NOT NULL,
    ALTER COLUMN bundle_sha256 DROP NOT NULL,
    ALTER COLUMN bundle_size_bytes DROP NOT NULL;

ALTER TABLE managed_agent_files
    ADD COLUMN IF NOT EXISTS content BYTEA,
    ADD COLUMN IF NOT EXISTS file_store_volume_id TEXT,
    ADD COLUMN IF NOT EXISTS file_store_path TEXT,
    ALTER COLUMN store_path DROP NOT NULL,
    ALTER COLUMN sha256 DROP NOT NULL;
