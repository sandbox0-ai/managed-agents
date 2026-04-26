package managedagentsruntime

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
	sandbox0sdk "github.com/sandbox0-ai/sdk-go"
	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
)

func TestMergeManagedGitHubCredentialPolicy(t *testing.T) {
	base := apispec.SandboxNetworkPolicy{
		Mode: apispec.SandboxNetworkPolicyModeAllowAll,
		CredentialBindings: []apispec.CredentialBinding{{
			Ref:       "existing-bind",
			SourceRef: "existing-source",
		}},
		Egress: apispec.NewOptNetworkEgressPolicy(apispec.NetworkEgressPolicy{
			CredentialRules: []apispec.EgressCredentialRule{{
				Name:          apispec.NewOptString("existing-rule"),
				CredentialRef: "existing-bind",
				Domains:       []string{"example.com"},
			}},
		}),
	}

	policy := mergeManagedCredentialPolicy(base, "sesn_123", []managedCredentialBinding{{
		key:        "rsc_123",
		sourceName: "managed-agent-sesn-123-rsc-123",
		domains:    []string{"github.com"},
		protocol:   apispec.EgressAuthProtocolHTTPS,
		tlsMode:    apispec.EgressTLSModeTerminateReoriginate,
		projectionHeaders: []managedProjectedHeader{{
			name:          "Authorization",
			valueTemplate: "{{ .authorization }}",
		}},
	}})

	if len(policy.CredentialBindings) != 2 {
		t.Fatalf("credential bindings = %d, want 2", len(policy.CredentialBindings))
	}
	if policy.CredentialBindings[1].SourceRef != "managed-agent-sesn-123-rsc-123" {
		t.Fatalf("managed source ref = %q", policy.CredentialBindings[1].SourceRef)
	}
	egress, ok := policy.Egress.Get()
	if !ok {
		t.Fatal("egress not set")
	}
	if len(egress.CredentialRules) != 2 {
		t.Fatalf("credential rules = %d, want 2", len(egress.CredentialRules))
	}
	if egress.CredentialRules[1].CredentialRef == "" {
		t.Fatal("managed credential ref is empty")
	}
	protocol, ok := egress.CredentialRules[1].Protocol.Get()
	if !ok || protocol != apispec.EgressAuthProtocolHTTPS {
		t.Fatalf("protocol = %q, want https", protocol)
	}
	tlsMode, ok := egress.CredentialRules[1].TlsMode.Get()
	if !ok || tlsMode != apispec.EgressTLSModeTerminateReoriginate {
		t.Fatalf("tls mode = %q, want terminate-reoriginate", tlsMode)
	}
	projection := policy.CredentialBindings[1].Projection
	headers, ok := projection.HttpHeaders.Get()
	if !ok || len(headers.Headers) != 1 || headers.Headers[0].Name != "Authorization" || headers.Headers[0].ValueTemplate != "{{ .authorization }}" {
		t.Fatalf("projection headers = %#v", headers.Headers)
	}
}

func TestClearManagedCredentialPolicyRemovesOnlySessionCredentialReferences(t *testing.T) {
	base := apispec.SandboxNetworkPolicy{
		Mode: apispec.SandboxNetworkPolicyModeBlockAll,
		CredentialBindings: []apispec.CredentialBinding{
			{Ref: "existing-bind", SourceRef: "existing-source"},
			{Ref: managedCredentialBindingRef("sesn_123", "vcrd_123"), SourceRef: managedCredentialSourceName("sesn_123", "vcrd_123")},
			{Ref: "legacy-bind", SourceRef: managedCredentialSourceName("sesn_123", "legacy")},
			{Ref: managedCredentialBindingRef("sesn_other", "vcrd_123"), SourceRef: managedCredentialSourceName("sesn_other", "vcrd_123")},
		},
		Egress: apispec.NewOptNetworkEgressPolicy(apispec.NetworkEgressPolicy{
			AllowedDomains: []string{"api.example.com"},
			TrafficRules: []apispec.TrafficRule{{
				Name: apispec.NewOptString("existing-traffic"),
			}},
			CredentialRules: []apispec.EgressCredentialRule{
				{
					Name:          apispec.NewOptString("existing-rule"),
					CredentialRef: "existing-bind",
					Domains:       []string{"example.com"},
				},
				{
					Name:          apispec.NewOptString(managedCredentialRuleName("sesn_123", "vcrd_123")),
					CredentialRef: managedCredentialBindingRef("sesn_123", "vcrd_123"),
					Domains:       []string{"api.example.com"},
				},
				{
					CredentialRef: managedCredentialBindingRef("sesn_123", "unnamed"),
					Domains:       []string{"unnamed.example.com"},
				},
				{
					Name:          apispec.NewOptString(managedCredentialRuleName("sesn_other", "vcrd_123")),
					CredentialRef: managedCredentialBindingRef("sesn_other", "vcrd_123"),
					Domains:       []string{"other.example.com"},
				},
			},
		}),
	}

	cleared, changed := clearManagedCredentialPolicy(base, "sesn_123")
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if len(cleared.CredentialBindings) != 2 {
		t.Fatalf("credential bindings = %#v, want existing and other session bindings", cleared.CredentialBindings)
	}
	if cleared.CredentialBindings[0].Ref != "existing-bind" || !strings.HasPrefix(cleared.CredentialBindings[1].Ref, managedCredentialBindingPrefix("sesn_other")) {
		t.Fatalf("credential bindings = %#v", cleared.CredentialBindings)
	}
	egress, ok := cleared.Egress.Get()
	if !ok {
		t.Fatal("egress not set")
	}
	if len(egress.CredentialRules) != 2 {
		t.Fatalf("credential rules = %#v, want existing and other session rules", egress.CredentialRules)
	}
	if egress.CredentialRules[0].CredentialRef != "existing-bind" || !strings.HasPrefix(egress.CredentialRules[1].CredentialRef, managedCredentialBindingPrefix("sesn_other")) {
		t.Fatalf("credential rules = %#v", egress.CredentialRules)
	}
	if len(egress.AllowedDomains) != 1 || egress.AllowedDomains[0] != "api.example.com" {
		t.Fatalf("allowed domains = %#v, want preserved", egress.AllowedDomains)
	}
	if len(egress.TrafficRules) != 1 {
		t.Fatalf("traffic rules = %#v, want preserved", egress.TrafficRules)
	}
}

