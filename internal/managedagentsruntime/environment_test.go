package managedagentsruntime

import (
	"reflect"
	"testing"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
)

func TestTemplateRequestForEnvironmentKeepsWarmTemplateStable(t *testing.T) {
	mgr, err := NewSDKRuntimeManager(nil, (Config{
		Enabled:           true,
		ClaudeTemplate:    "managed-agent-claude",
		TemplateMainImage: "example.com/wrapper:latest",
	}).WithDefaults(0), nil)
	if err != nil {
		t.Fatalf("NewSDKRuntimeManager returned error: %v", err)
	}
	environment := testEnvironment()
	request, err := mgr.templateRequestForEnvironment(environment)
	if err != nil {
		t.Fatalf("templateRequestForEnvironment returned error: %v", err)
	}
	if request.TemplateID != "managed-agent-claude" {
		t.Fatalf("TemplateID = %q, want managed-agent-claude", request.TemplateID)
	}
	if mgr.templateRequest.TemplateID != "managed-agent-claude" {
		t.Fatalf("base template mutated: %q", mgr.templateRequest.TemplateID)
	}
	if _, ok := request.Spec.Network.Get(); !ok {
		t.Fatal("template network should remain configured")
	}
}

func TestRuntimeNetworkPolicyAllowsEngineOverride(t *testing.T) {
	environment := testEnvironment()
	policy := runtimeNetworkPolicy(environment, map[string]any{"network": map[string]any{"mode": "allow-all"}}, nil)
	if policy.Mode != apispec.SandboxNetworkPolicyModeAllowAll {
		t.Fatalf("policy mode = %q, want allow-all", policy.Mode)
	}
}

func TestEnvironmentNetworkPolicyRequiresAllowPackageManagersFlag(t *testing.T) {
	environment := testEnvironment()
	policy := environmentNetworkPolicy(environment, nil)
	egress, ok := policy.Egress.Get()
	if !ok {
		t.Fatal("egress policy not set")
	}
	if containsString(egress.AllowedDomains, "pypi.org") {
		t.Fatalf("allowed domains = %#v, did not expect package-manager domains", egress.AllowedDomains)
	}
	if !containsString(egress.AllowedDomains, "api.example.com") {
		t.Fatalf("allowed domains = %#v, want api.example.com", egress.AllowedDomains)
	}
}

func TestEnvironmentNetworkPolicyAllowsMCPServersWhenEnabled(t *testing.T) {
	environment := testEnvironment()
	environment.Config.Networking.AllowMCPServers = true
	agent := map[string]any{
		"mcp_servers": []any{
			map[string]any{"type": "url", "name": "docs", "url": "https://MCP.Example.com/sse?b=2&a=1"},
		},
	}
	policy := environmentNetworkPolicy(environment, agent)
	egress, ok := policy.Egress.Get()
	if !ok {
		t.Fatal("egress policy not set")
	}
	if !containsString(egress.AllowedDomains, "mcp.example.com") {
		t.Fatalf("allowed domains = %#v, want mcp.example.com", egress.AllowedDomains)
	}
}

func TestBuilderNetworkPolicyIncludesPackageRegistries(t *testing.T) {
	environment := testEnvironment()
	policy := builderNetworkPolicy(environment)
	egress, ok := policy.Egress.Get()
	if !ok {
		t.Fatal("egress policy not set")
	}
	if !containsString(egress.AllowedDomains, "pypi.org") {
		t.Fatalf("allowed domains = %#v, want pypi.org", egress.AllowedDomains)
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
