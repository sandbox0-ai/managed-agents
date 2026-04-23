package managedagents

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestListAnthropicSkillsReturnsEmpty(t *testing.T) {
	service := NewService(&Repository{}, nil, nil)
	items, nextPage, hasMore, err := service.ListSkills(t.Context(), Principal{TeamID: "team_123"}, 2, "", "anthropic")
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0", len(items))
	}
	if hasMore || nextPage != nil {
		t.Fatalf("hasMore/nextPage = %v/%v, want false/nil", hasMore, nextPage)
	}
}

func TestNormalizeAgentSkillsRejectsAnthropicPrebuiltSkill(t *testing.T) {
	service := NewService(&Repository{}, nil, nil)
	_, err := service.normalizeAgentSkills(t.Context(), Principal{TeamID: "team_123"}, []any{map[string]any{
		"type":     "anthropic",
		"skill_id": "xlsx",
	}})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("error = %v, want unsupported anthropic pre-built skill", err)
	}
}

func TestListSkillsWithoutSourceIncludesOnlyCustomSkills(t *testing.T) {
	repo := newTestRepository(t)
	store := newTestAssetStore()
	service := NewService(repo, noopRuntimeManager{}, nil, WithAssetStore(store))
	principal := Principal{TeamID: "team_123"}
	_, err := service.CreateSkill(context.Background(), principal, nil, []uploadedSkillFile{{
		Path:    "demo-skill/SKILL.md",
		Content: []byte("---\nname: demo-skill\ndescription: >-\n  Demo skill\n---\n\n# Demo Skill\n"),
	}})
	if err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}
	items, nextPage, hasMore, err := service.ListSkills(context.Background(), principal, 5, "", "")
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if got := items[0].Source; got != "custom" {
		t.Fatalf("first source = %q, want custom", got)
	}
	if hasMore && nextPage == nil {
		t.Fatal("expected next_page when has_more is true")
	}
}

func TestCreateSkillStoresBundleOutsidePostgres(t *testing.T) {
	repo := newTestRepository(t)
	store := newTestAssetStore()
	service := NewService(repo, noopRuntimeManager{}, nil, WithAssetStore(store))
	ctx := context.Background()
	principal := Principal{TeamID: "team_123"}

	created, err := service.CreateSkill(ctx, principal, nil, []uploadedSkillFile{
		{
			Path:    "demo-skill/SKILL.md",
			Content: []byte("---\nname: demo-skill\ndescription: Demo skill\n---\n\n# Demo Skill\n"),
		},
		{
			Path:    "demo-skill/docs/guide.md",
			Content: []byte("guide"),
		},
	})
	if err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}
	if created.LatestVersion == nil || *created.LatestVersion == "" {
		t.Fatalf("latest_version = %#v, want populated", created.LatestVersion)
	}

	stored, err := repo.GetStoredSkillVersion(ctx, principal.TeamID, created.ID, *created.LatestVersion)
	if err != nil {
		t.Fatalf("GetStoredSkillVersion: %v", err)
	}
	if stored.Bundle.Path == "" {
		t.Fatal("bundle path is empty")
	}

	teamStore, err := repo.GetTeamAssetStore(ctx, principal.TeamID, "default")
	if err != nil {
		t.Fatalf("GetTeamAssetStore: %v", err)
	}
	bundleContent, ok := store.objects[testAssetStoreKey(teamStore.VolumeID, stored.Bundle.Path)]
	if !ok {
		t.Fatalf("missing bundle content for %s", stored.Bundle.Path)
	}
	bundleFiles := readSkillBundleFiles(t, bundleContent)
	if got := string(bundleFiles["demo-skill/SKILL.md"]); !strings.Contains(got, "name: demo-skill") {
		t.Fatalf("bundle SKILL.md = %q", got)
	}
	if got := string(bundleFiles["demo-skill/docs/guide.md"]); got != "guide" {
		t.Fatalf("bundle guide = %q, want guide", got)
	}
}

func TestDeleteSkillRemovesStoredBundles(t *testing.T) {
	repo := newTestRepository(t)
	store := newTestAssetStore()
	service := NewService(repo, noopRuntimeManager{}, nil, WithAssetStore(store))
	ctx := context.Background()
	principal := Principal{TeamID: "team_123"}

	created, err := service.CreateSkill(ctx, principal, nil, []uploadedSkillFile{{
		Path:    "demo-skill/SKILL.md",
		Content: []byte("---\nname: demo-skill\ndescription: Demo skill\n---\n\n# Demo Skill\n"),
	}})
	if err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}
	version, err := service.CreateSkillVersion(ctx, principal, created.ID, []uploadedSkillFile{{
		Path:    "demo-skill/SKILL.md",
		Content: []byte("---\nname: demo-skill\ndescription: Demo skill v2\n---\n\n# Demo Skill\n"),
	}})
	if err != nil {
		t.Fatalf("CreateSkillVersion: %v", err)
	}
	if store.createStoreCalls != 1 {
		t.Fatalf("createStoreCalls = %d, want 1", store.createStoreCalls)
	}

	storedVersions, err := repo.ListStoredSkillVersions(ctx, principal.TeamID, created.ID)
	if err != nil {
		t.Fatalf("ListStoredSkillVersions: %v", err)
	}
	if len(storedVersions) != 2 {
		t.Fatalf("stored version count = %d, want 2", len(storedVersions))
	}
	teamStore, err := repo.GetTeamAssetStore(ctx, principal.TeamID, "default")
	if err != nil {
		t.Fatalf("GetTeamAssetStore: %v", err)
	}
	for _, stored := range storedVersions {
		if _, ok := store.objects[testAssetStoreKey(teamStore.VolumeID, stored.Bundle.Path)]; !ok {
			t.Fatalf("missing stored bundle before delete: %s", stored.Bundle.Path)
		}
	}

	if _, err := service.DeleteSkill(ctx, principal, created.ID); err != nil {
		t.Fatalf("DeleteSkill: %v", err)
	}
	for _, stored := range storedVersions {
		if _, ok := store.objects[testAssetStoreKey(teamStore.VolumeID, stored.Bundle.Path)]; ok {
			t.Fatalf("bundle still present after delete: %s", stored.Bundle.Path)
		}
	}
	if _, err := repo.GetSkillVersion(ctx, principal.TeamID, created.ID, version.Version); err == nil {
		t.Fatal("expected deleted skill version to be gone")
	}
}

