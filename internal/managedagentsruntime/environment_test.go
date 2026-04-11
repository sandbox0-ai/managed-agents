package managedagentsruntime

import (
	"reflect"
	"testing"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
)

func TestTemplateRequestForEnvironmentAppliesEnvironmentTemplateAndNetwork(t *testing.T) {
	mgr, err := NewSDKRuntimeManager(nil, (Config{
		Enabled:              true,
		ClaudeTemplate:       "managed-agent-claude",
		TemplateMainImage:    "example.com/main:latest",
		TemplateSidecarImage: "example.com/sidecar:latest",
	}).WithDefaults(0), nil)
	if err != nil {
		t.Fatalf("NewSDKRuntimeManager returned error: %v", err)
	}
	environment := testEnvironment()
	request, err := mgr.templateRequestForEnvironment(environment)
	if err != nil {
		t.Fatalf("templateRequestForEnvironment returned error: %v", err)
	}
	if request.TemplateID != "managed-agent-claude-env-123" {
		t.Fatalf("TemplateID = %q, want managed-agent-claude-env-123", request.TemplateID)
	}
	if mgr.templateRequest.TemplateID != "managed-agent-claude" {
		t.Fatalf("base template mutated: %q", mgr.templateRequest.TemplateID)
	}
	envVars, ok := request.Spec.EnvVars.Get()
	if !ok || envVars["MANAGED_AGENT_ENVIRONMENT_ID"] != "env_123" {
		t.Fatalf("env vars = %#v, %v", envVars, ok)
	}
	policy, ok := request.Spec.Network.Get()
	if !ok {
		t.Fatal("network policy not set")
	}
	if policy.Mode != apispec.SandboxNetworkPolicyModeBlockAll {
		t.Fatalf("network mode = %q, want block-all", policy.Mode)
	}
	egress, ok := policy.Egress.Get()
	if !ok {
		t.Fatal("egress policy not set")
	}
	if !containsString(egress.AllowedDomains, "api.example.com") || !containsString(egress.AllowedDomains, "pypi.org") {
		t.Fatalf("allowed domains = %#v, want api.example.com and pypi.org", egress.AllowedDomains)
	}
}

func TestRuntimeNetworkPolicyAllowsEngineOverride(t *testing.T) {
	environment := testEnvironment()
	policy := runtimeNetworkPolicy(environment, map[string]any{"network": map[string]any{"mode": "allow-all"}})
	if policy.Mode != apispec.SandboxNetworkPolicyModeAllowAll {
		t.Fatalf("policy mode = %q, want allow-all", policy.Mode)
	}
}

func TestMergeManagedCredentialPolicyAllowsCredentialDomainsWhenBlockAll(t *testing.T) {
	base := apispec.SandboxNetworkPolicy{Mode: apispec.SandboxNetworkPolicyModeBlockAll}
	merged := mergeManagedCredentialPolicy(base, "sesn_123", []managedCredentialBinding{{key: "cred_1", sourceName: "source", domains: []string{"api.github.com"}}})
	egress, ok := merged.Egress.Get()
	if !ok {
		t.Fatal("egress policy not set")
	}
	if !reflect.DeepEqual(egress.AllowedDomains, []string{"api.github.com"}) {
		t.Fatalf("allowed domains = %#v, want api.github.com", egress.AllowedDomains)
	}
}

func testEnvironment() *gatewaymanagedagents.Environment {
	return &gatewaymanagedagents.Environment{
		Type: "environment",
		ID:   "env_123",
		Name: "Python tools",
		Config: gatewaymanagedagents.CloudConfig{
			Type: "cloud",
			Networking: gatewaymanagedagents.EnvironmentNetworking{
				Type:         "limited",
				AllowedHosts: []string{"api.example.com"},
			},
			Packages: gatewaymanagedagents.EnvironmentPackages{
				Type: "packages",
				Pip:  []string{"ruff==0.8.0"},
			},
		},
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
