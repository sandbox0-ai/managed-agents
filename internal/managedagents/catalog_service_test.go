package managedagents

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sandbox0-ai/managed-agent/internal/dbpool"
	managedagentmigrations "github.com/sandbox0-ai/managed-agent/internal/managedagents/migrations"
	"github.com/sandbox0-ai/managed-agent/internal/migrate"
)

func TestRedactCredentialAuthRemovesSecrets(t *testing.T) {
	redacted := redactCredentialAuth(map[string]any{
		"type":         "mcp_oauth",
		"access_token": "secret-access",
		"refresh": map[string]any{
			"refresh_token": "secret-refresh",
			"token_endpoint_auth": map[string]any{
				"type":          "client_secret_post",
				"client_secret": "secret-client",
			},
		},
	})
	if _, ok := redacted["access_token"]; ok {
		t.Fatal("expected access_token to be removed")
	}
	refresh, ok := redacted["refresh"].(map[string]any)
	if !ok {
		t.Fatal("expected refresh object")
	}
	if _, ok := refresh["refresh_token"]; ok {
		t.Fatal("expected refresh_token to be removed")
	}
	auth, ok := refresh["token_endpoint_auth"].(map[string]any)
	if !ok {
		t.Fatal("expected token_endpoint_auth object")
	}
	if _, ok := auth["client_secret"]; ok {
		t.Fatal("expected client_secret to be removed")
	}
}

func TestNormalizeCreateCredentialAuthStaticBearer(t *testing.T) {
	normalized, err := normalizeCreateCredentialAuth(map[string]any{
		"type":           "static_bearer",
		"token":          "bearer-secret",
		"mcp_server_url": "https://mcp.example.com/sse",
	})
	if err != nil {
		t.Fatalf("normalizeCreateCredentialAuth: %v", err)
	}
	if got := stringValue(normalized.Public["type"]); got != "static_bearer" {
		t.Fatalf("public auth type = %q, want static_bearer", got)
	}
	if got := stringValue(normalized.Public["mcp_server_url"]); got != "https://mcp.example.com/sse" {
		t.Fatalf("public auth mcp_server_url = %q", got)
	}
	if _, ok := normalized.Public["token"]; ok {
		t.Fatal("public auth must not expose token")
	}
	if got := stringValue(normalized.Secret["token"]); got != "bearer-secret" {
		t.Fatalf("secret token = %q, want bearer-secret", got)
	}
}