func TestGitHubAuthorizationHeaderUsesBasicAuth(t *testing.T) {
	header := githubAuthorizationHeader("token-123")
	if !strings.HasPrefix(header, "Basic ") {
		t.Fatalf("header = %q, want Basic prefix", header)
	}
}

func TestManagedBindingFromVaultCredentialUsesBearerAuth(t *testing.T) {
	binding, err := managedBindingFromVaultCredential("sesn_123", gatewaymanagedagents.StoredCredential{
		Snapshot: gatewaymanagedagents.Credential{
			ID: "vcrd_123",
			Auth: gatewaymanagedagents.CredentialAuth{
				Type:         "static_bearer",
				MCPServerURL: "https://mcp.example.com/sse",
			},
		},
		Secret: map[string]any{
			"type":           "static_bearer",
			"token":          "secret-token",
			"mcp_server_url": "https://mcp.example.com/sse",
		},
	}, sessionAgentMCPServerTargets(map[string]any{"mcp_servers": []any{
		map[string]any{"type": "url", "name": "docs", "url": "https://MCP.Example.com/sse/"},
	}}))
	if err != nil {
		t.Fatalf("managedBindingFromVaultCredential: %v", err)
	}
	if binding == nil {
		t.Fatal("expected binding")
	}
	if binding.secretValues["authorization"] != "Bearer secret-token" {
		t.Fatalf("binding secret = %#v, want bearer token", binding.secretValues)
	}
	if got := binding.secretValues["authorization"]; got != "Bearer secret-token" {
		t.Fatalf("binding authorization secret = %q, want bearer token", got)
	}
	if binding.protocol != apispec.EgressAuthProtocolHTTPS {
		t.Fatalf("binding protocol = %q, want https", binding.protocol)
	}
	if binding.tlsMode != apispec.EgressTLSModeTerminateReoriginate {
		t.Fatalf("binding tls mode = %q, want terminate-reoriginate", binding.tlsMode)
	}
	if binding.sourceName == "" {
		t.Fatal("binding source name is empty")
	}
	if binding.mcpServerName != "docs" {
		t.Fatalf("binding mcp server name = %q, want docs", binding.mcpServerName)
	}
}

func TestManagedBindingFromVaultCredentialMatchesCanonicalMCPURL(t *testing.T) {
	binding, err := managedBindingFromVaultCredential("sesn_123", gatewaymanagedagents.StoredCredential{
		Snapshot: gatewaymanagedagents.Credential{
			ID: "vcrd_123",
			Auth: gatewaymanagedagents.CredentialAuth{
				Type:         "static_bearer",
				MCPServerURL: "https://mcp.example.com/sse?a=1&b=2",
			},
		},
		Secret: map[string]any{
			"type":  "static_bearer",
			"token": "secret-token",
		},
	}, sessionAgentMCPServerTargets(map[string]any{"mcp_servers": []any{
		map[string]any{"type": "url", "name": "docs", "url": "HTTPS://MCP.Example.com:443/sse/?b=2&a=1#ignored"},
	}}))
	if err != nil {
		t.Fatalf("managedBindingFromVaultCredential: %v", err)
	}
	if binding == nil {
		t.Fatal("expected binding")
	}
	if binding.targetCanonicalURL != "https://mcp.example.com/sse?a=1&b=2" {
		t.Fatalf("target canonical URL = %q", binding.targetCanonicalURL)
	}
}

