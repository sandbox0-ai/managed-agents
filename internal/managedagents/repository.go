package managedagents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrSessionNotFound      = errors.New("managed-agent session not found")
	ErrRuntimeNotFound      = errors.New("managed-agent runtime not found")
	ErrAgentNotFound        = errors.New("managed-agent agent not found")
	ErrEnvironmentNotFound  = errors.New("managed-agent environment not found")
	ErrVaultNotFound        = errors.New("managed-agent vault not found")
	ErrCredentialNotFound   = errors.New("managed-agent credential not found")
	ErrFileNotFound         = errors.New("managed-agent file not found")
	ErrSkillNotFound        = errors.New("managed-agent skill not found")
	ErrSkillVersionNotFound = errors.New("managed-agent skill version not found")
	ErrResourceNotFound     = errors.New("managed-agent session resource not found")
	ErrEventNotFound        = errors.New("managed-agent event not found")
)

// Repository persists managed-agent session state.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository returns a new managed-agent repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) CreateSession(ctx context.Context, record *SessionRecord, engine map[string]any) error {
	agentJSON, err := json.Marshal(record.Agent)
	if err != nil {
		return fmt.Errorf("marshal agent: %w", err)
	}
	resourcesJSON, err := json.Marshal(record.Resources)
	if err != nil {
		return fmt.Errorf("marshal resources: %w", err)
	}
	vaultIDsJSON, err := json.Marshal(record.VaultIDs)
	if err != nil {
		return fmt.Errorf("marshal vault ids: %w", err)
	}
	metadataJSON, err := json.Marshal(record.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	engineJSON, err := json.Marshal(engine)
	if err != nil {
		return fmt.Errorf("marshal engine: %w", err)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO managed_agent_sessions (
			id, team_id, created_by_user_id, vendor, environment_id, working_directory, title, metadata,
			agent, resources, vault_ids, engine, status,
			usage_input_tokens, usage_output_tokens, usage_cache_read_input_tokens,
			usage_cache_creation_ephemeral_1h_input_tokens, usage_cache_creation_ephemeral_5m_input_tokens,
			stats_active_seconds, last_status_started_at, archived_at, deleted_at, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb, $10::jsonb, $11::jsonb, $12::jsonb, $13,
			$14, $15, $16, $17, $18, $19, $20, $21, NULL, $22, $23)
	`, record.ID, record.TeamID, nullableString(record.CreatedByUserID), record.Vendor, record.EnvironmentID, record.WorkingDirectory, nullableStringPointer(record.Title),
		string(metadataJSON), string(agentJSON), string(resourcesJSON), string(vaultIDsJSON), string(engineJSON), record.Status,
		record.Usage.InputTokens, record.Usage.OutputTokens, record.Usage.CacheReadInputTokens,
		usageCacheCreationEphemeral1H(record.Usage), usageCacheCreationEphemeral5M(record.Usage),
		record.StatsActiveSeconds, record.LastStatusStartedAt, record.ArchivedAt, record.CreatedAt, record.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert managed-agent session: %w", err)
	}
	return nil
}

func (r *Repository) GetSession(ctx context.Context, sessionID string) (*SessionRecord, map[string]any, error) {
	var (
		record        SessionRecord
		metadataJSON  []byte
		agentJSON     []byte
		resourcesJSON []byte
		vaultIDsJSON  []byte
		engineJSON    []byte
	)
	err := r.pool.QueryRow(ctx, `
		SELECT id, team_id, COALESCE(created_by_user_id::text, ''), vendor, environment_id, working_directory, title,
			metadata, agent, resources, vault_ids, engine, status,
			usage_input_tokens, usage_output_tokens, usage_cache_read_input_tokens,
			usage_cache_creation_ephemeral_1h_input_tokens, usage_cache_creation_ephemeral_5m_input_tokens,
			stats_active_seconds, last_status_started_at, archived_at, deleted_at, created_at, updated_at
		FROM managed_agent_sessions
		WHERE id = $1 AND deleted_at IS NULL
	`, strings.TrimSpace(sessionID)).Scan(
		&record.ID, &record.TeamID, &record.CreatedByUserID, &record.Vendor, &record.EnvironmentID, &record.WorkingDirectory, &record.Title,
		&metadataJSON, &agentJSON, &resourcesJSON, &vaultIDsJSON, &engineJSON, &record.Status,
		&record.Usage.InputTokens, &record.Usage.OutputTokens, &record.Usage.CacheReadInputTokens,
		usageCacheCreationEphemeral1HTarget(&record.Usage), usageCacheCreationEphemeral5MTarget(&record.Usage),
		&record.StatsActiveSeconds, &record.LastStatusStartedAt, &record.ArchivedAt, &record.DeletedAt, &record.CreatedAt, &record.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrSessionNotFound
		}
		return nil, nil, fmt.Errorf("query managed-agent session: %w", err)
	}
	if err := json.Unmarshal(metadataJSON, &record.Metadata); err != nil {
		return nil, nil, fmt.Errorf("decode metadata: %w", err)
	}
	if err := json.Unmarshal(agentJSON, &record.Agent); err != nil {
		return nil, nil, fmt.Errorf("decode agent: %w", err)
	}
	if err := json.Unmarshal(resourcesJSON, &record.Resources); err != nil {
		return nil, nil, fmt.Errorf("decode resources: %w", err)
	}
	if err := json.Unmarshal(vaultIDsJSON, &record.VaultIDs); err != nil {
		return nil, nil, fmt.Errorf("decode vault ids: %w", err)
	}
	var engine map[string]any
	if len(engineJSON) != 0 {
		if err := json.Unmarshal(engineJSON, &engine); err != nil {
			return nil, nil, fmt.Errorf("decode engine: %w", err)
		}
	}
	record.Usage = normalizeUsageValue(record.Usage)
	return &record, engine, nil
}

func (r *Repository) ListSessions(ctx context.Context, teamID string, opts SessionListOptions) ([]*SessionRecord, *string, error) {
	limit := opts.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	cursor, err := decodePageCursor(opts.Page)
	if err != nil {
		return nil, nil, err
	}
	order := normalizeListOrder(opts.Order)
	query := `
		SELECT id, team_id, COALESCE(created_by_user_id::text, ''), vendor, environment_id, working_directory, title,
			metadata, agent, resources, vault_ids, '{}'::jsonb, status,
			usage_input_tokens, usage_output_tokens, usage_cache_read_input_tokens,
			usage_cache_creation_ephemeral_1h_input_tokens, usage_cache_creation_ephemeral_5m_input_tokens,
			stats_active_seconds, last_status_started_at, archived_at, deleted_at, created_at, updated_at
		FROM managed_agent_sessions
		WHERE team_id = $1
	`
	args := []any{teamID}
	query += ` AND deleted_at IS NULL`
	if !opts.IncludeArchived {
		query += ` AND archived_at IS NULL`
	}
	if trimmed := strings.TrimSpace(opts.AgentID); trimmed != "" {
		args = append(args, trimmed)
		query += fmt.Sprintf(` AND agent->>'id' = $%d`, len(args))
	}
	if opts.AgentVersion > 0 {
		args = append(args, opts.AgentVersion)
		query += fmt.Sprintf(` AND COALESCE((agent->>'version')::int, 0) = $%d`, len(args))
	}
	if opts.CreatedAt.GTE != nil {
		args = append(args, opts.CreatedAt.GTE.UTC())
		query += fmt.Sprintf(` AND created_at >= $%d`, len(args))
	}
	if opts.CreatedAt.GT != nil {
		args = append(args, opts.CreatedAt.GT.UTC())
		query += fmt.Sprintf(` AND created_at > $%d`, len(args))
	}
	if opts.CreatedAt.LTE != nil {
		args = append(args, opts.CreatedAt.LTE.UTC())
		query += fmt.Sprintf(` AND created_at <= $%d`, len(args))
	}
	if opts.CreatedAt.LT != nil {
		args = append(args, opts.CreatedAt.LT.UTC())
		query += fmt.Sprintf(` AND created_at < $%d`, len(args))
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
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("list managed-agent sessions: %w", err)
	}
	defer rows.Close()
	var sessions []*SessionRecord
	for rows.Next() {
		var (
			record        SessionRecord
			metadataJSON  []byte
			agentJSON     []byte
			resourcesJSON []byte
			vaultIDsJSON  []byte
			ignoredEngine []byte
		)
		if err := rows.Scan(
			&record.ID, &record.TeamID, &record.CreatedByUserID, &record.Vendor, &record.EnvironmentID, &record.WorkingDirectory, &record.Title,
			&metadataJSON, &agentJSON, &resourcesJSON, &vaultIDsJSON, &ignoredEngine, &record.Status,
			&record.Usage.InputTokens, &record.Usage.OutputTokens, &record.Usage.CacheReadInputTokens,
			usageCacheCreationEphemeral1HTarget(&record.Usage), usageCacheCreationEphemeral5MTarget(&record.Usage),
			&record.StatsActiveSeconds, &record.LastStatusStartedAt, &record.ArchivedAt, &record.DeletedAt, &record.CreatedAt, &record.UpdatedAt,
		); err != nil {
			return nil, nil, fmt.Errorf("scan managed-agent session: %w", err)
		}
		_ = json.Unmarshal(metadataJSON, &record.Metadata)
		_ = json.Unmarshal(agentJSON, &record.Agent)
		_ = json.Unmarshal(resourcesJSON, &record.Resources)
		_ = json.Unmarshal(vaultIDsJSON, &record.VaultIDs)
		record.Usage = normalizeUsageValue(record.Usage)
		sessions = append(sessions, &record)
	}
	var nextPage *string
	if len(sessions) > limit {
		last := sessions[limit-1]
		nextPage = encodePageCursor(last.CreatedAt, last.ID)
		sessions = sessions[:limit]
	}
	return sessions, nextPage, nil
}

func (r *Repository) UpdateSession(ctx context.Context, record *SessionRecord, engine map[string]any) error {
	metadataJSON, err := json.Marshal(record.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	agentJSON, err := json.Marshal(record.Agent)
	if err != nil {
		return fmt.Errorf("marshal agent: %w", err)
	}
	resourcesJSON, err := json.Marshal(record.Resources)
	if err != nil {
		return fmt.Errorf("marshal resources: %w", err)
	}
	vaultIDsJSON, err := json.Marshal(record.VaultIDs)
	if err != nil {
		return fmt.Errorf("marshal vault ids: %w", err)
	}
	engineJSON, err := json.Marshal(engine)
	if err != nil {
		return fmt.Errorf("marshal engine: %w", err)
	}
	result, err := r.pool.Exec(ctx, `
		UPDATE managed_agent_sessions
		SET working_directory = $2, title = $3, metadata = $4::jsonb, agent = $5::jsonb, resources = $6::jsonb,
			vault_ids = $7::jsonb, engine = $8::jsonb, status = $9,
			usage_input_tokens = $10, usage_output_tokens = $11, usage_cache_read_input_tokens = $12,
			usage_cache_creation_ephemeral_1h_input_tokens = $13, usage_cache_creation_ephemeral_5m_input_tokens = $14,
			stats_active_seconds = $15, last_status_started_at = $16, archived_at = $17, deleted_at = $18, updated_at = $19
		WHERE id = $1
	`, record.ID, record.WorkingDirectory, nullableStringPointer(record.Title), string(metadataJSON), string(agentJSON), string(resourcesJSON),
		string(vaultIDsJSON), string(engineJSON), record.Status,
		record.Usage.InputTokens, record.Usage.OutputTokens, record.Usage.CacheReadInputTokens,
		usageCacheCreationEphemeral1H(record.Usage), usageCacheCreationEphemeral5M(record.Usage),
		record.StatsActiveSeconds, record.LastStatusStartedAt, record.ArchivedAt, record.DeletedAt, record.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update managed-agent session: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrSessionNotFound
	}
	return nil
}

func (r *Repository) MarkSessionDeleted(ctx context.Context, sessionID string, deletedAt time.Time) error {
	result, err := r.pool.Exec(ctx, `
		UPDATE managed_agent_sessions
		SET deleted_at = $2, status = 'terminated', last_status_started_at = NULL, updated_at = $2
		WHERE id = $1 AND deleted_at IS NULL
	`, strings.TrimSpace(sessionID), deletedAt.UTC())
	if err != nil {
		return fmt.Errorf("mark managed-agent session deleted: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrSessionNotFound
	}
	return nil
}

func (r *Repository) AppendEvents(ctx context.Context, sessionID string, events []map[string]any) error {
	if len(events) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, event := range events {
		payloadJSON, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("marshal session event: %w", err)
		}
		batch.Queue(`
			INSERT INTO managed_agent_session_events (id, session_id, event_type, payload, created_at)
			VALUES ($1, $2, $3, $4::jsonb, $5)
		`, stringValue(event["id"]), sessionID, stringValue(event["type"]), string(payloadJSON), time.Now().UTC())
	}
	results := r.pool.SendBatch(ctx, batch)
	defer results.Close()
	for range events {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("insert session event: %w", err)
		}
	}
	return nil
}

func (r *Repository) ListEvents(ctx context.Context, sessionID string, opts EventListOptions) ([]map[string]any, *string, error) {
	limit := opts.Limit
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	order := normalizeListOrder(opts.Order)
	cursor, err := decodePageCursor(opts.Page)
	if err != nil {
		return nil, nil, err
	}
	query := `
		SELECT payload, id, created_at
		FROM managed_agent_session_events
		WHERE session_id = $1
	`
	args := []any{strings.TrimSpace(sessionID)}
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
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("list session events: %w", err)
	}
	defer rows.Close()
	var events []map[string]any
	createdAt := make([]time.Time, 0, limit)
	ids := make([]string, 0, limit)
	for rows.Next() {
		var payloadJSON []byte
		var id string
		var when time.Time
		if err := rows.Scan(&payloadJSON, &id, &when); err != nil {
			return nil, nil, fmt.Errorf("scan session event: %w", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(payloadJSON, &payload); err != nil {
			return nil, nil, fmt.Errorf("decode session event: %w", err)
		}
		events = append(events, payload)
		createdAt = append(createdAt, when)
		ids = append(ids, id)
	}
	var nextPage *string
	if len(events) > limit {
		nextPage = encodePageCursor(createdAt[limit-1], ids[limit-1])
		events = events[:limit]
	}
	return events, nextPage, nil
}

func (r *Repository) ListEventsAfterID(ctx context.Context, sessionID, afterID string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	if trimmedAfterID := strings.TrimSpace(afterID); trimmedAfterID != "" {
		var exists bool
		if err := r.pool.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM managed_agent_session_events WHERE session_id = $1 AND id = $2
			)
		`, strings.TrimSpace(sessionID), trimmedAfterID).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check session event after id: %w", err)
		}
		if !exists {
			return nil, ErrEventNotFound
		}
	}
	query := `
		SELECT payload
		FROM managed_agent_session_events
		WHERE session_id = $1
	`
	args := []any{strings.TrimSpace(sessionID)}
	if trimmedAfterID := strings.TrimSpace(afterID); trimmedAfterID != "" {
		args = append(args, trimmedAfterID)
		query += `
			AND (
				created_at > (SELECT created_at FROM managed_agent_session_events WHERE session_id = $1 AND id = $2)
				OR (
					created_at = (SELECT created_at FROM managed_agent_session_events WHERE session_id = $1 AND id = $2)
					AND id > $2
				)
			)
		`
	}
	args = append(args, limit)
	query += fmt.Sprintf(` ORDER BY created_at ASC, id ASC LIMIT $%d`, len(args))
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		if strings.TrimSpace(afterID) != "" && strings.Contains(err.Error(), "more than one row") {
			return nil, ErrEventNotFound
		}
		return nil, fmt.Errorf("list session events after id: %w", err)
	}
	defer rows.Close()
	var events []map[string]any
	for rows.Next() {
		var payloadJSON []byte
		if err := rows.Scan(&payloadJSON); err != nil {
			return nil, fmt.Errorf("scan session event after id: %w", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(payloadJSON, &payload); err != nil {
			return nil, fmt.Errorf("decode session event after id: %w", err)
		}
		events = append(events, payload)
	}
	return events, nil
}

func (r *Repository) UpsertSessionResourceSecret(ctx context.Context, sessionID, resourceID string, secret map[string]any) error {
	secretJSON, err := json.Marshal(secret)
	if err != nil {
		return fmt.Errorf("marshal session resource secret: %w", err)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO managed_agent_session_resource_secrets (session_id, resource_id, secret)
		VALUES ($1, $2, $3::jsonb)
		ON CONFLICT (session_id, resource_id) DO UPDATE SET
			secret = EXCLUDED.secret,
			updated_at = NOW()
	`, strings.TrimSpace(sessionID), strings.TrimSpace(resourceID), string(secretJSON))
	if err != nil {
		return fmt.Errorf("upsert session resource secret: %w", err)
	}
	return nil
}

func (r *Repository) DeleteSessionResourceSecret(ctx context.Context, sessionID, resourceID string) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM managed_agent_session_resource_secrets
		WHERE session_id = $1 AND resource_id = $2
	`, strings.TrimSpace(sessionID), strings.TrimSpace(resourceID))
	if err != nil {
		return fmt.Errorf("delete session resource secret: %w", err)
	}
	return nil
}

func (r *Repository) GetSessionResourceSecret(ctx context.Context, sessionID, resourceID string) (map[string]any, error) {
	var secretJSON []byte
	err := r.pool.QueryRow(ctx, `
		SELECT secret
		FROM managed_agent_session_resource_secrets
		WHERE session_id = $1 AND resource_id = $2
	`, strings.TrimSpace(sessionID), strings.TrimSpace(resourceID)).Scan(&secretJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query session resource secret: %w", err)
	}
	var secret map[string]any
	if err := json.Unmarshal(secretJSON, &secret); err != nil {
		return nil, fmt.Errorf("decode session resource secret: %w", err)
	}
	return secret, nil
}

func (r *Repository) DeleteRuntime(ctx context.Context, sessionID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM managed_agent_session_runtimes WHERE session_id = $1`, strings.TrimSpace(sessionID))
	if err != nil {
		return fmt.Errorf("delete managed-agent runtime: %w", err)
	}
	return nil
}

func (r *Repository) ResolveRuntimeRegionID(ctx context.Context, teamID string, configuredRegionID string) (string, error) {
	_ = ctx
	_ = teamID
	if regionID := strings.TrimSpace(configuredRegionID); regionID != "" {
		return regionID, nil
	}
	return "default", nil
}

func (r *Repository) teamHomeRegionID(ctx context.Context, teamID string) (string, error) {
	var homeRegionID string
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(home_region_id, '')
		FROM teams
		WHERE id = $1
	`, strings.TrimSpace(teamID)).Scan(&homeRegionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("managed-agent team %q not found", strings.TrimSpace(teamID))
		}
		return "", fmt.Errorf("query managed-agent team home region: %w", err)
	}
	return strings.TrimSpace(homeRegionID), nil
}

