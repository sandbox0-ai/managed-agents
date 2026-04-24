package managedagents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrSessionNotFound             = errors.New("managed-agent session not found")
	ErrRuntimeNotFound             = errors.New("managed-agent runtime not found")
	ErrAgentNotFound               = errors.New("managed-agent agent not found")
	ErrEnvironmentNotFound         = errors.New("managed-agent environment not found")
	ErrEnvironmentArtifactNotFound = errors.New("managed-agent environment artifact not found")
	ErrEnvironmentArtifactBuilding = errors.New("managed-agent environment artifact is still building")
	ErrEnvironmentNameConflict     = errors.New("managed-agent environment already exists")
	ErrEnvironmentInUse            = errors.New("managed-agent environment is referenced by existing sessions")
	ErrTeamAssetStoreNotFound      = errors.New("managed-agent team asset store not found")
	ErrVaultNotFound               = errors.New("managed-agent vault not found")
	ErrVaultInUse                  = errors.New("managed-agent vault is referenced by existing sessions")
	ErrCredentialNotFound          = errors.New("managed-agent credential not found")
	ErrFileNotFound                = errors.New("managed-agent file not found")
	ErrSkillNotFound               = errors.New("managed-agent skill not found")
	ErrSkillVersionNotFound        = errors.New("managed-agent skill version not found")
	ErrResourceNotFound            = errors.New("managed-agent session resource not found")
	ErrEventNotFound               = errors.New("managed-agent event not found")
	ErrSessionRunning              = errors.New("managed-agent session is running")
	ErrSessionArchived             = errors.New("managed-agent session is archived")
)

// Repository persists managed-agent session state.
type Repository struct {
	pool *pgxpool.Pool
}

type repositoryDB interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	SendBatch(context.Context, *pgx.Batch) pgx.BatchResults
}

type repositoryDBContextKey struct{}

// NewRepository returns a new managed-agent repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) db(ctx context.Context) repositoryDB {
	if db, ok := ctx.Value(repositoryDBContextKey{}).(repositoryDB); ok && db != nil {
		return db
	}
	return r.pool
}

func (r *Repository) WithSessionLock(ctx context.Context, sessionID string, fn func(context.Context) error) error {
	if _, ok := ctx.Value(repositoryDBContextKey{}).(repositoryDB); ok {
		return fn(ctx)
	}
	lockKey := "managed-agent-session:" + strings.TrimSpace(sessionID)
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("managed-agent session id is required")
	}
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire managed-agent session lock connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return fmt.Errorf("lock managed-agent session: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, lockKey)
	}()
	return fn(context.WithValue(ctx, repositoryDBContextKey{}, conn))
}

