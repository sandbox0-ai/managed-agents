package managedagents

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

func (s *Service) CreateSkill(ctx context.Context, principal Principal, credential RequestCredential, displayTitle *string, files []uploadedSkillFile) (*Skill, error) {
	fallbackDirectory := ""
	if displayTitle != nil {
		fallbackDirectory = *displayTitle
	}
	parsed, err := parseSkillUpload(files, fallbackDirectory)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	skillID := NewID("skill")
	versionValue := strconv.FormatInt(now.UnixMicro(), 10)
	skill := buildSkillObject(skillID, displayTitle, &versionValue, now)
	version := buildSkillVersionObject(NewID("skillver"), skillID, versionValue, parsed, now)
	artifact, err := s.prepareSkillVersionStorage(ctx, credential, principal.TeamID, skillID, versionValue, parsed)
	if err != nil {
		return nil, err
	}
	if err := s.repo.CreateSkillWithVersion(ctx, principal.TeamID, skill, parsed.Name, version, artifact, now); err != nil {
		s.cleanupStoredSkillArtifact(ctx, credential, principal.TeamID, skillID, versionValue, artifact)
		return nil, err
	}
	return &skill, nil
}

func (s *Service) ListSkills(ctx context.Context, principal Principal, limit int, page, source string) ([]Skill, *string, bool, error) {
	trimmedSource := strings.TrimSpace(source)
	if trimmedSource != "" && trimmedSource != "custom" && trimmedSource != "anthropic" {
		return nil, nil, false, errors.New("source must be custom or anthropic")
	}
	if trimmedSource == "anthropic" {
		return []Skill{}, nil, false, nil
	}
	if trimmedSource == "" {
		return s.listAllSkills(ctx, principal, limit, page)
	}
	return s.repo.ListSkills(ctx, principal.TeamID, limit, page, trimmedSource)
}

func (s *Service) GetSkill(ctx context.Context, principal Principal, skillID string) (*Skill, error) {
	return s.repo.GetSkill(ctx, principal.TeamID, skillID)
}

func (s *Service) DeleteSkill(ctx context.Context, principal Principal, skillID string) (map[string]any, error) {
	if err := s.repo.DeleteSkill(ctx, principal.TeamID, skillID); err != nil {
		return nil, err
	}
	return deletedObject("skill_deleted", skillID), nil
}

func (s *Service) CreateSkillVersion(ctx context.Context, principal Principal, credential RequestCredential, skillID string, files []uploadedSkillFile) (*SkillVersion, error) {
	parsed, err := parseSkillUpload(files, skillID)
	if err != nil {
		return nil, err
	}
	storedSkill, err := s.repo.GetStoredSkill(ctx, principal.TeamID, skillID)
	if err != nil {
		return nil, err
	}
	existingMountSlug := strings.TrimSpace(storedSkill.MountSlug)
	if existingMountSlug == "" {
		existingMountSlug = strings.TrimSpace(parsed.Name)
	}
	if existingMountSlug != "" && strings.TrimSpace(parsed.Name) != "" && existingMountSlug != strings.TrimSpace(parsed.Name) {
		return nil, errors.New("skill name must remain stable across versions")
	}
	now := time.Now().UTC()
	versionValue := strconv.FormatInt(now.UnixMicro(), 10)
	version := buildSkillVersionObject(NewID("skillver"), skillID, versionValue, parsed, now)
	artifact, err := s.prepareSkillVersionStorage(ctx, credential, principal.TeamID, skillID, versionValue, parsed)
	if err != nil {
		return nil, err
	}
	if err := s.repo.CreateSkillVersion(ctx, principal.TeamID, skillID, version, existingMountSlug, artifact, now); err != nil {
		s.cleanupStoredSkillArtifact(ctx, credential, principal.TeamID, skillID, versionValue, artifact)
		return nil, err
	}
	return &version, nil
}

func (s *Service) ListSkillVersions(ctx context.Context, principal Principal, skillID string, limit int, page string) ([]SkillVersion, *string, bool, error) {
	return s.repo.ListSkillVersions(ctx, principal.TeamID, skillID, limit, page)
}

func (s *Service) GetSkillVersion(ctx context.Context, principal Principal, skillID, version string) (*SkillVersion, error) {
	return s.repo.GetSkillVersion(ctx, principal.TeamID, skillID, version)
}

func (s *Service) DeleteSkillVersion(ctx context.Context, principal Principal, skillID, version string) (map[string]any, error) {
	if err := s.repo.DeleteSkillVersion(ctx, principal.TeamID, skillID, version, time.Now().UTC()); err != nil {
		return nil, err
	}
	return deletedObject("skill_version_deleted", version), nil
}

func (s *Service) prepareSkillVersionStorage(ctx context.Context, credential RequestCredential, teamID, skillID, version string, parsed *parsedSkillUpload) (skillVersionArtifact, error) {
	if parsed == nil {
		return skillVersionArtifact{}, errors.New("parsed skill upload is required")
	}
	if s.skillArtifactStore == nil {
		return skillVersionArtifact{}, errors.New("skill artifact store is required")
	}
	artifact, err := buildSkillArtifact(parsed)
	if err != nil {
		return skillVersionArtifact{}, err
	}
	stored, err := s.skillArtifactStore.PutSkillVersion(ctx, credential, SkillArtifactPutRequest{
		TeamID:        teamID,
		SkillID:       skillID,
		Version:       version,
		ContentDigest: artifact.ContentDigest,
		Content:       bytes.NewReader(artifact.Archive),
	})
	if err != nil {
		return skillVersionArtifact{}, err
	}
	return skillVersionArtifact{
		VolumeID:      stored.VolumeID,
		Path:          stored.Path,
		ContentDigest: artifact.ContentDigest,
		ArchiveSHA256: stored.SHA256,
		SizeBytes:     stored.SizeBytes,
		FileCount:     artifact.FileCount,
	}, nil
}

func (s *Service) cleanupStoredSkillArtifact(ctx context.Context, credential RequestCredential, teamID, skillID, version string, artifact skillVersionArtifact) {
	if s.skillArtifactStore == nil || strings.TrimSpace(artifact.VolumeID) == "" || strings.TrimSpace(artifact.Path) == "" {
		return
	}
	_ = s.skillArtifactStore.DeleteSkillVersion(ctx, credential, SkillArtifactDeleteRequest{
		TeamID:   teamID,
		SkillID:  skillID,
		Version:  version,
		VolumeID: artifact.VolumeID,
		Path:     artifact.Path,
	})
}
