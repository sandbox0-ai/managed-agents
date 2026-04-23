package managedagents

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (s *Service) ensureTeamAssetStore(ctx context.Context, credential RequestCredential, teamID string) (*TeamAssetStore, error) {
	return s.teamAssetStore(ctx, credential, teamID, true)
}

func (s *Service) getTeamAssetStore(ctx context.Context, credential RequestCredential, teamID string) (*TeamAssetStore, error) {
	return s.teamAssetStore(ctx, credential, teamID, false)
}

func (s *Service) teamAssetStore(ctx context.Context, credential RequestCredential, teamID string, create bool) (*TeamAssetStore, error) {
	if s.assetStore == nil {
		return nil, errors.New("asset store is not configured")
	}
	regionID, err := s.repo.ResolveRuntimeRegionID(ctx, teamID)
	if err != nil {
		return nil, err
	}
	store, err := s.repo.GetTeamAssetStore(ctx, teamID, regionID)
	if err == nil {
		return store, nil
	}
	if !errors.Is(err, ErrTeamAssetStoreNotFound) || !create {
		return nil, err
	}
	created, err := s.assetStore.CreateStore(ctx, credential, AssetStoreCreateStoreRequest{
		TeamID:   teamID,
		RegionID: regionID,
	})
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	candidate := newTeamAssetStore(teamID, regionID, created.VolumeID, now)
	inserted, err := s.repo.CreateTeamAssetStoreIfAbsent(ctx, candidate)
	if err != nil {
		_ = s.assetStore.DeleteStore(ctx, credential, AssetStoreDeleteStoreRequest{
			TeamID:   teamID,
			RegionID: regionID,
			VolumeID: created.VolumeID,
		})
		return nil, err
	}
	if inserted {
		return candidate, nil
	}
	if err := s.assetStore.DeleteStore(ctx, credential, AssetStoreDeleteStoreRequest{
		TeamID:   teamID,
		RegionID: regionID,
		VolumeID: created.VolumeID,
	}); err != nil {
		return nil, fmt.Errorf("delete redundant team asset store volume: %w", err)
	}
	return s.repo.GetTeamAssetStore(ctx, teamID, regionID)
}
