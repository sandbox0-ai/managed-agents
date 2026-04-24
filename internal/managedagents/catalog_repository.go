package managedagents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (r *Repository) CreateAgent(ctx context.Context, teamID, vendor string, version int, snapshot Agent, now time.Time) error {
	payloadJSON, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal agent snapshot: %w", err)
	}
	_, err = r.db(ctx).Exec(ctx, `
		INSERT INTO managed_agent_agents (id, team_id, vendor, current_version, snapshot, archived_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, NULL, $6, $7)
	`, snapshot.ID, teamID, vendor, version, string(payloadJSON), now, now)
	if err != nil {
		return fmt.Errorf("insert managed-agent agent: %w", err)
	}
	_, err = r.db(ctx).Exec(ctx, `
		INSERT INTO managed_agent_agent_versions (agent_id, version, snapshot, created_at)
		VALUES ($1, $2, $3::jsonb, $4)
	`, snapshot.ID, version, string(payloadJSON), now)
	if err != nil {
		return fmt.Errorf("insert managed-agent agent version: %w", err)
	}
	return nil
}

func (r *Repository) ListAgents(ctx context.Context, teamID string, opts AgentListOptions) ([]Agent, *string, error) {
	limit := opts.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	cursor, err := decodePageCursor(opts.Page)
	if err != nil {
		return nil, nil, err
	}
	order := normalizeListOrder(opts.Order)
	query := `SELECT snapshot, id, created_at FROM managed_agent_agents WHERE team_id = $1`
	args := []any{teamID}
	if !opts.IncludeArchived {
		query += ` AND archived_at IS NULL`
	}
	if opts.CreatedAt.GTE != nil {
		args = append(args, opts.CreatedAt.GTE.UTC())
		query += fmt.Sprintf(` AND created_at >= $%d`, len(args))
	}
	if opts.CreatedAt.LTE != nil {
		args = append(args, opts.CreatedAt.LTE.UTC())
		query += fmt.Sprintf(` AND created_at <= $%d`, len(args))
	}
	if cursor != nil {
		cursorTime, _ := time.Parse(time.RFC3339, cursor.CreatedAt)
		args = append(args, cursorTime.UTC(), cursor.ID)
		cmp := "<"
		if order == "asc" {
			cmp = ">"
		}
		query += fmt.Sprintf(` AND (created_at %s $%d OR (created_at = $%d AND id %s $%d))`, cmp, len(args)-1, len(args)-1, cmp, len(args))
	}
	args = append(args, limit+1)
	query += fmt.Sprintf(` ORDER BY created_at %s, id %s LIMIT $%d`, strings.ToUpper(order), strings.ToUpper(order), len(args))
	rows, err := r.db(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("list managed-agent agents: %w", err)
	}
	defer rows.Close()
	agents := make([]Agent, 0, limit)
	createdAt := make([]time.Time, 0, limit)
	ids := make([]string, 0, limit)
	for rows.Next() {
		var payloadJSON []byte
		var id string
		var when time.Time
		if err := rows.Scan(&payloadJSON, &id, &when); err != nil {
			return nil, nil, fmt.Errorf("scan managed-agent agent: %w", err)
		}
		snapshot, err := decodeAgentSnapshot(payloadJSON)
		if err != nil {
			return nil, nil, err
		}
		agents = append(agents, snapshot)
		createdAt = append(createdAt, when)
		ids = append(ids, id)
	}
	var nextPage *string
	if len(agents) > limit {
		nextPage = encodePageCursor(createdAt[limit-1], ids[limit-1])
		agents = agents[:limit]
	}
	return agents, nextPage, nil
}

