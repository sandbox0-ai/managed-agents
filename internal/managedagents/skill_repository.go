package managedagents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (r *Repository) CreateSkillWithVersion(ctx context.Context, teamID string, skill Skill, mountSlug string, version SkillVersion, artifact skillVersionArtifact, files []storedSkillFile, now time.Time) error {
	skillJSON, err := json.Marshal(skill)
	if err != nil {
		return fmt.Errorf("marshal skill snapshot: %w", err)
	}
	versionJSON, err := json.Marshal(version)
	if err != nil {
		return fmt.Errorf("marshal skill version snapshot: %w", err)
	}
	filesJSON, err := json.Marshal(files)
	if err != nil {
		return fmt.Errorf("marshal skill version files: %w", err)
	}
	tx, err := r.db(ctx).Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin create skill transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO managed_agent_skills (id, team_id, source, latest_version, mount_slug, snapshot, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8)
	`, skill.ID, teamID, skill.Source, nullableStringPointer(skill.LatestVersion), nullableString(mountSlug), string(skillJSON), now, now); err != nil {
		return fmt.Errorf("insert managed-agent skill: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO managed_agent_skill_versions (
			id, skill_id, version, snapshot, files, artifact_volume_id, artifact_path, content_digest,
			artifact_sha256, artifact_size_bytes, file_count, created_at
		)
		VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6, $7, $8, $9, $10, $11, $12)
	`, version.ID, version.SkillID, version.Version, string(versionJSON), string(filesJSON),
		nullableString(artifact.VolumeID), nullableString(artifact.Path), nullableString(artifact.ContentDigest),
		nullableString(artifact.ArchiveSHA256), nullableInt64(artifact.SizeBytes), nullableInt(artifact.FileCount), now); err != nil {
		return fmt.Errorf("insert managed-agent skill version: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit create skill transaction: %w", err)
	}
	return nil
}

func (r *Repository) ListSkills(ctx context.Context, teamID string, limit int, page, source string) ([]Skill, *string, bool, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	cursor, err := decodePageCursor(page)
	if err != nil {
		return nil, nil, false, err
	}
	query := `SELECT snapshot, id, created_at FROM managed_agent_skills WHERE team_id = $1`
	args := []any{teamID}
	if trimmed := strings.TrimSpace(source); trimmed != "" {
		args = append(args, trimmed)
		query += fmt.Sprintf(` AND source = $%d`, len(args))
	}
	if cursor != nil {
		cursorTime, _ := time.Parse(time.RFC3339, cursor.CreatedAt)
		args = append(args, cursorTime.UTC(), cursor.ID)
		query += fmt.Sprintf(` AND (created_at < $%d OR (created_at = $%d AND id < $%d))`, len(args)-1, len(args)-1, len(args))
	}
	args = append(args, limit+1)
	query += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args))
	rows, err := r.db(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, nil, false, fmt.Errorf("list managed-agent skills: %w", err)
	}
	defer rows.Close()
	items := make([]Skill, 0, limit)
	createdAt := make([]time.Time, 0, limit)
	ids := make([]string, 0, limit)
	for rows.Next() {
		var (
			payloadJSON []byte
			id          string
			when        time.Time
		)
		if err := rows.Scan(&payloadJSON, &id, &when); err != nil {
			return nil, nil, false, fmt.Errorf("scan managed-agent skill: %w", err)
		}
		snapshot, err := decodeSkillSnapshot(payloadJSON)
		if err != nil {
			return nil, nil, false, err
		}
		items = append(items, snapshot)
		createdAt = append(createdAt, when)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, fmt.Errorf("iterate managed-agent skills: %w", err)
	}
	var nextPage *string
	if len(items) > limit {
		nextPage = encodePageCursor(createdAt[limit-1], ids[limit-1])
		items = items[:limit]
	}
	return items, nextPage, nextPage != nil, nil
}

