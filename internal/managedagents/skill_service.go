package managedagents

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

func (s *Service) CreateSkill(ctx context.Context, principal Principal, displayTitle *string, files []uploadedSkillFile) (*Skill, error) {
	if s.assetStore == nil {
		return nil, errors.New("asset store is not configured")
	}
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
	store, err := s.ensureTeamAssetStore(ctx, RequestCredential{}, principal.TeamID)
	if err != nil {
		return nil, err
	}
	bundleContent, bundle, err := buildSkillBundle(parsed)
	if err != nil {
		return nil, err
	}
	bundle.Path = teamSkillBundleAssetStorePath(skill.ID, version.Version)
	storedBundle, err := s.assetStore.PutObject(ctx, RequestCredential{}, AssetStorePutObjectRequest{
		TeamID:   principal.TeamID,
		RegionID: store.RegionID,
		VolumeID: store.VolumeID,
		Path:     bundle.Path,
		Content:  bytes.NewReader(bundleContent),
	})
	if err != nil {
		return nil, err
	}
	bundle = storedSkillBundle{
		Path:      storedBundle.Path,
		SHA256:    storedBundle.SHA256,
		SizeBytes: storedBundle.SizeBytes,
	}
	if err := s.repo.CreateSkillWithVersion(ctx, principal.TeamID, skill, version, []storedSkillFile{}, bundle, now); err != nil {
		_ = s.assetStore.DeleteObject(ctx, RequestCredential{}, AssetStoreDeleteObjectRequest{
			TeamID:   principal.TeamID,
			RegionID: store.RegionID,
			VolumeID: store.VolumeID,
			Path:     bundle.Path,
		})
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
	storedVersions, err := s.repo.ListStoredSkillVersions(ctx, principal.TeamID, skillID)
	if err != nil {
		if errors.Is(err, ErrSkillNotFound) {
			return nil, err
		}
		return nil, err
	}
	if err := s.repo.DeleteSkill(ctx, principal.TeamID, skillID); err != nil {
		return nil, err
	}
	s.cleanupStoredSkillBundles(ctx, principal.TeamID, storedVersions)
	return deletedObject("skill_deleted", skillID), nil
}

func (s *Service) CreateSkillVersion(ctx context.Context, principal Principal, skillID string, files []uploadedSkillFile) (*SkillVersion, error) {
	if s.assetStore == nil {
		return nil, errors.New("asset store is not configured")
	}
	parsed, err := parseSkillUpload(files, skillID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	versionValue := strconv.FormatInt(now.UnixMicro(), 10)
	version := buildSkillVersionObject(NewID("skillver"), skillID, versionValue, parsed, now)
	store, err := s.ensureTeamAssetStore(ctx, RequestCredential{}, principal.TeamID)
	if err != nil {
		return nil, err
	}
	bundleContent, bundle, err := buildSkillBundle(parsed)
	if err != nil {
		return nil, err
	}
	bundle.Path = teamSkillBundleAssetStorePath(skillID, version.Version)
	storedBundle, err := s.assetStore.PutObject(ctx, RequestCredential{}, AssetStorePutObjectRequest{
		TeamID:   principal.TeamID,
		RegionID: store.RegionID,
		VolumeID: store.VolumeID,
		Path:     bundle.Path,
		Content:  bytes.NewReader(bundleContent),
	})
	if err != nil {
		return nil, err
	}
	bundle = storedSkillBundle{
		Path:      storedBundle.Path,
		SHA256:    storedBundle.SHA256,
		SizeBytes: storedBundle.SizeBytes,
	}
	if err := s.repo.CreateSkillVersion(ctx, principal.TeamID, skillID, version, []storedSkillFile{}, bundle, now); err != nil {
		_ = s.assetStore.DeleteObject(ctx, RequestCredential{}, AssetStoreDeleteObjectRequest{
			TeamID:   principal.TeamID,
			RegionID: store.RegionID,
			VolumeID: store.VolumeID,
			Path:     bundle.Path,
		})
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
	stored, err := s.repo.GetStoredSkillVersion(ctx, principal.TeamID, skillID, version)
	if err != nil {
		return nil, err
	}
	if err := s.repo.DeleteSkillVersion(ctx, principal.TeamID, skillID, version, time.Now().UTC()); err != nil {
		return nil, err
	}
	s.cleanupStoredSkillBundles(ctx, principal.TeamID, []StoredSkillVersion{*stored})
	return deletedObject("skill_version_deleted", version), nil
}

func (s *Service) cleanupStoredSkillBundles(ctx context.Context, teamID string, versions []StoredSkillVersion) {
	if s.assetStore == nil || len(versions) == 0 {
		return
	}
	paths := make([]string, 0, len(versions))
	for _, version := range versions {
		if path := strings.TrimSpace(version.Bundle.Path); path != "" {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return
	}
	store, err := s.getTeamAssetStore(ctx, RequestCredential{}, teamID)
	if err != nil {
		s.logger.Warn("failed to resolve team asset store during skill bundle cleanup", zap.String("team_id", teamID), zap.Error(err))
		return
	}
	for _, path := range paths {
		if err := s.assetStore.DeleteObject(ctx, RequestCredential{}, AssetStoreDeleteObjectRequest{
			TeamID:   teamID,
			RegionID: store.RegionID,
			VolumeID: store.VolumeID,
			Path:     path,
		}); err != nil {
			s.logger.Warn("failed to delete skill bundle from team asset store", zap.String("team_id", teamID), zap.String("path", path), zap.Error(err))
		}
	}
}