func (r *Repository) GetAgent(ctx context.Context, teamID, agentID string, version int) (*Agent, string, error) {
	trimmedID := strings.TrimSpace(agentID)
	if version > 0 {
		var (
			payloadJSON []byte
			vendor      string
		)
		err := r.db(ctx).QueryRow(ctx, `
			SELECT v.snapshot, a.vendor
			FROM managed_agent_agent_versions v
			JOIN managed_agent_agents a ON a.id = v.agent_id
			WHERE a.team_id = $1 AND a.id = $2 AND v.version = $3
		`, teamID, trimmedID, version).Scan(&payloadJSON, &vendor)
		if err != nil {
			if err == pgx.ErrNoRows {
				return nil, "", ErrAgentNotFound
			}
			return nil, "", fmt.Errorf("query managed-agent agent version: %w", err)
		}
		snapshot, err := decodeAgentSnapshot(payloadJSON)
		if err != nil {
			return nil, "", err
		}
		return &snapshot, vendor, nil
	}
	var (
		payloadJSON []byte
		vendor      string
	)
	err := r.db(ctx).QueryRow(ctx, `
		SELECT snapshot, vendor
		FROM managed_agent_agents
		WHERE team_id = $1 AND id = $2
	`, teamID, trimmedID).Scan(&payloadJSON, &vendor)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, "", ErrAgentNotFound
		}
		return nil, "", fmt.Errorf("query managed-agent agent: %w", err)
	}
	snapshot, err := decodeAgentSnapshot(payloadJSON)
	if err != nil {
		return nil, "", err
	}
	return &snapshot, vendor, nil
}

