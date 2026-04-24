package managedagents

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	DeleteWrapperSession(ctx context.Context, credential RequestCredential, runtime *RuntimeRecord, sessionID string) error
	DestroyRuntime(ctx context.Context, credential RequestCredential, runtime *RuntimeRecord) error
}

// EnvironmentArtifactPrebuilder optionally moves environment package builds off the session cold-start path.
type EnvironmentArtifactPrebuilder interface {
	PrebuildEnvironmentArtifact(ctx context.Context, credential RequestCredential, teamID string, environment *Environment) error
}

// EnvironmentArtifactCleaner deletes sandbox0 resources owned by environment artifacts.
type EnvironmentArtifactCleaner interface {
	CleanupEnvironmentArtifacts(ctx context.Context, credential RequestCredential, teamID, environmentID string) error
}

const (
	runtimeWebhookLeaseDuration = 2 * time.Minute
	runtimeWebhookPollInterval  = 100 * time.Millisecond
	runtimeWebhookMaxAttempts   = 5
	runtimeBootstrapFreshness   = 2 * time.Minute
	sessionBootstrapTimeout     = 2 * time.Minute
)

// Service coordinates session truth and runtime orchestration.
type Service struct {
	repo          *Repository
	runtime       RuntimeManager
	logger        *zap.Logger
	assetStore    AssetStore
	observability *Observability
}

type ServiceOption func(*Service)

func WithAssetStore(store AssetStore) ServiceOption {
	return func(s *Service) {
		if store != nil {
			s.assetStore = store
		}
	}
}

func WithObservability(observability *Observability) ServiceOption {
	return func(s *Service) {
		s.observability = observability
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

func (s *Service) CreateSession(ctx context.Context, principal Principal, credential RequestCredential, params CreateSessionParams, gatewayBaseURL string) (created *Session, err error) {
	if strings.TrimSpace(principal.TeamID) == "" {
		return nil, errors.New("team id is required")
	}
	operationStarted := time.Now()
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
	if err := ensureSupportedVendor(vendor); err != nil {
		return nil, err
	}
	ctx, op := s.observability.StartOperation(ctx, "session_create", vendor,
		zap.String("team_id", principal.TeamID),
		zap.String("environment_id", environmentID),
	)
	defer func() { op.End(err) }()
	op.ObservePhase("request_validate_and_resolve_agent", time.Since(operationStarted), nil)
	phaseStarted := time.Now()
	environment, err := s.repo.GetEnvironment(ctx, principal.TeamID, environmentID)
	if err != nil {
		op.ObservePhase("load_environment", time.Since(phaseStarted), err)
		return nil, err
	}
	if err := ensureEnvironmentUsable(environment); err != nil {
		op.ObservePhase("load_environment", time.Since(phaseStarted), err)
		return nil, err
	}
	op.ObservePhase("load_environment", time.Since(phaseStarted), nil)
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
	phaseStarted = time.Now()
	resources, resourceSecrets, err := s.validateSessionDependencies(ctx, principal, environmentID, vaultIDs, cloneMapSlice(params.Resources))
	if err != nil {
		op.ObservePhase("validate_dependencies", time.Since(phaseStarted), err,
			zap.Int("vault_count", len(vaultIDs)),
			zap.Int("resource_count", len(params.Resources)),
		)
		return nil, err
	}
	op.ObservePhase("validate_dependencies", time.Since(phaseStarted), nil,
		zap.Int("vault_count", len(vaultIDs)),
		zap.Int("resource_count", len(params.Resources)),
	)
	phaseStarted = time.Now()
	vendor, err = s.resolveSessionVendorFromVaults(ctx, principal, vendor, vaultIDs)
	if err != nil {
		op.ObservePhase("resolve_vendor_from_vaults", time.Since(phaseStarted), err)
		return nil, err
	}
	op.ObservePhase("resolve_vendor_from_vaults", time.Since(phaseStarted), nil)
	phaseStarted = time.Now()
	artifact, err := s.ensureEnvironmentArtifactRecord(ctx, principal.TeamID, environment)
	if err != nil {
		op.ObservePhase("ensure_environment_artifact_record", time.Since(phaseStarted), err)
		return nil, err
	}
	op.ObservePhase("ensure_environment_artifact_record", time.Since(phaseStarted), nil,
		zap.String("environment_artifact_id", artifact.ID),
		zap.String("environment_artifact_status", artifact.Status),
	)
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
	op.AddFields(
		zap.String("session_id", record.ID),
		zap.Int("vault_count", len(vaultIDs)),
		zap.Int("resource_count", len(resources)),
	)
	lockStarted := time.Now()
	if err := s.repo.WithSessionLock(ctx, record.ID, func(ctx context.Context) error {
		return s.repo.WithTransaction(ctx, func(ctx context.Context) error {
			op.ObservePhase("acquire_session_lock", time.Since(lockStarted), nil,
				zap.String("session_id", record.ID),
			)
			phaseStarted := time.Now()
			if err := s.repo.CreateSession(ctx, record, nil); err != nil {
				op.ObservePhase("create_session_record", time.Since(phaseStarted), err,
					zap.String("session_id", record.ID),
				)
				return err
			}
			op.ObservePhase("create_session_record", time.Since(phaseStarted), nil,
				zap.String("session_id", record.ID),
			)
			phaseStarted = time.Now()
			for resourceID, secret := range resourceSecrets {
				if err := s.repo.UpsertSessionResourceSecret(ctx, record.ID, resourceID, secret); err != nil {
					op.ObservePhase("persist_resource_secrets", time.Since(phaseStarted), err,
						zap.String("session_id", record.ID),
						zap.Int("resource_secret_count", len(resourceSecrets)),
					)
					return err
				}
			}
			op.ObservePhase("persist_resource_secrets", time.Since(phaseStarted), nil,
				zap.String("session_id", record.ID),
				zap.Int("resource_secret_count", len(resourceSecrets)),
			)
			return nil
		})
	}); err != nil {
		return nil, err
	}
	created = record.toAPI(now)
	s.startAsyncSessionBootstrap(ctx, principal, credential, record, gatewayBaseURL)
	op.ObservePhase("schedule_async_bootstrap", 0, nil,
		zap.String("session_id", record.ID),
	)
	return created, nil
}

func (s *Service) startAsyncSessionBootstrap(parent context.Context, principal Principal, credential RequestCredential, record *SessionRecord, gatewayBaseURL string) {
	if record == nil {
		return
	}
	sessionID := strings.TrimSpace(record.ID)
	if sessionID == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), sessionBootstrapTimeout)
		defer cancel()
		var err error
		ctx, op := s.observability.StartOperation(ctx, "session_bootstrap_async", record.Vendor,
			zap.String("team_id", record.TeamID),
			zap.String("session_id", sessionID),
		)
		defer func() {
			if err != nil {
				s.logger.Warn("async managed-agent session bootstrap failed",
					zap.Error(err),
					zap.String("session_id", sessionID),
				)
			}
			op.End(err)
		}()
		lockStarted := time.Now()
		err = s.repo.WithSessionLock(ctx, sessionID, func(ctx context.Context) error {
			op.ObservePhase("acquire_session_lock", time.Since(lockStarted), nil,
				zap.String("session_id", sessionID),
			)
			phaseStarted := time.Now()
			current, engine, loadErr := s.repo.GetSession(ctx, sessionID)
			if loadErr != nil {
				if errors.Is(loadErr, ErrSessionNotFound) {
					op.ObservePhase("load_session", time.Since(phaseStarted), nil,
						zap.Bool("session_present", false),
					)
					return nil
				}
				op.ObservePhase("load_session", time.Since(phaseStarted), loadErr)
				return loadErr
			}
			op.ObservePhase("load_session", time.Since(phaseStarted), nil,
				zap.Bool("session_present", true),
			)
			if current.ArchivedAt != nil {
				op.ObservePhase("skip_archived_session", 0, nil)
				return nil
			}
			_, ensureErr := s.ensureRuntimeBootstrappedLocked(ctx, principal, credential, current, engine, gatewayBaseURL, nil, op, "ensure_runtime", "bootstrap_session")
			return ensureErr
		})
	}()
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
	var updated *Session
	err := s.repo.WithSessionLock(ctx, sessionID, func(ctx context.Context) error {
		var err error
		updated, err = s.updateSessionLocked(ctx, principal, credential, sessionID, params)
		return err
	})
	return updated, err
}

