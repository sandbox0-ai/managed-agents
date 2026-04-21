-- +goose Up

ALTER TABLE managed_agent_skills
    ADD COLUMN IF NOT EXISTS mount_slug TEXT;

ALTER TABLE managed_agent_skill_versions
    ADD COLUMN IF NOT EXISTS artifact_volume_id TEXT,
    ADD COLUMN IF NOT EXISTS artifact_path TEXT,
    ADD COLUMN IF NOT EXISTS content_digest TEXT,
    ADD COLUMN IF NOT EXISTS artifact_sha256 TEXT,
    ADD COLUMN IF NOT EXISTS artifact_size_bytes BIGINT,
    ADD COLUMN IF NOT EXISTS file_count INTEGER;

CREATE TABLE IF NOT EXISTS managed_agent_skill_artifact_stores (
    team_id TEXT PRIMARY KEY,
    volume_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS managed_agent_skill_set_bundles (
    id TEXT PRIMARY KEY,
    team_id TEXT NOT NULL,
    cache_key TEXT NOT NULL,
    volume_id TEXT NOT NULL,
    snapshot JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (team_id, cache_key)
);

CREATE INDEX IF NOT EXISTS idx_managed_agent_skill_set_bundles_team_id ON managed_agent_skill_set_bundles(team_id);

DROP TRIGGER IF EXISTS update_managed_agent_skill_artifact_stores_updated_at ON managed_agent_skill_artifact_stores;
CREATE TRIGGER update_managed_agent_skill_artifact_stores_updated_at
    BEFORE UPDATE ON managed_agent_skill_artifact_stores
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

DROP TRIGGER IF EXISTS update_managed_agent_skill_set_bundles_updated_at ON managed_agent_skill_set_bundles;
CREATE TRIGGER update_managed_agent_skill_set_bundles_updated_at
    BEFORE UPDATE ON managed_agent_skill_set_bundles
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- +goose Down

DROP TRIGGER IF EXISTS update_managed_agent_skill_set_bundles_updated_at ON managed_agent_skill_set_bundles;
DROP TRIGGER IF EXISTS update_managed_agent_skill_artifact_stores_updated_at ON managed_agent_skill_artifact_stores;
DROP INDEX IF EXISTS idx_managed_agent_skill_set_bundles_team_id;
DROP TABLE IF EXISTS managed_agent_skill_set_bundles;
DROP TABLE IF EXISTS managed_agent_skill_artifact_stores;

ALTER TABLE managed_agent_skill_versions
    DROP COLUMN IF EXISTS artifact_volume_id,
    DROP COLUMN IF EXISTS artifact_path,
    DROP COLUMN IF EXISTS content_digest,
    DROP COLUMN IF EXISTS artifact_sha256,
    DROP COLUMN IF EXISTS artifact_size_bytes,
    DROP COLUMN IF EXISTS file_count;

ALTER TABLE managed_agent_skills
    DROP COLUMN IF EXISTS mount_slug;