func (r *Repository) UpdateAgent(ctx context.Context, teamID, agentID, vendor string, expectedVersion, version int, snapshot *Agent, archivedAt *time.Time, updatedAt time.Time) error {
	payloadJSON, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal agent snapshot: %w", err)
	}
	result, err := r.db(ctx).Exec(ctx, `
		UPDATE managed_agent_agents
		SET vendor = $3, current_version = $4, snapshot = $5::jsonb, archived_at = $6, updated_at = $7
		WHERE team_id = $1 AND id = $2 AND current_version = $8
	`, teamID, strings.TrimSpace(agentID), vendor, version, string(payloadJSON), archivedAt, updatedAt, expectedVersion)
	if err != nil {
		return fmt.Errorf("update managed-agent agent: %w", err)
	}
	if result.RowsAffected() == 0 {
		var exists bool
		if err := r.db(ctx).QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_agent_agents WHERE team_id = $1 AND id = $2)`, teamID, strings.TrimSpace(agentID)).Scan(&exists); err != nil {
			return fmt.Errorf("query managed-agent agent existence: %w", err)
		}
		if !exists {
			return ErrAgentNotFound
		}
		return errors.New("invalid version")
	}
	_, err = r.db(ctx).Exec(ctx, `
		INSERT INTO managed_agent_agent_versions (agent_id, version, snapshot, created_at)
		VALUES ($1, $2, $3::jsonb, $4)
		ON CONFLICT (agent_id, version) DO UPDATE SET snapshot = EXCLUDED.snapshot, created_at = EXCLUDED.created_at
	`, strings.TrimSpace(agentID), version, string(payloadJSON), updatedAt)
	if err != nil {
		return fmt.Errorf("upsert managed-agent agent version: %w", err)
	}
	return nil
}

func (r *Repository) ListAgentVersions(ctx context.Context, teamID, agentID string, limit int, page string) ([]Agent, *string, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	cursor, err := decodePageCursor(page)
	if err != nil {
		return nil, nil, err
	}
	query := `
		SELECT v.snapshot, v.version, v.created_at
		FROM managed_agent_agent_versions v
		JOIN managed_agent_agents a ON a.id = v.agent_id
		WHERE a.team_id = $1 AND a.id = $2
	`
	args := []any{teamID, strings.TrimSpace(agentID)}
	if cursor != nil {
		cursorTime, _ := time.Parse(time.RFC3339, cursor.CreatedAt)
		cursorVersion, convErr := strconv.Atoi(strings.TrimSpace(cursor.ID))
		if convErr != nil {
			return nil, nil, fmt.Errorf("invalid page cursor")
		}
		args = append(args, cursorTime.UTC(), cursorVersion)
		query += fmt.Sprintf(` AND (v.created_at < $%d OR (v.created_at = $%d AND v.version < $%d))`, len(args)-1, len(args)-1, len(args))
	}
	args = append(args, limit+1)
	query += fmt.Sprintf(` ORDER BY v.created_at DESC, v.version DESC LIMIT $%d`, len(args))
	rows, err := r.db(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("list managed-agent agent versions: %w", err)
	}
	defer rows.Close()
	versions := make([]Agent, 0, limit)
	createdAt := make([]time.Time, 0, limit)
	versionNumbers := make([]int, 0, limit)
	for rows.Next() {
		var (
			payloadJSON []byte
			version     int
			when        time.Time
		)
		if err := rows.Scan(&payloadJSON, &version, &when); err != nil {
			return nil, nil, fmt.Errorf("scan managed-agent agent version: %w", err)
		}
		snapshot, err := decodeAgentSnapshot(payloadJSON)
		if err != nil {
			return nil, nil, err
		}
		versions = append(versions, snapshot)
		createdAt = append(createdAt, when)
		versionNumbers = append(versionNumbers, version)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate managed-agent agent versions: %w", err)
	}
	var nextPage *string
	if len(versions) > limit {
		nextPage = encodePageCursor(createdAt[limit-1], strconv.Itoa(versionNumbers[limit-1]))
		versions = versions[:limit]
	}
	return versions, nextPage, nil
}

func (r *Repository) CreateEnvironment(ctx context.Context, teamID string, snapshot Environment, archivedAt *time.Time, now time.Time) error {
	payloadJSON, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal managed_agent_environments snapshot: %w", err)
	}
	_, err = r.db(ctx).Exec(ctx, `
		INSERT INTO managed_agent_environments (id, team_id, snapshot, archived_at, created_at, updated_at)
		VALUES ($1, $2, $3::jsonb, $4, $5, $6)
	`, snapshot.ID, teamID, string(payloadJSON), archivedAt, now, now)
	if err != nil {
		return fmt.Errorf("insert managed_agent_environments: %w", err)
	}
	return nil
}

func (r *Repository) ListEnvironments(ctx context.Context, teamID string, limit int, page string, includeArchived bool) ([]Environment, *string, error) {
	if limit <= 0 || limit > 1000 {
		limit = 20
	}
	cursor, err := decodePageCursor(page)
	if err != nil {
		return nil, nil, err
	}
	query := `SELECT snapshot, id, created_at FROM managed_agent_environments WHERE team_id = $1`
	args := []any{teamID}
	if !includeArchived {
		query += ` AND archived_at IS NULL`
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
		return nil, nil, fmt.Errorf("query snapshot page: %w", err)
	}
	defer rows.Close()
	return scanEnvironmentPage(rows, limit)
}

func (r *Repository) GetEnvironment(ctx context.Context, teamID, environmentID string) (*Environment, error) {
	var payloadJSON []byte
	err := r.db(ctx).QueryRow(ctx, `SELECT snapshot FROM managed_agent_environments WHERE team_id = $1 AND id = $2`, teamID, strings.TrimSpace(environmentID)).Scan(&payloadJSON)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrEnvironmentNotFound
		}
		return nil, fmt.Errorf("query managed_agent_environments: %w", err)
	}
	snapshot, err := decodeEnvironmentSnapshot(payloadJSON)
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (r *Repository) UpdateEnvironment(ctx context.Context, teamID, environmentID string, snapshot *Environment, archivedAt *time.Time, updatedAt time.Time) error {
	payloadJSON, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal managed_agent_environments snapshot: %w", err)
	}
	result, err := r.db(ctx).Exec(ctx, `
		UPDATE managed_agent_environments SET snapshot = $3::jsonb, archived_at = $4, updated_at = $5 WHERE team_id = $1 AND id = $2
	`, teamID, strings.TrimSpace(environmentID), string(payloadJSON), archivedAt, updatedAt)
	if err != nil {
		return fmt.Errorf("update managed_agent_environments: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrEnvironmentNotFound
	}
	return nil
}

func (r *Repository) DeleteEnvironment(ctx context.Context, teamID, environmentID string) error {
	return r.deleteSnapshotObject(ctx, "managed_agent_environments", teamID, environmentID, ErrEnvironmentNotFound)
}

func (r *Repository) CreateVault(ctx context.Context, teamID string, snapshot Vault, archivedAt *time.Time, now time.Time) error {
	payloadJSON, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal managed_agent_vaults snapshot: %w", err)
	}
	_, err = r.db(ctx).Exec(ctx, `
		INSERT INTO managed_agent_vaults (id, team_id, snapshot, archived_at, created_at, updated_at)
		VALUES ($1, $2, $3::jsonb, $4, $5, $6)
	`, snapshot.ID, teamID, string(payloadJSON), archivedAt, now, now)
	if err != nil {
		return fmt.Errorf("insert managed_agent_vaults: %w", err)
	}
	return nil
}

func (r *Repository) ListVaults(ctx context.Context, teamID string, limit int, page string, includeArchived bool) ([]Vault, *string, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	cursor, err := decodePageCursor(page)
	if err != nil {
		return nil, nil, err
	}
	query := `SELECT snapshot, id, created_at FROM managed_agent_vaults WHERE team_id = $1`
	args := []any{teamID}
	if !includeArchived {
		query += ` AND archived_at IS NULL`
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
		return nil, nil, fmt.Errorf("query snapshot page: %w", err)
	}
	defer rows.Close()
	return scanVaultPage(rows, limit)
}

func (r *Repository) GetVault(ctx context.Context, teamID, vaultID string) (*Vault, error) {
	var payloadJSON []byte
	err := r.db(ctx).QueryRow(ctx, `SELECT snapshot FROM managed_agent_vaults WHERE team_id = $1 AND id = $2`, teamID, strings.TrimSpace(vaultID)).Scan(&payloadJSON)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrVaultNotFound
		}
		return nil, fmt.Errorf("query managed_agent_vaults: %w", err)
	}
	snapshot, err := decodeVaultSnapshot(payloadJSON)
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (r *Repository) CountActiveSessionsForVault(ctx context.Context, teamID, vaultID string) (int, error) {
	var count int
	err := r.db(ctx).QueryRow(ctx, `
		SELECT COUNT(1)
		FROM managed_agent_sessions
		WHERE team_id = $1
			AND vault_ids ? $2
			AND deleted_at IS NULL
	`, strings.TrimSpace(teamID), strings.TrimSpace(vaultID)).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count managed-agent vault sessions: %w", err)
	}
	return count, nil
}

func (r *Repository) UpdateVault(ctx context.Context, teamID, vaultID string, snapshot *Vault, archivedAt *time.Time, updatedAt time.Time) error {
	payloadJSON, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal managed_agent_vaults snapshot: %w", err)
	}
	result, err := r.db(ctx).Exec(ctx, `
		UPDATE managed_agent_vaults SET snapshot = $3::jsonb, archived_at = $4, updated_at = $5 WHERE team_id = $1 AND id = $2
	`, teamID, strings.TrimSpace(vaultID), string(payloadJSON), archivedAt, updatedAt)
	if err != nil {
		return fmt.Errorf("update managed_agent_vaults: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrVaultNotFound
	}
	return nil
}

func (r *Repository) DeleteVault(ctx context.Context, teamID, vaultID string) error {
	return r.deleteSnapshotObject(ctx, "managed_agent_vaults", teamID, vaultID, ErrVaultNotFound)
}

func (r *Repository) CreateCredential(ctx context.Context, teamID, vaultID string, snapshot Credential, secret map[string]any, archivedAt *time.Time, now time.Time) error {
	payloadJSON, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal credential snapshot: %w", err)
	}
	secretJSON, err := json.Marshal(secret)
	if err != nil {
		return fmt.Errorf("marshal credential secret: %w", err)
	}
	_, err = r.db(ctx).Exec(ctx, `
		INSERT INTO managed_agent_credentials (id, team_id, vault_id, snapshot, secret, archived_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6, $7, $8)
	`, snapshot.ID, teamID, vaultID, string(payloadJSON), string(secretJSON), archivedAt, now, now)
	if err != nil {
		return fmt.Errorf("insert managed-agent credential: %w", err)
	}
	return nil
}

func (r *Repository) ListCredentials(ctx context.Context, teamID, vaultID string, limit int, page string, includeArchived bool) ([]Credential, *string, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	cursor, err := decodePageCursor(page)
	if err != nil {
		return nil, nil, err
	}
	query := `SELECT snapshot, id, created_at FROM managed_agent_credentials WHERE team_id = $1 AND vault_id = $2`
	args := []any{teamID, vaultID}
	if !includeArchived {
		query += ` AND archived_at IS NULL`
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
		return nil, nil, fmt.Errorf("query snapshot page: %w", err)
	}
	defer rows.Close()
	return scanCredentialPage(rows, limit)
}

func (r *Repository) ListActiveCredentialsForVault(ctx context.Context, teamID, vaultID string) ([]StoredCredential, error) {
	rows, err := r.db(ctx).Query(ctx, `
		SELECT c.snapshot, c.secret
		FROM managed_agent_credentials c
		JOIN managed_agent_vaults v
			ON v.team_id = c.team_id AND v.id = c.vault_id
		WHERE c.team_id = $1
			AND c.vault_id = $2
			AND c.archived_at IS NULL
			AND v.archived_at IS NULL
		ORDER BY c.created_at ASC, c.id ASC
	`, teamID, strings.TrimSpace(vaultID))
	if err != nil {
		return nil, fmt.Errorf("list managed-agent credentials for vault: %w", err)
	}
	defer rows.Close()

	out := make([]StoredCredential, 0)
	for rows.Next() {
		var (
			payloadJSON []byte
			secretJSON  []byte
		)
		if err := rows.Scan(&payloadJSON, &secretJSON); err != nil {
			return nil, fmt.Errorf("scan managed-agent credential for vault: %w", err)
		}
		snapshot, err := decodeCredentialSnapshot(payloadJSON)
		if err != nil {
			return nil, err
		}
		secret, err := decodeSnapshot(secretJSON)
		if err != nil {
			return nil, err
		}
		out = append(out, StoredCredential{Snapshot: snapshot, Secret: secret})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed-agent credentials for vault: %w", err)
	}
	return out, nil
}

func (r *Repository) GetCredential(ctx context.Context, teamID, vaultID, credentialID string) (*Credential, map[string]any, error) {
	var (
		payloadJSON []byte
		secretJSON  []byte
	)
	err := r.db(ctx).QueryRow(ctx, `
		SELECT snapshot, secret
		FROM managed_agent_credentials
		WHERE team_id = $1 AND vault_id = $2 AND id = $3
	`, teamID, strings.TrimSpace(vaultID), strings.TrimSpace(credentialID)).Scan(&payloadJSON, &secretJSON)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil, ErrCredentialNotFound
		}
		return nil, nil, fmt.Errorf("query managed-agent credential: %w", err)
	}
	snapshot, err := decodeCredentialSnapshot(payloadJSON)
	if err != nil {
		return nil, nil, err
	}
	secret, err := decodeSnapshot(secretJSON)
	if err != nil {
		return nil, nil, err
	}
	return &snapshot, secret, nil
}

func (r *Repository) UpdateCredential(ctx context.Context, teamID, vaultID, credentialID string, snapshot *Credential, secret map[string]any, archivedAt *time.Time, updatedAt time.Time) error {
	payloadJSON, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal credential snapshot: %w", err)
	}
	secretJSON, err := json.Marshal(secret)
	if err != nil {
		return fmt.Errorf("marshal credential secret: %w", err)
	}
	result, err := r.db(ctx).Exec(ctx, `
		UPDATE managed_agent_credentials
		SET snapshot = $4::jsonb, secret = $5::jsonb, archived_at = $6, updated_at = $7
		WHERE team_id = $1 AND vault_id = $2 AND id = $3
	`, teamID, strings.TrimSpace(vaultID), strings.TrimSpace(credentialID), string(payloadJSON), string(secretJSON), archivedAt, updatedAt)
	if err != nil {
		return fmt.Errorf("update managed-agent credential: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

func (r *Repository) DeleteCredential(ctx context.Context, teamID, vaultID, credentialID string) error {
	result, err := r.db(ctx).Exec(ctx, `
		DELETE FROM managed_agent_credentials
		WHERE team_id = $1 AND vault_id = $2 AND id = $3
	`, teamID, strings.TrimSpace(vaultID), strings.TrimSpace(credentialID))
	if err != nil {
		return fmt.Errorf("delete managed-agent credential: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

func (r *Repository) CreateFile(ctx context.Context, record *managedFileRecord) error {
	payloadJSON, err := json.Marshal(buildFileObject(record))
	if err != nil {
		return fmt.Errorf("marshal file snapshot: %w", err)
	}
	_, err = r.db(ctx).Exec(ctx, `
		INSERT INTO managed_agent_files (
			id, team_id, filename, mime_type, size_bytes, downloadable, scope_type, scope_id,
			store_path, sha256, snapshot, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, TRUE, $6, $7, $8, $9, $10::jsonb, $11, $12)
	`, record.ID, record.TeamID, record.Filename, record.MimeType, record.SizeBytes, nullableString(record.ScopeType), nullableString(record.ScopeID),
		record.StorePath, record.SHA256, string(payloadJSON), record.CreatedAt, record.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert managed-agent file: %w", err)
	}
	return nil
}

func (r *Repository) ListFiles(ctx context.Context, teamID string, opts FileListOptions) ([]FileMetadata, bool, error) {
	limit := opts.Limit
	if limit <= 0 || limit > 1000 {
		limit = 20
	}
	query := `SELECT snapshot FROM managed_agent_files WHERE team_id = $1`
	args := []any{teamID}
	if strings.TrimSpace(opts.ScopeID) != "" {
		query += ` AND scope_id = $2`
		args = append(args, strings.TrimSpace(opts.ScopeID))
	}
	cursorID := strings.TrimSpace(opts.AfterID)
	cmp := "<"
	if strings.TrimSpace(opts.BeforeID) != "" {
		cursorID = strings.TrimSpace(opts.BeforeID)
		cmp = ">"
	}
	if cursorID != "" {
		cursor, err := r.GetFile(ctx, teamID, cursorID)
		if err != nil {
			return nil, false, err
		}
		args = append(args, cursor.CreatedAt, cursor.ID)
		query += fmt.Sprintf(` AND (created_at %s $%d OR (created_at = $%d AND id %s $%d))`, cmp, len(args)-1, len(args)-1, cmp, len(args))
	}
	args = append(args, limit+1)
	query += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args))
	rows, err := r.db(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("list managed-agent files: %w", err)
	}
	defer rows.Close()
	items := make([]FileMetadata, 0, limit)
	for rows.Next() {
		var payloadJSON []byte
		if err := rows.Scan(&payloadJSON); err != nil {
			return nil, false, fmt.Errorf("scan managed-agent file snapshot: %w", err)
		}
		var item FileMetadata
		if err := json.Unmarshal(payloadJSON, &item); err != nil {
			return nil, false, fmt.Errorf("decode managed-agent file snapshot: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate managed-agent file snapshots: %w", err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

func (r *Repository) GetFile(ctx context.Context, teamID, fileID string) (*managedFileRecord, error) {
	var record managedFileRecord
	err := r.db(ctx).QueryRow(ctx, `
		SELECT id, team_id, filename, mime_type, size_bytes, COALESCE(scope_type, ''), COALESCE(scope_id, ''),
			store_path, sha256, created_at, updated_at
		FROM managed_agent_files
		WHERE team_id = $1 AND id = $2
	`, teamID, strings.TrimSpace(fileID)).Scan(
		&record.ID, &record.TeamID, &record.Filename, &record.MimeType, &record.SizeBytes, &record.ScopeType, &record.ScopeID,
		&record.StorePath, &record.SHA256, &record.CreatedAt, &record.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrFileNotFound
		}
		return nil, fmt.Errorf("query managed-agent file: %w", err)
	}
	return &record, nil
}

func (r *Repository) DeleteFile(ctx context.Context, teamID, fileID string) error {
	result, err := r.db(ctx).Exec(ctx, `DELETE FROM managed_agent_files WHERE team_id = $1 AND id = $2`, teamID, strings.TrimSpace(fileID))
	if err != nil {
		return fmt.Errorf("delete managed-agent file: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrFileNotFound
	}
	return nil
}

func (r *Repository) createSnapshotObject(ctx context.Context, table, teamID string, snapshot map[string]any, archivedAt *time.Time, now time.Time) error {
	payloadJSON, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal %s snapshot: %w", table, err)
	}
	_, err = r.db(ctx).Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, team_id, snapshot, archived_at, created_at, updated_at)
		VALUES ($1, $2, $3::jsonb, $4, $5, $6)
	`, table), stringValue(snapshot["id"]), teamID, string(payloadJSON), archivedAt, now, now)
	if err != nil {
		return fmt.Errorf("insert %s: %w", table, err)
	}
	return nil
}