func (r *Repository) GetSkill(ctx context.Context, teamID, skillID string) (*Skill, error) {
	var payloadJSON []byte
	err := r.db(ctx).QueryRow(ctx, `SELECT snapshot FROM managed_agent_skills WHERE team_id = $1 AND id = $2`, teamID, strings.TrimSpace(skillID)).Scan(&payloadJSON)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrSkillNotFound
		}
		return nil, fmt.Errorf("query managed-agent skill: %w", err)
	}
	snapshot, err := decodeSkillSnapshot(payloadJSON)
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (r *Repository) GetStoredSkill(ctx context.Context, teamID, skillID string) (*storedSkill, error) {
	var (
		payloadJSON []byte
		mountSlug   string
	)
	err := r.db(ctx).QueryRow(ctx, `
		SELECT snapshot, COALESCE(mount_slug, '')
		FROM managed_agent_skills
		WHERE team_id = $1 AND id = $2
	`, teamID, strings.TrimSpace(skillID)).Scan(&payloadJSON, &mountSlug)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrSkillNotFound
		}
		return nil, fmt.Errorf("query managed-agent stored skill: %w", err)
	}
	snapshot, err := decodeSkillSnapshot(payloadJSON)
	if err != nil {
		return nil, err
	}
	return &storedSkill{
		Snapshot:  snapshot,
		MountSlug: strings.TrimSpace(mountSlug),
	}, nil
}

func (r *Repository) DeleteSkill(ctx context.Context, teamID, skillID string) error {
	return r.deleteSnapshotObject(ctx, "managed_agent_skills", teamID, skillID, ErrSkillNotFound)
}

func (r *Repository) CreateSkillVersion(ctx context.Context, teamID, skillID string, snapshot SkillVersion, mountSlug string, artifact skillVersionArtifact, files []storedSkillFile, now time.Time) error {
	payloadJSON, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal skill version snapshot: %w", err)
	}
	filesJSON, err := json.Marshal(files)
	if err != nil {
		return fmt.Errorf("marshal skill version files: %w", err)
	}
	tx, err := r.db(ctx).Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin create skill version transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	var skillJSON []byte
	var currentMountSlug string
	err = tx.QueryRow(ctx, `
		SELECT snapshot, COALESCE(mount_slug, '')
		FROM managed_agent_skills
		WHERE team_id = $1 AND id = $2
	`, teamID, strings.TrimSpace(skillID)).Scan(&skillJSON, &currentMountSlug)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ErrSkillNotFound
		}
		return fmt.Errorf("query managed-agent skill: %w", err)
	}
	skill, err := decodeSkillSnapshot(skillJSON)
	if err != nil {
		return err
	}
	skill.LatestVersion = normalizeNullableString(&snapshot.Version)
	skill.UpdatedAt = nowRFC3339(now)
	updatedSkillJSON, err := json.Marshal(skill)
	if err != nil {
		return fmt.Errorf("marshal updated skill snapshot: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO managed_agent_skill_versions (
			id, skill_id, version, snapshot, files, artifact_volume_id, artifact_path, content_digest,
			artifact_sha256, artifact_size_bytes, file_count, created_at
		)
		VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6, $7, $8, $9, $10, $11, $12)
	`, snapshot.ID, strings.TrimSpace(skillID), snapshot.Version, string(payloadJSON), string(filesJSON),
		nullableString(artifact.VolumeID), nullableString(artifact.Path), nullableString(artifact.ContentDigest),
		nullableString(artifact.ArchiveSHA256), nullableInt64(artifact.SizeBytes), nullableInt(artifact.FileCount), now); err != nil {
		return fmt.Errorf("insert managed-agent skill version: %w", err)
	}
	if strings.TrimSpace(currentMountSlug) == "" && strings.TrimSpace(mountSlug) != "" {
		currentMountSlug = strings.TrimSpace(mountSlug)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE managed_agent_skills
		SET latest_version = $3, mount_slug = $4, snapshot = $5::jsonb, updated_at = $6
		WHERE team_id = $1 AND id = $2
	`, teamID, strings.TrimSpace(skillID), nullableStringPointer(skill.LatestVersion), nullableString(currentMountSlug), string(updatedSkillJSON), now); err != nil {
		return fmt.Errorf("update managed-agent skill latest version: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit create skill version transaction: %w", err)
	}
	return nil
}

