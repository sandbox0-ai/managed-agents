package managedagentsruntime

import (
	"strings"
	"testing"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
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
	metadata := map[string]string{
		gatewaymanagedagents.ManagedAgentsVaultRoleKey:   gatewaymanagedagents.ManagedAgentsVaultRoleLLM,
		gatewaymanagedagents.ManagedAgentsVaultEngineKey: gatewaymanagedagents.ManagedAgentsEngineClaude,
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
	if got, ok := extraArgs["bare"]; !ok || got != nil {
		t.Fatalf("extra_args.bare = %#v, want explicit null flag", got)
	}
	if credential.VaultID != "vlt_llm" {
		t.Fatalf("credential vault id = %q, want vlt_llm", credential.VaultID)
	}
	if credential.Token != "secret-token" {
		t.Fatalf("credential token = %q, want secret-token", credential.Token)
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
	if got, ok := extraArgs["bare"]; !ok || got != nil {
		t.Fatalf("extra_args.bare = %#v, want explicit null flag", got)
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
	if got, ok := extraArgs["bare"]; !ok || got != nil {
		t.Fatalf("extra_args.bare = %#v, want explicit null flag", got)
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

func TestNormalizedStoredSkillRelativePathPreservesTopLevelDirectory(t *testing.T) {
	got := normalizedStoredSkillRelativePath("demo-skill", "demo-skill/SKILL.md")
	if got != "demo-skill/SKILL.md" {
		t.Fatalf("relative path = %q, want demo-skill/SKILL.md", got)
	}
}

func TestNormalizedStoredSkillRelativePathPrefixesDirectoryWhenUploadWasFlat(t *testing.T) {
	got := normalizedStoredSkillRelativePath("demo-skill", "SKILL.md")
	if got != "demo-skill/SKILL.md" {
		t.Fatalf("relative path = %q, want demo-skill/SKILL.md", got)
	}
}

func TestSkillFileTargetPathPlacesFilesUnderProjectSkillsDirectory(t *testing.T) {
	got := skillFileTargetPath("/workspace", "demo-skill", "docs/guide.md")
	if got != "/workspace/.claude/skills/demo-skill/docs/guide.md" {
		t.Fatalf("target path = %q", got)
	}
}

func TestMaterializeAgentSkillsRejectsAnthropicPrebuiltSkill(t *testing.T) {
	manager := &SDKRuntimeManager{}
	_, err := manager.materializeAgentSkills(t.Context(), nil, "vol_123", "team_123", "/workspace", "claude", map[string]any{}, map[string]any{
		"skills": []any{map[string]any{"type": "anthropic", "skill_id": "xlsx", "version": "1"}},
	})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("error = %v, want pre-built skill rejection", err)
	}
}
