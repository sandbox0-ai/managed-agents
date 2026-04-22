package managedagents

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (r *Repository) GetTeamAssetStore(ctx context.Context, teamID, regionID string) (*TeamAssetStore, error) {
	var store TeamAssetStore
	err := r.db(ctx).QueryRow(ctx, `
		SELECT team_id, region_id, volume_id, created_at, updated_at
		FROM managed_agent_team_asset_stores
		WHERE team_id = $1 AND region_id = $2
	`, strings.TrimSpace(teamID), strings.TrimSpace(regionID)).Scan(
		&store.TeamID,
		&store.RegionID,
		&store.VolumeID,
		&store.CreatedAt,
		&store.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrTeamAssetStoreNotFound
		}
		return nil, fmt.Errorf("query managed-agent team asset store: %w", err)
	}
	return &store, nil
}

func (r *Repository) CreateTeamAssetStoreIfAbsent(ctx context.Context, store *TeamAssetStore) (bool, error) {
	if store == nil {
		return false, fmt.Errorf("managed-agent team asset store is required")
	}
	result, err := r.db(ctx).Exec(ctx, `
		INSERT INTO managed_agent_team_asset_stores (team_id, region_id, volume_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (team_id, region_id) DO NOTHING
	`, strings.TrimSpace(store.TeamID), strings.TrimSpace(store.RegionID), strings.TrimSpace(store.VolumeID), store.CreatedAt.UTC(), store.UpdatedAt.UTC())
	if err != nil {
		return false, fmt.Errorf("insert managed-agent team asset store: %w", err)
	}
	return result.RowsAffected() > 0, nil
}

func newTeamAssetStore(teamID, regionID, volumeID string, now time.Time) *TeamAssetStore {
	return &TeamAssetStore{
		TeamID:    strings.TrimSpace(teamID),
		RegionID:  strings.TrimSpace(regionID),
		VolumeID:  strings.TrimSpace(volumeID),
		CreatedAt: now.UTC(),
		UpdatedAt: now.UTC(),
	}
}