func (r *Repository) ListSkillVersions(ctx context.Context, teamID, skillID string, limit int, page string) ([]SkillVersion, *string, bool, error) {
	if limit <= 0 || limit > 1000 {
		limit = 20
	}
	cursor, err := decodePageCursor(page)
	if err != nil {
		return nil, nil, false, err
	}
	query := `
		SELECT v.snapshot, v.id, v.created_at
		FROM managed_agent_skill_versions v
		JOIN managed_agent_skills s ON s.id = v.skill_id
		WHERE s.team_id = $1 AND s.id = $2
	`
	args := []any{teamID, strings.TrimSpace(skillID)}
	if cursor != nil {
		cursorTime, _ := time.Parse(time.RFC3339, cursor.CreatedAt)
		args = append(args, cursorTime.UTC(), cursor.ID)
		query += fmt.Sprintf(` AND (v.created_at < $%d OR (v.created_at = $%d AND v.id < $%d))`, len(args)-1, len(args)-1, len(args))
	}
	args = append(args, limit+1)
	query += fmt.Sprintf(` ORDER BY v.created_at DESC, v.id DESC LIMIT $%d`, len(args))
	rows, err := r.db(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, nil, false, fmt.Errorf("list managed-agent skill versions: %w", err)
	}
	defer rows.Close()
	items := make([]SkillVersion, 0, limit)
	createdAt := make([]time.Time, 0, limit)
	ids := make([]string, 0, limit)
	for rows.Next() {
		var (
			payloadJSON []byte
			id          string
			when        time.Time
		)
		if err := rows.Scan(&payloadJSON, &id, &when); err != nil {
			return nil, nil, false, fmt.Errorf("scan managed-agent skill version: %w", err)
		}
		snapshot, err := decodeSkillVersionSnapshot(payloadJSON)
		if err != nil {
			return nil, nil, false, err
		}
		items = append(items, snapshot)
		createdAt = append(createdAt, when)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, fmt.Errorf("iterate managed-agent skill versions: %w", err)
	}
	var nextPage *string
	if len(items) > limit {
		nextPage = encodePageCursor(createdAt[limit-1], ids[limit-1])
		items = items[:limit]
	}
	return items, nextPage, nextPage != nil, nil
}

func (r *Repository) GetSkillVersion(ctx context.Context, teamID, skillID, version string) (*SkillVersion, error) {
	var payloadJSON []byte
	err := r.db(ctx).QueryRow(ctx, `
		SELECT v.snapshot
		FROM managed_agent_skill_versions v
		JOIN managed_agent_skills s ON s.id = v.skill_id
		WHERE s.team_id = $1 AND s.id = $2 AND v.version = $3
	`, teamID, strings.TrimSpace(skillID), strings.TrimSpace(version)).Scan(&payloadJSON)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrSkillVersionNotFound
		}
		return nil, fmt.Errorf("query managed-agent skill version: %w", err)
	}
	snapshot, err := decodeSkillVersionSnapshot(payloadJSON)
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (r *Repository) GetStoredSkillVersion(ctx context.Context, teamID, skillID, version string) (*StoredSkillVersion, error) {
	var (
		payloadJSON      []byte
		filesJSON        []byte
		mountSlug        string
		artifactVolumeID string
		artifactPath     string
		contentDigest    string
		artifactSHA256   string
		artifactSize     int64
		fileCount        int
	)
	err := r.db(ctx).QueryRow(ctx, `
		SELECT
			v.snapshot,
			v.files,
			COALESCE(s.mount_slug, ''),
			COALESCE(v.artifact_volume_id, ''),
			COALESCE(v.artifact_path, ''),
			COALESCE(v.content_digest, ''),
			COALESCE(v.artifact_sha256, ''),
			COALESCE(v.artifact_size_bytes, 0),
			COALESCE(v.file_count, 0)
		FROM managed_agent_skill_versions v
		JOIN managed_agent_skills s ON s.id = v.skill_id
		WHERE s.team_id = $1 AND s.id = $2 AND v.version = $3
	`, teamID, strings.TrimSpace(skillID), strings.TrimSpace(version)).Scan(
		&payloadJSON,
		&filesJSON,
		&mountSlug,
		&artifactVolumeID,
		&artifactPath,
		&contentDigest,
		&artifactSHA256,
		&artifactSize,
		&fileCount,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrSkillVersionNotFound
		}
		return nil, fmt.Errorf("query managed-agent stored skill version: %w", err)
	}
	snapshot, err := decodeSkillVersionSnapshot(payloadJSON)
	if err != nil {
		return nil, err
	}
	files := []storedSkillFile{}
	if len(filesJSON) != 0 {
		if err := json.Unmarshal(filesJSON, &files); err != nil {
			return nil, fmt.Errorf("decode managed-agent skill version files: %w", err)
		}
	}
	return &StoredSkillVersion{
		Snapshot:  snapshot,
		MountSlug: strings.TrimSpace(mountSlug),
		Artifact: skillVersionArtifact{
			VolumeID:      strings.TrimSpace(artifactVolumeID),
			Path:          strings.TrimSpace(artifactPath),
			ContentDigest: strings.TrimSpace(contentDigest),
			ArchiveSHA256: strings.TrimSpace(artifactSHA256),
			SizeBytes:     artifactSize,
			FileCount:     fileCount,
		},
		Files: files,
	}, nil
}