func (r *Repository) ensureRegionExists(ctx context.Context, regionID string) error {
	var storedRegionID string
	err := r.pool.QueryRow(ctx, `SELECT id FROM regions WHERE id = $1`, strings.TrimSpace(regionID)).Scan(&storedRegionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("managed-agent runtime region %q not found", strings.TrimSpace(regionID))
		}
		return fmt.Errorf("query managed-agent runtime region: %w", err)
	}
	return nil
}

func (r *Repository) listRegionIDs(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT id FROM regions ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query managed-agent runtime regions: %w", err)
	}
	defer rows.Close()

	regionIDs := make([]string, 0)
	for rows.Next() {
		var regionID string
		if err := rows.Scan(&regionID); err != nil {
			return nil, fmt.Errorf("scan managed-agent runtime region: %w", err)
		}
		regionIDs = append(regionIDs, strings.TrimSpace(regionID))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed-agent runtime regions: %w", err)
	}
	return regionIDs, nil
}

func (r *Repository) GetRuntime(ctx context.Context, sessionID string) (*RuntimeRecord, error) {
	var runtime RuntimeRecord
	err := r.pool.QueryRow(ctx, `
		SELECT session_id, vendor, region_id, sandbox_id, COALESCE(wrapper_url, ''), workspace_volume_id, engine_state_volume_id,
			callback_token, COALESCE(vendor_session_id, ''), runtime_generation, active_run_id, created_at, updated_at
		FROM managed_agent_session_runtimes
		WHERE session_id = $1
	`, strings.TrimSpace(sessionID)).Scan(
		&runtime.SessionID, &runtime.Vendor, &runtime.RegionID, &runtime.SandboxID, &runtime.WrapperURL, &runtime.WorkspaceVolumeID, &runtime.EngineStateVolumeID,
		&runtime.ControlToken, &runtime.VendorSessionID, &runtime.RuntimeGeneration, &runtime.ActiveRunID, &runtime.CreatedAt, &runtime.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRuntimeNotFound
		}
		return nil, fmt.Errorf("query managed-agent runtime: %w", err)
	}
	return &runtime, nil
}