func (r *Repository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	if _, ok := ctx.Value(repositoryDBContextKey{}).(pgx.Tx); ok {
		return fn(ctx)
	}
	tx, err := r.db(ctx).Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin managed-agent transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	if err := fn(context.WithValue(ctx, repositoryDBContextKey{}, tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit managed-agent transaction: %w", err)
	}
	committed = true
	return nil
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
	_, err = r.db(ctx).Exec(ctx, `
		INSERT INTO managed_agent_sessions (
			id, team_id, created_by_user_id, vendor, environment_id, environment_artifact_id, working_directory, title, metadata,
			agent, resources, vault_ids, engine, status,
			usage_input_tokens, usage_output_tokens, usage_cache_read_input_tokens,
			usage_cache_creation_ephemeral_1h_input_tokens, usage_cache_creation_ephemeral_5m_input_tokens,
			stats_active_seconds, last_status_started_at, archived_at, deleted_at, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10::jsonb, $11::jsonb, $12::jsonb, $13::jsonb, $14,
			$15, $16, $17, $18, $19, $20, $21, $22, NULL, $23, $24)
	`, record.ID, record.TeamID, nullableString(record.CreatedByUserID), record.Vendor, record.EnvironmentID, nullableString(record.EnvironmentArtifactID), record.WorkingDirectory, nullableStringPointer(record.Title),
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
	err := r.db(ctx).QueryRow(ctx, `
		SELECT id, team_id, COALESCE(created_by_user_id::text, ''), vendor, environment_id, COALESCE(environment_artifact_id, ''), working_directory, title,
			metadata, agent, resources, vault_ids, engine, status,
			usage_input_tokens, usage_output_tokens, usage_cache_read_input_tokens,
			usage_cache_creation_ephemeral_1h_input_tokens, usage_cache_creation_ephemeral_5m_input_tokens,
			stats_active_seconds, last_status_started_at, archived_at, deleted_at, created_at, updated_at
		FROM managed_agent_sessions
		WHERE id = $1 AND deleted_at IS NULL
	`, strings.TrimSpace(sessionID)).Scan(
		&record.ID, &record.TeamID, &record.CreatedByUserID, &record.Vendor, &record.EnvironmentID, &record.EnvironmentArtifactID, &record.WorkingDirectory, &record.Title,
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
		SELECT id, team_id, COALESCE(created_by_user_id::text, ''), vendor, environment_id, COALESCE(environment_artifact_id, ''), working_directory, title,
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
	rows, err := r.db(ctx).Query(ctx, query, args...)
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
			&record.ID, &record.TeamID, &record.CreatedByUserID, &record.Vendor, &record.EnvironmentID, &record.EnvironmentArtifactID, &record.WorkingDirectory, &record.Title,
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
	result, err := r.db(ctx).Exec(ctx, `
		UPDATE managed_agent_sessions
		SET environment_artifact_id = $2, working_directory = $3, title = $4, metadata = $5::jsonb, agent = $6::jsonb, resources = $7::jsonb,
			vault_ids = $8::jsonb, engine = $9::jsonb, status = $10,
			usage_input_tokens = $11, usage_output_tokens = $12, usage_cache_read_input_tokens = $13,
			usage_cache_creation_ephemeral_1h_input_tokens = $14, usage_cache_creation_ephemeral_5m_input_tokens = $15,
			stats_active_seconds = $16, last_status_started_at = $17, archived_at = $18, deleted_at = $19, updated_at = $20
		WHERE id = $1
	`, record.ID, nullableString(record.EnvironmentArtifactID), record.WorkingDirectory, nullableStringPointer(record.Title), string(metadataJSON), string(agentJSON), string(resourcesJSON),
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
	result, err := r.db(ctx).Exec(ctx, `
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
			ON CONFLICT (id) DO NOTHING
		`, stringValue(event["id"]), sessionID, stringValue(event["type"]), string(payloadJSON), time.Now().UTC())
	}
	results := r.db(ctx).SendBatch(ctx, batch)
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
		SELECT payload, id, created_at, position
		FROM managed_agent_session_events
		WHERE session_id = $1
	`
	args := []any{strings.TrimSpace(sessionID)}
	if cursor != nil {
		cmp := "<"
		if order == "asc" {
			cmp = ">"
		}
		if cursor.Position > 0 {
			args = append(args, cursor.Position)
			query += fmt.Sprintf(` AND position %s $%d`, cmp, len(args))
		} else {
			cursorTime, _ := time.Parse(time.RFC3339, cursor.CreatedAt)
			args = append(args, cursorTime.UTC(), cursor.ID)
			query += fmt.Sprintf(` AND (created_at %s $%d OR (created_at = $%d AND id %s $%d))`, cmp, len(args)-1, len(args)-1, cmp, len(args))
		}
	}
	args = append(args, limit+1)
	query += fmt.Sprintf(` ORDER BY position %s LIMIT $%d`, strings.ToUpper(order), len(args))
	rows, err := r.db(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("list session events: %w", err)
	}
	defer rows.Close()
	var events []map[string]any
	createdAt := make([]time.Time, 0, limit)
	ids := make([]string, 0, limit)
	positions := make([]int64, 0, limit)
	for rows.Next() {
		var payloadJSON []byte
		var id string
		var when time.Time
		var position int64
		if err := rows.Scan(&payloadJSON, &id, &when, &position); err != nil {
			return nil, nil, fmt.Errorf("scan session event: %w", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(payloadJSON, &payload); err != nil {
			return nil, nil, fmt.Errorf("decode session event: %w", err)
		}
		events = append(events, payload)
		createdAt = append(createdAt, when)
		ids = append(ids, id)
		positions = append(positions, position)
	}
	var nextPage *string
	if len(events) > limit {
		nextPage = encodePositionPageCursor(createdAt[limit-1], ids[limit-1], positions[limit-1])
		events = events[:limit]
	}
	return events, nextPage, nil
}

func (r *Repository) ListEventsAfterID(ctx context.Context, sessionID, afterID string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	args := []any{strings.TrimSpace(sessionID)}
	if trimmedAfterID := strings.TrimSpace(afterID); trimmedAfterID != "" {
		var afterPosition int64
		if err := r.db(ctx).QueryRow(ctx, `
			SELECT position
			FROM managed_agent_session_events
			WHERE session_id = $1 AND id = $2
		`, strings.TrimSpace(sessionID), trimmedAfterID).Scan(&afterPosition); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrEventNotFound
			}
			return nil, fmt.Errorf("get session event position after id: %w", err)
		}
		args = append(args, afterPosition)
	}
	query := `
		SELECT payload
		FROM managed_agent_session_events
		WHERE session_id = $1
	`
	if trimmedAfterID := strings.TrimSpace(afterID); trimmedAfterID != "" {
		query += ` AND position > $2`
	}
	args = append(args, limit)
	query += fmt.Sprintf(` ORDER BY position ASC LIMIT $%d`, len(args))
	rows, err := r.db(ctx).Query(ctx, query, args...)
	if err != nil {
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
	_, err = r.db(ctx).Exec(ctx, `
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
	_, err := r.db(ctx).Exec(ctx, `
		DELETE FROM managed_agent_session_resource_secrets
		WHERE session_id = $1 AND resource_id = $2
	`, strings.TrimSpace(sessionID), strings.TrimSpace(resourceID))
	if err != nil {
		return fmt.Errorf("delete session resource secret: %w", err)
	}
	return nil
}

func (r *Repository) DeleteSessionResourceSecrets(ctx context.Context, sessionID string) error {
	_, err := r.db(ctx).Exec(ctx, `
		DELETE FROM managed_agent_session_resource_secrets
		WHERE session_id = $1
	`, strings.TrimSpace(sessionID))
	if err != nil {
		return fmt.Errorf("delete session resource secrets: %w", err)
	}
	return nil
}

func (r *Repository) GetSessionResourceSecret(ctx context.Context, sessionID, resourceID string) (map[string]any, error) {
	var secretJSON []byte
	err := r.db(ctx).QueryRow(ctx, `
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
	_, err := r.db(ctx).Exec(ctx, `DELETE FROM managed_agent_session_runtimes WHERE session_id = $1`, strings.TrimSpace(sessionID))
	if err != nil {
		return fmt.Errorf("delete managed-agent runtime: %w", err)
	}
	return nil
}

func (r *Repository) ResolveRuntimeRegionID(ctx context.Context, teamID string) (string, error) {
	_ = ctx
	_ = teamID
	return "default", nil
}

func (r *Repository) teamHomeRegionID(ctx context.Context, teamID string) (string, error) {
	var homeRegionID string
	err := r.db(ctx).QueryRow(ctx, `
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
	err := r.db(ctx).QueryRow(ctx, `SELECT id FROM regions WHERE id = $1`, strings.TrimSpace(regionID)).Scan(&storedRegionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("managed-agent runtime region %q not found", strings.TrimSpace(regionID))
		}
		return fmt.Errorf("query managed-agent runtime region: %w", err)
	}
	return nil
}

func (r *Repository) listRegionIDs(ctx context.Context) ([]string, error) {
	rows, err := r.db(ctx).Query(ctx, `SELECT id FROM regions ORDER BY id`)
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
	err := r.db(ctx).QueryRow(ctx, `
		SELECT session_id, vendor, region_id, COALESCE(sandbox_id, ''), COALESCE(wrapper_url, ''), workspace_volume_id,
			callback_token, COALESCE(vendor_session_id, ''), COALESCE(bootstrap_hash, ''), runtime_generation, active_run_id, sandbox_deleted_at, created_at, updated_at
		FROM managed_agent_session_runtimes
		WHERE session_id = $1
	`, strings.TrimSpace(sessionID)).Scan(
		&runtime.SessionID, &runtime.Vendor, &runtime.RegionID, &runtime.SandboxID, &runtime.WrapperURL, &runtime.WorkspaceVolumeID,
		&runtime.ControlToken, &runtime.VendorSessionID, &runtime.BootstrapHash, &runtime.RuntimeGeneration, &runtime.ActiveRunID, &runtime.SandboxDeletedAt, &runtime.CreatedAt, &runtime.UpdatedAt,
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
	err := r.db(ctx).QueryRow(ctx, `
		SELECT session_id, vendor, region_id, COALESCE(sandbox_id, ''), COALESCE(wrapper_url, ''), workspace_volume_id,
			callback_token, COALESCE(vendor_session_id, ''), COALESCE(bootstrap_hash, ''), runtime_generation, active_run_id, sandbox_deleted_at, created_at, updated_at
		FROM managed_agent_session_runtimes
		WHERE sandbox_id = $1
	`, strings.TrimSpace(sandboxID)).Scan(
		&runtime.SessionID, &runtime.Vendor, &runtime.RegionID, &runtime.SandboxID, &runtime.WrapperURL, &runtime.WorkspaceVolumeID,
		&runtime.ControlToken, &runtime.VendorSessionID, &runtime.BootstrapHash, &runtime.RuntimeGeneration, &runtime.ActiveRunID, &runtime.SandboxDeletedAt, &runtime.CreatedAt, &runtime.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRuntimeNotFound
		}
		return nil, fmt.Errorf("query managed-agent runtime by sandbox: %w", err)
	}
	return &runtime, nil
}

func (r *Repository) ListRunningRuntimes(ctx context.Context, limit int) ([]*RuntimeRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.db(ctx).Query(ctx, `
		SELECT r.session_id, r.vendor, r.region_id, COALESCE(r.sandbox_id, ''), COALESCE(r.wrapper_url, ''), r.workspace_volume_id,
			r.callback_token, COALESCE(r.vendor_session_id, ''), COALESCE(r.bootstrap_hash, ''), r.runtime_generation, r.active_run_id, r.sandbox_deleted_at, r.created_at, r.updated_at
		FROM managed_agent_session_runtimes r
		JOIN managed_agent_sessions s ON s.id = r.session_id
		WHERE s.deleted_at IS NULL
			AND s.status = 'running'
			AND r.sandbox_id IS NOT NULL
			AND r.sandbox_id <> ''
		ORDER BY s.updated_at ASC, r.session_id ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list running managed-agent runtimes: %w", err)
	}
	defer rows.Close()
	return scanRuntimeRows(rows)
}

func (r *Repository) ListIdleRuntimesForSandboxDeletion(ctx context.Context, cutoff time.Time, limit int) ([]*RuntimeRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.db(ctx).Query(ctx, `
		SELECT r.session_id, r.vendor, r.region_id, COALESCE(r.sandbox_id, ''), COALESCE(r.wrapper_url, ''), r.workspace_volume_id,
			r.callback_token, COALESCE(r.vendor_session_id, ''), COALESCE(r.bootstrap_hash, ''), r.runtime_generation, r.active_run_id, r.sandbox_deleted_at, r.created_at, r.updated_at
		FROM managed_agent_session_runtimes r
		JOIN managed_agent_sessions s ON s.id = r.session_id
		WHERE s.deleted_at IS NULL
			AND s.status IN ('idle', 'terminated')
			AND s.updated_at < $1
			AND r.sandbox_id IS NOT NULL
			AND r.sandbox_id <> ''
		ORDER BY s.updated_at ASC, r.session_id ASC
		LIMIT $2
	`, cutoff.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list idle managed-agent runtimes for sandbox deletion: %w", err)
	}
	defer rows.Close()
	return scanRuntimeRows(rows)
}

func (r *Repository) ListRuntimesWithSandboxes(ctx context.Context, limit int) ([]*RuntimeRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.db(ctx).Query(ctx, `
		SELECT r.session_id, r.vendor, r.region_id, COALESCE(r.sandbox_id, ''), COALESCE(r.wrapper_url, ''), r.workspace_volume_id,
			r.callback_token, COALESCE(r.vendor_session_id, ''), COALESCE(r.bootstrap_hash, ''), r.runtime_generation, r.active_run_id, r.sandbox_deleted_at, r.created_at, r.updated_at
		FROM managed_agent_session_runtimes r
		JOIN managed_agent_sessions s ON s.id = r.session_id
		WHERE s.deleted_at IS NULL
			AND r.sandbox_id IS NOT NULL
			AND r.sandbox_id <> ''
		ORDER BY r.updated_at ASC, r.session_id ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list managed-agent runtimes with sandboxes: %w", err)
	}
	defer rows.Close()
	return scanRuntimeRows(rows)
}

func (r *Repository) MarkRuntimeSandboxDeleted(ctx context.Context, sessionID, sandboxID string, deletedAt time.Time) error {
	result, err := r.db(ctx).Exec(ctx, `
		UPDATE managed_agent_session_runtimes
		SET sandbox_id = NULL,
			wrapper_url = '',
			callback_token = '',
			bootstrap_hash = '',
			sandbox_deleted_at = $3,
			updated_at = $3
		WHERE session_id = $1
			AND sandbox_id = $2
	`, strings.TrimSpace(sessionID), strings.TrimSpace(sandboxID), deletedAt.UTC())
	if err != nil {
		return fmt.Errorf("mark managed-agent runtime sandbox deleted: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrRuntimeNotFound
	}
	return nil
}

func (r *Repository) UpsertRuntime(ctx context.Context, runtime *RuntimeRecord) error {
	_, err := r.db(ctx).Exec(ctx, `
		INSERT INTO managed_agent_session_runtimes (
			session_id, vendor, region_id, sandbox_id, wrapper_url, workspace_volume_id,
			callback_token, vendor_session_id, bootstrap_hash, runtime_generation, active_run_id, sandbox_deleted_at, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (session_id) DO UPDATE SET
			vendor = EXCLUDED.vendor,
			region_id = EXCLUDED.region_id,
			sandbox_id = EXCLUDED.sandbox_id,
			wrapper_url = EXCLUDED.wrapper_url,
			workspace_volume_id = EXCLUDED.workspace_volume_id,
			callback_token = EXCLUDED.callback_token,
			vendor_session_id = EXCLUDED.vendor_session_id,
			bootstrap_hash = EXCLUDED.bootstrap_hash,
			runtime_generation = EXCLUDED.runtime_generation,
			active_run_id = EXCLUDED.active_run_id,
			sandbox_deleted_at = EXCLUDED.sandbox_deleted_at,
			updated_at = EXCLUDED.updated_at
	`, runtime.SessionID, runtime.Vendor, runtime.RegionID, nullableString(runtime.SandboxID), runtime.WrapperURL, runtime.WorkspaceVolumeID,
		runtime.ControlToken, nullableString(runtime.VendorSessionID), runtime.BootstrapHash, runtime.RuntimeGeneration, runtime.ActiveRunID, runtime.SandboxDeletedAt, runtime.CreatedAt, runtime.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert managed-agent runtime: %w", err)
	}
	return nil
}

func scanRuntimeRows(rows pgx.Rows) ([]*RuntimeRecord, error) {
	var runtimes []*RuntimeRecord
	for rows.Next() {
		var runtime RuntimeRecord
		if err := rows.Scan(
			&runtime.SessionID, &runtime.Vendor, &runtime.RegionID, &runtime.SandboxID, &runtime.WrapperURL, &runtime.WorkspaceVolumeID,
			&runtime.ControlToken, &runtime.VendorSessionID, &runtime.BootstrapHash, &runtime.RuntimeGeneration, &runtime.ActiveRunID, &runtime.SandboxDeletedAt, &runtime.CreatedAt, &runtime.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan managed-agent runtime: %w", err)
		}
		runtimes = append(runtimes, &runtime)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed-agent runtimes: %w", err)
	}
	return runtimes, nil
}

func (r *Repository) CreateRuntimeWebhookJob(ctx context.Context, job *runtimeWebhookJob) (bool, error) {
	if job == nil {
		return false, errors.New("managed-agent webhook job is required")
	}
	payloadJSON, err := json.Marshal(job.Payload)
	if err != nil {
		return false, fmt.Errorf("marshal runtime webhook payload: %w", err)
	}
	createdAt := job.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	updatedAt := job.UpdatedAt.UTC()
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	result, err := r.db(ctx).Exec(ctx, `
		INSERT INTO managed_agent_runtime_webhook_jobs (
			id, session_id, sandbox_id, runtime_generation, run_id, event_type, payload, status, attempts,
			created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, 'pending', 0, $8, $9)
		ON CONFLICT (id) DO NOTHING
	`, strings.TrimSpace(job.ID), strings.TrimSpace(job.SessionID), strings.TrimSpace(job.SandboxID), job.RuntimeGeneration, nullableString(job.RunID), strings.TrimSpace(job.EventType), string(payloadJSON), createdAt, updatedAt)
	if err != nil {
		return false, fmt.Errorf("insert runtime webhook job: %w", err)
	}
	return result.RowsAffected() > 0, nil
}

func (r *Repository) LeaseNextRuntimeWebhookJob(ctx context.Context, owner string, leaseUntil time.Time) (*runtimeWebhookJob, error) {
	if strings.TrimSpace(owner) == "" {
		owner = NewID("whworker")
	}
	var (
		job         runtimeWebhookJob
		payloadJSON []byte
	)
	err := r.db(ctx).QueryRow(ctx, `
		WITH candidate AS (
			SELECT j.id
			FROM managed_agent_runtime_webhook_jobs j
			WHERE (j.status = 'pending' OR (j.status = 'running' AND j.lease_expires_at < NOW()))
				AND NOT EXISTS (
					SELECT 1
					FROM managed_agent_runtime_webhook_jobs older
					WHERE older.session_id = j.session_id
						AND older.status IN ('pending', 'running')
						AND (older.created_at < j.created_at OR (older.created_at = j.created_at AND older.id < j.id))
				)
			ORDER BY j.created_at ASC, j.id ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE managed_agent_runtime_webhook_jobs j
		SET status = 'running', lease_owner = $1, lease_expires_at = $2, attempts = j.attempts + 1,
			last_error = NULL, updated_at = NOW()
		FROM candidate
		WHERE j.id = candidate.id
		RETURNING j.id, j.session_id, j.sandbox_id, j.runtime_generation, COALESCE(j.run_id, ''), j.event_type,
			j.payload, j.status, j.attempts, COALESCE(j.lease_owner, ''), j.lease_expires_at, COALESCE(j.last_error, ''), j.created_at, j.updated_at
	`, strings.TrimSpace(owner), leaseUntil.UTC()).Scan(
		&job.ID, &job.SessionID, &job.SandboxID, &job.RuntimeGeneration, &job.RunID, &job.EventType,
		&payloadJSON, &job.Status, &job.Attempts, &job.LeaseOwner, &job.LeaseExpiresAt, &job.LastError, &job.CreatedAt, &job.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lease runtime webhook job: %w", err)
	}
	if err := json.Unmarshal(payloadJSON, &job.Payload); err != nil {
		return nil, fmt.Errorf("decode runtime webhook payload: %w", err)
	}
	return &job, nil
}

func (r *Repository) CompleteRuntimeWebhookJob(ctx context.Context, jobID string) error {
	_, err := r.db(ctx).Exec(ctx, `
		UPDATE managed_agent_runtime_webhook_jobs
		SET status = 'done', lease_owner = NULL, lease_expires_at = NULL, last_error = NULL, updated_at = NOW()
		WHERE id = $1
	`, strings.TrimSpace(jobID))
	if err != nil {
		return fmt.Errorf("complete runtime webhook job: %w", err)
	}
	return nil
}

func (r *Repository) ReleaseRuntimeWebhookJob(ctx context.Context, jobID string, lastErr error, retry bool) error {
	status := "failed"
	if retry {
		status = "pending"
	}
	message := ""
	if lastErr != nil {
		message = lastErr.Error()
		if len(message) > 2048 {
			message = message[:2048]
		}
	}
	_, err := r.db(ctx).Exec(ctx, `
		UPDATE managed_agent_runtime_webhook_jobs
		SET status = $2, lease_owner = NULL, lease_expires_at = NULL, last_error = $3, updated_at = NOW()
		WHERE id = $1
	`, strings.TrimSpace(jobID), status, nullableString(message))
	if err != nil {
		return fmt.Errorf("release runtime webhook job: %w", err)
	}
	return nil
}

func (r *Repository) CreateRuntimeInputEventBatch(ctx context.Context, batch *runtimeInputEventBatch) error {
	if batch == nil {
		return errors.New("managed-agent runtime input event batch is required")
	}
	eventIDsJSON, err := json.Marshal(batch.EventIDs)
	if err != nil {
		return fmt.Errorf("marshal runtime input event ids: %w", err)
	}
	runtimeEventsJSON, err := json.Marshal(batch.RuntimeInputEvents)
	if err != nil {
		return fmt.Errorf("marshal runtime input events: %w", err)
	}
	createdAt := batch.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	updatedAt := batch.UpdatedAt.UTC()
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	_, err = r.db(ctx).Exec(ctx, `
		INSERT INTO managed_agent_runtime_input_event_batches (id, session_id, event_ids, runtime_input_events, created_at, updated_at)
		VALUES ($1, $2, $3::jsonb, $4::jsonb, $5, $6)
	`, strings.TrimSpace(batch.ID), strings.TrimSpace(batch.SessionID), string(eventIDsJSON), string(runtimeEventsJSON), createdAt, updatedAt)
	if err != nil {
		return fmt.Errorf("insert runtime input event batch: %w", err)
	}
	return nil
}

func (r *Repository) GetNextRuntimeInputEventBatch(ctx context.Context, sessionID string) (*runtimeInputEventBatch, error) {
	var (
		batch             runtimeInputEventBatch
		eventIDsJSON      []byte
		runtimeEventsJSON []byte
	)
	err := r.db(ctx).QueryRow(ctx, `
		SELECT id, session_id, event_ids, runtime_input_events, created_at, updated_at
		FROM managed_agent_runtime_input_event_batches
		WHERE session_id = $1
		ORDER BY created_at ASC, id ASC
		LIMIT 1
	`, strings.TrimSpace(sessionID)).Scan(&batch.ID, &batch.SessionID, &eventIDsJSON, &runtimeEventsJSON, &batch.CreatedAt, &batch.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query runtime input event batch: %w", err)
	}
	if err := json.Unmarshal(eventIDsJSON, &batch.EventIDs); err != nil {
		return nil, fmt.Errorf("decode runtime input event ids: %w", err)
	}
	if err := json.Unmarshal(runtimeEventsJSON, &batch.RuntimeInputEvents); err != nil {
		return nil, fmt.Errorf("decode runtime input events: %w", err)
	}
	return &batch, nil
}

func (r *Repository) MarkEventsProcessed(ctx context.Context, sessionID string, eventIDs []string, processedAt time.Time) error {
	if len(eventIDs) == 0 {
		return nil
	}
	_, err := r.db(ctx).Exec(ctx, `
		UPDATE managed_agent_session_events
		SET payload = jsonb_set(payload, '{processed_at}', to_jsonb($3::text), true)
		WHERE session_id = $1 AND id = ANY($2::text[])
	`, strings.TrimSpace(sessionID), eventIDs, processedAt.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("mark session events processed: %w", err)
	}
	return nil
}

func (r *Repository) DeleteRuntimeInputEventBatch(ctx context.Context, batchID string) error {
	_, err := r.db(ctx).Exec(ctx, `DELETE FROM managed_agent_runtime_input_event_batches WHERE id = $1`, strings.TrimSpace(batchID))
	if err != nil {
		return fmt.Errorf("delete runtime input event batch: %w", err)
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