func TestManagedBindingFromVaultCredentialUsesHTTPHeaderProjectionMetadata(t *testing.T) {
	vault := gatewaymanagedagents.Vault{
		ID: "vlt_headers",
		Metadata: map[string]string{
			gatewaymanagedagents.ManagedAgentsVaultRoleKey:          gatewaymanagedagents.ManagedAgentsVaultRoleCredential,
			gatewaymanagedagents.ManagedAgentsVaultKindKey:          gatewaymanagedagents.ManagedAgentsVaultKindHTTPHeaders,
			gatewaymanagedagents.ManagedAgentsVaultTargetDomainsKey: "https://API.Example.com/v1",
			gatewaymanagedagents.ManagedAgentsVaultProtocolKey:      "https",
			gatewaymanagedagents.ManagedAgentsVaultFailurePolicyKey: "fail-closed",
			gatewaymanagedagents.ManagedAgentsVaultHeadersJSONKey:   `{"x-api-key":"{{ .token }}","authorization":"{{ .authorization }}"}`,
		},
	}
	binding, err := managedBindingFromVaultCredential("sesn_123", gatewaymanagedagents.StoredCredential{
		Snapshot: gatewaymanagedagents.Credential{
			ID:   "vcrd_123",
			Auth: gatewaymanagedagents.CredentialAuth{Type: "static_bearer"},
		},
		Secret: map[string]any{
			"type":  "static_bearer",
			"token": "secret-token",
		},
		Vault: &vault,
	}, nil)
	if err != nil {
		t.Fatalf("managedBindingFromVaultCredential: %v", err)
	}
	if binding == nil {
		t.Fatal("expected binding")
	}
	if len(binding.domains) != 1 || binding.domains[0] != "api.example.com" {
		t.Fatalf("binding domains = %#v, want normalized target domain", binding.domains)
	}
	if binding.protocol != apispec.EgressAuthProtocolHTTPS {
		t.Fatalf("binding protocol = %q, want https", binding.protocol)
	}
	if binding.tlsMode != apispec.EgressTLSModeTerminateReoriginate {
		t.Fatalf("binding tls mode = %q, want terminate-reoriginate", binding.tlsMode)
	}
	if binding.failurePolicy != apispec.EgressAuthFailurePolicyFailClosed {
		t.Fatalf("binding failure policy = %q, want fail-closed", binding.failurePolicy)
	}
	if binding.secretValues["token"] != "secret-token" || binding.secretValues["authorization"] != "Bearer secret-token" {
		t.Fatalf("binding secret values = %#v", binding.secretValues)
	}
	if len(binding.projectionHeaders) != 2 {
		t.Fatalf("projection headers = %#v, want 2", binding.projectionHeaders)
	}

	policy := mergeManagedCredentialPolicy(apispec.SandboxNetworkPolicy{Mode: apispec.SandboxNetworkPolicyModeBlockAll}, "sesn_123", []managedCredentialBinding{*binding})
	if len(policy.CredentialBindings) != 1 {
		t.Fatalf("credential bindings = %#v, want one binding", policy.CredentialBindings)
	}
	egress, ok := policy.Egress.Get()
	if !ok {
		t.Fatal("egress not set")
	}
	if len(egress.AllowedDomains) != 1 || egress.AllowedDomains[0] != "api.example.com" {
		t.Fatalf("allowed domains = %#v, want credential target domain", egress.AllowedDomains)
	}
	if len(egress.CredentialRules) != 1 {
		t.Fatalf("credential rules = %#v, want one rule", egress.CredentialRules)
	}
	failurePolicy, ok := egress.CredentialRules[0].FailurePolicy.Get()
	if !ok || failurePolicy != apispec.EgressAuthFailurePolicyFailClosed {
		t.Fatalf("failure policy = %q, want fail-closed", failurePolicy)
	}
}

func testLLMStaticBearerCredential(id, token string) gatewaymanagedagents.StoredCredential {
	return gatewaymanagedagents.StoredCredential{
		Snapshot: gatewaymanagedagents.Credential{
			ID:   id,
			Auth: gatewaymanagedagents.CredentialAuth{Type: "static_bearer"},
		},
		Secret: map[string]any{
			"type":  "static_bearer",
			"token": token,
		},
	}
}

func testLLMVaultCredentials(credentials ...gatewaymanagedagents.StoredCredential) []managedVaultCredentials {
	return testLLMVaultCredentialsWithBaseURL("https://api.anthropic.com", credentials...)
}

func testLLMVaultCredentialsWithBaseURL(baseURL string, credentials ...gatewaymanagedagents.StoredCredential) []managedVaultCredentials {
	return testLLMVaultCredentialsForEngineWithBaseURL(gatewaymanagedagents.ManagedAgentsEngineClaude, baseURL, credentials...)
}

func testLLMVaultCredentialsForEngineWithBaseURL(engine, baseURL string, credentials ...gatewaymanagedagents.StoredCredential) []managedVaultCredentials {
	metadata := map[string]string{
		gatewaymanagedagents.ManagedAgentsVaultRoleKey:   gatewaymanagedagents.ManagedAgentsVaultRoleLLM,
		gatewaymanagedagents.ManagedAgentsVaultEngineKey: engine,
	}
	if baseURL != "" {
		metadata[gatewaymanagedagents.ManagedAgentsVaultLLMBaseURLKey] = baseURL
	}
	vault := gatewaymanagedagents.Vault{
		ID:       "vlt_llm",
		Metadata: metadata,
	}
	for i := range credentials {
		credentials[i].Vault = &vault
	}
	return []managedVaultCredentials{{vault: vault, credentials: credentials}}
}

func TestManagedBindingFromVaultCredentialSkipsManagedLLMCredential(t *testing.T) {
	vaults := testLLMVaultCredentials(testLLMStaticBearerCredential("vcrd_123", "secret-token"))
	binding, err := managedBindingFromVaultCredential("sesn_123", vaults[0].credentials[0], sessionAgentMCPServerTargets(map[string]any{"mcp_servers": []any{
		map[string]any{"type": "url", "name": "llm", "url": "https://api.anthropic.com"},
	}}))
	if err != nil {
		t.Fatalf("managedBindingFromVaultCredential: %v", err)
	}
	if binding != nil {
		t.Fatalf("binding = %#v, want nil", binding)
	}
}