func TestCreateSkillVersionMissingSkillCleansUploadedBundle(t *testing.T) {
	repo := newTestRepository(t)
	store := newTestAssetStore()
	service := NewService(repo, noopRuntimeManager{}, nil, WithAssetStore(store))
	ctx := context.Background()
	principal := Principal{TeamID: "team_123"}

	_, err := service.CreateSkillVersion(ctx, principal, "skill_missing", []uploadedSkillFile{{
		Path:    "demo-skill/SKILL.md",
		Content: []byte("---\nname: demo-skill\ndescription: Demo skill\n---\n\n# Demo Skill\n"),
	}})
	if !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("CreateSkillVersion error = %v, want ErrSkillNotFound", err)
	}
	if store.createStoreCalls != 1 {
		t.Fatalf("createStoreCalls = %d, want 1", store.createStoreCalls)
	}
	if len(store.objects) != 0 {
		t.Fatalf("asset-store objects = %#v, want empty after rollback", store.objects)
	}
}

func TestDeleteSkillVersionRemovesOnlyRequestedBundle(t *testing.T) {
	repo := newTestRepository(t)
	store := newTestAssetStore()
	service := NewService(repo, noopRuntimeManager{}, nil, WithAssetStore(store))
	ctx := context.Background()
	principal := Principal{TeamID: "team_123"}

	created, err := service.CreateSkill(ctx, principal, nil, []uploadedSkillFile{{
		Path:    "demo-skill/SKILL.md",
		Content: []byte("---\nname: demo-skill\ndescription: Demo skill\n---\n\n# Demo Skill\n"),
	}})
	if err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}
	firstVersion := *created.LatestVersion
	second, err := service.CreateSkillVersion(ctx, principal, created.ID, []uploadedSkillFile{{
		Path:    "demo-skill/SKILL.md",
		Content: []byte("---\nname: demo-skill\ndescription: Demo skill v2\n---\n\n# Demo Skill\n"),
	}})
	if err != nil {
		t.Fatalf("CreateSkillVersion: %v", err)
	}

	storedVersions, err := repo.ListStoredSkillVersions(ctx, principal.TeamID, created.ID)
	if err != nil {
		t.Fatalf("ListStoredSkillVersions: %v", err)
	}
	if len(storedVersions) != 2 {
		t.Fatalf("stored version count = %d, want 2", len(storedVersions))
	}
	teamStore, err := repo.GetTeamAssetStore(ctx, principal.TeamID, "default")
	if err != nil {
		t.Fatalf("GetTeamAssetStore: %v", err)
	}
	bundlePathByVersion := map[string]string{}
	for _, stored := range storedVersions {
		bundlePathByVersion[stored.Snapshot.Version] = stored.Bundle.Path
	}

	if _, err := service.DeleteSkillVersion(ctx, principal, created.ID, second.Version); err != nil {
		t.Fatalf("DeleteSkillVersion: %v", err)
	}
	if _, ok := store.objects[testAssetStoreKey(teamStore.VolumeID, bundlePathByVersion[second.Version])]; ok {
		t.Fatalf("deleted version bundle still present: %s", bundlePathByVersion[second.Version])
	}
	if _, ok := store.objects[testAssetStoreKey(teamStore.VolumeID, bundlePathByVersion[firstVersion])]; !ok {
		t.Fatalf("previous version bundle missing: %s", bundlePathByVersion[firstVersion])
	}
	skill, err := service.GetSkill(ctx, principal, created.ID)
	if err != nil {
		t.Fatalf("GetSkill: %v", err)
	}
	if skill.LatestVersion == nil || *skill.LatestVersion != firstVersion {
		t.Fatalf("latest_version = %#v, want %q", skill.LatestVersion, firstVersion)
	}
	if _, err := repo.GetSkillVersion(ctx, principal.TeamID, created.ID, second.Version); !errors.Is(err, ErrSkillVersionNotFound) {
		t.Fatalf("GetSkillVersion deleted version error = %v, want ErrSkillVersionNotFound", err)
	}
}

func readSkillBundleFiles(t *testing.T, content []byte) map[string][]byte {
	t.Helper()

	reader, err := gzip.NewReader(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer reader.Close()

	tarReader := tar.NewReader(reader)
	files := map[string][]byte{}
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		data, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatalf("read tar entry %s: %v", header.Name, err)
		}
		files[header.Name] = data
	}
}
