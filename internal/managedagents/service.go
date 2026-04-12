package managedagents

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sandbox0sdk "github.com/sandbox0-ai/sdk-go"
	"go.uber.org/zap"
)

// RuntimeManager owns sandbox-backed wrapper orchestration.
type RuntimeManager interface {
	EnsureRuntime(ctx context.Context, principal Principal, credential RequestCredential, session *SessionRecord, engine map[string]any, gatewayBaseURL string) (*RuntimeRecord, error)
	BootstrapSession(ctx context.Context, credential RequestCredential, runtime *RuntimeRecord, req *WrapperSessionBootstrapRequest) error
	StartRun(ctx context.Context, credential RequestCredential, runtime *RuntimeRecord, req *WrapperRunRequest) error
	ResolveActions(ctx context.Context, credential RequestCredential, runtime *RuntimeRecord, req *WrapperResolveActionsRequest) (*WrapperResolveActionsResponse, error)
	InterruptRun(ctx context.Context, credential RequestCredential, runtime *RuntimeRecord, runID string) error
	PauseRuntime(ctx context.Context, credential RequestCredential, runtime *RuntimeRecord) error
	ResumeRuntime(ctx context.Context, credential RequestCredential, runtime *RuntimeRecord) error
	DeleteWrapperSession(ctx context.Context, credential RequestCredential, runtime *RuntimeRecord, sessionID string) error
	DestroyRuntime(ctx context.Context, credential RequestCredential, runtime *RuntimeRecord) error
}

// Service coordinates session truth and runtime orchestration.
type Service struct {
	repo      *Repository
	runtime   RuntimeManager
	logger    *zap.Logger
	fileStore FileStore
}

type ServiceOption func(*Service)

func WithFileStore(store FileStore) ServiceOption {
	return func(s *Service) {
		if store != nil {
			s.fileStore = store
		}
	}
}

// NewService returns a managed-agent service.
func NewService(repo *Repository, runtime RuntimeManager, logger *zap.Logger, opts ...ServiceOption) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	service := &Service{repo: repo, runtime: runtime, logger: logger}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