func (r *Repository) getSnapshotObject(ctx context.Context, table, teamID, id string, notFound error) (map[string]any, error) {
	var payloadJSON []byte
	err := r.db(ctx).QueryRow(ctx, fmt.Sprintf(`
		SELECT snapshot FROM %s WHERE team_id = $1 AND id = $2
	`, table), teamID, strings.TrimSpace(id)).Scan(&payloadJSON)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, notFound
		}
		return nil, fmt.Errorf("query %s: %w", table, err)
	}
	return decodeSnapshot(payloadJSON)
}

func (r *Repository) updateSnapshotObject(ctx context.Context, table, teamID, id string, snapshot map[string]any, archivedAt *time.Time, updatedAt time.Time, notFound error) error {
	payloadJSON, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal %s snapshot: %w", table, err)
	}
	result, err := r.db(ctx).Exec(ctx, fmt.Sprintf(`
		UPDATE %s SET snapshot = $3::jsonb, archived_at = $4, updated_at = $5 WHERE team_id = $1 AND id = $2
	`, table), teamID, strings.TrimSpace(id), string(payloadJSON), archivedAt, updatedAt)
	if err != nil {
		return fmt.Errorf("update %s: %w", table, err)
	}
	if result.RowsAffected() == 0 {
		return notFound
	}
	return nil
}

