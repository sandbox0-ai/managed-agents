package managedagents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (r *Repository) GetSkillArtifactStoreVolume(ctx context.Context, teamID string) (string, error) {
	var volumeID string
	err := r.db(ctx).QueryRow(ctx, `
		SELECT volume_id
		FROM managed_agent_skill_artifact_stores
		WHERE team_id = $1
	`, strings.TrimSpace(teamID)).Scan(&volumeID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", ErrSkillArtifactStoreNotFound
		}
		return "", fmt.Errorf("query managed-agent skill artifact store: %w", err)
	}
	return strings.TrimSpace(volumeID), nil
}

func (r *Repository) CreateSkillArtifactStoreVolume(ctx context.Context, teamID, volumeID string, now time.Time) error {
	_, err := r.db(ctx).Exec(ctx, `
		INSERT INTO managed_agent_skill_artifact_stores (team_id, volume_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (team_id) DO NOTHING
	`, strings.TrimSpace(teamID), strings.TrimSpace(volumeID), now.UTC(), now.UTC())
	if err != nil {
		return fmt.Errorf("insert managed-agent skill artifact store: %w", err)
	}
	return nil
}

func (r *Repository) UpsertSkillArtifactStoreVolume(ctx context.Context, teamID, volumeID string, now time.Time) error {
	_, err := r.db(ctx).Exec(ctx, `
		INSERT INTO managed_agent_skill_artifact_stores (team_id, volume_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (team_id) DO UPDATE
		SET volume_id = EXCLUDED.volume_id,
		    updated_at = EXCLUDED.updated_at
	`, strings.TrimSpace(teamID), strings.TrimSpace(volumeID), now.UTC(), now.UTC())
	if err != nil {
		return fmt.Errorf("upsert managed-agent skill artifact store: %w", err)
	}
	return nil
}

func (r *Repository) GetSkillSetBundle(ctx context.Context, teamID, cacheKey string) (*skillBundle, error) {
	var (
		bundle       skillBundle
		snapshotJSON []byte
	)
	err := r.db(ctx).QueryRow(ctx, `
		SELECT id, team_id, cache_key, volume_id, snapshot, created_at, updated_at
		FROM managed_agent_skill_set_bundles
		WHERE team_id = $1 AND cache_key = $2
	`, strings.TrimSpace(teamID), strings.TrimSpace(cacheKey)).Scan(
		&bundle.ID,
		&bundle.TeamID,
		&bundle.CacheKey,
		&bundle.VolumeID,
		&snapshotJSON,
		&bundle.CreatedAt,
		&bundle.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrSkillSetBundleNotFound
		}
		return nil, fmt.Errorf("query managed-agent skill set bundle: %w", err)
	}
	if err := json.Unmarshal(snapshotJSON, &bundle.Snapshot); err != nil {
		return nil, fmt.Errorf("decode managed-agent skill set bundle snapshot: %w", err)
	}
	return &bundle, nil
}

func (r *Repository) CreateSkillSetBundle(ctx context.Context, bundle *skillBundle) error {
	snapshotJSON, err := json.Marshal(bundle.Snapshot)
	if err != nil {
		return fmt.Errorf("marshal managed-agent skill set bundle snapshot: %w", err)
	}
	_, err = r.db(ctx).Exec(ctx, `
		INSERT INTO managed_agent_skill_set_bundles (id, team_id, cache_key, volume_id, snapshot, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7)
	`, bundle.ID, bundle.TeamID, bundle.CacheKey, bundle.VolumeID, string(snapshotJSON), bundle.CreatedAt.UTC(), bundle.UpdatedAt.UTC())
	if err != nil {
		return fmt.Errorf("insert managed-agent skill set bundle: %w", err)
	}
	return nil
}

func (r *Repository) DeleteSkillSetBundle(ctx context.Context, teamID, cacheKey string) error {
	_, err := r.db(ctx).Exec(ctx, `
		DELETE FROM managed_agent_skill_set_bundles
		WHERE team_id = $1 AND cache_key = $2
	`, strings.TrimSpace(teamID), strings.TrimSpace(cacheKey))
	if err != nil {
		return fmt.Errorf("delete managed-agent skill set bundle: %w", err)
	}
	return nil
}