func (s *Service) CreateSession(ctx context.Context, principal Principal, credential RequestCredential, params CreateSessionParams, gatewayBaseURL string) (*Session, error) {
	if strings.TrimSpace(principal.TeamID) == "" {
		return nil, errors.New("team id is required")
	}
	environmentID, err := normalizeRequiredText(params.EnvironmentID, "environment_id", 128)
	if err != nil {
		return nil, err
	}
	vaultIDs, err := normalizeStringSlice(params.VaultIDs, "vault_ids", 128)
	if err != nil {
		return nil, err
	}
	vendor, agentSnapshot, err := s.resolveSessionAgentReference(ctx, principal, params.Agent)
	if err != nil {
		return nil, err
	}
	if err := ensureClaudeVendor(vendor); err != nil {
		return nil, err
	}
	environment, err := s.repo.GetEnvironment(ctx, principal.TeamID, environmentID)
	if err != nil {
		return nil, err
	}
	if err := ensureEnvironmentUsable(environment); err != nil {
		return nil, err
	}
	title, err := normalizeOptionalText(params.Title, "title", 500)
	if err != nil {
		return nil, err
	}
	metadata, err := normalizeMetadataMap(params.Metadata, 16, 64, 512)
	if err != nil {
		return nil, err
	}
	if err := ValidateManagedSessionMetadata(metadata); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	resources, resourceSecrets, err := s.validateSessionDependencies(ctx, principal, environmentID, vaultIDs, cloneMapSlice(params.Resources))
	if err != nil {
		return nil, err
	}
	artifact, err := s.ensureEnvironmentArtifactRecord(ctx, principal.TeamID, environment)
	if err != nil {
		return nil, err
	}
	record := &SessionRecord{
		ID:                    NewID("sesn"),
		TeamID:                strings.TrimSpace(principal.TeamID),
		CreatedByUserID:       strings.TrimSpace(principal.UserID),
		Vendor:                vendor,
		EnvironmentID:         environmentID,
		EnvironmentArtifactID: artifact.ID,
		WorkingDirectory:      "/workspace",
		Title:                 title,
		Metadata:              metadata,
		Agent:                 agentSnapshot,
		Resources:             resources,
		VaultIDs:              append([]string(nil), vaultIDs...),
		Status:                "idle",
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if err := s.repo.CreateSession(ctx, record, nil); err != nil {
		return nil, err
	}
	for resourceID, secret := range resourceSecrets {
		if err := s.repo.UpsertSessionResourceSecret(ctx, record.ID, resourceID, secret); err != nil {
			return nil, err
		}
	}
	runtime, err := s.runtime.EnsureRuntime(ctx, principal, credential, record, nil, gatewayBaseURL)
	if err != nil {
		s.cleanupFailedCreateRuntime(ctx, credential, record.ID, runtime)
		return nil, err
	}
	if runtime != nil {
		if err := s.runtime.BootstrapSession(ctx, credential, runtime, bootstrapRequestFor(record, nil, runtime)); err != nil {
			s.cleanupFailedCreateRuntime(ctx, credential, record.ID, runtime)
			return nil, err
		}
		s.pauseRuntimeBestEffort(ctx, credential, runtime, "create")
	}
	return record.toAPI(now), nil
}

func (s *Service) cleanupFailedCreateRuntime(ctx context.Context, credential RequestCredential, sessionID string, runtime *RuntimeRecord) {
	if runtime != nil {
		if err := s.runtime.DestroyRuntime(ctx, credential, runtime); err != nil {
			s.logger.Warn("failed to destroy runtime after create failure", zap.Error(err), zap.String("session_id", sessionID))
		}
		if err := s.repo.DeleteRuntime(ctx, sessionID); err != nil {
			s.logger.Warn("failed to delete runtime record after create failure", zap.Error(err), zap.String("session_id", sessionID))
		}
	}
	if err := s.repo.DeleteSessionResourceSecrets(ctx, sessionID); err != nil {
		s.logger.Warn("failed to delete session resource secrets after create failure", zap.Error(err), zap.String("session_id", sessionID))
	}
	if err := s.repo.MarkSessionDeleted(ctx, sessionID, time.Now().UTC()); err != nil {
		s.logger.Warn("failed to mark session deleted after create failure", zap.Error(err), zap.String("session_id", sessionID))
	}
}

func (s *Service) ListSessions(ctx context.Context, principal Principal, opts SessionListOptions) ([]*Session, *string, error) {
	records, nextPage, err := s.repo.ListSessions(ctx, principal.TeamID, opts)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	out := make([]*Session, 0, len(records))
	for _, record := range records {
		out = append(out, record.toAPI(now))
	}
	return out, nextPage, nil
}

func (s *Service) GetSession(ctx context.Context, principal Principal, sessionID string) (*Session, error) {
	record, _, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := ensureSessionAccess(record, principal); err != nil {
		return nil, err
	}
	return record.toAPI(time.Now().UTC()), nil
}

func (s *Service) UpdateSession(ctx context.Context, principal Principal, credential RequestCredential, sessionID string, params UpdateSessionParams) (*Session, error) {
	record, engine, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := ensureSessionAccess(record, principal); err != nil {
		return nil, err
	}
	if params.Title.Set {
		if params.Title.Value != nil {
			title, err := normalizeOptionalText(params.Title.Value, "title", 500)
			if err != nil {
				return nil, err
			}
			record.Title = title
		} else {
			record.Title = nil
		}
	}
	if params.Metadata.Set {
		if err := validateManagedSessionMetadataPatch(record.Metadata, params.Metadata); err != nil {
			return nil, err
		}
		metadata, err := mergeMetadataPatch(record.Metadata, params.Metadata, 16, 64, 512)
		if err != nil {
			return nil, err
		}
		if err := ValidateManagedSessionMetadata(metadata); err != nil {
			return nil, err
		}
		record.Metadata = metadata
	}
	if params.VaultIDs.Set {
		vaultIDs, err := normalizeStringSlice(params.VaultIDs.Values, "vault_ids", 128)
		if err != nil {
			return nil, err
		}
		resources, _, err := s.validateSessionDependencies(ctx, principal, record.EnvironmentID, vaultIDs, cloneMapSlice(record.Resources))
		if err != nil {
			return nil, err
		}
		record.Resources = resources
		record.VaultIDs = append([]string(nil), vaultIDs...)
		if runtime, runtimeErr := s.repo.GetRuntime(ctx, sessionID); runtimeErr == nil {
			if runtime.ActiveRunID != nil {
				return nil, errors.New("vault_ids cannot be updated while a run is active")
			}
			if err := s.runtime.ResumeRuntime(ctx, credential, runtime); err != nil {
				return nil, err
			}
			if err := s.runtime.BootstrapSession(ctx, credential, runtime, bootstrapRequestFor(record, engine, runtime)); err != nil {
				return nil, err
			}
		} else if !errors.Is(runtimeErr, ErrRuntimeNotFound) {
			return nil, runtimeErr
		}
	}
	record.UpdatedAt = time.Now().UTC()
	if err := s.repo.UpdateSession(ctx, record, engine); err != nil {
		return nil, err
	}
	return record.toAPI(time.Now().UTC()), nil
}

func (s *Service) DeleteSession(ctx context.Context, principal Principal, credential RequestCredential, sessionID string) (map[string]any, error) {
	record, _, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := ensureSessionAccess(record, principal); err != nil {
		return nil, err
	}
	var runtime *RuntimeRecord
	if existingRuntime, runtimeErr := s.repo.GetRuntime(ctx, sessionID); runtimeErr == nil {
		runtime = existingRuntime
	} else if !errors.Is(runtimeErr, ErrRuntimeNotFound) {
		return nil, runtimeErr
	}
	if record.Status == "running" || (runtime != nil && runtime.ActiveRunID != nil) {
		return nil, fmt.Errorf("%w: send user.interrupt before deleting session %s", ErrSessionRunning, sessionID)
	}
	if err := s.repo.DeleteSessionResourceSecrets(ctx, sessionID); err != nil {
		return nil, err
	}
	if runtime != nil {
		if err := s.runtime.ResumeRuntime(ctx, credential, runtime); err != nil {
			s.logger.Warn("failed to resume runtime before delete", zap.Error(err), zap.String("session_id", sessionID))
		}
		if err := s.runtime.DeleteWrapperSession(ctx, credential, runtime, sessionID); err != nil {
			s.logger.Warn("failed to delete wrapper session", zap.Error(err), zap.String("session_id", sessionID))
		}
		if err := s.runtime.DestroyRuntime(ctx, credential, runtime); err != nil {
			s.logger.Warn("failed to destroy runtime", zap.Error(err), zap.String("session_id", sessionID))
		}
		if err := s.repo.DeleteRuntime(ctx, sessionID); err != nil {
			s.logger.Warn("failed to delete runtime record", zap.Error(err), zap.String("session_id", sessionID))
		}
	}
	processedAt := time.Now().UTC()
	if err := s.repo.AppendEvents(ctx, sessionID, []map[string]any{stampEvent(map[string]any{"type": "session.deleted"}, processedAt)}); err != nil {
		return nil, err
	}
	if err := s.repo.MarkSessionDeleted(ctx, sessionID, processedAt); err != nil {
		return nil, err
	}
	return deletedObject("session_deleted", sessionID), nil
}

func (s *Service) ListEvents(ctx context.Context, principal Principal, sessionID string, opts EventListOptions) ([]map[string]any, *string, error) {
	record, _, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	if err := ensureSessionAccess(record, principal); err != nil {
		return nil, nil, err
	}
	return s.repo.ListEvents(ctx, sessionID, opts)
}

func (s *Service) SendEvents(ctx context.Context, principal Principal, credential RequestCredential, sessionID string, params SendEventsParams, gatewayBaseURL string) ([]map[string]any, error) {
	record, engine, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := ensureSessionAccess(record, principal); err != nil {
		return nil, err
	}
	if record.ArchivedAt != nil {
		return nil, fmt.Errorf("%w: archived sessions cannot accept new events", ErrSessionArchived)
	}
	processedAt := time.Now().UTC()
	inputEventMaps := inputEventsToMaps(params.Events)
	stampedEvents := stampEvents(inputEventMaps, processedAt)
	if err := validateInputEvents(stampedEvents); err != nil {
		return nil, err
	}
	runtimeInputEvents, err := s.resolveFileBackedInputEvents(ctx, principal, credential, stampedEvents)
	if err != nil {
		return nil, err
	}
	var runtime *RuntimeRecord
	if existingRuntime, runtimeErr := s.repo.GetRuntime(ctx, sessionID); runtimeErr == nil {
		runtime = existingRuntime
	} else if !errors.Is(runtimeErr, ErrRuntimeNotFound) {
		return nil, runtimeErr
	}
	requiredActionIDs, err := s.latestRequiredActionIDs(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := s.repo.AppendEvents(ctx, sessionID, stampedEvents); err != nil {
		return nil, err
	}
	if runtime != nil && containsInterruptEvent(stampedEvents) && runtime.ActiveRunID != nil {
		if err := s.runtime.InterruptRun(ctx, credential, runtime, *runtime.ActiveRunID); err != nil {
			return nil, err
		}
		idleEvent := stampEvent(map[string]any{
			"type":        "session.status_idle",
			"stop_reason": map[string]any{"type": "end_turn"},
		}, processedAt)
		runtime.ActiveRunID = nil
		runtime.UpdatedAt = processedAt
		if err := s.repo.UpsertRuntime(ctx, runtime); err != nil {
			return nil, err
		}
		if err := s.repo.AppendEvents(ctx, sessionID, []map[string]any{idleEvent}); err != nil {
			return nil, err
		}
		record = applySessionBatch(record, processedAt, Usage{}, []map[string]any{idleEvent})
		if err := s.repo.UpdateSession(ctx, record, engine); err != nil {
			return nil, err
		}
		if containsOnlyInterruptEvents(stampedEvents) {
			s.pauseRuntimeBestEffort(ctx, credential, runtime, "interrupt")
			return stampedEvents, nil
		}
	}
	if len(requiredActionIDs) > 0 && containsOnlyActionResolutionEvents(stampedEvents) {
		if runtime == nil || runtime.ActiveRunID == nil {
			return nil, errors.New("no pending action to resolve")
		}
		if err := ensureResolvesRequiredActions(requiredActionIDs, stampedEvents); err != nil {
			return nil, err
		}
		if err := s.runtime.ResumeRuntime(ctx, credential, runtime); err != nil {
			return nil, err
		}
		if err := s.runtime.BootstrapSession(ctx, credential, runtime, bootstrapRequestFor(record, engine, runtime)); err != nil {
			return nil, err
		}
		resolution, err := s.runtime.ResolveActions(ctx, credential, runtime, &WrapperResolveActionsRequest{SessionID: sessionID, Events: inputEventsFromMaps(runtimeInputEvents)})
		if err != nil {
			return nil, err
		}
		var nextEvent map[string]any
		if resolution != nil && len(resolution.RemainingActionIDs) > 0 {
			nextEvent = stampEvent(map[string]any{
				"type": "session.status_idle",
				"stop_reason": map[string]any{
					"type":      "requires_action",
					"event_ids": stringSliceToAny(resolution.RemainingActionIDs),
				},
			}, processedAt)
		} else {
			nextEvent = stampEvent(map[string]any{"type": "session.status_running"}, processedAt)
		}
		if resolution != nil && resolution.ResumeRequired && len(resolution.RemainingActionIDs) == 0 {
			runID := NewID("srun")
			runtime.ActiveRunID = &runID
			runtime.UpdatedAt = processedAt
			if err := s.repo.UpsertRuntime(ctx, runtime); err != nil {
				return nil, err
			}
			if err := s.repo.AppendEvents(ctx, sessionID, []map[string]any{nextEvent}); err != nil {
				return nil, err
			}
			record = applySessionBatch(record, processedAt, Usage{}, []map[string]any{nextEvent})
			if err := s.repo.UpdateSession(ctx, record, engine); err != nil {
				return nil, err
			}
			if err := s.runtime.StartRun(ctx, credential, runtime, &WrapperRunRequest{SessionID: sessionID, RunID: runID, InputEvents: inputEventsFromMaps(runtimeInputEvents)}); err != nil {
				failureEvents := []map[string]any{
					stampEvent(map[string]any{"type": "session.error", "error": map[string]any{"type": "unknown_error", "message": err.Error()}}, time.Now().UTC()),
					stampEvent(map[string]any{"type": "session.status_terminated"}, time.Now().UTC()),
				}
				_ = s.repo.AppendEvents(ctx, sessionID, failureEvents)
				record = applySessionBatch(record, time.Now().UTC(), Usage{}, failureEvents)
				_ = s.repo.UpdateSession(ctx, record, engine)
				return nil, err
			}
			return stampedEvents, nil
		}
		if err := s.repo.AppendEvents(ctx, sessionID, []map[string]any{nextEvent}); err != nil {
			return nil, err
		}
		record = applySessionBatch(record, processedAt, Usage{}, []map[string]any{nextEvent})
		if err := s.repo.UpdateSession(ctx, record, engine); err != nil {
			return nil, err
		}
		if stringValue(nextEvent["type"]) == "session.status_idle" {
			s.pauseRuntimeBestEffort(ctx, credential, runtime, "requires_action")
		}
		return stampedEvents, nil
	}
	hadRuntime := runtime != nil
	runtime, err = s.runtime.EnsureRuntime(ctx, principal, credential, record, engine, gatewayBaseURL)
	if err != nil {
		return nil, err
	}
	if hadRuntime {
		if err := s.runtime.ResumeRuntime(ctx, credential, runtime); err != nil {
			return nil, err
		}
	}
	bootstrapReq := bootstrapRequestFor(record, engine, runtime)
	if err := s.runtime.BootstrapSession(ctx, credential, runtime, bootstrapReq); err != nil {
		return nil, err
	}
	runID := NewID("srun")
	runtime.ActiveRunID = &runID
	runtime.UpdatedAt = processedAt
	if err := s.repo.UpsertRuntime(ctx, runtime); err != nil {
		return nil, err
	}
	runningEvent := stampEvent(map[string]any{"type": "session.status_running"}, processedAt)
	if err := s.repo.AppendEvents(ctx, sessionID, []map[string]any{runningEvent}); err != nil {
		return nil, err
	}
	record = applySessionBatch(record, processedAt, Usage{}, []map[string]any{runningEvent})
	if err := s.repo.UpdateSession(ctx, record, engine); err != nil {
		return nil, err
	}
	if err := s.runtime.StartRun(ctx, credential, runtime, &WrapperRunRequest{SessionID: sessionID, RunID: runID, InputEvents: inputEventsFromMaps(runtimeInputEvents)}); err != nil {
		failureEvents := []map[string]any{
			stampEvent(map[string]any{"type": "session.error", "error": map[string]any{"type": "unknown_error", "message": err.Error()}}, time.Now().UTC()),
			stampEvent(map[string]any{"type": "session.status_terminated"}, time.Now().UTC()),
		}
		_ = s.repo.AppendEvents(ctx, sessionID, failureEvents)
		record = applySessionBatch(record, time.Now().UTC(), Usage{}, failureEvents)
		_ = s.repo.UpdateSession(ctx, record, engine)
		return nil, err
	}
	return stampedEvents, nil
}

func (s *Service) HandleSandboxWebhook(ctx context.Context, rawBody []byte, signature string) error {
	if len(bytesTrimSpace(rawBody)) == 0 {
		return errors.New("webhook body is required")
	}
	var envelope sandboxWebhookEnvelope
	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		return errors.New("invalid webhook body")
	}
	runtime, err := s.repo.GetRuntimeBySandboxID(ctx, envelope.SandboxID)
	if err != nil {
		return err
	}
	if subtleTrim(runtime.ControlToken) == "" || !sandbox0sdk.VerifyWebhookSignature(runtime.ControlToken, rawBody, signature) {
		return errors.New("invalid webhook signature")
	}
	if strings.TrimSpace(envelope.EventType) != "agent.event" {
		return nil
	}
	var payload RuntimeCallbackPayload
	if len(envelope.Payload) == 0 {
		return errors.New("webhook payload is required")
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return errors.New("invalid webhook payload")
	}
	if trimmedSessionID := strings.TrimSpace(payload.SessionID); trimmedSessionID != "" && trimmedSessionID != runtime.SessionID {
		return errors.New("runtime payload session_id mismatch")
	}
	return s.applyRuntimePayload(ctx, runtime, payload)
}

func (s *Service) applyRuntimePayload(ctx context.Context, runtime *RuntimeRecord, payload RuntimeCallbackPayload) error {
	record, engine, err := s.repo.GetSession(ctx, runtime.SessionID)
	if err != nil {
		return err
	}
	processedAt := time.Now().UTC()
	stampedEvents := stampEvents(sessionEventsToMaps(payload.Events), processedAt)
	if err := s.repo.AppendEvents(ctx, runtime.SessionID, stampedEvents); err != nil {
		return err
	}
	if strings.TrimSpace(payload.VendorSessionID) != "" {
		runtime.VendorSessionID = strings.TrimSpace(payload.VendorSessionID)
	}
	shouldPause := false
	for _, event := range stampedEvents {
		switch stringValue(event["type"]) {
		case "session.status_idle":
			shouldPause = true
			if stopReasonType(event) != "requires_action" {
				runtime.ActiveRunID = nil
			}
		case "session.status_terminated", "session.status_rescheduled":
			runtime.ActiveRunID = nil
		}
	}
	runtime.UpdatedAt = processedAt
	if err := s.repo.UpsertRuntime(ctx, runtime); err != nil {
		return err
	}
	record = applySessionBatch(record, processedAt, payload.UsageDelta, stampedEvents)
	if err := s.repo.UpdateSession(ctx, record, engine); err != nil {
		return err
	}
	if shouldPause {
		s.pauseRuntimeInBackgroundBestEffort(ctx, runtime, "runtime_idle")
	}
	return nil
}

func (s *Service) pauseRuntimeBestEffort(ctx context.Context, credential RequestCredential, runtime *RuntimeRecord, reason string) {
	if runtime == nil {
		return
	}
	if err := s.runtime.PauseRuntime(ctx, credential, runtime); err != nil {
		s.logger.Warn("failed to pause managed-agent runtime", zap.Error(err), zap.String("session_id", runtime.SessionID), zap.String("reason", reason))
	}
}

type backgroundRuntimePauser interface {
	CanPauseRuntimeInBackground() bool
	PauseRuntimeInBackground(ctx context.Context, runtime *RuntimeRecord) error
}

func (s *Service) pauseRuntimeInBackgroundBestEffort(ctx context.Context, runtime *RuntimeRecord, reason string) {
	pauser, ok := s.runtime.(backgroundRuntimePauser)
	if !ok || !pauser.CanPauseRuntimeInBackground() || runtime == nil {
		return
	}
	if err := pauser.PauseRuntimeInBackground(ctx, runtime); err != nil {
		s.logger.Warn("failed to pause managed-agent runtime in background", zap.Error(err), zap.String("session_id", runtime.SessionID), zap.String("reason", reason))
	}
}

func ensureSessionAccess(record *SessionRecord, principal Principal) error {
	if record == nil {
		return ErrSessionNotFound
	}
	if strings.TrimSpace(principal.TeamID) == "" || strings.TrimSpace(principal.TeamID) != strings.TrimSpace(record.TeamID) {
		return errors.New("forbidden")
	}
	return nil
}

func bootstrapRequestFor(record *SessionRecord, engine map[string]any, runtime *RuntimeRecord) *WrapperSessionBootstrapRequest {
	req := &WrapperSessionBootstrapRequest{
		SessionID:        record.ID,
		Vendor:           record.Vendor,
		VendorSessionID:  runtime.VendorSessionID,
		WorkingDirectory: record.WorkingDirectory,
		EnvironmentID:    record.EnvironmentID,
		Agent:            cloneMap(record.Agent),
		Resources:        cloneMapSlice(record.Resources),
		VaultIDs:         append([]string(nil), record.VaultIDs...),
		SkillNames:       []string{},
		Engine:           cloneMap(engine),
	}
	return req
}

type sandboxWebhookEnvelope struct {
	EventType string          `json:"event_type"`
	SandboxID string          `json:"sandbox_id"`
	Payload   json.RawMessage `json:"payload"`
}

func bytesTrimSpace(value []byte) []byte {
	return []byte(strings.TrimSpace(string(value)))
}

func stampEvents(events []map[string]any, when time.Time) []map[string]any {
	if len(events) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(events))
	for _, event := range events {
		out = append(out, stampEvent(event, when))
	}
	return out
}

