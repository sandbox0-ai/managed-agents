package managedagents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (r *Repository) CreateSkillWithVersion(ctx context.Context, teamID string, skill map[string]any, version map[string]any, now time.Time) error {
	skillJSON, err := json.Marshal(skill)
	if err != nil {
		return fmt.Errorf("marshal skill snapshot: %w", err)
	}
	versionJSON, err := json.Marshal(version)
	if err != nil {
		return fmt.Errorf("marshal skill version snapshot: %w", err)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin create skill transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO managed_agent_skills (id, team_id, source, latest_version, snapshot, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7)
	`, stringValue(skill["id"]), teamID, stringValue(skill["source"]), nullableString(stringValue(skill["latest_version"])), string(skillJSON), now, now); err != nil {
		return fmt.Errorf("insert managed-agent skill: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO managed_agent_skill_versions (id, skill_id, version, snapshot, created_at)
		VALUES ($1, $2, $3, $4::jsonb, $5)
	`, stringValue(version["id"]), stringValue(version["skill_id"]), stringValue(version["version"]), string(versionJSON), now); err != nil {
		return fmt.Errorf("insert managed-agent skill version: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit create skill transaction: %w", err)
	}
	return nil
}

func (r *Repository) ListSkills(ctx context.Context, teamID string, limit int, page, source string) ([]map[string]any, *string, bool, error) {
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
	items, nextPage, err := r.listSnapshotsWithCursor(ctx, query, limit, args...)
	if err != nil {
		return nil, nil, false, err
	}
	return items, nextPage, nextPage != nil, nil
}

func (r *Repository) GetSkill(ctx context.Context, teamID, skillID string) (map[string]any, error) {
	return r.getSnapshotObject(ctx, "managed_agent_skills", teamID, skillID, ErrSkillNotFound)
}

func (r *Repository) DeleteSkill(ctx context.Context, teamID, skillID string) error {
	return r.deleteSnapshotObject(ctx, "managed_agent_skills", teamID, skillID, ErrSkillNotFound)
}

func (r *Repository) CreateSkillVersion(ctx context.Context, teamID, skillID string, snapshot map[string]any, now time.Time) error {
	payloadJSON, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal skill version snapshot: %w", err)
	}
	tx, err := r.pool.Begin(ctx)
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
	skill, err := decodeSnapshot(skillJSON)
	if err != nil {
		return err
	}
	skill["latest_version"] = stringValue(snapshot["version"])
	skill["updated_at"] = nowRFC3339(now)
	updatedSkillJSON, err := json.Marshal(skill)
	if err != nil {
		return fmt.Errorf("marshal updated skill snapshot: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO managed_agent_skill_versions (id, skill_id, version, snapshot, created_at)
		VALUES ($1, $2, $3, $4::jsonb, $5)
	`, stringValue(snapshot["id"]), strings.TrimSpace(skillID), stringValue(snapshot["version"]), string(payloadJSON), now); err != nil {
		return fmt.Errorf("insert managed-agent skill version: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE managed_agent_skills SET latest_version = $3, snapshot = $4::jsonb, updated_at = $5 WHERE team_id = $1 AND id = $2
	`, teamID, strings.TrimSpace(skillID), nullableString(stringValue(snapshot["version"])), string(updatedSkillJSON), now); err != nil {
		return fmt.Errorf("update managed-agent skill latest version: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit create skill version transaction: %w", err)
	}
	return nil
}

func (r *Repository) ListSkillVersions(ctx context.Context, teamID, skillID string, limit int, page string) ([]map[string]any, *string, bool, error) {
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
	items, nextPage, err := r.listSnapshotsWithCursor(ctx, query, limit, args...)
	if err != nil {
		return nil, nil, false, err
	}
	return items, nextPage, nextPage != nil, nil
}

func (r *Repository) GetSkillVersion(ctx context.Context, teamID, skillID, version string) (map[string]any, error) {
	var payloadJSON []byte
	err := r.pool.QueryRow(ctx, `
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
	return decodeSnapshot(payloadJSON)
}

func (r *Repository) DeleteSkillVersion(ctx context.Context, teamID, skillID, version string, now time.Time) error {
	tx, err := r.pool.Begin(ctx)
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
	skill, err := decodeSnapshot(skillJSON)
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
	skill["latest_version"] = nullableStringPointer(latestVersionPtr)
	skill["updated_at"] = nowRFC3339(now)
	updatedSkillJSON, err := json.Marshal(skill)
	if err != nil {
		return fmt.Errorf("marshal updated skill snapshot: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE managed_agent_skills SET latest_version = $3, snapshot = $4::jsonb, updated_at = $5 WHERE team_id = $1 AND id = $2
	`, teamID, strings.TrimSpace(skillID), nullableStringPointer(latestVersionPtr), string(updatedSkillJSON), now); err != nil {
		return fmt.Errorf("update managed-agent skill after delete: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete skill version transaction: %w", err)
	}
	return nil
}
