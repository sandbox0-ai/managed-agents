package managedagents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

const defaultAssetBackfillBatchSize = 100
const maxAssetBackfillFailureSamples = 100

type AssetBackfillOptions struct {
	DryRun    bool
	Files     bool
	Skills    bool
	TeamID    string
	BatchSize int
	MaxItems  int
}

type AssetBackfillCategorySummary struct {
	Remaining int `json:"remaining"`
	Scanned   int `json:"scanned"`
	Migrated  int `json:"migrated"`
	Failed    int `json:"failed"`
}

type AssetBackfillFailure struct {
	Kind    string `json:"kind"`
	TeamID  string `json:"team_id"`
	ID      string `json:"id"`
	Details string `json:"details"`
}

type AssetBackfillSummary struct {
	DryRun            bool                         `json:"dry_run"`
	Files             AssetBackfillCategorySummary `json:"files"`
	Skills            AssetBackfillCategorySummary `json:"skills"`
	TeamStoresSeen    int                          `json:"team_stores_seen"`
	TeamStoresCreated int                          `json:"team_stores_created"`
	Failures          []AssetBackfillFailure       `json:"failures,omitempty"`
}

func (s *Service) BackfillTeamAssetStore(ctx context.Context, credential RequestCredential, opts AssetBackfillOptions) (AssetBackfillSummary, error) {
	if s.assetStore == nil {
		return AssetBackfillSummary{}, errors.New("asset store is not configured")
	}
	if !opts.Files && !opts.Skills {
		opts.Files = true
		opts.Skills = true
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = defaultAssetBackfillBatchSize
	}
	summary := AssetBackfillSummary{DryRun: opts.DryRun}
	storeCache := make(map[string]*TeamAssetStore)
	storeSeen := make(map[string]struct{})
	processed := 0

	if opts.Files {
		if err := s.backfillFilesToTeamAssetStore(ctx, credential, opts, &summary, storeCache, storeSeen, &processed); err != nil {
			return summary, err
		}
	}
	if opts.Skills {
		if err := s.backfillSkillsToTeamAssetStore(ctx, credential, opts, &summary, storeCache, storeSeen, &processed); err != nil {
			return summary, err
		}
	}
	summary.TeamStoresSeen = len(storeSeen)
	var err error
	if summary.Files.Remaining, err = s.repo.CountFilesMissingStorePath(ctx, strings.TrimSpace(opts.TeamID)); err != nil {
		return summary, err
	}
	if summary.Skills.Remaining, err = s.repo.CountSkillVersionsMissingBundle(ctx, strings.TrimSpace(opts.TeamID)); err != nil {
		return summary, err
	}
	return summary, nil
}

func (s *Service) backfillFilesToTeamAssetStore(ctx context.Context, credential RequestCredential, opts AssetBackfillOptions, summary *AssetBackfillSummary, storeCache map[string]*TeamAssetStore, storeSeen map[string]struct{}, processed *int) error {
	var cursor *assetBackfillFileCursor
	for {
		if opts.MaxItems > 0 && *processed >= opts.MaxItems {
			return nil
		}
		limit := opts.BatchSize
		if opts.MaxItems > 0 && *processed+limit > opts.MaxItems {
			limit = opts.MaxItems - *processed
		}
		items, err := s.repo.ListFilesMissingStorePath(ctx, opts.TeamID, limit, cursor)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		for _, record := range items {
			if opts.MaxItems > 0 && *processed >= opts.MaxItems {
				return nil
			}
			*processed++
			summary.Files.Scanned++
			storeSeen[record.TeamID] = struct{}{}
			if err := s.backfillFileRecord(ctx, credential, opts.DryRun, record, summary, storeCache); err != nil {
				s.recordAssetBackfillFailure(summary, "file", record.TeamID, record.ID, err)
				continue
			}
			summary.Files.Migrated++
		}
		last := items[len(items)-1]
		cursor = &assetBackfillFileCursor{CreatedAt: last.CreatedAt, ID: last.ID}
		if len(items) < limit {
			return nil
		}
	}
}