func stampEvent(event map[string]any, when time.Time) map[string]any {
	cloned := cloneMap(event)
	if strings.TrimSpace(stringValue(cloned["id"])) == "" {
		cloned["id"] = NewID("evt")
	}
	if strings.TrimSpace(stringValue(cloned["processed_at"])) == "" {
		cloned["processed_at"] = when.UTC().Format(time.RFC3339)
	}
	return cloned
}

func (s *Service) latestRequiredActionIDs(ctx context.Context, sessionID string) ([]string, error) {
	events, _, err := s.repo.ListEvents(ctx, sessionID, EventListOptions{Limit: 10, Order: "desc"})
	if err != nil {
		return nil, err
	}
	for _, event := range events {
		if stringValue(event["type"]) != "session.status_idle" {
			continue
		}
		stopReason, _ := event["stop_reason"].(map[string]any)
		if stringValue(stopReason["type"]) != "requires_action" {
			return nil, nil
		}
		ids := make([]string, 0)
		for _, raw := range anySlice(stopReason["event_ids"]) {
			id := strings.TrimSpace(stringValue(raw))
			if id != "" {
				ids = append(ids, id)
			}
		}
		return ids, nil
	}
	return nil, nil
}

func validateInputEvents(events []map[string]any) error {
	for _, event := range events {
		switch stringValue(event["type"]) {
		case "user.message":
			if err := validateAllowedFields(event, []string{"type", "id", "content", "processed_at"}); err != nil {
				return err
			}
			content, ok := event["content"].([]any)
			if !ok {
				return errors.New("user.message content is required")
			}
			if err := validateUserContentBlocks(content); err != nil {
				return err
			}
		case "user.interrupt":
			if err := validateAllowedFields(event, []string{"type", "id", "processed_at"}); err != nil {
				return err
			}
		case "user.tool_confirmation":
			if err := validateAllowedFields(event, []string{"type", "id", "tool_use_id", "result", "deny_message", "processed_at"}); err != nil {
				return err
			}
			toolUseID, err := normalizeRequiredText(stringValue(event["tool_use_id"]), "user.tool_confirmation tool_use_id", 128)
			if err != nil {
				return err
			}
			event["tool_use_id"] = toolUseID
			result := strings.TrimSpace(stringValue(event["result"]))
			if result != "allow" && result != "deny" {
				return errors.New("user.tool_confirmation result must be allow or deny")
			}
			denyMessage, hasDenyMessage := event["deny_message"]
			if hasDenyMessage && denyMessage != nil {
				messageValue := stringValue(denyMessage)
				if message, err := normalizeOptionalText(&messageValue, "user.tool_confirmation deny_message", 10000); err != nil {
					return err
				} else {
					event["deny_message"] = message
				}
			}
			if result != "deny" && hasDenyMessage && denyMessage != nil && strings.TrimSpace(stringValue(denyMessage)) != "" {
				return errors.New("user.tool_confirmation deny_message is only allowed when result is deny")
			}
		case "user.custom_tool_result":
			if err := validateAllowedFields(event, []string{"type", "id", "custom_tool_use_id", "content", "is_error", "processed_at"}); err != nil {
				return err
			}
			customToolUseID, err := normalizeRequiredText(stringValue(event["custom_tool_use_id"]), "user.custom_tool_result custom_tool_use_id", 128)
			if err != nil {
				return err
			}
			event["custom_tool_use_id"] = customToolUseID
			if raw, ok := event["content"]; ok {
				content, ok := raw.([]any)
				if !ok {
					return errors.New("user.custom_tool_result content must be an array")
				}
				if err := validateToolResultContentBlocks(content); err != nil {
					return err
				}
			}
			if raw, ok := event["is_error"]; ok && raw != nil {
				if _, ok := raw.(bool); !ok {
					return errors.New("user.custom_tool_result is_error must be a boolean")
				}
			}
		default:
			return errors.New("invalid event type")
		}
	}
	return nil
}

