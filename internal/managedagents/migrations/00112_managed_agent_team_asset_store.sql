-- +goose Up

CREATE TABLE IF NOT EXISTS managed_agent_team_asset_stores (
    team_id TEXT NOT NULL,
    region_id TEXT NOT NULL,
    volume_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (team_id, region_id)
);

DROP TRIGGER IF EXISTS update_managed_agent_team_asset_stores_updated_at ON managed_agent_team_asset_stores;
CREATE TRIGGER update_managed_agent_team_asset_stores_updated_at
    BEFORE UPDATE ON managed_agent_team_asset_stores
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

ALTER TABLE managed_agent_files
    ADD COLUMN IF NOT EXISTS store_path TEXT;

ALTER TABLE managed_agent_skill_versions
    ADD COLUMN IF NOT EXISTS bundle_path TEXT,
    ADD COLUMN IF NOT EXISTS bundle_sha256 TEXT,
    ADD COLUMN IF NOT EXISTS bundle_size_bytes BIGINT;

-- +goose Down

ALTER TABLE managed_agent_skill_versions
    DROP COLUMN IF EXISTS bundle_path,
    DROP COLUMN IF EXISTS bundle_sha256,
    DROP COLUMN IF EXISTS bundle_size_bytes;

ALTER TABLE managed_agent_files
    DROP COLUMN IF EXISTS store_path;

DROP TRIGGER IF EXISTS update_managed_agent_team_asset_stores_updated_at ON managed_agent_team_asset_stores;
DROP TABLE IF EXISTS managed_agent_team_asset_stores;
