package managedagents

import "testing"

func TestNormalizedSkillArtifactFilesStripsTopLevelDirectory(t *testing.T) {
	files, err := normalizedSkillArtifactFiles(&parsedSkillUpload{
		Directory: "demo-skill",
		Files: []uploadedSkillFile{
			{Path: "demo-skill/SKILL.md", Content: []byte("# demo")},
			{Path: "demo-skill/docs/guide.md", Content: []byte("guide")},
		},
	})
	if err != nil {
		t.Fatalf("normalizedSkillArtifactFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("len(files) = %d, want 2", len(files))
	}
	if files[0].Path != "SKILL.md" {
		t.Fatalf("first path = %q, want SKILL.md", files[0].Path)
	}
	if files[1].Path != "docs/guide.md" {
		t.Fatalf("second path = %q, want docs/guide.md", files[1].Path)
	}
}

func TestSkillArtifactContentDigestIsStableAcrossFileOrder(t *testing.T) {
	first, err := skillArtifactContentDigest([]storedSkillFile{
		{Path: "SKILL.md", Content: []byte("# demo")},
		{Path: "docs/guide.md", Content: []byte("guide")},
	})
	if err != nil {
		t.Fatalf("skillArtifactContentDigest first: %v", err)
	}
	second, err := skillArtifactContentDigest([]storedSkillFile{
		{Path: "docs/guide.md", Content: []byte("guide")},
		{Path: "SKILL.md", Content: []byte("# demo")},
	})
	if err != nil {
		t.Fatalf("skillArtifactContentDigest second: %v", err)
	}
	if first == second {
		t.Fatal("digest should depend on canonical file ordering; got identical digests for unsorted input")
	}

	normalizedFirst, err := normalizedSkillArtifactFiles(&parsedSkillUpload{
		Directory: "demo-skill",
		Files: []uploadedSkillFile{
			{Path: "demo-skill/SKILL.md", Content: []byte("# demo")},
			{Path: "demo-skill/docs/guide.md", Content: []byte("guide")},
		},
	})
	if err != nil {
		t.Fatalf("normalizedSkillArtifactFiles first: %v", err)
	}
	normalizedSecond, err := normalizedSkillArtifactFiles(&parsedSkillUpload{
		Directory: "demo-skill",
		Files: []uploadedSkillFile{
			{Path: "demo-skill/docs/guide.md", Content: []byte("guide")},
			{Path: "demo-skill/SKILL.md", Content: []byte("# demo")},
		},
	})
	if err != nil {
		t.Fatalf("normalizedSkillArtifactFiles second: %v", err)
	}
	first, err = skillArtifactContentDigest(normalizedFirst)
	if err != nil {
		t.Fatalf("skillArtifactContentDigest normalized first: %v", err)
	}
	second, err = skillArtifactContentDigest(normalizedSecond)
	if err != nil {
		t.Fatalf("skillArtifactContentDigest normalized second: %v", err)
	}
	if first != second {
		t.Fatalf("normalized digests differ: %q vs %q", first, second)
	}
}