func (s *Service) resolveFileBackedInputEvents(ctx context.Context, principal Principal, credential RequestCredential, events []map[string]any) ([]map[string]any, error) {
	runtimeEvents := cloneDeepMapSlice(events)
	for _, event := range runtimeEvents {
		if stringValue(event["type"]) != "user.message" {
			continue
		}
		for _, rawBlock := range anySlice(event["content"]) {
			block := mapValue(rawBlock)
			blockType := stringValue(block["type"])
			if blockType != "image" && blockType != "document" {
				continue
			}
			source := mapValue(block["source"])
			if stringValue(source["type"]) != "file" {
				continue
			}
			fileID := strings.TrimSpace(stringValue(source["file_id"]))
			record, err := s.repo.GetFile(ctx, principal.TeamID, fileID)
			if err != nil {
				return nil, err
			}
			if blockType == "image" && !isSupportedImageMimeType(record.MimeType) {
				return nil, fmt.Errorf("file %s has MIME type %q, which is not supported for image content", fileID, record.MimeType)
			}
			if blockType == "document" && !isSupportedDocumentMimeType(record.MimeType) {
				return nil, fmt.Errorf("file %s has MIME type %q, which is not supported for document content", fileID, record.MimeType)
			}
			content, err := s.readFileContent(ctx, credential, record)
			if err != nil {
				return nil, err
			}
			source["type"] = "base64"
			source["media_type"] = record.MimeType
			source["data"] = base64.StdEncoding.EncodeToString(content)
			delete(source, "file_id")
			block["source"] = source
		}
	}
	return runtimeEvents, nil
}

