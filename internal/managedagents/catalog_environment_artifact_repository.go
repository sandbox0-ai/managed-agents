package managedagents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (r *Repository) EnvironmentNameExists(ctx context.Context, teamID, name, excludeID string) (bool, error) {
	var exists bool
	err := r.db(ctx).QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM managed_agent_environments
			WHERE team_id = $1
				AND LOWER(BTRIM(snapshot->>'name')) = LOWER(BTRIM($2))
				AND ($3 = '' OR id <> $3)
		)
	`, strings.TrimSpace(teamID), strings.TrimSpace(name), strings.TrimSpace(excludeID)).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("query managed-agent environment name conflict: %w", err)
	}
	return exists, nil
}

func (r *Repository) CountActiveSessionsForEnvironment(ctx context.Context, teamID, environmentID string) (int, error) {
	var count int
	err := r.db(ctx).QueryRow(ctx, `
		SELECT COUNT(1)
		FROM managed_agent_sessions
		WHERE team_id = $1
			AND environment_id = $2
			AND deleted_at IS NULL
	`, strings.TrimSpace(teamID), strings.TrimSpace(environmentID)).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count managed-agent environment sessions: %w", err)
	}
	return count, nil
}

func (r *Repository) CountActiveSessionsForEnvironmentArtifact(ctx context.Context, teamID, artifactID string) (int, error) {
	var count int
	err := r.db(ctx).QueryRow(ctx, `
		SELECT COUNT(1)
		FROM managed_agent_sessions
		WHERE team_id = $1
			AND environment_artifact_id = $2
			AND deleted_at IS NULL
	`, strings.TrimSpace(teamID), strings.TrimSpace(artifactID)).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count managed-agent environment artifact sessions: %w", err)
	}
	return count, nil
}

func (r *Repository) CreateEnvironmentArtifact(ctx context.Context, artifact *EnvironmentArtifact) error {
	configJSON, err := json.Marshal(artifact.ConfigSnapshot)
	if err != nil {
		return fmt.Errorf("marshal environment artifact config snapshot: %w", err)
	}
	compatibilityJSON, err := json.Marshal(artifact.Compatibility)
	if err != nil {
		return fmt.Errorf("marshal environment artifact compatibility: %w", err)
	}
	assetsJSON, err := json.Marshal(artifact.Assets)
	if err != nil {
		return fmt.Errorf("marshal environment artifact assets: %w", err)
	}
	_, err = r.db(ctx).Exec(ctx, `
		INSERT INTO managed_agent_environment_artifacts (
			id, team_id, environment_id, digest, status, config_snapshot, compatibility, assets,
			build_log, failure_reason, archived_at, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8::jsonb, $9, $10, $11, $12, $13)
	`, artifact.ID, artifact.TeamID, artifact.EnvironmentID, artifact.Digest, artifact.Status, string(configJSON), string(compatibilityJSON), string(assetsJSON),
		artifact.BuildLog, nullableStringPointer(artifact.FailureReason), artifact.ArchivedAt, artifact.CreatedAt, artifact.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert managed-agent environment artifact: %w", err)
	}
	return nil
}

func (r *Repository) UpdateEnvironmentArtifact(ctx context.Context, artifact *EnvironmentArtifact) error {
	configJSON, err := json.Marshal(artifact.ConfigSnapshot)
	if err != nil {
		return fmt.Errorf("marshal environment artifact config snapshot: %w", err)
	}
	compatibilityJSON, err := json.Marshal(artifact.Compatibility)
	if err != nil {
		return fmt.Errorf("marshal environment artifact compatibility: %w", err)
	}
	assetsJSON, err := json.Marshal(artifact.Assets)
	if err != nil {
		return fmt.Errorf("marshal environment artifact assets: %w", err)
	}
	result, err := r.db(ctx).Exec(ctx, `
		UPDATE managed_agent_environment_artifacts
		SET status = $3,
			config_snapshot = $4::jsonb,
			compatibility = $5::jsonb,
			assets = $6::jsonb,
			build_log = $7,
			failure_reason = $8,
			archived_at = $9,
			updated_at = $10
		WHERE team_id = $1 AND id = $2
	`, artifact.TeamID, artifact.ID, artifact.Status, string(configJSON), string(compatibilityJSON), string(assetsJSON),
		artifact.BuildLog, nullableStringPointer(artifact.FailureReason), artifact.ArchivedAt, artifact.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update managed-agent environment artifact: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrEnvironmentArtifactNotFound
	}
	return nil
}

func (r *Repository) BeginEnvironmentArtifactBuild(ctx context.Context, teamID, artifactID string, now time.Time) (bool, error) {
	result, err := r.db(ctx).Exec(ctx, `
		UPDATE managed_agent_environment_artifacts
		SET status = $3,
			build_log = '',
			failure_reason = NULL,
			updated_at = $4
		WHERE team_id = $1
			AND id = $2
			AND status IN ('pending', 'failed')
	`, strings.TrimSpace(teamID), strings.TrimSpace(artifactID), EnvironmentArtifactStatusBuilding, now.UTC())
	if err != nil {
		return false, fmt.Errorf("mark managed-agent environment artifact building: %w", err)
	}
	return result.RowsAffected() > 0, nil
}

func (r *Repository) GetEnvironmentArtifact(ctx context.Context, teamID, artifactID string) (*EnvironmentArtifact, error) {
	return r.getEnvironmentArtifact(ctx, `
		SELECT id, team_id, environment_id, digest, status, config_snapshot, compatibility, assets,
			build_log, failure_reason, archived_at, created_at, updated_at
		FROM managed_agent_environment_artifacts
		WHERE team_id = $1 AND id = $2
	`, strings.TrimSpace(teamID), strings.TrimSpace(artifactID))
}

func (r *Repository) GetEnvironmentArtifactByDigest(ctx context.Context, teamID, environmentID, digest string) (*EnvironmentArtifact, error) {
	return r.getEnvironmentArtifact(ctx, `
		SELECT id, team_id, environment_id, digest, status, config_snapshot, compatibility, assets,
			build_log, failure_reason, archived_at, created_at, updated_at
		FROM managed_agent_environment_artifacts
		WHERE team_id = $1 AND environment_id = $2 AND digest = $3
	`, strings.TrimSpace(teamID), strings.TrimSpace(environmentID), strings.TrimSpace(digest))
}

func (r *Repository) GetLatestEnvironmentArtifact(ctx context.Context, teamID, environmentID string) (*EnvironmentArtifact, error) {
	return r.getEnvironmentArtifact(ctx, `
		SELECT id, team_id, environment_id, digest, status, config_snapshot, compatibility, assets,
			build_log, failure_reason, archived_at, created_at, updated_at
		FROM managed_agent_environment_artifacts
		WHERE team_id = $1 AND environment_id = $2 AND archived_at IS NULL
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, strings.TrimSpace(teamID), strings.TrimSpace(environmentID))
}

