package managedagents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MarkRuntimeSandboxLost reconciles a runtime row after sandbox0 reports that the
// sandbox is gone or the runtime worker observes it missing.
func (r *Repository) MarkRuntimeSandboxLost(ctx context.Context, runtime *RuntimeRecord, reason string, observedAt time.Time) error {
	if runtime == nil {
		return nil
	}
	sessionID := strings.TrimSpace(runtime.SessionID)
	sandboxID := strings.TrimSpace(runtime.SandboxID)
	if sessionID == "" || sandboxID == "" {
		return nil
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	} else {
		observedAt = observedAt.UTC()
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "runtime sandbox was lost"
	}
	return r.WithSessionLock(ctx, sessionID, func(ctx context.Context) error {
		return r.markRuntimeSandboxLostLocked(ctx, sessionID, sandboxID, reason, observedAt)
	})
}

func (r *Repository) markRuntimeSandboxLostLocked(ctx context.Context, sessionID, sandboxID, reason string, observedAt time.Time) error {
	return r.WithTransaction(ctx, func(ctx context.Context) error {
		current, err := r.GetRuntime(ctx, sessionID)
		if err != nil {
			if errors.Is(err, ErrRuntimeNotFound) {
				return nil
			}
			return err
		}
		if strings.TrimSpace(current.SandboxID) != sandboxID {
			return nil
		}
		record, engine, err := r.GetSession(ctx, sessionID)
		if err != nil {
			if !errors.Is(err, ErrSessionNotFound) {
				return err
			}
			if markErr := r.MarkRuntimeSandboxDeleted(ctx, sessionID, sandboxID, observedAt); markErr != nil && !errors.Is(markErr, ErrRuntimeNotFound) {
				return markErr
			}
			return nil
		}
		if record.Status == "running" {
			current.ActiveRunID = nil
			current.UpdatedAt = observedAt
			if err := r.UpsertRuntime(ctx, current); err != nil {
				return err
			}
			events := []map[string]any{
				stampEvent(map[string]any{
					"type": "session.error",
					"error": map[string]any{
						"type":    "unknown_error",
						"message": fmt.Sprintf("runtime sandbox %s was lost: %s", sandboxID, reason),
					},
				}, observedAt),
				stampEvent(map[string]any{"type": "session.status_terminated"}, observedAt),
			}
			if err := r.AppendEvents(ctx, sessionID, events); err != nil {
				return err
			}
			record = applySessionBatch(record, observedAt, Usage{}, events)
			if err := r.UpdateSession(ctx, record, engine); err != nil {
				return err
			}
		}
		if err := r.MarkRuntimeSandboxDeleted(ctx, sessionID, sandboxID, observedAt); err != nil && !errors.Is(err, ErrRuntimeNotFound) {
			return err
		}
		return nil
	})
}