func isSupportedImageMimeType(mimeType string) bool {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func isSupportedDocumentMimeType(mimeType string) bool {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "application/pdf", "text/plain", "text/markdown", "text/csv", "application/json":
		return true
	default:
		return false
	}
}

func cloneDeepMapSlice(items []map[string]any) []map[string]any {
	if len(items) == 0 {
		return nil
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return cloneMapSlice(items)
	}
	var out []map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return cloneMapSlice(items)
	}
	return out
}

func validateUserContentBlocks(blocks []any) error {
	for _, raw := range blocks {
		if err := validateContentBlock(raw); err != nil {
			return err
		}
	}
	return nil
}

func validateToolResultContentBlocks(blocks []any) error {
	for _, raw := range blocks {
		if err := validateContentBlock(raw); err != nil {
			return err
		}
	}
	return nil
}

func validateContentBlock(raw any) error {
	block, ok := raw.(map[string]any)
	if !ok {
		return errors.New("content blocks must be objects")
	}
	switch stringValue(block["type"]) {
	case "text":
		if err := validateAllowedFields(block, []string{"type", "text"}); err != nil {
			return err
		}
		if _, ok := block["text"].(string); !ok {
			return errors.New("text content block requires text")
		}
	case "image":
		if err := validateAllowedFields(block, []string{"type", "source"}); err != nil {
			return err
		}
		return validateImageSource(block["source"])
	case "document":
		if err := validateAllowedFields(block, []string{"type", "source", "title", "context"}); err != nil {
			return err
		}
		return validateDocumentSource(block["source"])
	default:
		return errors.New("content block type is invalid")
	}
	return nil
}