func (s *Service) updateSessionLocked(ctx context.Context, principal Principal, credential RequestCredential, sessionID string, params UpdateSessionParams) (*Session, error) {
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
			// Sandbox0 treats vault updates as runtime state refreshes, not run configuration changes.
			// Refresh bootstrap state even while a run is active so the next credential use sees the latest vault set.
			if strings.TrimSpace(runtime.SandboxID) != "" {
				if err := s.bootstrapRuntimeLocked(ctx, credential, record, engine, runtime, nil, "bootstrap_session_for_update"); err != nil {
					return nil, err
				}
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
	var deleted map[string]any
	err := s.repo.WithSessionLock(ctx, sessionID, func(ctx context.Context) error {
		var err error
		deleted, err = s.deleteSessionLocked(ctx, principal, credential, sessionID)
		return err
	})
	return deleted, err
}

func (s *Service) deleteSessionLocked(ctx context.Context, principal Principal, credential RequestCredential, sessionID string) (map[string]any, error) {
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
		if strings.TrimSpace(runtime.SandboxID) != "" && strings.TrimSpace(runtime.WrapperURL) != "" {
			if err := s.runtime.DeleteWrapperSession(ctx, credential, runtime, sessionID); err != nil {
				s.logger.Warn("failed to delete wrapper session", zap.Error(err), zap.String("session_id", sessionID))
			}
		}
		if err := s.runtime.DestroyRuntime(ctx, credential, runtime); err != nil {
			return nil, fmt.Errorf("destroy runtime for session %s: %w", sessionID, err)
		}
		if err := s.repo.DeleteRuntime(ctx, sessionID); err != nil {
			return nil, err
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
	var events []map[string]any
	err := s.repo.WithSessionLock(ctx, sessionID, func(ctx context.Context) error {
		var err error
		events, err = s.sendEventsLocked(ctx, principal, credential, sessionID, params, gatewayBaseURL)
		return err
	})
	return events, err
}

func (s *Service) sendEventsLocked(ctx context.Context, principal Principal, credential RequestCredential, sessionID string, params SendEventsParams, gatewayBaseURL string) (events []map[string]any, err error) {
	operationStarted := time.Now()
	record, engine, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := ensureSessionAccess(record, principal); err != nil {
		return nil, err
	}
	ctx, op := s.observability.StartOperation(ctx, "session_send_events", record.Vendor,
		zap.String("team_id", record.TeamID),
		zap.String("session_id", sessionID),
		zap.Int("input_event_count", len(params.Events)),
	)
	defer func() { op.End(err) }()
	op.ObservePhase("load_session", time.Since(operationStarted), nil,
		zap.String("session_id", sessionID),
	)
	if record.ArchivedAt != nil {
		return nil, fmt.Errorf("%w: archived sessions cannot accept new events", ErrSessionArchived)
	}
	processedAt := time.Now().UTC()
	phaseStarted := time.Now()
	inputEventMaps := inputEventsToMaps(params.Events)
	stampedEvents := stampEvents(inputEventMaps, processedAt)
	if err := validateInputEvents(stampedEvents); err != nil {
		op.ObservePhase("validate_input_events", time.Since(phaseStarted), err)
		return nil, err
	}
	op.ObservePhase("validate_input_events", time.Since(phaseStarted), nil)
	var runtime *RuntimeRecord
	phaseStarted = time.Now()
	if existingRuntime, runtimeErr := s.repo.GetRuntime(ctx, sessionID); runtimeErr == nil {
		runtime = existingRuntime
	} else if !errors.Is(runtimeErr, ErrRuntimeNotFound) {
		op.ObservePhase("load_runtime", time.Since(phaseStarted), runtimeErr)
		return nil, runtimeErr
	}
	op.ObservePhase("load_runtime", time.Since(phaseStarted), nil,
		zap.Bool("runtime_present", runtime != nil),
		zap.Bool("runtime_active_run", runtime != nil && runtime.ActiveRunID != nil),
		zap.String("sandbox_id", runtimeSandboxIDForLog(runtime)),
	)
	phaseStarted = time.Now()
	requiredActionIDs, err := s.latestRequiredActionIDs(ctx, sessionID)
	if err != nil {
		op.ObservePhase("load_required_actions", time.Since(phaseStarted), err)
		return nil, err
	}
	op.ObservePhase("load_required_actions", time.Since(phaseStarted), nil,
		zap.Int("required_action_count", len(requiredActionIDs)),
	)
	if runtime != nil && runtime.ActiveRunID != nil && !containsInterruptEvent(stampedEvents) && !(len(requiredActionIDs) > 0 && containsOnlyActionResolutionEvents(stampedEvents)) {
		queuedEvents := queueEvents(stampedEvents)
		phaseStarted = time.Now()
		runtimeInputEvents, err := s.resolveFileBackedInputEvents(ctx, principal, credential, queuedEvents)
		if err != nil {
			op.ObservePhase("resolve_file_backed_input_events", time.Since(phaseStarted), err)
			return nil, err
		}
		op.ObservePhase("resolve_file_backed_input_events", time.Since(phaseStarted), nil)
		phaseStarted = time.Now()
		if err := s.repo.AppendEvents(ctx, sessionID, queuedEvents); err != nil {
			op.ObservePhase("append_queued_events", time.Since(phaseStarted), err)
			return nil, err
		}
		op.ObservePhase("append_queued_events", time.Since(phaseStarted), nil,
			zap.Int("event_count", len(queuedEvents)),
		)
		phaseStarted = time.Now()
		if err := s.repo.CreateRuntimeInputEventBatch(ctx, &runtimeInputEventBatch{
			ID:                 NewID("srunq"),
			SessionID:          sessionID,
			EventIDs:           eventIDsFromMaps(queuedEvents),
			RuntimeInputEvents: runtimeInputEvents,
			CreatedAt:          processedAt,
			UpdatedAt:          processedAt,
		}); err != nil {
			op.ObservePhase("create_runtime_input_batch", time.Since(phaseStarted), err)
			return nil, err
		}
		op.ObservePhase("create_runtime_input_batch", time.Since(phaseStarted), nil)
		return queuedEvents, nil
	}
	phaseStarted = time.Now()
	runtimeInputEvents, err := s.resolveFileBackedInputEvents(ctx, principal, credential, stampedEvents)
	if err != nil {
		op.ObservePhase("resolve_file_backed_input_events", time.Since(phaseStarted), err)
		return nil, err
	}
	op.ObservePhase("resolve_file_backed_input_events", time.Since(phaseStarted), nil)
	phaseStarted = time.Now()
	if err := s.repo.AppendEvents(ctx, sessionID, stampedEvents); err != nil {
		op.ObservePhase("append_events", time.Since(phaseStarted), err)
		return nil, err
	}
	op.ObservePhase("append_events", time.Since(phaseStarted), nil,
		zap.Int("event_count", len(stampedEvents)),
	)
	if runtime != nil && runtime.ActiveRunID != nil && strings.TrimSpace(runtime.SandboxID) == "" &&
		(containsInterruptEvent(stampedEvents) || (len(requiredActionIDs) > 0 && containsOnlyActionResolutionEvents(stampedEvents))) {
		phaseStarted = time.Now()
		runtime, err = s.runtime.EnsureRuntime(ctx, principal, credential, record, engine, gatewayBaseURL)
		if err != nil {
			op.ObservePhase("ensure_runtime_for_action", time.Since(phaseStarted), err)
			return nil, err
		}
		op.ObservePhase("ensure_runtime_for_action", time.Since(phaseStarted), nil,
			zap.String("sandbox_id", runtimeSandboxIDForLog(runtime)),
		)
	}
	if runtime != nil && containsInterruptEvent(stampedEvents) && runtime.ActiveRunID != nil {
		phaseStarted = time.Now()
		if err := s.runtime.InterruptRun(ctx, credential, runtime, *runtime.ActiveRunID); err != nil {
			op.ObservePhase("interrupt_run", time.Since(phaseStarted), err)
			return nil, err
		}
		op.ObservePhase("interrupt_run", time.Since(phaseStarted), nil)
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
		runtime, err = s.ensureRuntimeBootstrappedLocked(ctx, principal, credential, record, engine, gatewayBaseURL, runtime, op, "ensure_runtime_for_action_resolution", "bootstrap_session_for_action_resolution")
		if err != nil {
			return nil, err
		}
		phaseStarted = time.Now()
		resolution, err := s.runtime.ResolveActions(ctx, credential, runtime, &WrapperResolveActionsRequest{SessionID: sessionID, Events: inputEventsFromMaps(runtimeInputEvents)})
		if err != nil {
			op.ObservePhase("resolve_actions", time.Since(phaseStarted), err)
			return nil, err
		}
		op.ObservePhase("resolve_actions", time.Since(phaseStarted), nil)
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
			phaseStarted = time.Now()
			if err := s.runtime.StartRun(ctx, credential, runtime, &WrapperRunRequest{SessionID: sessionID, RunID: runID, InputEvents: inputEventsFromMaps(runtimeInputEvents)}); err != nil {
				op.ObservePhase("start_run_after_action_resolution", time.Since(phaseStarted), err,
					zap.String("run_id", runID),
				)
				runtime.ActiveRunID = nil
				runtime.UpdatedAt = time.Now().UTC()
				_ = s.repo.UpsertRuntime(ctx, runtime)
				failureEvents := []map[string]any{
					stampEvent(map[string]any{"type": "session.error", "error": map[string]any{"type": "unknown_error", "message": err.Error()}}, time.Now().UTC()),
					stampEvent(map[string]any{"type": "session.status_terminated"}, time.Now().UTC()),
				}
				_ = s.repo.AppendEvents(ctx, sessionID, failureEvents)
				record = applySessionBatch(record, time.Now().UTC(), Usage{}, failureEvents)
				_ = s.repo.UpdateSession(ctx, record, engine)
				return nil, err
			}
			op.ObservePhase("start_run_after_action_resolution", time.Since(phaseStarted), nil,
				zap.String("run_id", runID),
			)
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
	runtime, err = s.ensureRuntimeBootstrappedLocked(ctx, principal, credential, record, engine, gatewayBaseURL, runtime, op, "ensure_runtime", "bootstrap_session")
	if err != nil {
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
	phaseStarted = time.Now()
	if err := s.runtime.StartRun(ctx, credential, runtime, &WrapperRunRequest{SessionID: sessionID, RunID: runID, InputEvents: inputEventsFromMaps(runtimeInputEvents)}); err != nil {
		op.ObservePhase("start_run", time.Since(phaseStarted), err,
			zap.String("run_id", runID),
		)
		runtime.ActiveRunID = nil
		runtime.UpdatedAt = time.Now().UTC()
		_ = s.repo.UpsertRuntime(ctx, runtime)
		failureEvents := []map[string]any{
			stampEvent(map[string]any{"type": "session.error", "error": map[string]any{"type": "unknown_error", "message": err.Error()}}, time.Now().UTC()),
			stampEvent(map[string]any{"type": "session.status_terminated"}, time.Now().UTC()),
		}
		_ = s.repo.AppendEvents(ctx, sessionID, failureEvents)
		record = applySessionBatch(record, time.Now().UTC(), Usage{}, failureEvents)
		_ = s.repo.UpdateSession(ctx, record, engine)
		return nil, err
	}
	op.ObservePhase("start_run", time.Since(phaseStarted), nil,
		zap.String("run_id", runID),
	)
	return stampedEvents, nil
}

func (s *Service) HandleSandboxWebhook(ctx context.Context, rawBody []byte, signature string) (err error) {
	var (
		envelope  sandboxWebhookEnvelope
		eventType string
		sandboxID string
	)
	ctx, op := s.observability.StartOperation(ctx, "runtime_receive_webhook", "",
		zap.Int("payload_bytes", len(rawBody)),
	)
	defer func() { op.End(err) }()
	if len(bytesTrimSpace(rawBody)) == 0 {
		return errors.New("webhook body is required")
	}
	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		return errors.New("invalid webhook body")
	}
	eventType = strings.TrimSpace(envelope.EventType)
	sandboxID = strings.TrimSpace(envelope.SandboxID)
	op.AddFields(
		zap.String("event_type", eventType),
		zap.String("sandbox_id", sandboxID),
	)
	if sandboxID == "" {
		return errors.New("webhook sandbox_id is required")
	}
	phaseStarted := time.Now()
	runtime, err := s.repo.GetRuntimeBySandboxID(ctx, sandboxID)
	if err != nil {
		op.ObservePhase("load_runtime_by_sandbox", time.Since(phaseStarted), err)
		if errors.Is(err, ErrRuntimeNotFound) && sandboxWebhookEventMarksRuntimeLost(eventType) {
			return nil
		}
		return err
	}
	op.ObservePhase("load_runtime_by_sandbox", time.Since(phaseStarted), nil,
		zap.String("session_id", runtime.SessionID),
		zap.Int64("runtime_generation", runtime.RuntimeGeneration),
	)
	op.AddFields(
		zap.String("session_id", runtime.SessionID),
		zap.Int64("runtime_generation", runtime.RuntimeGeneration),
	)
	phaseStarted = time.Now()
	if subtleTrim(runtime.ControlToken) == "" || !sandbox0sdk.VerifyWebhookSignature(runtime.ControlToken, rawBody, signature) {
		op.ObservePhase("verify_webhook_signature", time.Since(phaseStarted), errors.New("invalid webhook signature"))
		return errors.New("invalid webhook signature")
	}
	op.ObservePhase("verify_webhook_signature", time.Since(phaseStarted), nil)
	if sandboxWebhookEventMarksRuntimeLost(eventType) {
		phaseStarted = time.Now()
		err = s.repo.WithSessionLock(ctx, runtime.SessionID, func(ctx context.Context) error {
			current, err := s.repo.GetRuntime(ctx, runtime.SessionID)
			if err != nil {
				if errors.Is(err, ErrRuntimeNotFound) {
					return nil
				}
				return err
			}
			return s.repo.MarkRuntimeSandboxLost(ctx, current, eventType, time.Now().UTC())
		})
		op.ObservePhase("mark_runtime_lost", time.Since(phaseStarted), err)
		return err
	}
	if eventType != "agent.event" {
		return nil
	}
	var payload RuntimeCallbackPayload
	if len(envelope.Payload) == 0 {
		return errors.New("webhook payload is required")
	}
	phaseStarted = time.Now()
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		op.ObservePhase("decode_webhook_payload", time.Since(phaseStarted), err)
		return errors.New("invalid webhook payload")
	}
	op.ObservePhase("decode_webhook_payload", time.Since(phaseStarted), nil,
		zap.Int("event_count", len(payload.Events)),
		zap.String("run_id", strings.TrimSpace(payload.RunID)),
	)
	if trimmedSessionID := strings.TrimSpace(payload.SessionID); trimmedSessionID != "" && trimmedSessionID != runtime.SessionID {
		return errors.New("runtime payload session_id mismatch")
	}
	if strings.TrimSpace(payload.SessionID) == "" {
		payload.SessionID = runtime.SessionID
	}
	jobID := strings.TrimSpace(envelope.EventID)
	if jobID == "" {
		jobID = NewID("whjob")
	}
	now := time.Now().UTC()
	op.AddFields(
		zap.String("job_id", jobID),
		zap.String("run_id", strings.TrimSpace(payload.RunID)),
		zap.Int("event_count", len(payload.Events)),
	)
	phaseStarted = time.Now()
	_, err = s.repo.CreateRuntimeWebhookJob(ctx, &runtimeWebhookJob{
		ID:                jobID,
		SessionID:         runtime.SessionID,
		SandboxID:         runtime.SandboxID,
		RuntimeGeneration: runtime.RuntimeGeneration,
		RunID:             strings.TrimSpace(payload.RunID),
		EventType:         eventType,
		Payload:           payload,
		CreatedAt:         now,
		UpdatedAt:         now,
	})
	op.ObservePhase("enqueue_webhook_job", time.Since(phaseStarted), err,
		zap.String("job_id", jobID),
		zap.Int("event_count", len(payload.Events)),
	)
	return err
}

func sandboxWebhookEventMarksRuntimeLost(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "sandbox.killed", "sandbox.deleted":
		return true
	default:
		return false
	}
}

func (s *Service) StartRuntimeWebhookWorker(ctx context.Context) {
	owner := NewID("whworker")
	go s.runtimeWebhookWorkerLoop(ctx, owner)
}

func (s *Service) runtimeWebhookWorkerLoop(ctx context.Context, owner string) {
	ticker := time.NewTicker(runtimeWebhookPollInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		processed, err := s.ProcessNextRuntimeWebhookJob(ctx, owner)
		if err != nil {
			s.logger.Warn("runtime webhook worker failed", zap.Error(err), zap.String("worker", owner))
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) ProcessNextRuntimeWebhookJob(ctx context.Context, owner string) (bool, error) {
	phaseStarted := time.Now()
	job, err := s.repo.LeaseNextRuntimeWebhookJob(ctx, owner, time.Now().UTC().Add(runtimeWebhookLeaseDuration))
	leaseDuration := time.Since(phaseStarted)
	if err != nil {
		_, op := s.observability.StartOperation(ctx, "runtime_process_webhook_job", "",
			zap.String("worker", owner),
		)
		defer func() { op.End(err) }()
		op.ObservePhase("lease_webhook_job", leaseDuration, err,
			zap.Bool("job_present", false),
		)
		return false, err
	}
	if job == nil {
		return false, nil
	}
	_, op := s.observability.StartOperation(ctx, "runtime_process_webhook_job", "",
		zap.String("worker", owner),
	)
	defer func() { op.End(err) }()
	op.ObservePhase("lease_webhook_job", leaseDuration, nil,
		zap.Bool("job_present", true),
	)
	op.AddFields(
		zap.String("job_id", job.ID),
		zap.String("session_id", job.SessionID),
		zap.String("sandbox_id", job.SandboxID),
		zap.String("run_id", job.RunID),
		zap.String("event_type", job.EventType),
		zap.Int("attempts", job.Attempts),
		zap.Int("event_count", len(job.Payload.Events)),
		zap.Duration("queue_delay", time.Since(job.CreatedAt)),
	)
	phaseStarted = time.Now()
	if err := s.applyRuntimeWebhookJob(ctx, job); err != nil {
		op.ObservePhase("apply_webhook_job", time.Since(phaseStarted), err)
		retry := job.Attempts < runtimeWebhookMaxAttempts
		releaseStarted := time.Now()
		if releaseErr := s.repo.ReleaseRuntimeWebhookJob(ctx, job.ID, err, retry); releaseErr != nil {
			op.ObservePhase("release_webhook_job", time.Since(releaseStarted), releaseErr,
				zap.Bool("retry", retry),
			)
			return true, releaseErr
		}
		op.ObservePhase("release_webhook_job", time.Since(releaseStarted), nil,
			zap.Bool("retry", retry),
		)
		s.logger.Warn("runtime webhook job failed",
			zap.Error(err),
			zap.String("job_id", job.ID),
			zap.String("session_id", job.SessionID),
			zap.Int("attempts", job.Attempts),
			zap.Bool("retry", retry),
		)
		return true, nil
	}
	op.ObservePhase("apply_webhook_job", time.Since(phaseStarted), nil)
	phaseStarted = time.Now()
	if err := s.repo.CompleteRuntimeWebhookJob(ctx, job.ID); err != nil {
		op.ObservePhase("complete_webhook_job", time.Since(phaseStarted), err)
		return true, err
	}
	op.ObservePhase("complete_webhook_job", time.Since(phaseStarted), nil)
	return true, nil
}

func (s *Service) applyRuntimeWebhookJob(ctx context.Context, job *runtimeWebhookJob) (err error) {
	if job == nil {
		return nil
	}
	ctx, op := s.observability.StartOperation(ctx, "runtime_apply_webhook_job", "",
		zap.String("job_id", job.ID),
		zap.String("session_id", job.SessionID),
		zap.String("sandbox_id", job.SandboxID),
		zap.String("run_id", job.RunID),
		zap.Int64("runtime_generation", job.RuntimeGeneration),
		zap.Int("event_count", len(job.Payload.Events)),
	)
	defer func() { op.End(err) }()
	phaseStarted := time.Now()
	err = s.repo.WithSessionLock(ctx, job.SessionID, func(ctx context.Context) error {
		runtime, err := s.repo.GetRuntime(ctx, job.SessionID)
		if err != nil {
			if errors.Is(err, ErrRuntimeNotFound) {
				return nil
			}
			return err
		}
		if runtimeWebhookJobIsStale(runtime, job) {
			s.logger.Debug("skipping stale runtime webhook job",
				zap.String("job_id", job.ID),
				zap.String("session_id", job.SessionID),
				zap.String("run_id", job.RunID),
				zap.Int64("runtime_generation", job.RuntimeGeneration),
			)
			return nil
		}
		return s.applyRuntimePayloadLocked(ctx, runtime, job.Payload)
	})
	op.ObservePhase("lock_and_apply_payload", time.Since(phaseStarted), err)
	return err
}

func runtimeWebhookJobIsStale(runtime *RuntimeRecord, job *runtimeWebhookJob) bool {
	if runtime == nil || job == nil {
		return true
	}
	if strings.TrimSpace(runtime.SandboxID) != strings.TrimSpace(job.SandboxID) || runtime.RuntimeGeneration != job.RuntimeGeneration {
		return true
	}
	runID := strings.TrimSpace(job.RunID)
	if runID == "" {
		runID = strings.TrimSpace(job.Payload.RunID)
	}
	if runID == "" {
		return len(job.Payload.Events) > 0
	}
	return runtime.ActiveRunID == nil || strings.TrimSpace(*runtime.ActiveRunID) != runID
}

func (s *Service) applyRuntimePayloadLocked(ctx context.Context, runtime *RuntimeRecord, payload RuntimeCallbackPayload) error {
	record, engine, err := s.repo.GetSession(ctx, runtime.SessionID)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return nil
		}
		return err
	}
	processedAt := time.Now().UTC()
	stampedEvents := stampEvents(sessionEventsToMaps(payload.Events), processedAt)
	if runtimePayloadAllowsQueuedRun(stampedEvents) {
		batch, err := s.repo.GetNextRuntimeInputEventBatch(ctx, runtime.SessionID)
		if err != nil {
			return err
		}
		if batch != nil {
			if err := s.bootstrapRuntimeLocked(ctx, RequestCredential{}, record, engine, runtime, nil, "bootstrap_session_for_queued_run"); err != nil {
				return err
			}
		}
	}
	if err := s.repo.AppendEvents(ctx, runtime.SessionID, stampedEvents); err != nil {
		return err
	}
	if strings.TrimSpace(payload.VendorSessionID) != "" {
		runtime.VendorSessionID = strings.TrimSpace(payload.VendorSessionID)
	}
	reachedIdle := false
	for _, event := range stampedEvents {
		switch stringValue(event["type"]) {
		case "session.status_idle":
			reachedIdle = true
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
	if reachedIdle {
		if runtime.ActiveRunID == nil {
			started, err := s.startNextQueuedRunLocked(ctx, record, engine, runtime, processedAt)
			if err != nil {
				return err
			}
			if started {
				return nil
			}
		}
	}
	return nil
}

func (s *Service) startNextQueuedRunLocked(ctx context.Context, record *SessionRecord, engine map[string]any, runtime *RuntimeRecord, processedAt time.Time) (bool, error) {
	batch, err := s.repo.GetNextRuntimeInputEventBatch(ctx, runtime.SessionID)
	if err != nil || batch == nil {
		return batch != nil, err
	}
	runtimeInputEvents := markRuntimeInputEventsProcessed(batch.RuntimeInputEvents, processedAt)
	if err := s.repo.MarkEventsProcessed(ctx, runtime.SessionID, batch.EventIDs, processedAt); err != nil {
		return false, err
	}
	runID := NewID("srun")
	runtime.ActiveRunID = &runID
	runtime.UpdatedAt = processedAt
	if err := s.repo.UpsertRuntime(ctx, runtime); err != nil {
		return false, err
	}
	runningEvent := stampEvent(map[string]any{"type": "session.status_running"}, processedAt)
	if err := s.repo.AppendEvents(ctx, runtime.SessionID, []map[string]any{runningEvent}); err != nil {
		return false, err
	}
	record = applySessionBatch(record, processedAt, Usage{}, []map[string]any{runningEvent})
	if err := s.repo.UpdateSession(ctx, record, engine); err != nil {
		return false, err
	}
	if err := s.runtime.StartRun(ctx, RequestCredential{}, runtime, &WrapperRunRequest{SessionID: runtime.SessionID, RunID: runID, InputEvents: inputEventsFromMaps(runtimeInputEvents)}); err != nil {
		s.logger.Warn("failed to start queued managed-agent run", zap.Error(err), zap.String("session_id", runtime.SessionID))
		runtime.ActiveRunID = nil
		runtime.UpdatedAt = time.Now().UTC()
		_ = s.repo.UpsertRuntime(ctx, runtime)
		failureAt := time.Now().UTC()
		failureEvents := []map[string]any{
			stampEvent(map[string]any{"type": "session.error", "error": map[string]any{"type": "unknown_error", "message": err.Error()}}, failureAt),
			stampEvent(map[string]any{"type": "session.status_terminated"}, failureAt),
		}
		_ = s.repo.AppendEvents(ctx, runtime.SessionID, failureEvents)
		record = applySessionBatch(record, failureAt, Usage{}, failureEvents)
		_ = s.repo.UpdateSession(ctx, record, engine)
		_ = s.repo.DeleteRuntimeInputEventBatch(ctx, batch.ID)
		return true, nil
	}
	if err := s.repo.DeleteRuntimeInputEventBatch(ctx, batch.ID); err != nil {
		return false, err
	}
	return true, nil
}

func runtimePayloadAllowsQueuedRun(events []map[string]any) bool {
	for _, event := range events {
		if stringValue(event["type"]) == "session.status_idle" && stopReasonType(event) != "requires_action" {
			return true
		}
	}
	return false
}

func markRuntimeInputEventsProcessed(events []map[string]any, processedAt time.Time) []map[string]any {
	processed := cloneDeepMapSlice(events)
	for _, event := range processed {
		event["processed_at"] = processedAt.UTC().Format(time.RFC3339)
	}
	return processed
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

func (s *Service) ensureRuntimeBootstrappedLocked(ctx context.Context, principal Principal, credential RequestCredential, record *SessionRecord, engine map[string]any, gatewayBaseURL string, runtime *RuntimeRecord, op *Operation, ensurePhase, bootstrapPhase string) (*RuntimeRecord, error) {
	if runtimeBootstrapCurrent(record, engine, runtime) {
		op.ObservePhase(ensurePhase+"_skipped", 0, nil,
			zap.String("sandbox_id", runtimeSandboxIDForLog(runtime)),
		)
		op.ObservePhase(bootstrapPhase+"_skipped", 0, nil,
			zap.String("sandbox_id", runtimeSandboxIDForLog(runtime)),
		)
		return runtime, nil
	}
	phaseStarted := time.Now()
	ensured, err := s.runtime.EnsureRuntime(ctx, principal, credential, record, engine, gatewayBaseURL)
	if err != nil {
		op.ObservePhase(ensurePhase, time.Since(phaseStarted), err)
		return nil, err
	}
	op.ObservePhase(ensurePhase, time.Since(phaseStarted), nil,
		zap.String("sandbox_id", runtimeSandboxIDForLog(ensured)),
	)
	if ensured != nil {
		runtime = ensured
	}
	if current, runtimeErr := s.repo.GetRuntime(ctx, record.ID); runtimeErr == nil {
		runtime = current
	} else if !errors.Is(runtimeErr, ErrRuntimeNotFound) {
		return nil, runtimeErr
	}
	if runtime == nil {
		return nil, ErrRuntimeNotFound
	}
	if runtimeBootstrapCurrent(record, engine, runtime) {
		op.ObservePhase(bootstrapPhase+"_skipped", 0, nil,
			zap.String("sandbox_id", runtimeSandboxIDForLog(runtime)),
		)
		return runtime, nil
	}
	return runtime, s.bootstrapRuntimeLocked(ctx, credential, record, engine, runtime, op, bootstrapPhase)
}

func (s *Service) bootstrapRuntimeLocked(ctx context.Context, credential RequestCredential, record *SessionRecord, engine map[string]any, runtime *RuntimeRecord, op *Operation, phase string) error {
	phaseStarted := time.Now()
	if err := s.runtime.BootstrapSession(ctx, credential, runtime, bootstrapRequestFor(record, engine, runtime)); err != nil {
		op.ObservePhase(phase, time.Since(phaseStarted), err,
			zap.String("sandbox_id", runtimeSandboxIDForLog(runtime)),
		)
		return err
	}
	op.ObservePhase(phase, time.Since(phaseStarted), nil,
		zap.String("sandbox_id", runtimeSandboxIDForLog(runtime)),
	)
	return s.markRuntimeBootstrapped(ctx, runtime, bootstrapHashFor(record, engine))
}

func bootstrapHashFor(record *SessionRecord, engine map[string]any) string {
	if record == nil {
		return ""
	}
	payload := map[string]any{
		"vendor":                  record.Vendor,
		"environment_id":          record.EnvironmentID,
		"environment_artifact_id": record.EnvironmentArtifactID,
		"working_directory":       record.WorkingDirectory,
		"agent":                   record.Agent,
		"resources":               record.Resources,
		"vault_ids":               record.VaultIDs,
		"engine":                  engine,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func runtimeBootstrapCurrent(record *SessionRecord, engine map[string]any, runtime *RuntimeRecord) bool {
	if runtime == nil || strings.TrimSpace(runtime.BootstrapHash) == "" {
		return false
	}
	if runtime.UpdatedAt.IsZero() || time.Since(runtime.UpdatedAt) > runtimeBootstrapFreshness {
		return false
	}
	return runtime.BootstrapHash == bootstrapHashFor(record, engine)
}

func (s *Service) markRuntimeBootstrapped(ctx context.Context, runtime *RuntimeRecord, bootstrapHash string) error {
	if runtime == nil {
		return nil
	}
	runtime.BootstrapHash = strings.TrimSpace(bootstrapHash)
	runtime.UpdatedAt = time.Now().UTC()
	return s.repo.UpsertRuntime(ctx, runtime)
}

func runtimeSandboxIDForLog(runtime *RuntimeRecord) string {
	if runtime == nil {
		return ""
	}
	return runtime.SandboxID
}

type sandboxWebhookEnvelope struct {
	EventID   string          `json:"event_id"`
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

func queueEvents(events []map[string]any) []map[string]any {
	if len(events) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(events))
	for _, event := range events {
		queued := cloneMap(event)
		if strings.TrimSpace(stringValue(queued["id"])) == "" {
			queued["id"] = NewID("evt")
		}
		queued["processed_at"] = nil
		out = append(out, queued)
	}
	return out
}

func eventIDsFromMaps(events []map[string]any) []string {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		if id := strings.TrimSpace(stringValue(event["id"])); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
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
			block, ok := rawBlock.(map[string]any)
			if !ok {
				continue
			}
			blockType := stringValue(block["type"])
			if blockType != "image" && blockType != "document" {
				continue
			}
			source, ok := block["source"].(map[string]any)
			if !ok {
				continue
			}
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