func (r *Repository) DeleteSkillVersion(ctx context.Context, teamID, skillID, version string, now time.Time) error {
	tx, err := r.db(ctx).Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete skill version transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	var skillJSON []byte
	err = tx.QueryRow(ctx, `SELECT snapshot FROM managed_agent_skills WHERE team_id = $1 AND id = $2`, teamID, strings.TrimSpace(skillID)).Scan(&skillJSON)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ErrSkillNotFound
		}
		return fmt.Errorf("query managed-agent skill: %w", err)
	}
	result, err := tx.Exec(ctx, `
		DELETE FROM managed_agent_skill_versions
		WHERE skill_id = $1 AND version = $2
	`, strings.TrimSpace(skillID), strings.TrimSpace(version))
	if err != nil {
		return fmt.Errorf("delete managed-agent skill version: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrSkillVersionNotFound
	}
	skill, err := decodeSkillSnapshot(skillJSON)
	if err != nil {
		return err
	}
	var latestVersion string
	err = tx.QueryRow(ctx, `
		SELECT version
		FROM managed_agent_skill_versions
		WHERE skill_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, strings.TrimSpace(skillID)).Scan(&latestVersion)
	if err != nil && err != pgx.ErrNoRows {
		return fmt.Errorf("query managed-agent latest skill version: %w", err)
	}
	var latestVersionPtr *string
	if err != pgx.ErrNoRows {
		latestVersionPtr = &latestVersion
	}
	skill.LatestVersion = normalizeNullableString(latestVersionPtr)
	skill.UpdatedAt = nowRFC3339(now)
	updatedSkillJSON, err := json.Marshal(skill)
	if err != nil {
		return fmt.Errorf("marshal updated skill snapshot: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE managed_agent_skills SET latest_version = $3, snapshot = $4::jsonb, updated_at = $5 WHERE team_id = $1 AND id = $2
	`, teamID, strings.TrimSpace(skillID), nullableStringPointer(skill.LatestVersion), string(updatedSkillJSON), now); err != nil {
		return fmt.Errorf("update managed-agent skill after delete: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete skill version transaction: %w", err)
	}
	return nil
}

func decodeSkillSnapshot(payloadJSON []byte) (Skill, error) {
	var snapshot Skill
	if err := json.Unmarshal(payloadJSON, &snapshot); err != nil {
		return Skill{}, fmt.Errorf("decode managed-agent skill snapshot: %w", err)
	}
	return snapshot, nil
}

func decodeSkillVersionSnapshot(payloadJSON []byte) (SkillVersion, error) {
	var snapshot SkillVersion
	if err := json.Unmarshal(payloadJSON, &snapshot); err != nil {
		return SkillVersion{}, fmt.Errorf("decode managed-agent skill version snapshot: %w", err)
	}
	return snapshot, nil
}