func TestApplyManagedLLMEnvInjectsAnthropicCredential(t *testing.T) {
	engine, credential, err := applyManagedLLMEnv("claude", map[string]any{}, testLLMVaultCredentials(
		testLLMStaticBearerCredential("vcrd_123", "secret-token"),
	))
	if err != nil {
		t.Fatalf("applyManagedLLMEnv: %v", err)
	}
	if credential == nil {
		t.Fatal("expected managed llm credential")
	}
	env := mapValue(engine["env"])
	if got := stringValue(env["ANTHROPIC_API_KEY"]); got != managedAnthropicFakeAPIKey {
		t.Fatalf("ANTHROPIC_API_KEY = %q, want fake key", got)
	}
	if got := stringValue(env["ANTHROPIC_AUTH_TOKEN"]); got != managedAnthropicFakeAuthToken {
		t.Fatalf("ANTHROPIC_AUTH_TOKEN = %q, want fake token", got)
	}
	if got := stringValue(env["ANTHROPIC_BASE_URL"]); got != "https://api.anthropic.com" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q, want https://api.anthropic.com", got)
	}
	extraArgs := mapValue(engine["extra_args"])
	if _, ok := extraArgs["bare"]; ok {
		t.Fatalf("extra_args.bare is set, want omitted")
	}
	if credential.VaultID != "vlt_llm" {
		t.Fatalf("credential vault id = %q, want vlt_llm", credential.VaultID)
	}
	if credential.Token != "secret-token" {
		t.Fatalf("credential token = %q, want secret-token", credential.Token)
	}
}

func TestApplyManagedLLMEnvRemovesBareMode(t *testing.T) {
	engine, _, err := applyManagedLLMEnv("claude", map[string]any{
		"extra_args": map[string]any{
			"bare":       nil,
			"debug-file": "/tmp/debug.log",
		},
	}, testLLMVaultCredentials(
		testLLMStaticBearerCredential("vcrd_123", "secret-token"),
	))
	if err != nil {
		t.Fatalf("applyManagedLLMEnv: %v", err)
	}
	extraArgs := mapValue(engine["extra_args"])
	if got := stringValue(extraArgs["debug-file"]); got != "/tmp/debug.log" {
		t.Fatalf("extra_args[debug-file] = %q, want /tmp/debug.log", got)
	}
	if _, ok := extraArgs["bare"]; ok {
		t.Fatalf("extra_args.bare is set, want omitted")
	}
}

func TestApplyManagedLLMEnvOverwritesExplicitEngineCredentialWithFakeKey(t *testing.T) {
	engine, credential, err := applyManagedLLMEnv("claude", map[string]any{"env": map[string]any{
		"ANTHROPIC_API_KEY":  "existing-token",
		"ANTHROPIC_BASE_URL": "https://api.anthropic.com/",
	}}, testLLMVaultCredentials(testLLMStaticBearerCredential("vcrd_123", "secret-token")))
	if err != nil {
		t.Fatalf("applyManagedLLMEnv: %v", err)
	}
	if credential == nil {
		t.Fatal("expected managed llm credential")
	}
	env := mapValue(engine["env"])
	if got := stringValue(env["ANTHROPIC_API_KEY"]); got != managedAnthropicFakeAPIKey {
		t.Fatalf("ANTHROPIC_API_KEY = %q, want fake key", got)
	}
	if got := stringValue(env["ANTHROPIC_AUTH_TOKEN"]); got != managedAnthropicFakeAuthToken {
		t.Fatalf("ANTHROPIC_AUTH_TOKEN = %q, want fake token", got)
	}
	if got := stringValue(env["ANTHROPIC_BASE_URL"]); got != "https://api.anthropic.com" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q, want canonical managed base URL", got)
	}
	extraArgs := mapValue(engine["extra_args"])
	if _, ok := extraArgs["bare"]; ok {
		t.Fatalf("extra_args.bare is set, want omitted")
	}
}

func TestApplyManagedLLMEnvPreservesExistingExtraArgs(t *testing.T) {
	engine, _, err := applyManagedLLMEnv("claude", map[string]any{
		"extra_args": map[string]any{
			"debug-file": "/tmp/debug.log",
		},
	}, testLLMVaultCredentials(testLLMStaticBearerCredential("vcrd_123", "secret-token")))
	if err != nil {
		t.Fatalf("applyManagedLLMEnv: %v", err)
	}
	extraArgs := mapValue(engine["extra_args"])
	if got := stringValue(extraArgs["debug-file"]); got != "/tmp/debug.log" {
		t.Fatalf("extra_args[debug-file] = %q, want /tmp/debug.log", got)
	}
	if _, ok := extraArgs["bare"]; ok {
		t.Fatalf("extra_args.bare is set, want omitted")
	}
}

func TestApplyManagedLLMEnvRejectsConflictingBaseURL(t *testing.T) {
	_, _, err := applyManagedLLMEnv("claude", map[string]any{"env": map[string]any{
		"ANTHROPIC_BASE_URL": "https://proxy.example.com",
	}}, testLLMVaultCredentials(testLLMStaticBearerCredential("vcrd_123", "secret-token")))
	if err == nil || !strings.Contains(err.Error(), "conflicts with engine ANTHROPIC_BASE_URL") {
		t.Fatalf("applyManagedLLMEnv error = %v, want base URL conflict", err)
	}
}

