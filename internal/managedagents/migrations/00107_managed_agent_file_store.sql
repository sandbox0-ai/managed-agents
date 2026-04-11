-- +goose Up

ALTER TABLE managed_agent_files
    ADD COLUMN IF NOT EXISTS file_store_volume_id TEXT,
    ADD COLUMN IF NOT EXISTS file_store_path TEXT,
    ADD COLUMN IF NOT EXISTS sha256 TEXT;

ALTER TABLE managed_agent_files
    ALTER COLUMN content DROP NOT NULL;

-- +goose Down

ALTER TABLE managed_agent_files
    DROP COLUMN IF EXISTS file_store_volume_id,
    DROP COLUMN IF EXISTS file_store_path,
    DROP COLUMN IF EXISTS sha256;