func validateImageSource(raw any) error {
	source, ok := raw.(map[string]any)
	if !ok {
		return errors.New("image source must be an object")
	}
	switch stringValue(source["type"]) {
	case "base64":
		if err := validateAllowedFields(source, []string{"type", "media_type", "data"}); err != nil {
			return err
		}
		if strings.TrimSpace(stringValue(source["media_type"])) == "" || strings.TrimSpace(stringValue(source["data"])) == "" {
			return errors.New("base64 image source requires media_type and data")
		}
	case "url":
		if err := validateAllowedFields(source, []string{"type", "url"}); err != nil {
			return err
		}
		if strings.TrimSpace(stringValue(source["url"])) == "" {
			return errors.New("url image source requires url")
		}
	case "file":
		if err := validateAllowedFields(source, []string{"type", "file_id"}); err != nil {
			return err
		}
		if strings.TrimSpace(stringValue(source["file_id"])) == "" {
			return errors.New("file image source requires file_id")
		}
	default:
		return errors.New("image source type is invalid")
	}
	return nil
}

func validateDocumentSource(raw any) error {
	source, ok := raw.(map[string]any)
	if !ok {
		return errors.New("document source must be an object")
	}
	switch stringValue(source["type"]) {
	case "base64":
		if err := validateAllowedFields(source, []string{"type", "media_type", "data"}); err != nil {
			return err
		}
		if strings.TrimSpace(stringValue(source["media_type"])) == "" || strings.TrimSpace(stringValue(source["data"])) == "" {
			return errors.New("base64 document source requires media_type and data")
		}
	case "text":
		if err := validateAllowedFields(source, []string{"type", "media_type", "data"}); err != nil {
			return err
		}
		if stringValue(source["media_type"]) != "text/plain" || strings.TrimSpace(stringValue(source["data"])) == "" {
			return errors.New("text document source requires media_type text/plain and data")
		}
	case "url":
		if err := validateAllowedFields(source, []string{"type", "url"}); err != nil {
			return err
		}
		if strings.TrimSpace(stringValue(source["url"])) == "" {
			return errors.New("url document source requires url")
		}
	case "file":
		if err := validateAllowedFields(source, []string{"type", "file_id"}); err != nil {
			return err
		}
		if strings.TrimSpace(stringValue(source["file_id"])) == "" {
			return errors.New("file document source requires file_id")
		}
	default:
		return errors.New("document source type is invalid")
	}
	return nil
}

