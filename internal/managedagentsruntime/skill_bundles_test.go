package managedagentsruntime

import (
	"testing"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
)

func TestSkillBundleCacheKeyIncludesVersionDigestAndSlug(t *testing.T) {
	base := []resolvedAgentSkill{{
		skillID:       "skill_123",
		version:       "100",
		mountSlug:     "demo-skill",
		contentDigest: "abc",
	}}
	first, err := skillBundleCacheKey(base)
	if err != nil {
		t.Fatalf("skillBundleCacheKey first: %v", err)
	}
	second, err := skillBundleCacheKey([]resolvedAgentSkill{{
		skillID:       "skill_123",
		version:       "101",
		mountSlug:     "demo-skill",
		contentDigest: "abc",
	}})
	if err != nil {
		t.Fatalf("skillBundleCacheKey second: %v", err)
	}
	third, err := skillBundleCacheKey([]resolvedAgentSkill{{
		skillID:       "skill_123",
		version:       "100",
		mountSlug:     "demo-skill-v2",
		contentDigest: "abc",
	}})
	if err != nil {
		t.Fatalf("skillBundleCacheKey third: %v", err)
	}
	fourth, err := skillBundleCacheKey([]resolvedAgentSkill{{
		skillID:       "skill_123",
		version:       "100",
		mountSlug:     "demo-skill",
		contentDigest: "def",
	}})
	if err != nil {
		t.Fatalf("skillBundleCacheKey fourth: %v", err)
	}
	if first == second || first == third || first == fourth {
		t.Fatalf("cache key did not change across version/slug/digest updates: %q %q %q %q", first, second, third, fourth)
	}
}

func TestStableSkillMountSlugPrefersStoredMountSlug(t *testing.T) {
	stored := &gatewaymanagedagents.StoredSkillVersion{
		MountSlug: "stable-slug",
		Snapshot: gatewaymanagedagents.SkillVersion{
			Name:      "skill-name",
			Directory: "uploaded-dir",
		},
	}
	if got := stableSkillMountSlug(stored, "fallback"); got != "stable-slug" {
		t.Fatalf("stableSkillMountSlug = %q, want stable-slug", got)
	}
	stored.MountSlug = ""
	if got := stableSkillMountSlug(stored, "fallback"); got != "skill-name" {
		t.Fatalf("stableSkillMountSlug fallback to name = %q, want skill-name", got)
	}
}

func TestLegacyStoredSkillBundlePathReRootsUploadedDirectory(t *testing.T) {
	got := legacyStoredSkillBundlePath("uploaded-dir", "stable-slug", "uploaded-dir/docs/guide.md")
	if got != "/stable-slug/docs/guide.md" {
		t.Fatalf("legacyStoredSkillBundlePath = %q, want /stable-slug/docs/guide.md", got)
	}
}
