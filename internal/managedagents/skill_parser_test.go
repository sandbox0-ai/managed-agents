package managedagents

import "testing"

func TestParseSkillUploadExtractsFrontMatter(t *testing.T) {
	parsed, err := parseSkillUpload([]uploadedSkillFile{{
		Path:    "demo-skill/SKILL.md",
		Content: []byte("---\nname: demo-skill\ndescription: >-\n  A useful demo skill\n---\n\n# Demo Skill\n"),
	}}, "")
	if err != nil {
		t.Fatalf("parseSkillUpload returned error: %v", err)
	}
	if parsed.Directory != "demo-skill" {
		t.Fatalf("directory = %q, want demo-skill", parsed.Directory)
	}
	if parsed.Name != "demo-skill" {
		t.Fatalf("name = %q, want demo-skill", parsed.Name)
	}
	if parsed.Description != "A useful demo skill" {
		t.Fatalf("description = %q, want A useful demo skill", parsed.Description)
	}
}

func TestParseSkillUploadRejectsMixedDirectories(t *testing.T) {
	_, err := parseSkillUpload([]uploadedSkillFile{
		{Path: "skill-a/SKILL.md", Content: []byte("# A")},
		{Path: "skill-b/extra.md", Content: []byte("extra")},
	}, "")
	if err == nil {
		t.Fatal("expected error for mixed directories")
	}
}

func TestParseSkillUploadRequiresTopLevelSkillMarkdown(t *testing.T) {
	_, err := parseSkillUpload([]uploadedSkillFile{{
		Path:    "demo-skill/docs/SKILL.md",
		Content: []byte("# Demo"),
	}}, "")
	if err == nil {
		t.Fatal("expected error when SKILL.md is not at the top-level root")
	}
}