func TestApplyManagedLLMEnvRejectsMultipleCredentialsInLLMVault(t *testing.T) {
	_, _, err := applyManagedLLMEnv("claude", map[string]any{}, testLLMVaultCredentials(
		testLLMStaticBearerCredential("vcrd_123", "secret-token-1"),
		testLLMStaticBearerCredential("vcrd_456", "secret-token-2"),
	))
	if err == nil || !strings.Contains(err.Error(), "exactly one active credential") {
		t.Fatalf("applyManagedLLMEnv error = %v, want multiple credential rejection", err)
	}
}

func TestApplyManagedLLMEnvRejectsBoundLLMCredential(t *testing.T) {
	credential := testLLMStaticBearerCredential("vcrd_123", "secret-token")
	credential.Snapshot.Auth.MCPServerURL = "https://api.anthropic.com"
	credential.Secret["mcp_server_url"] = "https://api.anthropic.com"
	_, _, err := applyManagedLLMEnv("claude", map[string]any{}, testLLMVaultCredentials(credential))
	if err == nil || !strings.Contains(err.Error(), "must not set mcp_server_url") {
		t.Fatalf("applyManagedLLMEnv error = %v, want bound credential rejection", err)
	}
}

func TestApplyManagedLLMEnvRejectsOAuthCredential(t *testing.T) {
	_, _, err := applyManagedLLMEnv("claude", map[string]any{}, testLLMVaultCredentials(gatewaymanagedagents.StoredCredential{
		Snapshot: gatewaymanagedagents.Credential{
			ID:   "vcrd_123",
			Auth: gatewaymanagedagents.CredentialAuth{Type: "mcp_oauth", MCPServerURL: "https://api.anthropic.com"},
		},
		Secret: map[string]any{"type": "mcp_oauth", "access_token": "oauth-token"},
	}))
	if err == nil || !strings.Contains(err.Error(), "must use static_bearer") {
		t.Fatalf("applyManagedLLMEnv error = %v, want oauth rejection", err)
	}
}

func TestApplyManagedLLMEnvDefaultsAnthropicBaseURL(t *testing.T) {
	engine, credential, err := applyManagedLLMEnv("claude", map[string]any{}, testLLMVaultCredentialsWithBaseURL("",
		testLLMStaticBearerCredential("vcrd_123", "secret-token"),
	))
	if err != nil {
		t.Fatalf("applyManagedLLMEnv: %v", err)
	}
	if credential == nil || credential.BaseURL != managedAnthropicDefaultBaseURL {
		t.Fatalf("credential base URL = %#v, want default anthropic URL", credential)
	}
	env := mapValue(engine["env"])
	if got := stringValue(env["ANTHROPIC_BASE_URL"]); got != managedAnthropicDefaultBaseURL {
		t.Fatalf("ANTHROPIC_BASE_URL = %q, want default anthropic URL", got)
	}
}

func TestApplyManagedLLMEnvInjectsCodexCredential(t *testing.T) {
	engine, credential, err := applyManagedLLMEnv("codex", map[string]any{}, testLLMVaultCredentialsForEngineWithBaseURL(gatewaymanagedagents.ManagedAgentsEngineCodex, "https://api.openai.com/v1",
		testLLMStaticBearerCredential("vcrd_123", "secret-token"),
	))
	if err != nil {
		t.Fatalf("applyManagedLLMEnv: %v", err)
	}
	if credential == nil {
		t.Fatal("expected managed llm credential")
	}
	env := mapValue(engine["env"])
	if got := stringValue(env["CODEX_API_KEY"]); got != managedCodexFakeAPIKey {
		t.Fatalf("CODEX_API_KEY = %q, want fake key", got)
	}
	if got := stringValue(env["OPENAI_API_KEY"]); got != managedCodexFakeAPIKey {
		t.Fatalf("OPENAI_API_KEY = %q, want fake key", got)
	}
	if got := stringValue(engine["model_provider"]); got != "" {
		t.Fatalf("model_provider = %q, want empty", got)
	}
	if got := stringValue(engine["openai_base_url"]); got != "https://api.openai.com/v1" {
		t.Fatalf("openai_base_url = %q, want OpenAI base URL", got)
	}
	if credential.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("credential base URL = %q, want OpenAI base URL", credential.BaseURL)
	}
}

func TestApplyManagedLLMEnvDefaultsCodexBaseURL(t *testing.T) {
	engine, credential, err := applyManagedLLMEnv("codex", map[string]any{}, testLLMVaultCredentialsForEngineWithBaseURL(gatewaymanagedagents.ManagedAgentsEngineCodex, "",
		testLLMStaticBearerCredential("vcrd_123", "secret-token"),
	))
	if err != nil {
		t.Fatalf("applyManagedLLMEnv: %v", err)
	}
	if credential == nil || credential.BaseURL != managedOpenAIDefaultBaseURL {
		t.Fatalf("credential base URL = %#v, want default OpenAI URL", credential)
	}
	if got := stringValue(engine["openai_base_url"]); got != managedOpenAIDefaultBaseURL {
		t.Fatalf("openai_base_url = %q, want default OpenAI URL", got)
	}
}

