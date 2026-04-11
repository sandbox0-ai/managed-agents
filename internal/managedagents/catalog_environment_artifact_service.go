package managedagents

import (
	"context"
	"errors"
	"time"
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
