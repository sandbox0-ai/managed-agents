package managedagentsruntime

import (
	"context"
	"errors"
	"strings"
	"time"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
	"go.uber.org/zap"
)

const (
	managedTemplateStartupSyncAttempts = 5
	managedTemplateStartupSyncDelay    = 10 * time.Second
)

var errManagedTemplateAdminKeyMissing = errors.New("sandbox admin api key is required for startup template sync")

// SyncManagedTemplateOnStartup ensures the configured managed-agent template is present before serving traffic.
func (m *SDKRuntimeManager) SyncManagedTemplateOnStartup(ctx context.Context) {
	if m == nil || m.templateRequest == nil {
		return
	}
	logger := m.logger
	if logger == nil {
		logger = zap.NewNop()
	}
	templateID := strings.TrimSpace(m.templateRequest.TemplateID)
	if strings.TrimSpace(m.cfg.SandboxAdminAPIKey) == "" {
		logger.Info("skipping startup managed template sync; sandbox admin api key is not configured", zap.String("template_id", templateID))
		return
	}
	if err := m.syncManagedTemplateWithRetry(ctx); err != nil {
		logger.Warn("startup managed template sync failed",
			zap.String("template_id", templateID),
			zap.Int("max_attempts", managedTemplateStartupSyncAttempts),
			zap.Error(err),
		)
		return
	}
	logger.Info("startup managed template synced", zap.String("template_id", templateID))
}

// StartManagedTemplateSync asynchronously pushes the configured managed-agent template into sandbox0.
func (m *SDKRuntimeManager) StartManagedTemplateSync(ctx context.Context) {
	if m == nil || m.templateRequest == nil {
		return
	}
	go m.SyncManagedTemplateOnStartup(ctx)
}

func (m *SDKRuntimeManager) syncManagedTemplateWithRetry(ctx context.Context) error {
	var lastErr error
	for attempt := 1; attempt <= managedTemplateStartupSyncAttempts; attempt++ {
		if err := m.syncManagedTemplateOnce(ctx); err != nil {
			lastErr = err
			if attempt == managedTemplateStartupSyncAttempts {
				break
			}
			if !sleepManagedTemplateSync(ctx, managedTemplateStartupSyncDelay) {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				break
			}
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = context.Canceled
	}
	return lastErr
}

func (m *SDKRuntimeManager) syncManagedTemplateOnce(ctx context.Context) error {
	if m == nil || m.templateRequest == nil {
		return nil
	}
	if strings.TrimSpace(m.cfg.SandboxAdminAPIKey) == "" {
		return errManagedTemplateAdminKeyMissing
	}
	client, err := m.templateClient(ctx, gatewaymanagedagents.RequestCredential{}, "")
	if err != nil {
		return err
	}
	return m.ensureConfiguredManagedTemplate(ctx, client)
}

func (m *SDKRuntimeManager) ensureConfiguredManagedTemplate(ctx context.Context, client templateClient) error {
	if m == nil || m.templateRequest == nil {
		return nil
	}
	if client == nil {
		return errors.New("template client is required")
	}
	return m.ensureManagedTemplate(ctx, client, m.templateRequest)
}

func sleepManagedTemplateSync(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