func (r *Repository) deleteSnapshotObject(ctx context.Context, table, teamID, id string, notFound error) error {
	result, err := r.db(ctx).Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE team_id = $1 AND id = $2`, table), teamID, strings.TrimSpace(id))
	if err != nil {
		return fmt.Errorf("delete %s: %w", table, err)
	}
	if result.RowsAffected() == 0 {
		return notFound
	}
	return nil
}

func (r *Repository) listSnapshots(ctx context.Context, query string, args ...any) ([]map[string]any, error) {
	rows, err := r.db(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	defer rows.Close()
	return scanSnapshots(rows)
}

func (r *Repository) listSnapshotsWithCursor(ctx context.Context, query string, limit int, args ...any) ([]map[string]any, *string, error) {
	rows, err := r.db(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("query snapshot page: %w", err)
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit)
	createdAt := make([]time.Time, 0, limit)
	ids := make([]string, 0, limit)
	for rows.Next() {
		var payloadJSON []byte
		var id string
		var when time.Time
		if err := rows.Scan(&payloadJSON, &id, &when); err != nil {
			return nil, nil, fmt.Errorf("scan snapshot page: %w", err)
		}
		snapshot, err := decodeSnapshot(payloadJSON)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, snapshot)
		createdAt = append(createdAt, when)
		ids = append(ids, id)
	}
	var nextPage *string
	if len(items) > limit {
		nextPage = encodePageCursor(createdAt[limit-1], ids[limit-1])
		items = items[:limit]
	}
	return items, nextPage, nil
}

func scanSnapshots(rows pgx.Rows) ([]map[string]any, error) {
	out := []map[string]any{}
	for rows.Next() {
		var payloadJSON []byte
		if err := rows.Scan(&payloadJSON); err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}
		snapshot, err := decodeSnapshot(payloadJSON)
		if err != nil {
			return nil, err
		}
		out = append(out, snapshot)
	}
	return out, rows.Err()
}

func decodeSnapshot(payloadJSON []byte) (map[string]any, error) {
	var snapshot map[string]any
	if err := json.Unmarshal(payloadJSON, &snapshot); err != nil {
		return nil, fmt.Errorf("decode snapshot: %w", err)
	}
	return snapshot, nil
}

func decodeAgentSnapshot(payloadJSON []byte) (Agent, error) {
	var snapshot Agent
	if err := json.Unmarshal(payloadJSON, &snapshot); err != nil {
		return Agent{}, fmt.Errorf("decode agent snapshot: %w", err)
	}
	return snapshot, nil
}

func decodeEnvironmentSnapshot(payloadJSON []byte) (Environment, error) {
	var snapshot Environment
	if err := json.Unmarshal(payloadJSON, &snapshot); err != nil {
		return Environment{}, fmt.Errorf("decode environment snapshot: %w", err)
	}
	return snapshot, nil
}

func decodeVaultSnapshot(payloadJSON []byte) (Vault, error) {
	var snapshot Vault
	if err := json.Unmarshal(payloadJSON, &snapshot); err != nil {
		return Vault{}, fmt.Errorf("decode vault snapshot: %w", err)
	}
	return snapshot, nil
}

func decodeCredentialSnapshot(payloadJSON []byte) (Credential, error) {
	var snapshot Credential
	if err := json.Unmarshal(payloadJSON, &snapshot); err != nil {
		return Credential{}, fmt.Errorf("decode credential snapshot: %w", err)
	}
	return snapshot, nil
}

func scanEnvironmentPage(rows pgx.Rows, limit int) ([]Environment, *string, error) {
	items := make([]Environment, 0, limit)
	createdAt := make([]time.Time, 0, limit)
	ids := make([]string, 0, limit)
	for rows.Next() {
		var (
			payloadJSON []byte
			id          string
			when        time.Time
		)
		if err := rows.Scan(&payloadJSON, &id, &when); err != nil {
			return nil, nil, fmt.Errorf("scan snapshot page: %w", err)
		}
		snapshot, err := decodeEnvironmentSnapshot(payloadJSON)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, snapshot)
		createdAt = append(createdAt, when)
		ids = append(ids, id)
	}
	var nextPage *string
	if len(items) > limit {
		nextPage = encodePageCursor(createdAt[limit-1], ids[limit-1])
		items = items[:limit]
	}
	return items, nextPage, nil
}

func scanVaultPage(rows pgx.Rows, limit int) ([]Vault, *string, error) {
	items := make([]Vault, 0, limit)
	createdAt := make([]time.Time, 0, limit)
	ids := make([]string, 0, limit)
	for rows.Next() {
		var (
			payloadJSON []byte
			id          string
			when        time.Time
		)
		if err := rows.Scan(&payloadJSON, &id, &when); err != nil {
			return nil, nil, fmt.Errorf("scan snapshot page: %w", err)
		}
		snapshot, err := decodeVaultSnapshot(payloadJSON)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, snapshot)
		createdAt = append(createdAt, when)
		ids = append(ids, id)
	}
	var nextPage *string
	if len(items) > limit {
		nextPage = encodePageCursor(createdAt[limit-1], ids[limit-1])
		items = items[:limit]
	}
	return items, nextPage, nil
}

func scanCredentialPage(rows pgx.Rows, limit int) ([]Credential, *string, error) {
	items := make([]Credential, 0, limit)
	createdAt := make([]time.Time, 0, limit)
	ids := make([]string, 0, limit)
	for rows.Next() {
		var (
			payloadJSON []byte
			id          string
			when        time.Time
		)
		if err := rows.Scan(&payloadJSON, &id, &when); err != nil {
			return nil, nil, fmt.Errorf("scan snapshot page: %w", err)
		}
		snapshot, err := decodeCredentialSnapshot(payloadJSON)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, snapshot)
		createdAt = append(createdAt, when)
		ids = append(ids, id)
	}
	var nextPage *string
	if len(items) > limit {
		nextPage = encodePageCursor(createdAt[limit-1], ids[limit-1])
		items = items[:limit]
	}
	return items, nextPage, nil
}
