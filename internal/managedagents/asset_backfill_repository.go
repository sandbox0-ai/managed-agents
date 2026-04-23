package managedagents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type assetBackfillFileCursor struct {
	CreatedAt time.Time
	ID        string
}

type assetBackfillSkillCursor struct {
	CreatedAt time.Time
	ID        string
}

type assetBackfillSkillVersionRecord struct {
	TeamID    string
	CreatedAt time.Time
	ID        string
	Stored    StoredSkillVersion
}

func (r *Repository) ListFilesMissingStorePath(ctx context.Context, teamID string, limit int, cursor *assetBackfillFileCursor) ([]*managedFileRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	args := []any{}
	query := `
		SELECT id, team_id, filename, mime_type, size_bytes, COALESCE(scope_type, ''), COALESCE(scope_id, ''),
			COALESCE(store_path, ''),
			COALESCE(file_store_volume_id, ''), COALESCE(file_store_path, ''), COALESCE(sha256, ''),
			COALESCE(content, ''::bytea), created_at, updated_at
		FROM managed_agent_files
		WHERE COALESCE(store_path, '') = ''
	`
	if strings.TrimSpace(teamID) != "" {
		args = append(args, strings.TrimSpace(teamID))
		query += fmt.Sprintf(" AND team_id = $%d", len(args))
	}
	if cursor != nil && !cursor.CreatedAt.IsZero() && strings.TrimSpace(cursor.ID) != "" {
		args = append(args, cursor.CreatedAt.UTC(), strings.TrimSpace(cursor.ID))
		query += fmt.Sprintf(" AND (created_at > $%d OR (created_at = $%d AND id > $%d))", len(args)-1, len(args)-1, len(args))
	}
	args = append(args, limit)
	query += fmt.Sprintf(" ORDER BY created_at ASC, id ASC LIMIT $%d", len(args))

	rows, err := r.db(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list managed-agent files missing store path: %w", err)
	}
	defer rows.Close()

	out := make([]*managedFileRecord, 0, limit)
	for rows.Next() {
		var record managedFileRecord
		if err := rows.Scan(
			&record.ID, &record.TeamID, &record.Filename, &record.MimeType, &record.SizeBytes, &record.ScopeType, &record.ScopeID,
			&record.StorePath,
			&record.FileStoreVolumeID, &record.FileStorePath, &record.SHA256, &record.Content, &record.CreatedAt, &record.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan managed-agent file backfill candidate: %w", err)
		}
		out = append(out, &record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed-agent file backfill candidates: %w", err)
	}
	return out, nil
}

func (r *Repository) BackfillFileStorePath(ctx context.Context, teamID, fileID, storePath, sha256 string, sizeBytes int64) (bool, error) {
	result, err := r.db(ctx).Exec(ctx, `
		UPDATE managed_agent_files
		SET store_path = $3,
			sha256 = CASE WHEN COALESCE(sha256, '') = '' THEN $4 ELSE sha256 END,
			size_bytes = $5,
			updated_at = NOW()
		WHERE team_id = $1
			AND id = $2
			AND COALESCE(store_path, '') = ''
	`, strings.TrimSpace(teamID), strings.TrimSpace(fileID), strings.TrimSpace(storePath), strings.TrimSpace(sha256), sizeBytes)
	if err != nil {
		return false, fmt.Errorf("backfill managed-agent file store path: %w", err)
	}
	return result.RowsAffected() > 0, nil
}

func (r *Repository) ListSkillVersionsMissingBundle(ctx context.Context, teamID string, limit int, cursor *assetBackfillSkillCursor) ([]assetBackfillSkillVersionRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	args := []any{}
	query := `
		SELECT s.team_id, v.id, v.created_at, v.snapshot, v.files
		FROM managed_agent_skill_versions v
		JOIN managed_agent_skills s ON s.id = v.skill_id
		WHERE COALESCE(v.bundle_path, '') = ''
	`
	if strings.TrimSpace(teamID) != "" {
		args = append(args, strings.TrimSpace(teamID))
		query += fmt.Sprintf(" AND s.team_id = $%d", len(args))
	}
	if cursor != nil && !cursor.CreatedAt.IsZero() && strings.TrimSpace(cursor.ID) != "" {
		args = append(args, cursor.CreatedAt.UTC(), strings.TrimSpace(cursor.ID))
		query += fmt.Sprintf(" AND (v.created_at > $%d OR (v.created_at = $%d AND v.id > $%d))", len(args)-1, len(args)-1, len(args))
	}
	args = append(args, limit)
	query += fmt.Sprintf(" ORDER BY v.created_at ASC, v.id ASC LIMIT $%d", len(args))

	rows, err := r.db(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list managed-agent skill versions missing bundle: %w", err)
	}
	defer rows.Close()

	out := make([]assetBackfillSkillVersionRecord, 0, limit)
	for rows.Next() {
		var (
			record       assetBackfillSkillVersionRecord
			snapshotJSON []byte
			filesJSON    []byte
		)
		if err := rows.Scan(&record.TeamID, &record.ID, &record.CreatedAt, &snapshotJSON, &filesJSON); err != nil {
			return nil, fmt.Errorf("scan managed-agent skill version backfill candidate: %w", err)
		}
		snapshot, err := decodeSkillVersionSnapshot(snapshotJSON)
		if err != nil {
			return nil, err
		}
		files := []storedSkillFile{}
		if len(filesJSON) != 0 {
			if err := json.Unmarshal(filesJSON, &files); err != nil {
				return nil, fmt.Errorf("decode managed-agent skill version files for backfill: %w", err)
			}
		}
		record.Stored = StoredSkillVersion{
			Snapshot: snapshot,
			Files:    files,
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed-agent skill version backfill candidates: %w", err)
	}
	return out, nil
}

func (r *Repository) BackfillSkillBundle(ctx context.Context, teamID, skillID, version, bundlePath, bundleSHA256 string, bundleSizeBytes int64) (bool, error) {
	result, err := r.db(ctx).Exec(ctx, `
		UPDATE managed_agent_skill_versions v
		SET bundle_path = $4,
			bundle_sha256 = $5,
			bundle_size_bytes = $6
		FROM managed_agent_skills s
		WHERE s.id = v.skill_id
			AND s.team_id = $1
			AND s.id = $2
			AND v.version = $3
			AND COALESCE(v.bundle_path, '') = ''
	`, strings.TrimSpace(teamID), strings.TrimSpace(skillID), strings.TrimSpace(version), strings.TrimSpace(bundlePath), strings.TrimSpace(bundleSHA256), bundleSizeBytes)
	if err != nil {
		return false, fmt.Errorf("backfill managed-agent skill bundle: %w", err)
	}
	return result.RowsAffected() > 0, nil
}

func (r *Repository) CountFilesMissingStorePath(ctx context.Context, teamID string) (int, error) {
	args := []any{}
	query := `SELECT COUNT(1) FROM managed_agent_files WHERE COALESCE(store_path, '') = ''`
	if strings.TrimSpace(teamID) != "" {
		args = append(args, strings.TrimSpace(teamID))
		query += fmt.Sprintf(" AND team_id = $%d", len(args))
	}
	var count int
	if err := r.db(ctx).QueryRow(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count managed-agent files missing store path: %w", err)
	}
	return count, nil
}

func (r *Repository) CountSkillVersionsMissingBundle(ctx context.Context, teamID string) (int, error) {
	args := []any{}
	query := `
		SELECT COUNT(1)
		FROM managed_agent_skill_versions v
		JOIN managed_agent_skills s ON s.id = v.skill_id
		WHERE COALESCE(v.bundle_path, '') = ''
	`
	if strings.TrimSpace(teamID) != "" {
		args = append(args, strings.TrimSpace(teamID))
		query += fmt.Sprintf(" AND s.team_id = $%d", len(args))
	}
	var count int
	if err := r.db(ctx).QueryRow(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count managed-agent skill versions missing bundle: %w", err)
	}
	return count, nil
}