func TestApplyManagedLLMEnvUsesVaultBackedMiniMaxCodexCredential(t *testing.T) {
	engine, credential, err := applyManagedLLMEnv("codex", map[string]any{
		"env": map[string]any{
			"MINIMAX_API_KEY": "direct-minimax-key",
			"MINIMAX_TOKEN":   "direct-minimax-token",
			"CODEX_API_KEY":   "direct-codex-key",
			"OPENAI_API_KEY":  "direct-openai-key",
		},
	}, testLLMVaultCredentialsForEngineWithBaseURL(gatewaymanagedagents.ManagedAgentsEngineCodex, "https://api.minimax.io/v1",
		testLLMStaticBearerCredential("vcrd_123", "secret-token"),
	))
	if err != nil {
		t.Fatalf("applyManagedLLMEnv: %v", err)
	}
	if credential == nil {
		t.Fatal("expected managed llm credential")
	}
	env := mapValue(engine["env"])
	if got := stringValue(env["MINIMAX_API_KEY"]); got != managedCodexFakeAPIKey {
		t.Fatalf("MINIMAX_API_KEY = %q, want fake sandbox key", got)
	}
	if got := stringValue(env["MINIMAX_TOKEN"]); got != "" {
		t.Fatalf("MINIMAX_TOKEN = %q, want empty", got)
	}
	if got := stringValue(env["CODEX_API_KEY"]); got != "" {
		t.Fatalf("CODEX_API_KEY = %q, want empty for MiniMax provider", got)
	}
	if got := stringValue(env["OPENAI_API_KEY"]); got != "" {
		t.Fatalf("OPENAI_API_KEY = %q, want empty for MiniMax provider", got)
	}
	if got := stringValue(engine["model_provider"]); got != "minimax" {
		t.Fatalf("model_provider = %q, want minimax", got)
	}
	if got := stringValue(engine["openai_base_url"]); got != "https://api.minimax.io/v1" {
		t.Fatalf("openai_base_url = %q, want MiniMax base URL", got)
	}
	if credential.BaseURL != "https://api.minimax.io/v1" {
		t.Fatalf("credential base URL = %q, want MiniMax base URL", credential.BaseURL)
	}
	if credential.Provider != "minimax" {
		t.Fatalf("credential provider = %q, want minimax", credential.Provider)
	}
	if credential.Token != "secret-token" {
		t.Fatalf("credential token = %q, want original vault token", credential.Token)
	}
}

func TestApplyManagedLLMEnvRejectsConflictingCodexBaseURL(t *testing.T) {
	_, _, err := applyManagedLLMEnv("codex", map[string]any{"openai_base_url": "https://proxy.example.com/v1"}, testLLMVaultCredentialsForEngineWithBaseURL(gatewaymanagedagents.ManagedAgentsEngineCodex, "https://api.openai.com/v1",
		testLLMStaticBearerCredential("vcrd_123", "secret-token"),
	))
	if err == nil || !strings.Contains(err.Error(), "conflicts with engine openai_base_url") {
		t.Fatalf("applyManagedLLMEnv error = %v, want base URL conflict", err)
	}
}

func TestManagedLLMCredentialBindingUsesDualProjection(t *testing.T) {
	binding, err := managedLLMCredentialBinding("sesn_123", "claude", &managedLLMCredential{
		CredentialID: "vcrd_123",
		Token:        "secret-token",
		BaseURL:      "https://api.anthropic.com",
	})
	if err != nil {
		t.Fatalf("managedLLMCredentialBinding: %v", err)
	}
	if binding == nil {
		t.Fatal("expected binding")
	}
	if len(binding.projectionHeaders) != 2 {
		t.Fatalf("projection headers = %#v, want 2", binding.projectionHeaders)
	}
	if binding.projectionHeaders[0].name != "X-Api-Key" || binding.projectionHeaders[0].valueTemplate != "{{ .x_api_key }}" {
		t.Fatalf("projection headers[0] = %#v", binding.projectionHeaders[0])
	}
	if binding.projectionHeaders[1].name != "Authorization" || binding.projectionHeaders[1].valueTemplate != "{{ .authorization }}" {
		t.Fatalf("projection headers[1] = %#v", binding.projectionHeaders[1])
	}
	if binding.secretValues["x_api_key"] != "secret-token" || binding.secretValues["authorization"] != "Bearer secret-token" {
		t.Fatalf("binding secrets = %#v", binding.secretValues)
	}
	if binding.tlsMode != apispec.EgressTLSModeTerminateReoriginate {
		t.Fatalf("binding tls mode = %q, want terminate-reoriginate", binding.tlsMode)
	}
	if len(binding.domains) != 1 || binding.domains[0] != "api.anthropic.com" {
		t.Fatalf("binding domains = %#v", binding.domains)
	}
}

func TestManagedLLMCredentialBindingAllowsVaultBaseURLDomain(t *testing.T) {
	engine, credential, err := applyManagedLLMEnv("claude", map[string]any{}, testLLMVaultCredentialsWithBaseURL("https://LLM.Proxy.Example.com/v1",
		testLLMStaticBearerCredential("vcrd_123", "secret-token"),
	))
	if err != nil {
		t.Fatalf("applyManagedLLMEnv: %v", err)
	}
	if got := stringValue(mapValue(engine["env"])["ANTHROPIC_BASE_URL"]); got != "https://llm.proxy.example.com/v1" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q, want canonical vault base URL", got)
	}
	binding, err := managedLLMCredentialBinding("sesn_123", "claude", credential)
	if err != nil {
		t.Fatalf("managedLLMCredentialBinding: %v", err)
	}
	merged := mergeManagedCredentialPolicy(apispec.SandboxNetworkPolicy{Mode: apispec.SandboxNetworkPolicyModeBlockAll}, "sesn_123", []managedCredentialBinding{*binding})
	egress, ok := merged.Egress.Get()
	if !ok || !containsString(egress.AllowedDomains, "llm.proxy.example.com") {
		t.Fatalf("allowed domains = %#v, want vault LLM host", egress.AllowedDomains)
	}
}

