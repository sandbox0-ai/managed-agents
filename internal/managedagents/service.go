package managedagents

import (
	"context"
	"encoding/json"
	"errors"
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
	DeleteWrapperSession(ctx context.Context, credential RequestCredential, runtime *RuntimeRecord, sessionID string) error
	DestroyRuntime(ctx context.Context, credential RequestCredential, runtime *RuntimeRecord) error
}

// Service coordinates session truth and runtime orchestration.
type Service struct {
	repo            *Repository
	runtime         RuntimeManager
	logger          *zap.Logger
	anthropicSkills AnthropicSkillCatalog
}

type ServiceOption func(*Service)

func WithAnthropicSkillCatalog(catalog AnthropicSkillCatalog) ServiceOption {
	return func(s *Service) {
		if catalog != nil {
			s.anthropicSkills = catalog
		}
	}
}

// NewService returns a managed-agent service.
func NewService(repo *Repository, runtime RuntimeManager, logger *zap.Logger, opts ...ServiceOption) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	service := &Service{repo: repo, runtime: runtime, logger: logger, anthropicSkills: defaultAnthropicSkillRegistry()}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

func (s *Service) CreateSession(ctx context.Context, principal Principal, params CreateSessionParams) (*Session, error) {
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
	now := time.Now().UTC()
	resources, resourceSecrets, err := s.validateSessionDependencies(ctx, principal, environmentID, vaultIDs, cloneMapSlice(params.Resources))
	if err != nil {
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
	record := &SessionRecord{
		ID:               NewID("sesn"),
		TeamID:           strings.TrimSpace(principal.TeamID),
		CreatedByUserID:  strings.TrimSpace(principal.UserID),
		Vendor:           vendor,
		EnvironmentID:    environmentID,
		WorkingDirectory: "/workspace",
		Title:            title,
		Metadata:         metadata,
		Agent:            agentSnapshot,
		Resources:        resources,
		VaultIDs:         append([]string(nil), vaultIDs...),
		Status:           "idle",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := s.repo.CreateSession(ctx, record, nil); err != nil {
		return nil, err
	}
	for resourceID, secret := range resourceSecrets {
		if err := s.repo.UpsertSessionResourceSecret(ctx, record.ID, resourceID, secret); err != nil {
			return nil, err
		}
	}
	return record.toAPI(now), nil
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

func (s *Service) UpdateSession(ctx context.Context, principal Principal, sessionID string, params UpdateSessionParams) (*Session, error) {
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
		metadata, err := mergeMetadataPatch(record.Metadata, params.Metadata, 16, 64, 512)
		if err != nil {
			return nil, err
		}
		record.Metadata = metadata
	}
	if params.VaultIDs.Set {
		return nil, errors.New("vault_ids is not yet supported")
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
	if runtime, err := s.repo.GetRuntime(ctx, sessionID); err == nil {
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
	processedAt := time.Now().UTC()
	inputEventMaps := inputEventsToMaps(params.Events)
	stampedEvents := stampEvents(inputEventMaps, processedAt)
	if err := validateInputEvents(stampedEvents); err != nil {
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
		if containsOnlyInterruptEvents(stampedEvents) {
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
		if err := s.runtime.BootstrapSession(ctx, credential, runtime, bootstrapRequestFor(record, engine, runtime)); err != nil {
			return nil, err
		}
		resolution, err := s.runtime.ResolveActions(ctx, credential, runtime, &WrapperResolveActionsRequest{SessionID: sessionID, Events: inputEventsFromMaps(stampedEvents)})
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
			if err := s.runtime.StartRun(ctx, credential, runtime, &WrapperRunRequest{SessionID: sessionID, RunID: runID, InputEvents: inputEventsFromMaps(stampedEvents)}); err != nil {
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
		return stampedEvents, nil
	}
	runtime, err = s.runtime.EnsureRuntime(ctx, principal, credential, record, engine, gatewayBaseURL)
	if err != nil {
		return nil, err
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
	if err := s.runtime.StartRun(ctx, credential, runtime, &WrapperRunRequest{SessionID: sessionID, RunID: runID, InputEvents: inputEventsFromMaps(stampedEvents)}); err != nil {
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
	for _, event := range stampedEvents {
		switch stringValue(event["type"]) {
		case "session.status_idle":
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
	return s.repo.UpdateSession(ctx, record, engine)
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
