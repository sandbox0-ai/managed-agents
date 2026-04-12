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
		Content: []byte("---\nname: demo-skill\ndescription: Demo skill\n---\n"),
	}}, "")
	if err == nil {
		t.Fatal("expected error when SKILL.md is not at the top-level root")
	}
}

func TestParseSkillUploadRejectsRootLevelSkillMarkdown(t *testing.T) {
	_, err := parseSkillUpload([]uploadedSkillFile{{
		Path:    "SKILL.md",
		Content: []byte("---\nname: demo-skill\ndescription: Demo skill\n---\n"),
	}}, "")
	if err == nil {
		t.Fatal("expected root-level SKILL.md upload to be rejected")
	}
}

func TestParseSkillUploadRejectsParentDirectorySegments(t *testing.T) {
	_, err := parseSkillUpload([]uploadedSkillFile{{
		Path:    "demo-skill/../other-skill/SKILL.md",
		Content: []byte("---\nname: demo-skill\ndescription: Demo skill\n---\n"),
	}}, "")
	if err == nil {
		t.Fatal("expected parent directory segment to be rejected")
	}
}

func TestParseSkillUploadRequiresFrontMatterMetadata(t *testing.T) {
	_, err := parseSkillUpload([]uploadedSkillFile{{
		Path:    "demo-skill/SKILL.md",
		Content: []byte("# Demo Skill\n\nDemo skill."),
	}}, "")
	if err == nil {
		t.Fatal("expected missing front matter to be rejected")
	}
}

func TestParseSkillUploadValidatesSkillName(t *testing.T) {
	_, err := parseSkillUpload([]uploadedSkillFile{{
		Path:    "demo-skill/SKILL.md",
		Content: []byte("---\nname: Demo_Skill\ndescription: Demo skill\n---\n"),
	}}, "")
	if err == nil {
		t.Fatal("expected invalid skill name to be rejected")
	}
}

func TestParseSkillUploadRequiresDescription(t *testing.T) {
	_, err := parseSkillUpload([]uploadedSkillFile{{
		Path:    "demo-skill/SKILL.md",
		Content: []byte("---\nname: demo-skill\n---\n"),
	}}, "")
	if err == nil {
		t.Fatal("expected missing description to be rejected")
	}
}

func TestParseSkillUploadRejectsXMLTagsInDescription(t *testing.T) {
	_, err := parseSkillUpload([]uploadedSkillFile{{
		Path:    "demo-skill/SKILL.md",
		Content: []byte("---\nname: demo-skill\ndescription: Use <tool> tags\n---\n"),
	}}, "")
	if err == nil {
		t.Fatal("expected XML tags in description to be rejected")
	}
}
