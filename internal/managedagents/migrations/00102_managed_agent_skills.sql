-- +goose Up

CREATE TABLE IF NOT EXISTS managed_agent_skills (
    id TEXT PRIMARY KEY,
    team_id TEXT NOT NULL,
    source TEXT NOT NULL,
    latest_version TEXT,
    snapshot JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_managed_agent_skills_team_id ON managed_agent_skills(team_id);
CREATE INDEX IF NOT EXISTS idx_managed_agent_skills_created_at ON managed_agent_skills(created_at DESC);

CREATE TABLE IF NOT EXISTS managed_agent_skill_versions (
    id TEXT PRIMARY KEY,
    skill_id TEXT NOT NULL REFERENCES managed_agent_skills(id) ON DELETE CASCADE,
    version TEXT NOT NULL,
    snapshot JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (skill_id, version)
);

CREATE INDEX IF NOT EXISTS idx_managed_agent_skill_versions_skill_id ON managed_agent_skill_versions(skill_id);
CREATE INDEX IF NOT EXISTS idx_managed_agent_skill_versions_created_at ON managed_agent_skill_versions(created_at DESC);

DROP TRIGGER IF EXISTS update_managed_agent_skills_updated_at ON managed_agent_skills;
CREATE TRIGGER update_managed_agent_skills_updated_at
    BEFORE UPDATE ON managed_agent_skills
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- +goose Down

DROP TRIGGER IF EXISTS update_managed_agent_skills_updated_at ON managed_agent_skills;
DROP INDEX IF EXISTS idx_managed_agent_skill_versions_created_at;
DROP INDEX IF EXISTS idx_managed_agent_skill_versions_skill_id;
DROP TABLE IF EXISTS managed_agent_skill_versions;
DROP INDEX IF EXISTS idx_managed_agent_skills_created_at;
DROP INDEX IF EXISTS idx_managed_agent_skills_team_id;
DROP TABLE IF EXISTS managed_agent_skills;