func (r *Repository) GetRuntimeBySandboxID(ctx context.Context, sandboxID string) (*RuntimeRecord, error) {
	var runtime RuntimeRecord
	err := r.pool.QueryRow(ctx, `
		SELECT session_id, vendor, region_id, sandbox_id, COALESCE(wrapper_url, ''), workspace_volume_id, engine_state_volume_id,
			callback_token, COALESCE(vendor_session_id, ''), runtime_generation, active_run_id, created_at, updated_at
		FROM managed_agent_session_runtimes
		WHERE sandbox_id = $1
	`, strings.TrimSpace(sandboxID)).Scan(
		&runtime.SessionID, &runtime.Vendor, &runtime.RegionID, &runtime.SandboxID, &runtime.WrapperURL, &runtime.WorkspaceVolumeID, &runtime.EngineStateVolumeID,
		&runtime.ControlToken, &runtime.VendorSessionID, &runtime.RuntimeGeneration, &runtime.ActiveRunID, &runtime.CreatedAt, &runtime.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRuntimeNotFound
		}
		return nil, fmt.Errorf("query managed-agent runtime by sandbox: %w", err)
	}
	return &runtime, nil
}

func (r *Repository) UpsertRuntime(ctx context.Context, runtime *RuntimeRecord) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO managed_agent_session_runtimes (
			session_id, vendor, region_id, sandbox_id, wrapper_url, workspace_volume_id, engine_state_volume_id,
			callback_token, vendor_session_id, runtime_generation, active_run_id, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (session_id) DO UPDATE SET
			vendor = EXCLUDED.vendor,
			region_id = EXCLUDED.region_id,
			sandbox_id = EXCLUDED.sandbox_id,
			wrapper_url = EXCLUDED.wrapper_url,
			workspace_volume_id = EXCLUDED.workspace_volume_id,
			engine_state_volume_id = EXCLUDED.engine_state_volume_id,
			callback_token = EXCLUDED.callback_token,
			vendor_session_id = EXCLUDED.vendor_session_id,
			runtime_generation = EXCLUDED.runtime_generation,
			active_run_id = EXCLUDED.active_run_id,
			updated_at = EXCLUDED.updated_at
	`, runtime.SessionID, runtime.Vendor, runtime.RegionID, runtime.SandboxID, runtime.WrapperURL, runtime.WorkspaceVolumeID, runtime.EngineStateVolumeID,
		runtime.ControlToken, nullableString(runtime.VendorSessionID), runtime.RuntimeGeneration, runtime.ActiveRunID, runtime.CreatedAt, runtime.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert managed-agent runtime: %w", err)
	}
	return nil
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullableStringPointer(value *string) any {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	return *value
}

func usageCacheCreationEphemeral1H(usage Usage) int64 {
	if usage.CacheCreation == nil {
		return 0
	}
	return usage.CacheCreation.Ephemeral1HInputTokens
}

func usageCacheCreationEphemeral5M(usage Usage) int64 {
	if usage.CacheCreation == nil {
		return 0
	}
	return usage.CacheCreation.Ephemeral5MInputTokens
}

func usageCacheCreationEphemeral1HTarget(usage *Usage) *int64 {
	if usage.CacheCreation == nil {
		usage.CacheCreation = &CacheCreationUsage{}
	}
	return &usage.CacheCreation.Ephemeral1HInputTokens
}

func usageCacheCreationEphemeral5MTarget(usage *Usage) *int64 {
	if usage.CacheCreation == nil {
		usage.CacheCreation = &CacheCreationUsage{}
	}
	return &usage.CacheCreation.Ephemeral5MInputTokens
}