func containsInterruptEvent(events []map[string]any) bool {
	for _, event := range events {
		if stringValue(event["type"]) == "user.interrupt" {
			return true
		}
	}
	return false
}

func containsOnlyInterruptEvents(events []map[string]any) bool {
	if len(events) == 0 {
		return false
	}
	for _, event := range events {
		if stringValue(event["type"]) != "user.interrupt" {
			return false
		}
	}
	return true
}

func containsOnlyActionResolutionEvents(events []map[string]any) bool {
	if len(events) == 0 {
		return false
	}
	for _, event := range events {
		typeName := stringValue(event["type"])
		if typeName != "user.tool_confirmation" && typeName != "user.custom_tool_result" {
			return false
		}
	}
	return true
}

func ensureResolvesRequiredActions(requiredIDs []string, events []map[string]any) error {
	if len(requiredIDs) == 0 {
		return errors.New("no pending action to resolve")
	}
	required := make(map[string]struct{}, len(requiredIDs))
	for _, id := range requiredIDs {
		required[strings.TrimSpace(id)] = struct{}{}
	}
	seen := make(map[string]struct{}, len(events))
	for _, event := range events {
		if toolUseID := strings.TrimSpace(stringValue(event["tool_use_id"])); toolUseID != "" {
			if _, ok := required[toolUseID]; !ok {
				return errors.New("input events contain an unknown pending action id")
			}
			seen[toolUseID] = struct{}{}
		}
		if customToolUseID := strings.TrimSpace(stringValue(event["custom_tool_use_id"])); customToolUseID != "" {
			if _, ok := required[customToolUseID]; !ok {
				return errors.New("input events contain an unknown pending action id")
			}
			seen[customToolUseID] = struct{}{}
		}
	}
	for _, id := range requiredIDs {
		if _, ok := seen[id]; ok {
			return nil
		}
	}
	return errors.New("input events do not resolve the pending action")
}

