package managedagents

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBackfillTeamAssetStoreMigratesLegacyFiles(t *testing.T) {
	repo := newTestRepository(t)
	store := newTestAssetStore()
	service := NewService(repo, noopRuntimeManager{}, nil, WithAssetStore(store))
	principal := Principal{TeamID: "team_123"}
	ctx := context.Background()
	now := time.Now().UTC()

	legacyVolume := &managedFileRecord{
		ID:                "file_legacy_volume",
		TeamID:            principal.TeamID,
		Filename:          "legacy-volume.txt",
		MimeType:          "text/plain",
		SizeBytes:         int64(len("legacy-volume-bytes")),
		FileStoreVolumeID: "vol_legacy",
		FileStorePath:     "/managed-agent-files/team_123/file_legacy_volume/content",
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	store.objects[testAssetStoreKey(legacyVolume.FileStoreVolumeID, legacyVolume.FileStorePath)] = []byte("legacy-volume-bytes")
	if err := repo.CreateFile(ctx, legacyVolume); err != nil {
		t.Fatalf("CreateFile legacyVolume: %v", err)
	}

	legacyPostgres := &managedFileRecord{
		ID:        "file_legacy_postgres",
		TeamID:    principal.TeamID,
		Filename:  "legacy-postgres.txt",
		MimeType:  "text/plain",
		SizeBytes: int64(len("legacy-postgres-bytes")),
		Content:   []byte("legacy-postgres-bytes"),
		CreatedAt: now.Add(time.Millisecond),
		UpdatedAt: now.Add(time.Millisecond),
	}
	if err := repo.CreateFile(ctx, legacyPostgres); err != nil {
		t.Fatalf("CreateFile legacyPostgres: %v", err)
	}

	alreadyBackfilled := &managedFileRecord{
		ID:        "file_new_store",
		TeamID:    principal.TeamID,
		Filename:  "new-store.txt",
		MimeType:  "text/plain",
		SizeBytes: 3,
		StorePath: teamFileAssetStorePath("file_new_store"),
		SHA256:    "existing-sha",
		CreatedAt: now.Add(2 * time.Millisecond),
		UpdatedAt: now.Add(2 * time.Millisecond),
	}
	if err := repo.CreateFile(ctx, alreadyBackfilled); err != nil {
		t.Fatalf("CreateFile alreadyBackfilled: %v", err)
	}

	summary, err := service.BackfillTeamAssetStore(ctx, RequestCredential{}, AssetBackfillOptions{
		Files:     true,
		Skills:    false,
		BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("BackfillTeamAssetStore: %v", err)
	}
	if summary.Files.Scanned != 2 || summary.Files.Migrated != 2 || summary.Files.Failed != 0 || summary.Files.Remaining != 0 {
		t.Fatalf("file summary = %#v", summary.Files)
	}
	if summary.TeamStoresCreated != 1 {
		t.Fatalf("team stores created = %d, want 1", summary.TeamStoresCreated)
	}

	teamStore, err := repo.GetTeamAssetStore(ctx, principal.TeamID, "default")
	if err != nil {
		t.Fatalf("GetTeamAssetStore: %v", err)
	}
	legacyVolumeStored, err := repo.GetFile(ctx, principal.TeamID, legacyVolume.ID)
	if err != nil {
		t.Fatalf("GetFile legacyVolume: %v", err)
	}
	if strings.TrimSpace(legacyVolumeStored.StorePath) == "" {
		t.Fatal("legacy volume file store_path = empty, want migrated path")
	}
	if got := string(store.objects[testAssetStoreKey(teamStore.VolumeID, legacyVolumeStored.StorePath)]); got != "legacy-volume-bytes" {
		t.Fatalf("migrated legacy volume content = %q", got)
	}

	legacyPostgresStored, err := repo.GetFile(ctx, principal.TeamID, legacyPostgres.ID)
	if err != nil {
		t.Fatalf("GetFile legacyPostgres: %v", err)
	}
	if strings.TrimSpace(legacyPostgresStored.StorePath) == "" {
		t.Fatal("legacy postgres file store_path = empty, want migrated path")
	}
	if got := string(store.objects[testAssetStoreKey(teamStore.VolumeID, legacyPostgresStored.StorePath)]); got != "legacy-postgres-bytes" {
		t.Fatalf("migrated legacy postgres content = %q", got)
	}
}

func TestBackfillTeamAssetStoreMigratesLegacySkillBundlesIdempotently(t *testing.T) {
	repo := newTestRepository(t)
	store := newTestAssetStore()
	service := NewService(repo, noopRuntimeManager{}, nil, WithAssetStore(store))
	principal := Principal{TeamID: "team_123"}
	ctx := context.Background()
	now := time.Now().UTC()

	parsed := &parsedSkillUpload{
		Name:        "Demo Skill",
		Description: "legacy skill",
		Directory:   "demo-skill",
		Files: []uploadedSkillFile{
			{Path: "demo-skill/SKILL.md", Content: []byte("---\nname: Demo Skill\ndescription: legacy skill\n---\n")},
			{Path: "demo-skill/helper.txt", Content: []byte("hello from legacy skill")},
		},
	}
	skill := buildSkillObject("skill_legacy", nil, nil, now)
	version := buildSkillVersionObject("skillver_legacy", skill.ID, "1", parsed, now)
	if err := repo.CreateSkillWithVersion(ctx, principal.TeamID, skill, version, buildStoredSkillFiles(parsed), storedSkillBundle{}, now); err != nil {
		t.Fatalf("CreateSkillWithVersion: %v", err)
	}

	firstSummary, err := service.BackfillTeamAssetStore(ctx, RequestCredential{}, AssetBackfillOptions{
		Files:     false,
		Skills:    true,
		BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("BackfillTeamAssetStore first run: %v", err)
	}
	if firstSummary.Skills.Scanned != 1 || firstSummary.Skills.Migrated != 1 || firstSummary.Skills.Failed != 0 || firstSummary.Skills.Remaining != 0 {
		t.Fatalf("first skill summary = %#v", firstSummary.Skills)
	}

	storedVersion, err := repo.GetStoredSkillVersion(ctx, principal.TeamID, skill.ID, version.Version)
	if err != nil {
		t.Fatalf("GetStoredSkillVersion: %v", err)
	}
	if strings.TrimSpace(storedVersion.Bundle.Path) == "" {
		t.Fatal("stored skill bundle path = empty, want migrated bundle")
	}
	teamStore, err := repo.GetTeamAssetStore(ctx, principal.TeamID, "default")
	if err != nil {
		t.Fatalf("GetTeamAssetStore: %v", err)
	}
	if _, ok := store.objects[testAssetStoreKey(teamStore.VolumeID, storedVersion.Bundle.Path)]; !ok {
		t.Fatalf("missing migrated skill bundle for %s", storedVersion.Bundle.Path)
	}

	secondSummary, err := service.BackfillTeamAssetStore(ctx, RequestCredential{}, AssetBackfillOptions{
		Files:     false,
		Skills:    true,
		BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("BackfillTeamAssetStore second run: %v", err)
	}
	if secondSummary.Skills.Scanned != 0 || secondSummary.Skills.Migrated != 0 || secondSummary.Skills.Failed != 0 || secondSummary.Skills.Remaining != 0 {
		t.Fatalf("second skill summary = %#v, want no-op", secondSummary.Skills)
	}
}
