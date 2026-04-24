package managedagents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

type preparedEnvironmentArtifact struct {
	artifact *EnvironmentArtifact
	persist  func(context.Context) error
	cleanup  func(context.Context)
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

func (s *Service) readyEnvironmentArtifactForSession(ctx context.Context, teamID string, environment *Environment) (*EnvironmentArtifact, error) {
	if environment == nil {
		return nil, ErrEnvironmentNotFound
	}
	compatibility := defaultEnvironmentArtifactCompatibility()
	digest, err := environmentArtifactDigest(environment.Config, compatibility)
	if err != nil {
		return nil, err
	}
	artifact, err := s.repo.GetEnvironmentArtifactByDigest(ctx, teamID, environment.ID, digest)
	if err != nil {
		if errors.Is(err, ErrEnvironmentArtifactNotFound) && len(ConfiguredEnvironmentPackageManagers(environment.Config)) == 0 {
			prepared, prepErr := s.prepareReadyEnvironmentArtifact(ctx, RequestCredential{}, teamID, environment)
			if prepErr != nil {
				return nil, prepErr
			}
			if persistErr := prepared.persist(ctx); persistErr != nil {
				return nil, persistErr
			}
			return prepared.artifact, nil
		}
		if errors.Is(err, ErrEnvironmentArtifactNotFound) {
			return nil, fmt.Errorf("%w: environment packages are not prepared", ErrEnvironmentBuildFailed)
		}
		return nil, err
	}
	switch strings.TrimSpace(artifact.Status) {
	case EnvironmentArtifactStatusReady:
		return artifact, nil
	case EnvironmentArtifactStatusBuilding:
		return nil, ErrEnvironmentArtifactBuilding
	case EnvironmentArtifactStatusFailed:
		message := "environment package artifact failed"
		if artifact.FailureReason != nil && strings.TrimSpace(*artifact.FailureReason) != "" {
			message = strings.TrimSpace(*artifact.FailureReason)
		}
		return nil, fmt.Errorf("%w: %s", ErrEnvironmentBuildFailed, message)
	default:
		return nil, fmt.Errorf("%w: environment artifact %s is %s", ErrEnvironmentBuildFailed, artifact.ID, artifact.Status)
	}
}

func (s *Service) prepareReadyEnvironmentArtifact(ctx context.Context, credential RequestCredential, teamID string, environment *Environment) (*preparedEnvironmentArtifact, error) {
	if environment == nil {
		return nil, ErrEnvironmentNotFound
	}
	compatibility := defaultEnvironmentArtifactCompatibility()
	digest, err := environmentArtifactDigest(environment.Config, compatibility)
	if err != nil {
		return nil, err
	}
	if artifact, err := s.repo.GetEnvironmentArtifactByDigest(ctx, teamID, environment.ID, digest); err == nil {
		switch strings.TrimSpace(artifact.Status) {
		case EnvironmentArtifactStatusReady:
			return &preparedEnvironmentArtifact{
				artifact: artifact,
				persist:  func(context.Context) error { return nil },
				cleanup:  func(context.Context) {},
			}, nil
		case EnvironmentArtifactStatusBuilding:
			return nil, ErrEnvironmentArtifactBuilding
		case EnvironmentArtifactStatusArchived:
			return nil, fmt.Errorf("environment artifact %s is archived", artifact.ID)
		}
		return s.prepareReadyEnvironmentArtifactBuild(ctx, credential, teamID, environment, digest, compatibility, artifact)
	} else if !errors.Is(err, ErrEnvironmentArtifactNotFound) {
		return nil, err
	}
	return s.prepareReadyEnvironmentArtifactBuild(ctx, credential, teamID, environment, digest, compatibility, nil)
}

func (s *Service) prepareReadyEnvironmentArtifactBuild(ctx context.Context, credential RequestCredential, teamID string, environment *Environment, digest string, compatibility map[string]any, existing *EnvironmentArtifact) (*preparedEnvironmentArtifact, error) {
	var (
		assets       EnvironmentArtifactAssets
		buildLog     string
		builtVolumes bool
	)
	if len(ConfiguredEnvironmentPackageManagers(environment.Config)) > 0 {
		builder, ok := s.runtime.(EnvironmentArtifactBuilder)
		if !ok || builder == nil {
			return nil, fmt.Errorf("%w: environment packages require a runtime builder", ErrEnvironmentBuildFailed)
		}
		result, err := builder.BuildEnvironmentArtifact(ctx, credential, teamID, cloneEnvironmentForBuild(environment))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrEnvironmentBuildFailed, err)
		}
		if result != nil {
			assets = result.Assets
			buildLog = result.BuildLog
		}
		builtVolumes = len(assets.VolumeIDs()) > 0
	} else {
		buildLog = "no environment packages requested; no package volumes created\n"
	}

	now := time.Now().UTC()
	artifactID := NewID("envart")
	createdAt := now
	if existing != nil {
		artifactID = existing.ID
		createdAt = existing.CreatedAt
		if createdAt.IsZero() {
			createdAt = now
		}
	}
	artifact := &EnvironmentArtifact{
		ID:             artifactID,
		TeamID:         teamID,
		EnvironmentID:  environment.ID,
		Digest:         digest,
		Status:         EnvironmentArtifactStatusReady,
		ConfigSnapshot: environmentConfigToMap(environment.Config),
		Compatibility:  compatibility,
		Assets:         assets,
		BuildLog:       buildLog,
		FailureReason:  nil,
		CreatedAt:      createdAt,
		UpdatedAt:      now,
	}
	cleanup := func(cleanupCtx context.Context) {
		if !builtVolumes {
			return
		}
		builder, ok := s.runtime.(EnvironmentArtifactBuilder)
		if !ok || builder == nil {
			return
		}
		if err := builder.CleanupEnvironmentArtifactAssets(cleanupCtx, credential, teamID, assets); err != nil {
			s.logger.Warn("cleanup prepared environment artifact assets failed", zap.Error(err), zap.String("team_id", teamID), zap.String("environment_id", environment.ID), zap.String("artifact_id", artifact.ID))
		}
	}
	persist := func(persistCtx context.Context) error {
		if existing != nil {
			return s.repo.UpdateEnvironmentArtifact(persistCtx, artifact)
		}
		if err := s.repo.CreateEnvironmentArtifact(persistCtx, artifact); err != nil {
			return err
		}
		return nil
	}
	return &preparedEnvironmentArtifact{artifact: artifact, persist: persist, cleanup: cleanup}, nil
}

func cloneEnvironmentForBuild(environment *Environment) *Environment {
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
