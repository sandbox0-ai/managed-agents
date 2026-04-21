package managedagentsruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
)

const sandboxTemplateReadyConditionType = "Ready"

// Ready reports whether the managed-agent runtime dependencies are ready to serve traffic.
func (m *SDKRuntimeManager) Ready(ctx context.Context) error {
	if m == nil || !m.cfg.Enabled || m.templateRequest == nil {
		return nil
	}
	templateClient, err := m.templateClient(ctx, gatewaymanagedagents.RequestCredential{}, "")
	if err != nil {
		return fmt.Errorf("create template client: %w", err)
	}
	if err := m.ensureConfiguredManagedTemplate(ctx, templateClient); err != nil {
		return fmt.Errorf("ensure managed template: %w", err)
	}
	client, err := m.runtimeSandboxClient()
	if err != nil {
		return fmt.Errorf("create sandbox client: %w", err)
	}
	templateID := m.templateIDForSession("", m.templateRequest)
	template, err := client.GetTemplate(ctx, templateID)
	if err != nil {
		return fmt.Errorf("get managed template %q: %w", templateID, err)
	}
	if err := validateManagedTemplateReadiness(template, m.templateRequest); err != nil {
		return fmt.Errorf("managed template %q not ready: %w", templateID, err)
	}
	return nil
}

func validateManagedTemplateReadiness(template *apispec.Template, request *managedTemplateRequest) error {
	if template == nil {
		return errors.New("template response is missing")
	}
	status, ok := template.Status.Get()
	if !ok {
		return errors.New("template status is missing")
	}
	ready, found, readyErr := managedTemplateReadyCondition(status)
	if found {
		if ready {
			return nil
		}
		if readyErr != nil {
			return readyErr
		}
		return errors.New("template ready condition is false")
	}

	minIdle := int32(0)
	if request != nil {
		if pool, ok := request.Spec.Pool.Get(); ok {
			minIdle = pool.MinIdle
		}
	}
	idleCount, ok := status.IdleCount.Get()
	if !ok {
		return errors.New("template ready condition is missing and idleCount is unavailable")
	}
	if idleCount < minIdle {
		return fmt.Errorf("idleCount=%d is below minIdle=%d", idleCount, minIdle)
	}
	return nil
}

func managedTemplateReadyCondition(status apispec.SandboxTemplateStatus) (ready bool, found bool, err error) {
	for _, condition := range status.Conditions {
		conditionType, ok := condition.Type.Get()
		if !ok || !strings.EqualFold(strings.TrimSpace(conditionType), sandboxTemplateReadyConditionType) {
			continue
		}
		found = true
		conditionStatus, _ := condition.Status.Get()
		switch strings.ToLower(strings.TrimSpace(conditionStatus)) {
		case "true":
			return true, true, nil
		case "false":
			return false, true, managedTemplateConditionError("false", condition)
		case "unknown":
			return false, true, managedTemplateConditionError("unknown", condition)
		default:
			return false, true, managedTemplateConditionError(conditionStatus, condition)
		}
	}
	return false, false, nil
}

func managedTemplateConditionError(status string, condition apispec.SandboxTemplateCondition) error {
	reason, _ := condition.Reason.Get()
	message, _ := condition.Message.Get()
	details := make([]string, 0, 2)
	if trimmedReason := strings.TrimSpace(reason); trimmedReason != "" {
		details = append(details, "reason="+trimmedReason)
	}
	if trimmedMessage := strings.TrimSpace(message); trimmedMessage != "" {
		details = append(details, "message="+trimmedMessage)
	}
	if len(details) == 0 {
		return fmt.Errorf("template ready condition is %s", strings.TrimSpace(status))
	}
	return fmt.Errorf("template ready condition is %s (%s)", strings.TrimSpace(status), strings.Join(details, ", "))
}
