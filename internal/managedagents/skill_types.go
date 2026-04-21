package managedagents

import (
	"strings"
	"time"
)

type uploadedSkillFile struct {
	Path    string
	Content []byte
}

type storedSkillFile struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
}

type skillVersionArtifact struct {
	VolumeID      string `json:"volume_id"`
	Path          string `json:"path"`
	ContentDigest string `json:"content_digest"`
	ArchiveSHA256 string `json:"archive_sha256"`
	SizeBytes     int64  `json:"size_bytes"`
	FileCount     int    `json:"file_count"`
}

type SkillVersionArtifact = skillVersionArtifact

type skillBundle struct {
	ID        string         `json:"id"`
	TeamID    string         `json:"team_id"`
	CacheKey  string         `json:"cache_key"`
	VolumeID  string         `json:"volume_id"`
	Snapshot  map[string]any `json:"snapshot"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type SkillBundle = skillBundle

type Skill struct {
	Type          string  `json:"type"`
	ID            string  `json:"id"`
	DisplayTitle  *string `json:"display_title"`
	LatestVersion *string `json:"latest_version"`
	Source        string  `json:"source"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

type SkillVersion struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	SkillID     string `json:"skill_id"`
	Version     string `json:"version"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Directory   string `json:"directory"`
	CreatedAt   string `json:"created_at"`
}

type ListSkillsResponse struct {
	Data     []Skill `json:"data"`
	HasMore  bool    `json:"has_more"`
	NextPage *string `json:"next_page"`
}

type ListSkillVersionsResponse struct {
	Data     []SkillVersion `json:"data"`
	HasMore  bool           `json:"has_more"`
	NextPage *string        `json:"next_page"`
}

type StoredSkillVersion struct {
	Snapshot  SkillVersion
	MountSlug string
	Artifact  skillVersionArtifact
}

type storedSkill struct {
	Snapshot  Skill
	MountSlug string
}

type parsedSkillUpload struct {
	Name        string
	Description string
	Directory   string
	Files       []uploadedSkillFile
}

func buildSkillObject(id string, displayTitle *string, latestVersion *string, now time.Time) Skill {
	return Skill{
		Type:          "skill",
		ID:            id,
		DisplayTitle:  normalizeNullableString(displayTitle),
		LatestVersion: normalizeNullableString(latestVersion),
		Source:        "custom",
		CreatedAt:     nowRFC3339(now),
		UpdatedAt:     nowRFC3339(now),
	}
}

func buildSkillVersionObject(id, skillID, version string, parsed *parsedSkillUpload, now time.Time) SkillVersion {
	if parsed == nil {
		parsed = &parsedSkillUpload{}
	}
	return SkillVersion{
		Type:        "skill_version",
		ID:          strings.TrimSpace(id),
		SkillID:     strings.TrimSpace(skillID),
		Version:     strings.TrimSpace(version),
		Name:        strings.TrimSpace(parsed.Name),
		Description: strings.TrimSpace(parsed.Description),
		Directory:   strings.TrimSpace(parsed.Directory),
		CreatedAt:   nowRFC3339(now),
	}
}

func normalizeNullableString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
