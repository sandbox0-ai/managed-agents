package managedagents

import (
	"strings"
	"time"
)

type uploadedSkillFile struct {
	Path    string
	Content []byte
}

type parsedSkillUpload struct {
	Name        string
	Description string
	Directory   string
	Files       []uploadedSkillFile
}

func buildSkillObject(id string, displayTitle *string, latestVersion *string, now time.Time) map[string]any {
	return map[string]any{
		"type":           "skill",
		"id":             id,
		"display_title":  nullableText(displayTitle),
		"latest_version": nullableStringPointer(latestVersion),
		"source":         "custom",
		"created_at":     nowRFC3339(now),
		"updated_at":     nowRFC3339(now),
	}
}

func buildSkillVersionObject(id, skillID, version string, parsed *parsedSkillUpload, now time.Time) map[string]any {
	if parsed == nil {
		parsed = &parsedSkillUpload{}
	}
	return map[string]any{
		"type":        "skill_version",
		"id":          id,
		"skill_id":    strings.TrimSpace(skillID),
		"version":     strings.TrimSpace(version),
		"name":        strings.TrimSpace(parsed.Name),
		"description": strings.TrimSpace(parsed.Description),
		"directory":   strings.TrimSpace(parsed.Directory),
		"created_at":  nowRFC3339(now),
	}
}