func (s *Service) backfillFileRecord(ctx context.Context, credential RequestCredential, dryRun bool, record *managedFileRecord, summary *AssetBackfillSummary, storeCache map[string]*TeamAssetStore) error {
	if record == nil {
		return ErrFileNotFound
	}
	content, err := s.readFileContent(ctx, credential, record)
	if err != nil {
		return err
	}
	if dryRun {
		s.logger.Info("asset backfill would migrate file",
			zap.String("team_id", record.TeamID),
			zap.String("file_id", record.ID),
			zap.String("filename", record.Filename),
			zap.Int64("size_bytes", int64(len(content))),
		)
		return nil
	}
	store, created, err := s.backfillTeamStore(ctx, credential, record.TeamID, storeCache)
	if err != nil {
		return err
	}
	if created {
		summary.TeamStoresCreated++
	}
	storePath := teamFileAssetStorePath(record.ID)
	stored, err := s.assetStore.PutObject(ctx, credential, AssetStorePutObjectRequest{
		TeamID:   record.TeamID,
		RegionID: store.RegionID,
		VolumeID: store.VolumeID,
		Path:     storePath,
		Content:  bytes.NewReader(content),
	})
	if err != nil {
		return err
	}
	updated, err := s.repo.BackfillFileStorePath(ctx, record.TeamID, record.ID, stored.Path, stored.SHA256, stored.SizeBytes)
	if err != nil {
		_ = s.assetStore.DeleteObject(ctx, credential, AssetStoreDeleteObjectRequest{
			TeamID:   record.TeamID,
			RegionID: store.RegionID,
			VolumeID: store.VolumeID,
			Path:     stored.Path,
		})
		return err
	}
	if !updated {
		_ = s.assetStore.DeleteObject(ctx, credential, AssetStoreDeleteObjectRequest{
			TeamID:   record.TeamID,
			RegionID: store.RegionID,
			VolumeID: store.VolumeID,
			Path:     stored.Path,
		})
		return nil
	}
	s.logger.Info("asset backfill migrated file",
		zap.String("team_id", record.TeamID),
		zap.String("file_id", record.ID),
		zap.String("store_path", stored.Path),
		zap.Int64("size_bytes", stored.SizeBytes),
	)
	return nil
}

func (s *Service) backfillSkillsToTeamAssetStore(ctx context.Context, credential RequestCredential, opts AssetBackfillOptions, summary *AssetBackfillSummary, storeCache map[string]*TeamAssetStore, storeSeen map[string]struct{}, processed *int) error {
	var cursor *assetBackfillSkillCursor
	for {
		if opts.MaxItems > 0 && *processed >= opts.MaxItems {
			return nil
		}
		limit := opts.BatchSize
		if opts.MaxItems > 0 && *processed+limit > opts.MaxItems {
			limit = opts.MaxItems - *processed
		}
		items, err := s.repo.ListSkillVersionsMissingBundle(ctx, opts.TeamID, limit, cursor)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		for _, item := range items {
			if opts.MaxItems > 0 && *processed >= opts.MaxItems {
				return nil
			}
			*processed++
			summary.Skills.Scanned++
			storeSeen[item.TeamID] = struct{}{}
			if err := s.backfillSkillVersionRecord(ctx, credential, opts.DryRun, item, summary, storeCache); err != nil {
				s.recordAssetBackfillFailure(summary, "skill_version", item.TeamID, item.Stored.Snapshot.ID, err)
				continue
			}
			summary.Skills.Migrated++
		}
		last := items[len(items)-1]
		cursor = &assetBackfillSkillCursor{CreatedAt: last.CreatedAt, ID: last.ID}
		if len(items) < limit {
			return nil
		}
	}
}

