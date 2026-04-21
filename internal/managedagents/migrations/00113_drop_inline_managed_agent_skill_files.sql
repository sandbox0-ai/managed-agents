-- +goose Up

UPDATE managed_agent_skills AS s
SET mount_slug = v.snapshot->>'name'
FROM managed_agent_skill_versions AS v
WHERE s.id = v.skill_id
  AND s.latest_version = v.version
  AND COALESCE(s.mount_slug, '') = ''
  AND COALESCE(v.snapshot->>'name', '') <> '';

ALTER TABLE managed_agent_skill_versions
    DROP COLUMN IF EXISTS files;

-- +goose Down

ALTER TABLE managed_agent_skill_versions
    ADD COLUMN IF NOT EXISTS files JSONB NOT NULL DEFAULT '[]'::jsonb;