func TestNormalizeUpdateCredentialAuthMcpOAuth(t *testing.T) {
	normalized, err := normalizeUpdateCredentialAuth(
		map[string]any{
			"type":           "mcp_oauth",
			"mcp_server_url": "https://mcp.example.com/sse",
			"refresh": map[string]any{
				"token_endpoint": "https://auth.example.com/token",
				"client_id":      "client-1",
				"token_endpoint_auth": map[string]any{
					"type": "client_secret_basic",
				},
			},
		},
		map[string]any{
			"type":           "mcp_oauth",
			"mcp_server_url": "https://mcp.example.com/sse",
			"access_token":   "old-access",
			"refresh": map[string]any{
				"refresh_token":  "old-refresh",
				"token_endpoint": "https://auth.example.com/token",
				"client_id":      "client-1",
				"token_endpoint_auth": map[string]any{
					"type":          "client_secret_basic",
					"client_secret": "old-secret",
				},
			},
		},
		map[string]any{
			"type":         "mcp_oauth",
			"access_token": "new-access",
			"refresh": map[string]any{
				"refresh_token": "new-refresh",
				"scope":         "scope-a",
				"token_endpoint_auth": map[string]any{
					"type":          "client_secret_post",
					"client_secret": "new-secret",
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("normalizeUpdateCredentialAuth: %v", err)
	}
	if got := stringValue(normalized.Secret["access_token"]); got != "new-access" {
		t.Fatalf("access_token = %q, want new-access", got)
	}
	refresh := mapValue(normalized.Public["refresh"])
	if got := stringValue(refresh["scope"]); got != "scope-a" {
		t.Fatalf("refresh scope = %q, want scope-a", got)
	}
	refreshSecret := mapValue(normalized.Secret["refresh"])
	if got := stringValue(refreshSecret["refresh_token"]); got != "new-refresh" {
		t.Fatalf("refresh_token = %q, want new-refresh", got)
	}
	tokenEndpointAuth := mapValue(refresh["token_endpoint_auth"])
	if got := stringValue(tokenEndpointAuth["type"]); got != "client_secret_post" {
		t.Fatalf("token_endpoint_auth.type = %q, want client_secret_post", got)
	}
	tokenEndpointAuthSecret := mapValue(refreshSecret["token_endpoint_auth"])
	if got := stringValue(tokenEndpointAuthSecret["client_secret"]); got != "new-secret" {
		t.Fatalf("token_endpoint_auth.client_secret = %q, want new-secret", got)
	}
}

func TestNormalizeSessionResourceSetsIdentityAndTimestamps(t *testing.T) {
	when := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	resource := normalizeSessionResource(map[string]any{
		"type":       "file",
		"file_id":    "file_123",
		"mount_path": "/uploads/example.txt",
	}, when)
	if got := stringValue(resource["id"]); got == "" {
		t.Fatal("expected resource id to be assigned")
	}
	if got := stringValue(resource["created_at"]); got != when.Format(time.RFC3339) {
		t.Fatalf("created_at = %q, want %q", got, when.Format(time.RFC3339))
	}
	if got := stringValue(resource["updated_at"]); got != when.Format(time.RFC3339) {
		t.Fatalf("updated_at = %q, want %q", got, when.Format(time.RFC3339))
	}
}

func TestEnsureEnvironmentConfigProvidesCloudDefault(t *testing.T) {
	config := ensureEnvironmentConfig(nil)
	if got := stringValue(config["type"]); got != "cloud" {
		t.Fatalf("type = %q, want cloud", got)
	}
	networking, ok := config["networking"].(map[string]any)
	if !ok {
		t.Fatal("expected networking map")
	}
	if got := stringValue(networking["type"]); got != "limited" {
		t.Fatalf("networking.type = %q, want limited", got)
	}
}

func TestNormalizeAgentToolsResolvesDefaults(t *testing.T) {
	tools, err := normalizeAgentTools([]any{
		map[string]any{"type": "agent_toolset_20260401"},
		map[string]any{
			"type":            "mcp_toolset",
			"mcp_server_name": "docs",
			"configs": []any{
				map[string]any{"name": "search"},
			},
		},
	})
	if err != nil {
		t.Fatalf("normalizeAgentTools: %v", err)
	}
	agentToolset := mapValue(tools[0])
	defaultConfig := mapValue(agentToolset["default_config"])
	if got, ok := defaultConfig["enabled"].(bool); !ok || !got {
		t.Fatalf("default_config.enabled = %v, want true", defaultConfig["enabled"])
	}
	permissionPolicy := mapValue(defaultConfig["permission_policy"])
	if got := stringValue(permissionPolicy["type"]); got != "always_ask" {
		t.Fatalf("default_config.permission_policy.type = %q, want always_ask", got)
	}
	mcpToolset := mapValue(tools[1])
	configs, ok := mcpToolset["configs"].([]any)
	if !ok || len(configs) != 1 {
		t.Fatalf("configs = %#v, want 1 entry", mcpToolset["configs"])
	}
	config := mapValue(configs[0])
	if got := stringValue(config["name"]); got != "search" {
		t.Fatalf("config.name = %q, want search", got)
	}
	if got, ok := config["enabled"].(bool); !ok || !got {
		t.Fatalf("config.enabled = %v, want true", config["enabled"])
	}
}

func TestNormalizeUpdateEnvironmentConfigMergesExistingState(t *testing.T) {
	existing := map[string]any{
		"type": "cloud",
		"networking": map[string]any{
			"type":                   "limited",
			"allowed_hosts":          []any{"api.example.com"},
			"allow_package_managers": false,
			"allow_mcp_servers":      false,
		},
		"packages": map[string]any{
			"type":  "packages",
			"pip":   []any{"pandas"},
			"npm":   []any{},
			"apt":   []any{},
			"cargo": []any{},
			"gem":   []any{},
			"go":    []any{},
		},
	}
	updated, err := normalizeUpdateEnvironmentConfig(existing, map[string]any{
		"type": "cloud",
		"networking": map[string]any{
			"type":              "limited",
			"allow_mcp_servers": true,
		},
		"packages": map[string]any{
			"pip": []any{"numpy"},
		},
	})
	if err != nil {
		t.Fatalf("normalizeUpdateEnvironmentConfig: %v", err)
	}
	networking := mapValue(updated["networking"])
	allowedHosts, ok := networking["allowed_hosts"].([]any)
	if !ok || len(allowedHosts) != 1 || stringValue(allowedHosts[0]) != "api.example.com" {
		t.Fatalf("allowed_hosts = %#v, want existing host preserved", networking["allowed_hosts"])
	}
	if got, ok := networking["allow_mcp_servers"].(bool); !ok || !got {
		t.Fatalf("allow_mcp_servers = %v, want true", networking["allow_mcp_servers"])
	}
	packages := mapValue(updated["packages"])
	pip, ok := packages["pip"].([]any)
	if !ok || len(pip) != 1 || stringValue(pip[0]) != "numpy" {
		t.Fatalf("packages.pip = %#v, want numpy", packages["pip"])
	}
	npm, ok := packages["npm"].([]any)
	if !ok || len(npm) != 0 {
		t.Fatalf("packages.npm = %#v, want existing empty array", packages["npm"])
	}
}

func TestServiceCreateCredentialRejectsInvalidManagedLLMMetadata(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	ctx := context.Background()
	principal := Principal{TeamID: "team_123"}
	now := time.Now().UTC()
	vault := buildVaultObject("vault_123", CreateVaultRequest{DisplayName: "runtime"}, now, nil)
	if err := repo.CreateVault(ctx, principal.TeamID, vault, nil, now); err != nil {
		t.Fatalf("CreateVault: %v", err)
	}
	_, err := service.CreateCredential(ctx, principal, vault.ID, CreateCredentialRequest{
		Auth: map[string]any{
			"type":           "static_bearer",
			"token":          "secret-token",
			"mcp_server_url": "https://api.anthropic.com",
		},
		Metadata: map[string]string{
			ManagedAgentCredentialKindKey:     ManagedAgentCredentialKindLLM,
			ManagedAgentCredentialProviderKey: "openai",
		},
	})
	if err == nil || !strings.Contains(err.Error(), ManagedAgentCredentialProviderKey) {
		t.Fatalf("CreateCredential error = %v, want provider validation", err)
	}
}

func TestServiceUpdateCredentialRejectsManagedMetadataWithoutKind(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	ctx := context.Background()
	principal := Principal{TeamID: "team_123"}
	now := time.Now().UTC()
	vault := buildVaultObject("vault_123", CreateVaultRequest{DisplayName: "runtime"}, now, nil)
	if err := repo.CreateVault(ctx, principal.TeamID, vault, nil, now); err != nil {
		t.Fatalf("CreateVault: %v", err)
	}
	created, err := service.CreateCredential(ctx, principal, vault.ID, CreateCredentialRequest{
		Auth: map[string]any{
			"type":           "static_bearer",
			"token":          "secret-token",
			"mcp_server_url": "https://api.anthropic.com",
		},
	})
	if err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}
	baseURL := "https://api.anthropic.com"
	_, err = service.UpdateCredential(ctx, principal, vault.ID, created.ID, UpdateCredentialRequest{
		Metadata: MetadataPatchField{
			Set: true,
			Values: map[string]*string{
				ManagedAgentCredentialBaseURLKey: &baseURL,
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), ManagedAgentCredentialKindKey) {
		t.Fatalf("UpdateCredential error = %v, want managed kind validation", err)
	}
}

func TestServiceUpdateAgentRejectsVersionMismatch(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	principal := Principal{TeamID: "team_123"}
	created, err := service.CreateAgent(context.Background(), principal, CreateAgentRequest{
		Name:  "Claude Agent",
		Model: "claude-sonnet-4-5",
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	_, err = service.UpdateAgent(context.Background(), principal, created.ID, UpdateAgentRequest{
		Version: 99,
	})
	if err == nil || !strings.Contains(err.Error(), "invalid version") {
		t.Fatalf("UpdateAgent error = %v, want invalid version", err)
	}
}

func TestCreateSessionRejectsInvalidAgentReferenceObject(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	principal := Principal{TeamID: "team_123"}
	ctx := context.Background()

	environment := buildEnvironmentObject("env_123", CreateEnvironmentRequest{Name: "python", Config: defaultEnvironmentConfig()}, time.Now().UTC(), nil)
	if err := repo.CreateEnvironment(ctx, principal.TeamID, environment, nil, time.Now().UTC()); err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}

	_, err := service.CreateSession(ctx, principal, CreateSessionParams{
		Agent: map[string]any{
			"type":    "agent",
			"id":      "agent_123",
			"version": 1,
			"name":    "unexpected",
		},
		EnvironmentID: "env_123",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid resource field") {
		t.Fatalf("CreateSession error = %v, want invalid field rejection", err)
	}
}

func TestCreateSessionRejectsEmptyVaultID(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	principal := Principal{TeamID: "team_123"}
	ctx := context.Background()
	now := time.Now().UTC()

	environment := buildEnvironmentObject("env_123", CreateEnvironmentRequest{Name: "python", Config: defaultEnvironmentConfig()}, now, nil)
	if err := repo.CreateEnvironment(ctx, principal.TeamID, environment, nil, now); err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	agent := buildAgentObject("agent_123", 1, "claude", CreateAgentRequest{Name: "Claude Agent", Model: "claude-sonnet-4-5"}, now, nil)
	if err := repo.CreateAgent(ctx, principal.TeamID, "claude", 1, agent, now); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	_, err := service.CreateSession(ctx, principal, CreateSessionParams{
		Agent:         "agent_123",
		EnvironmentID: "env_123",
		VaultIDs:      []string{"   "},
	})
	if err == nil || !strings.Contains(err.Error(), "vault_ids entries must be non-empty") {
		t.Fatalf("CreateSession error = %v, want vault_ids rejection", err)
	}
}

func TestUpdateSessionSupportsVaultIDsAndRebootstrapsIdleRuntime(t *testing.T) {
	repo := newTestRepository(t)
	runtime := &updateSessionRuntimeManager{}
	service := NewService(repo, runtime, nil)
	principal := Principal{TeamID: "team_123", UserID: "user_123"}
	credential := RequestCredential{Token: "token_123"}
	ctx := context.Background()
	now := time.Now().UTC()

	environment := buildEnvironmentObject("env_123", CreateEnvironmentRequest{Name: "python", Config: defaultEnvironmentConfig()}, now, nil)
	if err := repo.CreateEnvironment(ctx, principal.TeamID, environment, nil, now); err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	agent := buildAgentObject("agent_123", 1, "claude", CreateAgentRequest{Name: "Claude Agent", Model: "claude-sonnet-4-5"}, now, nil)
	if err := repo.CreateAgent(ctx, principal.TeamID, "claude", 1, agent, now); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	vaultA := buildVaultObject("vlt_a", CreateVaultRequest{DisplayName: "Vault A"}, now, nil)
	if err := repo.CreateVault(ctx, principal.TeamID, vaultA, nil, now); err != nil {
		t.Fatalf("CreateVault A: %v", err)
	}
	vaultB := buildVaultObject("vlt_b", CreateVaultRequest{DisplayName: "Vault B"}, now, nil)
	if err := repo.CreateVault(ctx, principal.TeamID, vaultB, nil, now); err != nil {
		t.Fatalf("CreateVault B: %v", err)
	}

	session, err := service.CreateSession(ctx, principal, CreateSessionParams{
		Agent:         "agent_123",
		EnvironmentID: "env_123",
		VaultIDs:      []string{"vlt_a"},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := repo.UpsertRuntime(ctx, &RuntimeRecord{
		SessionID:           session.ID,
		Vendor:              "claude",
		RegionID:            "test-region",
		SandboxID:           "sbx_123",
		WorkspaceVolumeID:   "vol_workspace",
		EngineStateVolumeID: "vol_state",
		ControlToken:        "ctl_123",
		RuntimeGeneration:   1,
		CreatedAt:           now,
		UpdatedAt:           now,
	}); err != nil {
		t.Fatalf("UpsertRuntime: %v", err)
	}

	updated, err := service.UpdateSession(ctx, principal, credential, session.ID, UpdateSessionParams{
		VaultIDs: StringSliceField{Set: true, Values: []string{"vlt_b"}},
	})
	if err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}
	if len(updated.VaultIDs) != 1 || updated.VaultIDs[0] != "vlt_b" {
		t.Fatalf("updated vault_ids = %#v, want [vlt_b]", updated.VaultIDs)
	}
	stored, _, err := repo.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if len(stored.VaultIDs) != 1 || stored.VaultIDs[0] != "vlt_b" {
		t.Fatalf("stored vault_ids = %#v, want [vlt_b]", stored.VaultIDs)
	}
	if len(runtime.bootstrapReqs) != 1 {
		t.Fatalf("bootstrap calls = %d, want 1", len(runtime.bootstrapReqs))
	}
	if len(runtime.bootstrapReqs[0].VaultIDs) != 1 || runtime.bootstrapReqs[0].VaultIDs[0] != "vlt_b" {
		t.Fatalf("bootstrap vault_ids = %#v, want [vlt_b]", runtime.bootstrapReqs[0].VaultIDs)
	}
}

func TestUpdateSessionRejectsVaultIDsChangeWhileRunIsActive(t *testing.T) {
	repo := newTestRepository(t)
	runtime := &updateSessionRuntimeManager{}
	service := NewService(repo, runtime, nil)
	principal := Principal{TeamID: "team_123", UserID: "user_123"}
	credential := RequestCredential{Token: "token_123"}
	ctx := context.Background()
	now := time.Now().UTC()

	environment := buildEnvironmentObject("env_123", CreateEnvironmentRequest{Name: "python", Config: defaultEnvironmentConfig()}, now, nil)
	if err := repo.CreateEnvironment(ctx, principal.TeamID, environment, nil, now); err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	agent := buildAgentObject("agent_123", 1, "claude", CreateAgentRequest{Name: "Claude Agent", Model: "claude-sonnet-4-5"}, now, nil)
	if err := repo.CreateAgent(ctx, principal.TeamID, "claude", 1, agent, now); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	vaultA := buildVaultObject("vlt_a", CreateVaultRequest{DisplayName: "Vault A"}, now, nil)
	if err := repo.CreateVault(ctx, principal.TeamID, vaultA, nil, now); err != nil {
		t.Fatalf("CreateVault A: %v", err)
	}
	vaultB := buildVaultObject("vlt_b", CreateVaultRequest{DisplayName: "Vault B"}, now, nil)
	if err := repo.CreateVault(ctx, principal.TeamID, vaultB, nil, now); err != nil {
		t.Fatalf("CreateVault B: %v", err)
	}

	session, err := service.CreateSession(ctx, principal, CreateSessionParams{
		Agent:         "agent_123",
		EnvironmentID: "env_123",
		VaultIDs:      []string{"vlt_a"},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	activeRunID := "srun_123"
	if err := repo.UpsertRuntime(ctx, &RuntimeRecord{
		SessionID:           session.ID,
		Vendor:              "claude",
		RegionID:            "test-region",
		SandboxID:           "sbx_123",
		WorkspaceVolumeID:   "vol_workspace",
		EngineStateVolumeID: "vol_state",
		ControlToken:        "ctl_123",
		RuntimeGeneration:   1,
		ActiveRunID:         &activeRunID,
		CreatedAt:           now,
		UpdatedAt:           now,
	}); err != nil {
		t.Fatalf("UpsertRuntime: %v", err)
	}

	_, err = service.UpdateSession(ctx, principal, credential, session.ID, UpdateSessionParams{
		VaultIDs: StringSliceField{Set: true, Values: []string{"vlt_b"}},
	})
	if err == nil || !strings.Contains(err.Error(), "vault_ids cannot be updated while a run is active") {
		t.Fatalf("UpdateSession error = %v, want active-run rejection", err)
	}
	if len(runtime.bootstrapReqs) != 0 {
		t.Fatalf("bootstrap calls = %d, want 0", len(runtime.bootstrapReqs))
	}
}

func TestListAgentVersionsSupportsCursorPagination(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	principal := Principal{TeamID: "team_123"}
	baseTime := time.Date(2026, 4, 10, 1, 0, 0, 0, time.UTC)

	agent := Agent{
		ID:         "agent_123",
		Type:       "agent",
		Version:    1,
		Name:       "Claude Agent",
		Model:      SessionModelConfig{ID: "claude-sonnet-4-20250514", Speed: "standard"},
		Tools:      []AgentTool{},
		MCPServers: []MCPServer{},
		Skills:     []AgentSkill{},
		Metadata:   map[string]string{},
		CreatedAt:  baseTime.Format(time.RFC3339),
		UpdatedAt:  baseTime.Format(time.RFC3339),
	}
	if err := repo.CreateAgent(context.Background(), principal.TeamID, "claude", 1, agent, baseTime); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	for version := 2; version <= 3; version++ {
		when := baseTime.Add(time.Duration(version-1) * time.Minute)
		snapshot := agent
		snapshot.Version = version
		snapshot.UpdatedAt = when.Format(time.RFC3339)
		if err := repo.UpdateAgent(context.Background(), principal.TeamID, "agent_123", "claude", version-1, version, &snapshot, nil, when); err != nil {
			t.Fatalf("UpdateAgent version %d: %v", version, err)
		}
		agent = snapshot
	}

	page1, nextPage, err := service.ListAgentVersions(context.Background(), principal, "agent_123", 2, "")
	if err != nil {
		t.Fatalf("ListAgentVersions page1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page1 len = %d, want 2", len(page1))
	}
	if got := page1[0].Version; got != 3 {
		t.Fatalf("page1[0].version = %d, want 3", got)
	}
	if got := page1[1].Version; got != 2 {
		t.Fatalf("page1[1].version = %d, want 2", got)
	}
	if nextPage == nil || *nextPage == "" {
		t.Fatal("expected next_page token")
	}

	page2, finalNextPage, err := service.ListAgentVersions(context.Background(), principal, "agent_123", 2, *nextPage)
	if err != nil {
		t.Fatalf("ListAgentVersions page2: %v", err)
	}
	if len(page2) != 1 {
		t.Fatalf("page2 len = %d, want 1", len(page2))
	}
	if got := page2[0].Version; got != 1 {
		t.Fatalf("page2[0].version = %d, want 1", got)
	}
	if finalNextPage != nil {
		t.Fatalf("final next_page = %v, want nil", *finalNextPage)
	}
}

type noopRuntimeManager struct{}

func (noopRuntimeManager) EnsureRuntime(context.Context, Principal, RequestCredential, *SessionRecord, map[string]any, string) (*RuntimeRecord, error) {
	return nil, nil
}

func (noopRuntimeManager) BootstrapSession(context.Context, RequestCredential, *RuntimeRecord, *WrapperSessionBootstrapRequest) error {
	return nil
}

func (noopRuntimeManager) StartRun(context.Context, RequestCredential, *RuntimeRecord, *WrapperRunRequest) error {
	return nil
}

func (noopRuntimeManager) ResolveActions(context.Context, RequestCredential, *RuntimeRecord, *WrapperResolveActionsRequest) (*WrapperResolveActionsResponse, error) {
	return nil, nil
}

func (noopRuntimeManager) InterruptRun(context.Context, RequestCredential, *RuntimeRecord, string) error {
	return nil
}

func (noopRuntimeManager) DeleteWrapperSession(context.Context, RequestCredential, *RuntimeRecord, string) error {
	return nil
}

func (noopRuntimeManager) DestroyRuntime(context.Context, RequestCredential, *RuntimeRecord) error {
	return nil
}

type updateSessionRuntimeManager struct {
	bootstrapReqs []*WrapperSessionBootstrapRequest
}

func (m *updateSessionRuntimeManager) EnsureRuntime(context.Context, Principal, RequestCredential, *SessionRecord, map[string]any, string) (*RuntimeRecord, error) {
	return nil, nil
}

func (m *updateSessionRuntimeManager) BootstrapSession(_ context.Context, _ RequestCredential, _ *RuntimeRecord, req *WrapperSessionBootstrapRequest) error {
	m.bootstrapReqs = append(m.bootstrapReqs, req)
	return nil
}

func (m *updateSessionRuntimeManager) StartRun(context.Context, RequestCredential, *RuntimeRecord, *WrapperRunRequest) error {
	return nil
}

func (m *updateSessionRuntimeManager) ResolveActions(context.Context, RequestCredential, *RuntimeRecord, *WrapperResolveActionsRequest) (*WrapperResolveActionsResponse, error) {
	return nil, nil
}

func (m *updateSessionRuntimeManager) InterruptRun(context.Context, RequestCredential, *RuntimeRecord, string) error {
	return nil
}

func (m *updateSessionRuntimeManager) DeleteWrapperSession(context.Context, RequestCredential, *RuntimeRecord, string) error {
	return nil
}

func (m *updateSessionRuntimeManager) DestroyRuntime(context.Context, RequestCredential, *RuntimeRecord) error {
	return nil
}

func newTestRepository(t *testing.T) *Repository {
	t.Helper()

	ctx := context.Background()
	dbURL := os.Getenv("INTEGRATION_DATABASE_URL")
	if dbURL == "" {
		dbURL = os.Getenv("TEST_DATABASE_URL")
	}
	if dbURL == "" {
		t.Skip("missing INTEGRATION_DATABASE_URL or TEST_DATABASE_URL")
		return nil
	}

	schema := fmt.Sprintf("managed_agents_test_%s", strings.ReplaceAll(uuid.NewString(), "-", ""))
	adminPool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(adminPool.Close)

	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	pool, err := dbpool.New(ctx, dbpool.Options{
		DatabaseURL: dbURL,
		Schema:      schema,
	})
	if err != nil {
		t.Fatalf("connect schema-scoped pool: %v", err)
	}
	t.Cleanup(pool.Close)
	t.Cleanup(func() {
		_, _ = adminPool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	})

	if err := migrate.Up(ctx, pool, ".", migrate.WithBaseFS(managedagentmigrations.FS), migrate.WithSchema(schema)); err != nil {
		t.Fatalf("migrate managed-agent schema: %v", err)
	}

	return NewRepository(pool)
}