func (s *Service) backfillSkillVersionRecord(ctx context.Context, credential RequestCredential, dryRun bool, item assetBackfillSkillVersionRecord, summary *AssetBackfillSummary, storeCache map[string]*TeamAssetStore) error {
	if len(item.Stored.Files) == 0 {
		return fmt.Errorf("skill version %s has no legacy files to bundle", item.Stored.Snapshot.ID)
	}
	files := make([]uploadedSkillFile, 0, len(item.Stored.Files))
	for _, file := range item.Stored.Files {
		files = append(files, uploadedSkillFile{
			Path:    file.Path,
			Content: append([]byte(nil), file.Content...),
		})
	}
	parsed := &parsedSkillUpload{Files: files}
	bundleContent, bundle, err := buildSkillBundle(parsed)
	if err != nil {
		return err
	}
	bundle.Path = teamSkillBundleAssetStorePath(item.Stored.Snapshot.SkillID, item.Stored.Snapshot.Version)
	if dryRun {
		s.logger.Info("asset backfill would migrate skill version",
			zap.String("team_id", item.TeamID),
			zap.String("skill_id", item.Stored.Snapshot.SkillID),
			zap.String("skill_version_id", item.Stored.Snapshot.ID),
			zap.String("version", item.Stored.Snapshot.Version),
			zap.Int("file_count", len(item.Stored.Files)),
			zap.Int64("bundle_size_bytes", bundle.SizeBytes),
		)
		return nil
	}
	store, created, err := s.backfillTeamStore(ctx, credential, item.TeamID, storeCache)
	if err != nil {
		return err
	}
	if created {
		summary.TeamStoresCreated++
	}
	stored, err := s.assetStore.PutObject(ctx, credential, AssetStorePutObjectRequest{
		TeamID:   item.TeamID,
		RegionID: store.RegionID,
		VolumeID: store.VolumeID,
		Path:     bundle.Path,
		Content:  bytes.NewReader(bundleContent),
	})
	if err != nil {
		return err
	}
	updated, err := s.repo.BackfillSkillBundle(ctx, item.TeamID, item.Stored.Snapshot.SkillID, item.Stored.Snapshot.Version, stored.Path, stored.SHA256, stored.SizeBytes)
	if err != nil {
		_ = s.assetStore.DeleteObject(ctx, credential, AssetStoreDeleteObjectRequest{
			TeamID:   item.TeamID,
			RegionID: store.RegionID,
			VolumeID: store.VolumeID,
			Path:     stored.Path,
		})
		return err
	}
	if !updated {
		_ = s.assetStore.DeleteObject(ctx, credential, AssetStoreDeleteObjectRequest{
			TeamID:   item.TeamID,
			RegionID: store.RegionID,
			VolumeID: store.VolumeID,
			Path:     stored.Path,
		})
		return nil
	}
	s.logger.Info("asset backfill migrated skill version",
		zap.String("team_id", item.TeamID),
		zap.String("skill_id", item.Stored.Snapshot.SkillID),
		zap.String("skill_version_id", item.Stored.Snapshot.ID),
		zap.String("version", item.Stored.Snapshot.Version),
		zap.String("bundle_path", stored.Path),
		zap.Int64("bundle_size_bytes", stored.SizeBytes),
	)
	return nil
}

func (s *Service) backfillTeamStore(ctx context.Context, credential RequestCredential, teamID string, storeCache map[string]*TeamAssetStore) (*TeamAssetStore, bool, error) {
	trimmedTeamID := strings.TrimSpace(teamID)
	if trimmedTeamID == "" {
		return nil, false, errors.New("team id is required")
	}
	if store, ok := storeCache[trimmedTeamID]; ok && store != nil {
		return store, false, nil
	}
	if store, err := s.getTeamAssetStore(ctx, credential, trimmedTeamID); err == nil {
		storeCache[trimmedTeamID] = store
		return store, false, nil
	} else if !errors.Is(err, ErrTeamAssetStoreNotFound) {
		return nil, false, err
	}
	store, err := s.ensureTeamAssetStore(ctx, credential, trimmedTeamID)
	if err != nil {
		return nil, false, err
	}
	storeCache[trimmedTeamID] = store
	return store, true, nil
}

func (s *Service) recordAssetBackfillFailure(summary *AssetBackfillSummary, kind, teamID, id string, err error) {
	if summary == nil {
		return
	}
	switch kind {
	case "file":
		summary.Files.Failed++
	case "skill_version":
		summary.Skills.Failed++
	}
	if len(summary.Failures) < maxAssetBackfillFailureSamples {
		summary.Failures = append(summary.Failures, AssetBackfillFailure{
			Kind:    kind,
			TeamID:  strings.TrimSpace(teamID),
			ID:      strings.TrimSpace(id),
			Details: err.Error(),
		})
	}
	s.logger.Warn("asset backfill item failed",
		zap.String("kind", kind),
		zap.String("team_id", teamID),
		zap.String("id", id),
		zap.Error(err),
	)
}
