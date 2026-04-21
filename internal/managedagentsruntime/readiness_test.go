package managedagentsruntime

import (
	"testing"

	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
)

func TestValidateManagedTemplateReadinessUsesReadyCondition(t *testing.T) {
	request := &managedTemplateRequest{
		TemplateID: "managed-agents",
		Spec: apispec.SandboxTemplateSpec{
			Pool: apispec.NewOptPoolStrategy(apispec.PoolStrategy{MinIdle: 1, MaxIdle: 1}),
		},
	}
	template := &apispec.Template{
		Status: apispec.NewOptSandboxTemplateStatus(apispec.SandboxTemplateStatus{
			IdleCount: apispec.NewOptInt32(0),
			Conditions: []apispec.SandboxTemplateCondition{
				{
					Type:    apispec.NewOptString("Ready"),
					Status:  apispec.NewOptString("True"),
					Reason:  apispec.NewOptString("PoolReady"),
					Message: apispec.NewOptString("Pool is ready"),
				},
			},
		}),
	}
	if err := validateManagedTemplateReadiness(template, request); err != nil {
		t.Fatalf("validateManagedTemplateReadiness returned error: %v", err)
	}
}

func TestValidateManagedTemplateReadinessReportsConditionFailure(t *testing.T) {
	request := &managedTemplateRequest{
		TemplateID: "managed-agents",
		Spec: apispec.SandboxTemplateSpec{
			Pool: apispec.NewOptPoolStrategy(apispec.PoolStrategy{MinIdle: 1, MaxIdle: 1}),
		},
	}
	template := &apispec.Template{
		Status: apispec.NewOptSandboxTemplateStatus(apispec.SandboxTemplateStatus{
			Conditions: []apispec.SandboxTemplateCondition{
				{
					Type:    apispec.NewOptString("Ready"),
					Status:  apispec.NewOptString("False"),
					Reason:  apispec.NewOptString("InsufficientIdlePods"),
					Message: apispec.NewOptString("Idle pod count (0) is less than minIdle (1)"),
				},
			},
		}),
	}
	err := validateManagedTemplateReadiness(template, request)
	if err == nil {
		t.Fatal("validateManagedTemplateReadiness error = nil, want non-nil")
	}
	if got := err.Error(); got != "template ready condition is false (reason=InsufficientIdlePods, message=Idle pod count (0) is less than minIdle (1))" {
		t.Fatalf("validateManagedTemplateReadiness error = %q", got)
	}
}

func TestValidateManagedTemplateReadinessFallsBackToIdleCount(t *testing.T) {
	request := &managedTemplateRequest{
		TemplateID: "managed-agents",
		Spec: apispec.SandboxTemplateSpec{
			Pool: apispec.NewOptPoolStrategy(apispec.PoolStrategy{MinIdle: 1, MaxIdle: 1}),
		},
	}
	template := &apispec.Template{
		Status: apispec.NewOptSandboxTemplateStatus(apispec.SandboxTemplateStatus{
			IdleCount: apispec.NewOptInt32(1),
		}),
	}
	if err := validateManagedTemplateReadiness(template, request); err != nil {
		t.Fatalf("validateManagedTemplateReadiness returned error: %v", err)
	}
}

func TestValidateManagedTemplateReadinessRequiresEnoughIdlePodsWithoutCondition(t *testing.T) {
	request := &managedTemplateRequest{
		TemplateID: "managed-agents",
		Spec: apispec.SandboxTemplateSpec{
			Pool: apispec.NewOptPoolStrategy(apispec.PoolStrategy{MinIdle: 2, MaxIdle: 2}),
		},
	}
	template := &apispec.Template{
		Status: apispec.NewOptSandboxTemplateStatus(apispec.SandboxTemplateStatus{
			IdleCount: apispec.NewOptInt32(1),
		}),
	}
	err := validateManagedTemplateReadiness(template, request)
	if err == nil {
		t.Fatal("validateManagedTemplateReadiness error = nil, want non-nil")
	}
	if got := err.Error(); got != "idleCount=1 is below minIdle=2" {
		t.Fatalf("validateManagedTemplateReadiness error = %q", got)
	}
}
