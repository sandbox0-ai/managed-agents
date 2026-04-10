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

type StoredSkillFile = storedSkillFile

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
	Snapshot SkillVersion
	Files    []storedSkillFile
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

func buildStoredSkillFiles(parsed *parsedSkillUpload) []storedSkillFile {
	if parsed == nil || len(parsed.Files) == 0 {
		return []storedSkillFile{}
	}
	files := make([]storedSkillFile, 0, len(parsed.Files))
	for _, file := range parsed.Files {
		files = append(files, storedSkillFile{
			Path:    strings.TrimSpace(file.Path),
			Content: append([]byte(nil), file.Content...),
		})
	}
	return files
}