func stringSliceToAny(values []string) []any {
	if len(values) == 0 {
		return []any{}
	}
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func stopReasonType(event map[string]any) string {
	stopReason, _ := event["stop_reason"].(map[string]any)
	return stringValue(stopReason["type"])
}

func anySlice(value any) []any {
	slice, _ := value.([]any)
	return slice
}

func applySessionBatch(record *SessionRecord, processedAt time.Time, usage Usage, events []map[string]any) *SessionRecord {
	mergeUsageTotals(&record.Usage, usage)
	for _, event := range events {
		switch stringValue(event["type"]) {
		case "session.status_running":
			if record.Status != "running" {
				startedAt := processedAt
				record.LastStatusStartedAt = &startedAt
			}
			record.Status = "running"
		case "session.status_idle":
			record = stopRunning(record, processedAt)
			record.Status = "idle"
		case "session.status_rescheduled":
			record = stopRunning(record, processedAt)
			record.Status = "rescheduling"
		case "session.status_terminated":
			record = stopRunning(record, processedAt)
			record.Status = "terminated"
		case "session.archived":
			archivedAt := processedAt
			record.ArchivedAt = &archivedAt
		}
	}
	record.UpdatedAt = processedAt
	return record
}

func mergeUsageTotals(total *Usage, delta Usage) {
	if total == nil {
		return
	}
	total.InputTokens += delta.InputTokens
	total.OutputTokens += delta.OutputTokens
	total.CacheReadInputTokens += delta.CacheReadInputTokens
	if delta.CacheCreation == nil {
		return
	}
	if total.CacheCreation == nil {
		total.CacheCreation = &CacheCreationUsage{}
	}
	total.CacheCreation.Ephemeral1HInputTokens += delta.CacheCreation.Ephemeral1HInputTokens
	total.CacheCreation.Ephemeral5MInputTokens += delta.CacheCreation.Ephemeral5MInputTokens
	if total.CacheCreation.Ephemeral1HInputTokens == 0 && total.CacheCreation.Ephemeral5MInputTokens == 0 {
		total.CacheCreation = nil
	}
}

func stopRunning(record *SessionRecord, processedAt time.Time) *SessionRecord {
	if record.Status == "running" && record.LastStatusStartedAt != nil {
		record.StatsActiveSeconds += processedAt.Sub(record.LastStatusStartedAt.UTC()).Seconds()
		record.LastStatusStartedAt = nil
	}
	return record
}

func subtleTrim(value string) string {
	return strings.TrimSpace(value)
}
