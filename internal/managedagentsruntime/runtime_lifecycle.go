package managedagentsruntime

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
	sandbox0sdk "github.com/sandbox0-ai/sdk-go"
	"go.uber.org/zap"
)

const (
	runtimeLifecycleInterval = time.Minute
	runtimeIdleDeleteAfter   = 10 * time.Minute
	runtimeLifecycleBatch    = 100
)

func (m *SDKRuntimeManager) StartRuntimeLifecycleWorker(ctx context.Context) {
	if m == nil || !m.cfg.Enabled {
		return
	}
	if strings.TrimSpace(m.cfg.SandboxAdminAPIKey) == "" {
		m.logger.Warn("managed-agent runtime lifecycle worker disabled because MANAGED_AGENT_SANDBOX0_ADMIN_API_KEY is not configured")
		return
	}
	go m.runtimeLifecycleLoop(ctx)
}

func (m *SDKRuntimeManager) runtimeLifecycleLoop(ctx context.Context) {
	ticker := time.NewTicker(runtimeLifecycleInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		m.runRuntimeLifecycleOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *SDKRuntimeManager) runRuntimeLifecycleOnce(ctx context.Context) {
	if err := m.ReconcileRuntimeSandboxes(ctx); err != nil {
		m.logger.Warn("reconcile managed-agent runtime sandboxes failed", zap.Error(err))
	}
	if err := m.RefreshRunningRuntimeTTLs(ctx); err != nil {
		m.logger.Warn("refresh running managed-agent runtimes failed", zap.Error(err))
	}
	if err := m.DeleteIdleRuntimeSandboxes(ctx, time.Now().UTC().Add(-runtimeIdleDeleteAfter)); err != nil {
		m.logger.Warn("delete idle managed-agent runtime sandboxes failed", zap.Error(err))
	}
}

func (m *SDKRuntimeManager) ReconcileRuntimeSandboxes(ctx context.Context) error {
	runtimes, err := m.repo.ListRuntimesWithSandboxes(ctx, runtimeLifecycleBatch)
	if err != nil {
		return err
	}
	for _, runtime := range runtimes {
		sandboxID := strings.TrimSpace(runtime.SandboxID)
		if sandboxID == "" {
			continue
		}
		client, err := m.sandboxClientForRuntime(ctx, runtime)
		if err != nil {
			m.logger.Warn("create sandbox client for runtime reconcile failed",
				zap.Error(err),
				zap.String("session_id", runtimeSessionID(runtime)),
				zap.String("sandbox_id", sandboxID),
			)
			continue
		}
		opCtx, cancel := context.WithTimeout(ctx, m.cfg.SandboxRequestTimeout)
		_, err = client.GetSandbox(opCtx, sandboxID)
		cancel()
		if err == nil {
			continue
		}
		if isSandboxNotFound(err) {
			if markErr := m.markRuntimeSandboxLost(ctx, runtime, "sandbox not found during runtime reconcile"); markErr != nil {
				m.logger.Warn("mark runtime sandbox lost during reconcile failed",
					zap.Error(markErr),
					zap.String("session_id", runtimeSessionID(runtime)),
					zap.String("sandbox_id", sandboxID),
				)
			}
			continue
		}
		m.logger.Warn("get runtime sandbox during reconcile failed",
			zap.Error(err),
			zap.String("session_id", runtimeSessionID(runtime)),
			zap.String("sandbox_id", sandboxID),
		)
	}
	return nil
}

func (m *SDKRuntimeManager) RefreshRunningRuntimeTTLs(ctx context.Context) error {
	runtimes, err := m.repo.ListRunningRuntimes(ctx, runtimeLifecycleBatch)
	if err != nil {
		return err
	}
	for _, runtime := range runtimes {
		opCtx, cancel := context.WithTimeout(ctx, m.cfg.SandboxRequestTimeout)
		err := m.RefreshRuntimeTTL(opCtx, gatewaymanagedagents.RequestCredential{}, runtime)
		cancel()
		if err != nil {
			if isSandboxNotFound(err) {
				if markErr := m.markRuntimeSandboxLost(ctx, runtime, "sandbox not found during ttl refresh"); markErr != nil {
					m.logger.Warn("mark runtime sandbox lost after ttl refresh failed",
						zap.Error(markErr),
						zap.String("session_id", runtimeSessionID(runtime)),
						zap.String("sandbox_id", runtimeSandboxID(runtime)),
					)
				}
				continue
			}
			m.logger.Warn("refresh running runtime ttl failed",
				zap.Error(err),
				zap.String("session_id", runtimeSessionID(runtime)),
				zap.String("sandbox_id", runtimeSandboxID(runtime)),
			)
		}
	}
	return nil
}

func (m *SDKRuntimeManager) DeleteIdleRuntimeSandboxes(ctx context.Context, cutoff time.Time) error {
	runtimes, err := m.repo.ListIdleRuntimesForSandboxDeletion(ctx, cutoff, runtimeLifecycleBatch)
	if err != nil {
		return err
	}
	for _, runtime := range runtimes {
		sandboxID := strings.TrimSpace(runtime.SandboxID)
		if sandboxID == "" {
			continue
		}
		if err := m.deleteIdleRuntimeSandbox(ctx, runtime, cutoff, sandboxID); err != nil {
			m.logger.Warn("delete idle runtime sandbox failed",
				zap.Error(err),
				zap.String("session_id", runtimeSessionID(runtime)),
				zap.String("sandbox_id", sandboxID),
			)
		}
	}
	return nil
}

func (m *SDKRuntimeManager) deleteIdleRuntimeSandbox(ctx context.Context, runtime *gatewaymanagedagents.RuntimeRecord, cutoff time.Time, sandboxID string) error {
	return m.repo.WithSessionLock(ctx, runtime.SessionID, func(ctx context.Context) error {
		record, _, err := m.repo.GetSession(ctx, runtime.SessionID)
		if err != nil {
			if errors.Is(err, gatewaymanagedagents.ErrSessionNotFound) {
				return nil
			}
			return err
		}
		if record.Status != "idle" && record.Status != "terminated" {
			return nil
		}
		if !record.UpdatedAt.Before(cutoff.UTC()) {
			return nil
		}
		current, err := m.repo.GetRuntime(ctx, runtime.SessionID)
		if err != nil {
			if errors.Is(err, gatewaymanagedagents.ErrRuntimeNotFound) {
				return nil
			}
			return err
		}
		if strings.TrimSpace(current.SandboxID) != sandboxID {
			return nil
		}
		opCtx, cancel := context.WithTimeout(ctx, m.cfg.SandboxRequestTimeout)
		err = m.DeleteRuntimeSandbox(opCtx, current)
		cancel()
		if err != nil && !isSandboxNotFound(err) {
			return err
		}
		if err := m.repo.MarkRuntimeSandboxDeleted(ctx, current.SessionID, sandboxID, time.Now().UTC()); err != nil && !errors.Is(err, gatewaymanagedagents.ErrRuntimeNotFound) {
			return err
		}
		return nil
	})
}

func (m *SDKRuntimeManager) markRuntimeSandboxLost(ctx context.Context, runtime *gatewaymanagedagents.RuntimeRecord, reason string) error {
	if runtime == nil {
		return nil
	}
	return m.repo.WithSessionLock(ctx, runtime.SessionID, func(ctx context.Context) error {
		current, err := m.repo.GetRuntime(ctx, runtime.SessionID)
		if err != nil {
			if errors.Is(err, gatewaymanagedagents.ErrRuntimeNotFound) {
				return nil
			}
			return err
		}
		return m.repo.MarkRuntimeSandboxLost(ctx, current, reason, time.Now().UTC())
	})
}

func (m *SDKRuntimeManager) markRuntimeSandboxLostAndReload(ctx context.Context, runtime *gatewaymanagedagents.RuntimeRecord, reason string) (*gatewaymanagedagents.RuntimeRecord, error) {
	if err := m.markRuntimeSandboxLost(ctx, runtime, reason); err != nil {
		return nil, err
	}
	reloaded, err := m.repo.GetRuntime(ctx, runtime.SessionID)
	if err != nil {
		if errors.Is(err, gatewaymanagedagents.ErrRuntimeNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return reloaded, nil
}

func isSandboxNotFound(err error) bool {
	var apiErr *sandbox0sdk.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}
