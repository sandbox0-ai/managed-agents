package managedagents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (r *Repository) CreateSkillWithVersion(ctx context.Context, teamID string, skill Skill, version SkillVersion, bundle storedSkillBundle, now time.Time) error {
	skillJSON, err := json.Marshal(skill)
	if err != nil {
		return fmt.Errorf("marshal skill snapshot: %w", err)
	}
	versionJSON, err := json.Marshal(version)
	if err != nil {
		return fmt.Errorf("marshal skill version snapshot: %w", err)
	}
	tx, err := r.db(ctx).Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin create skill transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO managed_agent_skills (id, team_id, source, latest_version, snapshot, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7)
	`, skill.ID, teamID, skill.Source, nullableStringPointer(skill.LatestVersion), string(skillJSON), now, now); err != nil {
		return fmt.Errorf("insert managed-agent skill: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO managed_agent_skill_versions (id, skill_id, version, snapshot, bundle_path, bundle_sha256, bundle_size_bytes, created_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8)
	`, version.ID, version.SkillID, version.Version, string(versionJSON), bundle.Path, bundle.SHA256, bundle.SizeBytes, now); err != nil {
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

func (r *Repository) DeleteSkill(ctx context.Context, teamID, skillID string) error {
	return r.deleteSnapshotObject(ctx, "managed_agent_skills", teamID, skillID, ErrSkillNotFound)
}

func (r *Repository) CreateSkillVersion(ctx context.Context, teamID, skillID string, snapshot SkillVersion, bundle storedSkillBundle, now time.Time) error {
	payloadJSON, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal skill version snapshot: %w", err)
	}
	tx, err := r.db(ctx).Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin create skill version transaction: %w", err)
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
		INSERT INTO managed_agent_skill_versions (id, skill_id, version, snapshot, bundle_path, bundle_sha256, bundle_size_bytes, created_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8)
	`, snapshot.ID, strings.TrimSpace(skillID), snapshot.Version, string(payloadJSON), bundle.Path, bundle.SHA256, bundle.SizeBytes, now); err != nil {
		return fmt.Errorf("insert managed-agent skill version: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE managed_agent_skills SET latest_version = $3, snapshot = $4::jsonb, updated_at = $5 WHERE team_id = $1 AND id = $2
	`, teamID, strings.TrimSpace(skillID), nullableStringPointer(skill.LatestVersion), string(updatedSkillJSON), now); err != nil {
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
		payloadJSON     []byte
		bundlePath      string
		bundleSHA256    string
		bundleSizeBytes int64
	)
	err := r.db(ctx).QueryRow(ctx, `
		SELECT v.snapshot, v.bundle_path, v.bundle_sha256, v.bundle_size_bytes
		FROM managed_agent_skill_versions v
		JOIN managed_agent_skills s ON s.id = v.skill_id
		WHERE s.team_id = $1 AND s.id = $2 AND v.version = $3
	`, teamID, strings.TrimSpace(skillID), strings.TrimSpace(version)).Scan(&payloadJSON, &bundlePath, &bundleSHA256, &bundleSizeBytes)
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
	return &StoredSkillVersion{
		Snapshot: snapshot,
		Bundle: storedSkillBundle{
			Path:      strings.TrimSpace(bundlePath),
			SHA256:    strings.TrimSpace(bundleSHA256),
			SizeBytes: bundleSizeBytes,
		},
	}, nil
}

func (r *Repository) ListStoredSkillVersions(ctx context.Context, teamID, skillID string) ([]StoredSkillVersion, error) {
	rows, err := r.db(ctx).Query(ctx, `
		SELECT v.snapshot, v.bundle_path, v.bundle_sha256, v.bundle_size_bytes
		FROM managed_agent_skill_versions v
		JOIN managed_agent_skills s ON s.id = v.skill_id
		WHERE s.team_id = $1 AND s.id = $2
		ORDER BY v.created_at DESC, v.id DESC
	`, teamID, strings.TrimSpace(skillID))
	if err != nil {
		return nil, fmt.Errorf("list managed-agent stored skill versions: %w", err)
	}
	defer rows.Close()

	items := make([]StoredSkillVersion, 0)
	for rows.Next() {
		var (
			payloadJSON     []byte
			bundlePath      string
			bundleSHA256    string
			bundleSizeBytes int64
		)
		if err := rows.Scan(&payloadJSON, &bundlePath, &bundleSHA256, &bundleSizeBytes); err != nil {
			return nil, fmt.Errorf("scan managed-agent stored skill version: %w", err)
		}
		snapshot, err := decodeSkillVersionSnapshot(payloadJSON)
		if err != nil {
			return nil, err
		}
		items = append(items, StoredSkillVersion{
			Snapshot: snapshot,
			Bundle: storedSkillBundle{
				Path:      strings.TrimSpace(bundlePath),
				SHA256:    strings.TrimSpace(bundleSHA256),
				SizeBytes: bundleSizeBytes,
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed-agent stored skill versions: %w", err)
	}
	return items, nil
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
