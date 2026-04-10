package managedagents

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

func (s *Service) CreateSkill(ctx context.Context, principal Principal, displayTitle *string, files []uploadedSkillFile) (*Skill, error) {
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
	if err := s.repo.CreateSkillWithVersion(ctx, principal.TeamID, skill, version, buildStoredSkillFiles(parsed), now); err != nil {
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
		return s.anthropicSkills.ListSkills(ctx, limit, page)
	}
	if trimmedSource == "" {
		return s.listAllSkills(ctx, principal, limit, page)
	}
	return s.repo.ListSkills(ctx, principal.TeamID, limit, page, trimmedSource)
}

func (s *Service) GetSkill(ctx context.Context, principal Principal, skillID string) (*Skill, error) {
	if skill, err := s.anthropicSkills.GetSkill(ctx, skillID); err == nil {
		return skill, nil
	} else if !errors.Is(err, ErrSkillNotFound) {
		return nil, err
	}
	return s.repo.GetSkill(ctx, principal.TeamID, skillID)
}

func (s *Service) DeleteSkill(ctx context.Context, principal Principal, skillID string) (map[string]any, error) {
	if err := s.repo.DeleteSkill(ctx, principal.TeamID, skillID); err != nil {
		return nil, err
	}
	return deletedObject("skill_deleted", skillID), nil
}

func (s *Service) CreateSkillVersion(ctx context.Context, principal Principal, skillID string, files []uploadedSkillFile) (*SkillVersion, error) {
	parsed, err := parseSkillUpload(files, skillID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	versionValue := strconv.FormatInt(now.UnixMicro(), 10)
	version := buildSkillVersionObject(NewID("skillver"), skillID, versionValue, parsed, now)
	if err := s.repo.CreateSkillVersion(ctx, principal.TeamID, skillID, version, buildStoredSkillFiles(parsed), now); err != nil {
		return nil, err
	}
	return &version, nil
}

func (s *Service) ListSkillVersions(ctx context.Context, principal Principal, skillID string, limit int, page string) ([]SkillVersion, *string, bool, error) {
	if _, err := s.anthropicSkills.GetSkill(ctx, skillID); err == nil {
		return s.anthropicSkills.ListSkillVersions(ctx, skillID, limit, page)
	} else if !errors.Is(err, ErrSkillNotFound) {
		return nil, nil, false, err
	}
	return s.repo.ListSkillVersions(ctx, principal.TeamID, skillID, limit, page)
}

func (s *Service) GetSkillVersion(ctx context.Context, principal Principal, skillID, version string) (*SkillVersion, error) {
	if _, err := s.anthropicSkills.GetSkill(ctx, skillID); err == nil {
		return s.anthropicSkills.GetSkillVersion(ctx, skillID, version)
	} else if !errors.Is(err, ErrSkillNotFound) {
		return nil, err
	}
	return s.repo.GetSkillVersion(ctx, principal.TeamID, skillID, version)
}

func (s *Service) DeleteSkillVersion(ctx context.Context, principal Principal, skillID, version string) (map[string]any, error) {
	if err := s.repo.DeleteSkillVersion(ctx, principal.TeamID, skillID, version, time.Now().UTC()); err != nil {
		return nil, err
	}
	return deletedObject("skill_version_deleted", version), nil
}
