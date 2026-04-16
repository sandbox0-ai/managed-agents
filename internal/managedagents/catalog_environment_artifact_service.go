package managedagents

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.uber.org/zap"
)

func (s *Service) ensureEnvironmentArtifactRecord(ctx context.Context, teamID string, environment *Environment) (*EnvironmentArtifact, error) {
	if environment == nil {
		return nil, ErrEnvironmentNotFound
	}
	compatibility := defaultEnvironmentArtifactCompatibility()
	digest, err := environmentArtifactDigest(environment.Config, compatibility)
	if err != nil {
		return nil, err
	}
	if artifact, err := s.repo.GetEnvironmentArtifactByDigest(ctx, teamID, environment.ID, digest); err == nil {
		return artifact, nil
	} else if !errors.Is(err, ErrEnvironmentArtifactNotFound) {
		return nil, err
	}
	now := time.Now().UTC()
	artifact := &EnvironmentArtifact{
		ID:             NewID("envart"),
		TeamID:         teamID,
		EnvironmentID:  environment.ID,
		Digest:         digest,
		Status:         EnvironmentArtifactStatusPending,
		ConfigSnapshot: environmentConfigToMap(environment.Config),
		Compatibility:  compatibility,
		Assets:         EnvironmentArtifactAssets{},
		BuildLog:       "",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.repo.CreateEnvironmentArtifact(ctx, artifact); err != nil {
		if existing, lookupErr := s.repo.GetEnvironmentArtifactByDigest(ctx, teamID, environment.ID, digest); lookupErr == nil {
			return existing, nil
		}
		return nil, err
	}
	return artifact, nil
}

func ensureEnvironmentUsable(environment *Environment) error {
	if environment == nil {
		return ErrEnvironmentNotFound
	}
	if environment.ArchivedAt != nil {
		return errors.New("archived environments cannot create new sessions")
	}
	return nil
}

func (s *Service) prebuildEnvironmentArtifact(ctx context.Context, credential RequestCredential, teamID string, environment *Environment, artifact *EnvironmentArtifact) {
	prebuilder, ok := s.runtime.(EnvironmentArtifactPrebuilder)
	if !ok || prebuilder == nil || environment == nil || artifact == nil {
		return
	}
	switch strings.TrimSpace(artifact.Status) {
	case EnvironmentArtifactStatusPending, EnvironmentArtifactStatusFailed:
	default:
		return
	}
	environmentSnapshot := cloneEnvironmentForPrebuild(environment)
	environmentID := environment.ID
	artifactID := artifact.ID
	prebuildCtx := context.WithoutCancel(ctx)
	go func() {
		if err := prebuilder.PrebuildEnvironmentArtifact(prebuildCtx, credential, teamID, environmentSnapshot); err != nil {
			s.logger.Warn("prebuild environment artifact failed", zap.Error(err), zap.String("team_id", teamID), zap.String("environment_id", environmentID), zap.String("artifact_id", artifactID))
		}
	}()
}

func cloneEnvironmentForPrebuild(environment *Environment) *Environment {
	if environment == nil {
		return nil
	}
	clone := *environment
	clone.Metadata = cloneStringMap(environment.Metadata)
	clone.Config = environmentConfigFromMap(environmentConfigToMap(environment.Config))
	if environment.ArchivedAt != nil {
		archivedAt := *environment.ArchivedAt
		clone.ArchivedAt = &archivedAt
	}
	return &clone
}