func TestManagedLLMCredentialBindingUsesMiniMaxVaultToken(t *testing.T) {
	binding, err := managedLLMCredentialBinding("sesn_123", "codex", &managedLLMCredential{
		CredentialID: "vcrd_123",
		Token:        "secret-token",
		BaseURL:      "https://API.MINIMAX.IO/v1",
	})
	if err != nil {
		t.Fatalf("managedLLMCredentialBinding: %v", err)
	}
	if binding == nil {
		t.Fatal("expected binding")
	}
	if len(binding.domains) != 1 || binding.domains[0] != "api.minimax.io" {
		t.Fatalf("binding domains = %#v, want api.minimax.io", binding.domains)
	}
	if binding.secretValues["x_api_key"] != "secret-token" || binding.secretValues["authorization"] != "Bearer secret-token" {
		t.Fatalf("binding secrets = %#v", binding.secretValues)
	}
}

func TestWorkspaceMountedPathToVolumePathRejectsSiblingPrefix(t *testing.T) {
	got := workspaceMountedPathToVolumePath("/workspace", "/workspace-other/.claude")
	if got != "" {
		t.Fatalf("volume path = %q, want empty", got)
	}
}

func TestWorkspaceVolumePathToMountedPathUsesWorkspaceMount(t *testing.T) {
	got := workspaceVolumePathToMountedPath("/workspace", "/.sandbox0/bootstrap/demo.tar.gz")
	if got != "/workspace/.sandbox0/bootstrap/demo.tar.gz" {
		t.Fatalf("mounted path = %q, want workspace-mounted path", got)
	}
}

func TestSkillBundleWorkspaceVolumePathUsesVersionScopedDirectory(t *testing.T) {
	got := skillBundleWorkspaceVolumePath("skillver_123")
	if got != "/.sandbox0/managed-agents/skills/skillver-123/bundle.tar.gz" {
		t.Fatalf("bundle volume path = %q", got)
	}
}

func TestMaterializeAgentSkillsRejectsAnthropicPrebuiltSkill(t *testing.T) {
	manager := &SDKRuntimeManager{}
	_, err := manager.materializeAgentSkills(t.Context(), nil, "sbox_123", "vol_123", "team_123", "/workspace", "claude", map[string]any{}, map[string]any{
		"skills": []any{map[string]any{"type": "anthropic", "skill_id": "xlsx", "version": "1"}},
	})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("error = %v, want pre-built skill rejection", err)
	}
}

