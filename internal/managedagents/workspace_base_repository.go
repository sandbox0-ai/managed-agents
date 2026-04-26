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

const (
	WorkspaceBaseStatusBuilding = "building"
	WorkspaceBaseStatusReady    = "ready"
	WorkspaceBaseStatusFailed   = "failed"
)

func (r *Repository) GetWorkspaceBase(ctx context.Context, teamID, digest string) (*WorkspaceBaseRecord, error) {
	base, err := scanWorkspaceBase(r.db(ctx).QueryRow(ctx, `
		SELECT id, team_id, digest, status, volume_id, COALESCE(input_snapshot, '{}'::jsonb), failure_reason, created_at, updated_at
		FROM managed_agent_workspace_bases
		WHERE team_id = $1 AND digest = $2
	`, strings.TrimSpace(teamID), strings.TrimSpace(digest)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrWorkspaceBaseNotFound
		}
		return nil, fmt.Errorf("query managed-agent workspace base: %w", err)
	}
	return base, nil
}

func (r *Repository) UpsertWorkspaceBase(ctx context.Context, base *WorkspaceBaseRecord) error {
	if base == nil {
		return errors.New("workspace base record is required")
	}
	inputSnapshotJSON, err := json.Marshal(base.InputSnapshot)
	if err != nil {
		return fmt.Errorf("marshal workspace base input snapshot: %w", err)
	}
	_, err = r.db(ctx).Exec(ctx, `
		INSERT INTO managed_agent_workspace_bases (
			id, team_id, digest, status, volume_id, input_snapshot, failure_reason, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9)
		ON CONFLICT (team_id, digest) DO UPDATE SET
			status = EXCLUDED.status,
			volume_id = EXCLUDED.volume_id,
			input_snapshot = EXCLUDED.input_snapshot,
			failure_reason = EXCLUDED.failure_reason,
			updated_at = EXCLUDED.updated_at
	`, strings.TrimSpace(base.ID), strings.TrimSpace(base.TeamID), strings.TrimSpace(base.Digest), strings.TrimSpace(base.Status),
		strings.TrimSpace(base.VolumeID), string(inputSnapshotJSON), strings.TrimSpace(base.FailureReason), base.CreatedAt.UTC(), base.UpdatedAt.UTC())
	if err != nil {
		return fmt.Errorf("upsert managed-agent workspace base: %w", err)
	}
	return nil
}

type workspaceBaseScanner interface {
	Scan(dest ...any) error
}

func scanWorkspaceBase(scanner workspaceBaseScanner) (*WorkspaceBaseRecord, error) {
	var (
		base              WorkspaceBaseRecord
		inputSnapshotJSON []byte
	)
	if err := scanner.Scan(
		&base.ID,
		&base.TeamID,
		&base.Digest,
		&base.Status,
		&base.VolumeID,
		&inputSnapshotJSON,
		&base.FailureReason,
		&base.CreatedAt,
		&base.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if len(inputSnapshotJSON) > 0 {
		if err := json.Unmarshal(inputSnapshotJSON, &base.InputSnapshot); err != nil {
			return nil, fmt.Errorf("decode workspace base input snapshot: %w", err)
		}
	}
	if base.InputSnapshot == nil {
		base.InputSnapshot = map[string]any{}
	}
	return &base, nil
}

func NewWorkspaceBaseRecord(teamID, digest, status, volumeID string, inputSnapshot map[string]any, now time.Time) *WorkspaceBaseRecord {
	return &WorkspaceBaseRecord{
		ID:            NewID("wbase"),
		TeamID:        strings.TrimSpace(teamID),
		Digest:        strings.TrimSpace(digest),
		Status:        strings.TrimSpace(status),
		VolumeID:      strings.TrimSpace(volumeID),
		InputSnapshot: inputSnapshot,
		CreatedAt:     now.UTC(),
		UpdatedAt:     now.UTC(),
	}
}