func (r *Repository) ListGCableEnvironmentArtifacts(ctx context.Context, teamID, environmentID, keepArtifactID string) ([]*EnvironmentArtifact, error) {
	rows, err := r.db(ctx).Query(ctx, `
		SELECT id, team_id, environment_id, digest, status, config_snapshot, compatibility, assets,
			build_log, failure_reason, archived_at, created_at, updated_at
		FROM managed_agent_environment_artifacts artifact
		WHERE artifact.team_id = $1
			AND artifact.environment_id = $2
			AND artifact.id <> $3
			AND artifact.status <> 'building'
			AND NOT EXISTS (
				SELECT 1
				FROM managed_agent_sessions session
				WHERE session.team_id = artifact.team_id
					AND session.environment_artifact_id = artifact.id
					AND session.deleted_at IS NULL
			)
		ORDER BY artifact.created_at ASC, artifact.id ASC
	`, strings.TrimSpace(teamID), strings.TrimSpace(environmentID), strings.TrimSpace(keepArtifactID))
	if err != nil {
		return nil, fmt.Errorf("list managed-agent garbage-collectable environment artifacts: %w", err)
	}
	defer rows.Close()

	out := make([]*EnvironmentArtifact, 0)
	for rows.Next() {
		artifact, err := scanEnvironmentArtifact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed-agent environment artifacts: %w", err)
	}
	return out, nil
}

func (r *Repository) getEnvironmentArtifact(ctx context.Context, query string, args ...any) (*EnvironmentArtifact, error) {
	row := r.db(ctx).QueryRow(ctx, query, args...)
	artifact, err := scanEnvironmentArtifact(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrEnvironmentArtifactNotFound
		}
		return nil, err
	}
	return artifact, nil
}

type environmentArtifactScanner interface {
	Scan(dest ...any) error
}

func scanEnvironmentArtifact(scanner environmentArtifactScanner) (*EnvironmentArtifact, error) {
	var (
		artifact          EnvironmentArtifact
		configJSON        []byte
		compatibilityJSON []byte
		assetsJSON        []byte
		failureReason     *string
	)
	err := scanner.Scan(
		&artifact.ID,
		&artifact.TeamID,
		&artifact.EnvironmentID,
		&artifact.Digest,
		&artifact.Status,
		&configJSON,
		&compatibilityJSON,
		&assetsJSON,
		&artifact.BuildLog,
		&failureReason,
		&artifact.ArchivedAt,
		&artifact.CreatedAt,
		&artifact.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(configJSON, &artifact.ConfigSnapshot); err != nil {
		return nil, fmt.Errorf("decode environment artifact config snapshot: %w", err)
	}
	if err := json.Unmarshal(compatibilityJSON, &artifact.Compatibility); err != nil {
		return nil, fmt.Errorf("decode environment artifact compatibility: %w", err)
	}
	if err := json.Unmarshal(assetsJSON, &artifact.Assets); err != nil {
		return nil, fmt.Errorf("decode environment artifact assets: %w", err)
	}
	artifact.FailureReason = failureReason
	return &artifact, nil
}