func TestCloneFileResourceEntriesUsesBatchCowClone(t *testing.T) {
	client := &recordingVolumeFileCloneClient{}
	entries := []apispec.CloneVolumeFileEntry{
		{
			SourceVolumeID: "vol_assets",
			SourcePath:     "/managed-agents-assets/files/file_1/content",
			TargetPath:     "/mnt/session/uploads/file_1",
			Overwrite:      apispec.NewOptBool(true),
			CreateParents:  apispec.NewOptBool(true),
		},
		{
			SourceVolumeID: "vol_assets",
			SourcePath:     "/managed-agents-assets/files/file_2/content",
			TargetPath:     "/workspace/input.txt",
			Overwrite:      apispec.NewOptBool(true),
			CreateParents:  apispec.NewOptBool(true),
		},
	}

	if err := cloneFileResourceEntries(t.Context(), client, "vol_workspace", entries); err != nil {
		t.Fatalf("cloneFileResourceEntries: %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("CloneVolumeFiles calls = %d, want 1", client.calls)
	}
	if client.volumeID != "vol_workspace" {
		t.Fatalf("target volume = %q, want vol_workspace", client.volumeID)
	}
	mode, ok := client.request.Mode.Get()
	if !ok || mode != apispec.CloneVolumeFilesRequestModeCowOrCopy {
		t.Fatalf("clone mode = %q set=%v, want cow_or_copy", mode, ok)
	}
	atomic, ok := client.request.Atomic.Get()
	if !ok || !atomic {
		t.Fatalf("atomic = %v set=%v, want true", atomic, ok)
	}
	if len(client.request.Entries) != len(entries) {
		t.Fatalf("entries = %d, want %d", len(client.request.Entries), len(entries))
	}
	for i, entry := range client.request.Entries {
		if entry.SourceVolumeID != entries[i].SourceVolumeID || entry.SourcePath != entries[i].SourcePath || entry.TargetPath != entries[i].TargetPath {
			t.Fatalf("entry[%d] = %#v, want %#v", i, entry, entries[i])
		}
		overwrite, overwriteSet := entry.Overwrite.Get()
		createParents, createParentsSet := entry.CreateParents.Get()
		if !overwriteSet || !overwrite || !createParentsSet || !createParents {
			t.Fatalf("entry[%d] overwrite=%v/%v create_parents=%v/%v, want both true", i, overwrite, overwriteSet, createParents, createParentsSet)
		}
	}
}

func TestCloneFileResourceEntriesRetriesTransientFailures(t *testing.T) {
	client := &recordingVolumeFileCloneClient{failures: 2}
	err := cloneFileResourceEntries(t.Context(), client, "vol_workspace", []apispec.CloneVolumeFileEntry{{
		SourceVolumeID: "vol_assets",
		SourcePath:     "/file.txt",
		TargetPath:     "/file.txt",
	}})
	if err != nil {
		t.Fatalf("cloneFileResourceEntries: %v", err)
	}
	if client.calls != 3 {
		t.Fatalf("CloneVolumeFiles calls = %d, want 3", client.calls)
	}
}

func TestCloneFileResourceEntriesFallsBackToCopyWhenCloneEndpointIsMissing(t *testing.T) {
	client := &recordingVolumeFileCloneClient{
		cloneErr: &sandbox0sdk.APIError{StatusCode: http.StatusNotFound, Code: "not_found", Message: "not found"},
		content: map[string][]byte{
			"vol_assets:/file.txt": []byte("file content"),
		},
	}
	err := cloneFileResourceEntries(t.Context(), client, "vol_workspace", []apispec.CloneVolumeFileEntry{{
		SourceVolumeID: "vol_assets",
		SourcePath:     "/file.txt",
		TargetPath:     "/nested/file.txt",
	}})
	if err != nil {
		t.Fatalf("cloneFileResourceEntries: %v", err)
	}
	if client.calls != volumeFileOperationAttempts {
		t.Fatalf("CloneVolumeFiles calls = %d, want %d", client.calls, volumeFileOperationAttempts)
	}
	if len(client.reads) != 1 || client.reads[0] != "vol_assets:/file.txt" {
		t.Fatalf("reads = %#v", client.reads)
	}
	if len(client.mkdirs) != 1 || client.mkdirs[0] != "vol_workspace:/nested" {
		t.Fatalf("mkdirs = %#v", client.mkdirs)
	}
	if got := string(client.writes["vol_workspace:/nested/file.txt"]); got != "file content" {
		t.Fatalf("written content = %q", got)
	}
}

func TestCloneFileResourceEntriesDoesNotFallbackForSpecificCloneNotFound(t *testing.T) {
	client := &recordingVolumeFileCloneClient{
		cloneErr: &sandbox0sdk.APIError{StatusCode: http.StatusNotFound, Code: "not_found", Message: "source path not found"},
	}
	err := cloneFileResourceEntries(t.Context(), client, "vol_workspace", []apispec.CloneVolumeFileEntry{{
		SourceVolumeID: "vol_assets",
		SourcePath:     "/missing.txt",
		TargetPath:     "/missing.txt",
	}})
	if err == nil || !strings.Contains(err.Error(), "source path not found") {
		t.Fatalf("error = %v, want clone source not found error", err)
	}
	if len(client.reads) != 0 || len(client.writes) != 0 {
		t.Fatalf("fallback reads=%#v writes=%#v, want none", client.reads, client.writes)
	}
}

type recordingVolumeFileCloneClient struct {
	calls    int
	failures int
	cloneErr error
	volumeID string
	request  apispec.CloneVolumeFilesRequest
	content  map[string][]byte
	reads    []string
	mkdirs   []string
	writes   map[string][]byte
}

func (c *recordingVolumeFileCloneClient) CloneVolumeFiles(_ context.Context, volumeID string, request apispec.CloneVolumeFilesRequest) ([]apispec.CloneVolumeFileResult, error) {
	c.calls++
	c.volumeID = volumeID
	c.request = request
	if c.cloneErr != nil {
		return nil, c.cloneErr
	}
	if c.calls <= c.failures {
		return nil, errors.New("temporary clone failure")
	}
	results := make([]apispec.CloneVolumeFileResult, 0, len(request.Entries))
	for _, entry := range request.Entries {
		results = append(results, apispec.CloneVolumeFileResult{
			SourceVolumeID: entry.SourceVolumeID,
			SourcePath:     entry.SourcePath,
			TargetPath:     entry.TargetPath,
			Mode:           apispec.CloneVolumeFileResultModeCow,
		})
	}
	return results, nil
}

func (c *recordingVolumeFileCloneClient) ReadVolumeFile(_ context.Context, volumeID, filePath string) ([]byte, error) {
	key := volumeID + ":" + filePath
	c.reads = append(c.reads, key)
	content, ok := c.content[key]
	if !ok {
		return nil, errors.New("missing fake content")
	}
	return content, nil
}

func (c *recordingVolumeFileCloneClient) MkdirVolumeFile(_ context.Context, volumeID, filePath string, recursive bool) (*apispec.SuccessCreatedResponse, error) {
	if !recursive {
		return nil, errors.New("recursive mkdir required")
	}
	c.mkdirs = append(c.mkdirs, volumeID+":"+filePath)
	return &apispec.SuccessCreatedResponse{}, nil
}

func (c *recordingVolumeFileCloneClient) WriteVolumeFile(_ context.Context, volumeID, filePath string, data []byte) (*apispec.SuccessWrittenResponse, error) {
	if c.writes == nil {
		c.writes = make(map[string][]byte)
	}
	c.writes[volumeID+":"+filePath] = append([]byte(nil), data...)
	return &apispec.SuccessWrittenResponse{}, nil
}
