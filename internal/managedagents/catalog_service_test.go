package managedagents

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
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

func TestNormalizeCreateCredentialAuthStaticBearerAllowsUnboundSecret(t *testing.T) {
	normalized, err := normalizeCreateCredentialAuth(map[string]any{
		"type":  "static_bearer",
		"token": "bearer-secret",
	})
	if err != nil {
		t.Fatalf("normalizeCreateCredentialAuth: %v", err)
	}
	if got := stringValue(normalized.Public["type"]); got != "static_bearer" {
		t.Fatalf("public auth type = %q, want static_bearer", got)
	}
	if _, ok := normalized.Public["mcp_server_url"]; ok {
		t.Fatal("public auth must not set mcp_server_url when the credential is unbound")
	}
	if _, ok := normalized.Secret["mcp_server_url"]; ok {
		t.Fatal("secret auth must not set mcp_server_url when the credential is unbound")
	}
	if got := stringValue(normalized.Secret["token"]); got != "bearer-secret" {
		t.Fatalf("secret token = %q, want bearer-secret", got)
	}
	if _, ok := normalized.Public["token"]; ok {
		t.Fatal("public auth must not expose token")
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
	if got := stringValue(networking["type"]); got != "unrestricted" {
		t.Fatalf("networking.type = %q, want unrestricted", got)
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
	if got := stringValue(permissionPolicy["type"]); got != "always_allow" {
		t.Fatalf("default_config.permission_policy.type = %q, want always_allow", got)
	}
	mcpToolset := mapValue(tools[1])
	mcpDefaultConfig := mapValue(mcpToolset["default_config"])
	mcpPermissionPolicy := mapValue(mcpDefaultConfig["permission_policy"])
	if got := stringValue(mcpPermissionPolicy["type"]); got != "always_ask" {
		t.Fatalf("mcp default_config.permission_policy.type = %q, want always_ask", got)
	}
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

func TestValidateToolReferencesRequiresEveryMCPServerExactlyOnce(t *testing.T) {
	err := validateToolReferences([]any{
		map[string]any{"type": "mcp_toolset", "mcp_server_name": "docs"},
		map[string]any{"type": "mcp_toolset", "mcp_server_name": "docs"},
	}, map[string]struct{}{"docs": {}})
	if err == nil || !strings.Contains(err.Error(), "must not be referenced by more than one") {
		t.Fatalf("validateToolReferences duplicate error = %v", err)
	}

	err = validateToolReferences([]any{
		map[string]any{"type": "mcp_toolset", "mcp_server_name": "docs"},
	}, map[string]struct{}{"docs": {}, "search": {}})
	if err == nil || !strings.Contains(err.Error(), "must be referenced by exactly one") {
		t.Fatalf("validateToolReferences missing error = %v", err)
	}
}

func TestCanonicalMCPServerURLNormalizesForCredentialMatching(t *testing.T) {
	got, err := CanonicalMCPServerURL("HTTPS://MCP.Example.com:443/sse/?b=2&a=1#ignored")
	if err != nil {
		t.Fatalf("CanonicalMCPServerURL: %v", err)
	}
	if got != "https://mcp.example.com/sse?a=1&b=2" {
		t.Fatalf("canonical URL = %q, want normalized URL", got)
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

func TestServiceCreateVaultRejectsInvalidManagedLLMMetadata(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	ctx := context.Background()
	principal := Principal{TeamID: "team_123"}

	_, err := service.CreateVault(ctx, principal, CreateVaultRequest{
		DisplayName: "llm runtime",
		Metadata: map[string]string{
			ManagedAgentsVaultRoleKey:   ManagedAgentsVaultRoleLLM,
			ManagedAgentsVaultEngineKey: "unsupported-engine",
		},
	})
	if err == nil || !strings.Contains(err.Error(), ManagedAgentsVaultEngineKey) {
		t.Fatalf("CreateVault error = %v, want engine validation", err)
	}
}

func TestServiceCreateAgentRejectsReservedManagedMetadata(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	principal := Principal{TeamID: "team_123"}

	key := ManagedAgentsMetadataPrefix + "future_agent_key"
	_, err := service.CreateAgent(context.Background(), principal, CreateAgentRequest{
		Name:  "Claude Agent",
		Model: "claude-sonnet-4-5",
		Metadata: map[string]string{
			key: "enabled",
		},
	})
	if err == nil || !strings.Contains(err.Error(), key) {
		t.Fatalf("CreateAgent error = %v, want reserved metadata rejection", err)
	}
}

func TestServiceCreateAgentAllowsCustomMetadata(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	principal := Principal{TeamID: "team_123"}

	created, err := service.CreateAgent(context.Background(), principal, CreateAgentRequest{
		Name:  "Claude Agent",
		Model: "claude-sonnet-4-5",
		Metadata: map[string]string{
			"backend.llm_host": "https://llm.example.com",
		},
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if got := created.Metadata["backend.llm_host"]; got != "https://llm.example.com" {
		t.Fatalf("agent metadata = %#v", created.Metadata)
	}
}

func TestServiceCreateAgentDoesNotBlockOnWorkspaceBaseFailure(t *testing.T) {
	repo := newTestRepository(t)
	runtime := &failingWorkspaceBaseRuntime{called: make(chan Agent, 1)}
	service := NewService(repo, runtime, nil)
	principal := Principal{TeamID: "team_123"}

	created, err := service.CreateAgent(context.Background(), principal, CreateAgentRequest{
		Name:  "Claude Agent",
		Model: "claude-sonnet-4-5",
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if created.ID == "" {
		t.Fatal("CreateAgent returned empty agent id")
	}
	if _, _, err := repo.GetAgent(context.Background(), principal.TeamID, created.ID, 0); err != nil {
		t.Fatalf("GetAgent after workspace base failure: %v", err)
	}
	select {
	case prepared := <-runtime.called:
		if prepared.ID != created.ID {
			t.Fatalf("prepared agent id = %q, want %q", prepared.ID, created.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("workspace base preparation was not attempted")
	}
}

func TestServiceUpdateVaultRejectsManagedMetadataWithoutRole(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	ctx := context.Background()
	principal := Principal{TeamID: "team_123"}
	now := time.Now().UTC()
	vault := buildVaultObject("vault_123", CreateVaultRequest{DisplayName: "runtime"}, now, nil)
	if err := repo.CreateVault(ctx, principal.TeamID, vault, nil, now); err != nil {
		t.Fatalf("CreateVault: %v", err)
	}
	baseURL := "https://api.anthropic.com"
	_, err := service.UpdateVault(ctx, principal, vault.ID, UpdateVaultRequest{
		Metadata: MetadataPatchField{
			Set: true,
			Values: map[string]*string{
				ManagedAgentsVaultLLMBaseURLKey: &baseURL,
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), ManagedAgentsVaultRoleKey) {
		t.Fatalf("UpdateVault error = %v, want managed role validation", err)
	}
}

func TestServiceCreateCredentialRejectsReservedManagedMetadata(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	ctx := context.Background()
	principal := Principal{TeamID: "team_123"}
	now := time.Now().UTC()
	vault := buildVaultObject("vault_123", CreateVaultRequest{DisplayName: "runtime"}, now, nil)
	if err := repo.CreateVault(ctx, principal.TeamID, vault, nil, now); err != nil {
		t.Fatalf("CreateVault: %v", err)
	}
	key := ManagedAgentsMetadataPrefix + "future_credential_key"
	_, err := service.CreateCredential(ctx, principal, vault.ID, CreateCredentialRequest{
		Auth: map[string]any{
			"type":  "static_bearer",
			"token": "secret-token",
		},
		Metadata: map[string]string{
			key: "enabled",
		},
	})
	if err == nil || !strings.Contains(err.Error(), key) {
		t.Fatalf("CreateCredential error = %v, want reserved metadata rejection", err)
	}
}

func TestServiceCreateCredentialAllowsCustomMetadata(t *testing.T) {
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
			"type":  "static_bearer",
			"token": "secret-token",
		},
		Metadata: map[string]string{
			"backend.provider": "anthropic",
		},
	})
	if err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}
	if got := created.Metadata["backend.provider"]; got != "anthropic" {
		t.Fatalf("credential metadata = %#v", created.Metadata)
	}
}

func TestServiceCreateCredentialRejectsDuplicateCanonicalMCPServerURL(t *testing.T) {
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
			"mcp_server_url": "https://mcp.example.com/sse/",
		},
	})
	if err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}
	_, err = service.CreateCredential(ctx, principal, vault.ID, CreateCredentialRequest{
		Auth: map[string]any{
			"type":           "static_bearer",
			"token":          "another-secret",
			"mcp_server_url": "HTTPS://MCP.Example.com:443/sse",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("CreateCredential duplicate error = %v", err)
	}
}

func TestServiceArchiveVaultCascadesAndPurgesCredentialSecrets(t *testing.T) {
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
			"mcp_server_url": "https://mcp.example.com/sse",
		},
	})
	if err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}
	if _, err := service.ArchiveVault(ctx, principal, vault.ID); err != nil {
		t.Fatalf("ArchiveVault: %v", err)
	}
	active, err := repo.ListActiveCredentialsForVault(ctx, principal.TeamID, vault.ID)
	if err != nil {
		t.Fatalf("ListActiveCredentialsForVault: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active credentials = %d, want 0", len(active))
	}
	credential, secret, err := repo.GetCredential(ctx, principal.TeamID, vault.ID, created.ID)
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if credential.ArchivedAt == nil {
		t.Fatal("credential archived_at = nil, want archived timestamp")
	}
	if len(secret) != 0 {
		t.Fatalf("credential secret = %#v, want purged", secret)
	}
}

func TestDeleteVaultAndCredentialRejectReferencedSessions(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	ctx := context.Background()
	principal := Principal{TeamID: "team_123", UserID: "user_123"}
	now := time.Now().UTC()
	vault := buildVaultObject("vlt_in_use", CreateVaultRequest{DisplayName: "runtime"}, now, nil)
	if err := repo.CreateVault(ctx, principal.TeamID, vault, nil, now); err != nil {
		t.Fatalf("CreateVault: %v", err)
	}
	displayName := "token"
	credential := buildCredentialObject("vcrd_in_use", vault.ID, CreateCredentialRequest{DisplayName: &displayName}, now, nil)
	if err := repo.CreateCredential(ctx, principal.TeamID, vault.ID, credential, map[string]any{"token": "secret"}, nil, now); err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}
	session := &SessionRecord{
		ID:               "sesn_vault_in_use",
		TeamID:           principal.TeamID,
		CreatedByUserID:  principal.UserID,
		Vendor:           ManagedAgentsEngineClaude,
		EnvironmentID:    "env_123",
		WorkingDirectory: "/workspace",
		Metadata:         map[string]string{},
		Agent:            map[string]any{"type": "agent"},
		Resources:        []map[string]any{},
		VaultIDs:         []string{vault.ID},
		Status:           "idle",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := repo.CreateSession(ctx, session, nil); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := service.DeleteVault(ctx, principal, vault.ID); !errors.Is(err, ErrVaultInUse) {
		t.Fatalf("DeleteVault error = %v, want ErrVaultInUse", err)
	}
	if _, err := service.DeleteCredential(ctx, principal, vault.ID, credential.ID); !errors.Is(err, ErrVaultInUse) {
		t.Fatalf("DeleteCredential error = %v, want ErrVaultInUse", err)
	}
}

func TestServiceUploadFileStoresBytesOutsidePostgres(t *testing.T) {
	repo := newTestRepository(t)
	store := newTestAssetStore()
	service := NewService(repo, noopRuntimeManager{}, nil, WithAssetStore(store))
	ctx := context.Background()
	principal := Principal{TeamID: "team_123"}
	credential := RequestCredential{Token: "token"}

	metadata, err := service.UploadFile(ctx, principal, credential, "report.pdf", "application/pdf", strings.NewReader("%PDF-1.7"))
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	record, err := repo.GetFile(ctx, principal.TeamID, metadata.ID)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if len(record.Content) != 0 {
		t.Fatalf("postgres content bytes = %d, want 0", len(record.Content))
	}
	if record.StorePath == "" {
		t.Fatalf("asset-store path missing: %#v", record)
	}
	file, err := service.GetFileContent(ctx, principal, credential, metadata.ID)
	if err != nil {
		t.Fatalf("GetFileContent: %v", err)
	}
	if string(file.Content) != "%PDF-1.7" {
		t.Fatalf("file content = %q", string(file.Content))
	}
}

func TestDeleteFileKeepsRecordWhenFileStoreDeleteFails(t *testing.T) {
	repo := newTestRepository(t)
	store := newTestAssetStore()
	service := NewService(repo, noopRuntimeManager{}, nil, WithAssetStore(store))
	ctx := context.Background()
	principal := Principal{TeamID: "team_123"}
	credential := RequestCredential{Token: "token"}
	metadata, err := service.UploadFile(ctx, principal, credential, "report.pdf", "application/pdf", strings.NewReader("%PDF-1.7"))
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	store.deleteObjectErr = errors.New("delete object failed")

	_, err = service.DeleteFile(ctx, principal, credential, metadata.ID)
	if err == nil || !strings.Contains(err.Error(), "delete object failed") {
		t.Fatalf("DeleteFile error = %v, want asset-store failure", err)
	}
	if _, err := repo.GetFile(ctx, principal.TeamID, metadata.ID); err != nil {
		t.Fatalf("GetFile after failed delete: %v", err)
	}
}

func TestResolveFileBackedInputEventsValidatesMIMEAndInlinesBase64(t *testing.T) {
	repo := newTestRepository(t)
	store := newTestAssetStore()
	service := NewService(repo, noopRuntimeManager{}, nil, WithAssetStore(store))
	ctx := context.Background()
	principal := Principal{TeamID: "team_123"}
	credential := RequestCredential{Token: "token"}
	metadata, err := service.UploadFile(ctx, principal, credential, "image.png", "image/png", strings.NewReader("png-bytes"))
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	events := []map[string]any{{
		"type": "user.message",
		"content": []any{
			map[string]any{"type": "image", "source": map[string]any{"type": "file", "file_id": metadata.ID}},
		},
	}}
	if err := validateInputEvents(events); err != nil {
		t.Fatalf("validateInputEvents: %v", err)
	}
	runtimeEvents, err := service.resolveFileBackedInputEvents(ctx, principal, credential, events)
	if err != nil {
		t.Fatalf("resolveFileBackedInputEvents: %v", err)
	}
	source := mapValue(mapValue(anySlice(runtimeEvents[0]["content"])[0])["source"])
	if got := stringValue(source["type"]); got != "base64" {
		t.Fatalf("source.type = %q, want base64", got)
	}
	if got := stringValue(source["media_type"]); got != "image/png" {
		t.Fatalf("source.media_type = %q, want image/png", got)
	}
	if got := stringValue(source["data"]); got == "" || got == metadata.ID {
		t.Fatalf("source.data = %q, want base64 content", got)
	}

	badEvents := []map[string]any{{
		"type": "user.message",
		"content": []any{
			map[string]any{"type": "document", "source": map[string]any{"type": "file", "file_id": metadata.ID}},
		},
	}}
	_, err = service.resolveFileBackedInputEvents(ctx, principal, credential, badEvents)
	if err == nil || !strings.Contains(err.Error(), "not supported for document") {
		t.Fatalf("resolveFileBackedInputEvents error = %v, want MIME rejection", err)
	}
}

func TestServiceUploadFileReusesTeamAssetStore(t *testing.T) {
	repo := newTestRepository(t)
	store := newTestAssetStore()
	service := NewService(repo, noopRuntimeManager{}, nil, WithAssetStore(store))
	ctx := context.Background()
	principal := Principal{TeamID: "team_123"}
	credential := RequestCredential{Token: "token"}

	first, err := service.UploadFile(ctx, principal, credential, "first.txt", "text/plain", strings.NewReader("first"))
	if err != nil {
		t.Fatalf("UploadFile first: %v", err)
	}
	second, err := service.UploadFile(ctx, principal, credential, "second.txt", "text/plain", strings.NewReader("second"))
	if err != nil {
		t.Fatalf("UploadFile second: %v", err)
	}
	if store.createStoreCalls != 1 {
		t.Fatalf("createStoreCalls = %d, want 1", store.createStoreCalls)
	}
	teamStore, err := repo.GetTeamAssetStore(ctx, principal.TeamID, "default")
	if err != nil {
		t.Fatalf("GetTeamAssetStore: %v", err)
	}
	firstRecord, err := repo.GetFile(ctx, principal.TeamID, first.ID)
	if err != nil {
		t.Fatalf("GetFile first: %v", err)
	}
	secondRecord, err := repo.GetFile(ctx, principal.TeamID, second.ID)
	if err != nil {
		t.Fatalf("GetFile second: %v", err)
	}
	if _, ok := store.objects[testAssetStoreKey(teamStore.VolumeID, firstRecord.StorePath)]; !ok {
		t.Fatalf("missing first asset object for %s", firstRecord.StorePath)
	}
	if _, ok := store.objects[testAssetStoreKey(teamStore.VolumeID, secondRecord.StorePath)]; !ok {
		t.Fatalf("missing second asset object for %s", secondRecord.StorePath)
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

	_, err := createTestSession(ctx, service, principal, CreateSessionParams{
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

	_, err := createTestSession(ctx, service, principal, CreateSessionParams{
		Agent:         "agent_123",
		EnvironmentID: "env_123",
		VaultIDs:      []string{"   "},
	})
	if err == nil || !strings.Contains(err.Error(), "vault_ids entries must be non-empty") {
		t.Fatalf("CreateSession error = %v, want vault_ids rejection", err)
	}
}

func TestCreateSessionRejectsLegacyHardTTLMetadata(t *testing.T) {
	repo := newTestRepository(t)
	runtime := &createSessionRuntimeManager{}
	service := NewService(repo, runtime, nil)
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

	_, err := createTestSession(ctx, service, principal, CreateSessionParams{
		Agent:         "agent_123",
		EnvironmentID: environment.ID,
		Metadata: map[string]string{
			ManagedAgentsMetadataPrefix + "hard_ttl_seconds": "3600",
		},
	})
	if err == nil || !strings.Contains(err.Error(), ManagedAgentsMetadataPrefix+"hard_ttl_seconds") {
		t.Fatalf("CreateSession error = %v, want legacy hard TTL metadata rejection", err)
	}
	if len(runtime.ensureCalls) != 0 {
		t.Fatalf("EnsureRuntime calls = %d, want 0", len(runtime.ensureCalls))
	}
}

func TestCreateSessionPinsEnvironmentArtifact(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	principal := Principal{TeamID: "team_123"}
	ctx := context.Background()
	now := time.Now().UTC()

	environment := buildEnvironmentObject("env_123", CreateEnvironmentRequest{
		Name: "python",
		Config: map[string]any{
			"type":       "cloud",
			"networking": map[string]any{"type": "unrestricted"},
			"packages": map[string]any{
				"type": "packages",
				"pip":  []any{"ruff==0.9.0"},
			},
		},
	}, now, nil)
	if err := repo.CreateEnvironment(ctx, principal.TeamID, environment, nil, now); err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	artifact := readyTestEnvironmentArtifact(t, principal.TeamID, &environment, EnvironmentArtifactAssets{PipVolumeID: "vol_pip"})
	if err := repo.CreateEnvironmentArtifact(ctx, artifact); err != nil {
		t.Fatalf("CreateEnvironmentArtifact: %v", err)
	}
	agent := buildAgentObject("agent_123", 1, "claude", CreateAgentRequest{Name: "Claude Agent", Model: "claude-sonnet-4-5"}, now, nil)
	if err := repo.CreateAgent(ctx, principal.TeamID, "claude", 1, agent, now); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	session, err := createTestSession(ctx, service, principal, CreateSessionParams{
		Agent:         "agent_123",
		EnvironmentID: environment.ID,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	stored, _, err := repo.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if stored.EnvironmentArtifactID != artifact.ID {
		t.Fatalf("environment_artifact_id = %q, want %q", stored.EnvironmentArtifactID, artifact.ID)
	}
}

func TestCreateSessionBootstrapsRuntime(t *testing.T) {
	repo := newTestRepository(t)
	runtime := &createSessionRuntimeManager{runtime: &RuntimeRecord{
		Vendor:            "claude",
		RegionID:          "test-region",
		SandboxID:         "sbx_create",
		WrapperURL:        "https://wrapper.example.test",
		WorkspaceVolumeID: "vol_workspace",
		ControlToken:      "ctl_123",
		RuntimeGeneration: 1,
	}}
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

	session, err := service.CreateSession(ctx, principal, credential, CreateSessionParams{
		Agent:         "agent_123",
		EnvironmentID: environment.ID,
	}, "http://gateway.test")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if len(runtime.ensureCalls) != 1 {
		t.Fatalf("EnsureRuntime calls = %d, want 1", len(runtime.ensureCalls))
	}
	if runtime.ensureCalls[0].SessionID != session.ID {
		t.Fatalf("EnsureRuntime session_id = %q, want %q", runtime.ensureCalls[0].SessionID, session.ID)
	}
	if runtime.ensureCalls[0].Credential.Token != credential.Token {
		t.Fatalf("EnsureRuntime token = %q, want user token", runtime.ensureCalls[0].Credential.Token)
	}
	if runtime.ensureCalls[0].GatewayBaseURL != "http://gateway.test" {
		t.Fatalf("EnsureRuntime gateway = %q, want http://gateway.test", runtime.ensureCalls[0].GatewayBaseURL)
	}
	if len(runtime.bootstrapReqs) != 1 {
		t.Fatalf("BootstrapSession calls = %d, want 1", len(runtime.bootstrapReqs))
	}
	if runtime.bootstrapReqs[0].SessionID != session.ID {
		t.Fatalf("BootstrapSession session_id = %q, want %q", runtime.bootstrapReqs[0].SessionID, session.ID)
	}
	if runtime.bootstrapReqs[0].EnvironmentID != environment.ID {
		t.Fatalf("BootstrapSession environment_id = %q, want %q", runtime.bootstrapReqs[0].EnvironmentID, environment.ID)
	}
	if !reflect.DeepEqual(runtime.callOrder, []string{"ensure", "bootstrap"}) {
		t.Fatalf("call order = %#v", runtime.callOrder)
	}
}

func TestCreateSessionRollsBackSessionWhenRuntimeEnsureFails(t *testing.T) {
	repo := newTestRepository(t)
	ensureErr := errors.New("ensure runtime failed")
	runtime := &createSessionRuntimeManager{ensureErr: ensureErr}
	service := NewService(repo, runtime, nil)
	principal := Principal{TeamID: "team_123", UserID: "user_123"}
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

	_, err := service.CreateSession(ctx, principal, RequestCredential{Token: "token_123"}, CreateSessionParams{
		Agent:         "agent_123",
		EnvironmentID: environment.ID,
	}, "http://gateway.test")
	if !errors.Is(err, ensureErr) {
		t.Fatalf("CreateSession error = %v, want %v", err, ensureErr)
	}
	sessions, _, err := repo.ListSessions(ctx, principal.TeamID, SessionListOptions{Limit: 10, IncludeArchived: true})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions len = %d, want rollback to leave none", len(sessions))
	}
}

func TestCreateSessionUsesLLMVaultEngineAsRuntimeVendor(t *testing.T) {
	repo := newTestRepository(t)
	runtime := &createSessionRuntimeManager{}
	service := NewService(repo, runtime, nil)
	principal := Principal{TeamID: "team_123", UserID: "user_123"}
	ctx := context.Background()
	now := time.Now().UTC()

	environment := buildEnvironmentObject("env_123", CreateEnvironmentRequest{Name: "python", Config: defaultEnvironmentConfig()}, now, nil)
	if err := repo.CreateEnvironment(ctx, principal.TeamID, environment, nil, now); err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	agent := buildAgentObject("agent_123", 1, "claude", CreateAgentRequest{Name: "Codex Agent", Model: "gpt-5.1-codex"}, now, nil)
	if err := repo.CreateAgent(ctx, principal.TeamID, "claude", 1, agent, now); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	vault := buildVaultObject("vlt_codex", CreateVaultRequest{
		DisplayName: "Codex LLM",
		Metadata: map[string]string{
			ManagedAgentsVaultRoleKey:       ManagedAgentsVaultRoleLLM,
			ManagedAgentsVaultEngineKey:     ManagedAgentsEngineCodex,
			ManagedAgentsVaultLLMBaseURLKey: "https://api.openai.com/v1",
		},
	}, now, nil)
	if err := repo.CreateVault(ctx, principal.TeamID, vault, nil, now); err != nil {
		t.Fatalf("CreateVault: %v", err)
	}

	_, err := createTestSession(ctx, service, principal, CreateSessionParams{
		Agent:         "agent_123",
		EnvironmentID: environment.ID,
		VaultIDs:      []string{vault.ID},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if len(runtime.ensureCalls) != 1 || runtime.ensureCalls[0].Vendor != ManagedAgentsEngineCodex {
		t.Fatalf("EnsureRuntime vendor calls = %#v, want codex", runtime.ensureCalls)
	}
	if len(runtime.bootstrapReqs) != 1 || runtime.bootstrapReqs[0].Vendor != ManagedAgentsEngineCodex {
		t.Fatalf("BootstrapSession vendor reqs = %#v, want codex", runtime.bootstrapReqs)
	}
}

func TestCreateSessionRejectsArchivedEnvironment(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	principal := Principal{TeamID: "team_123"}
	ctx := context.Background()
	now := time.Now().UTC()

	environment := buildEnvironmentObject("env_123", CreateEnvironmentRequest{Name: "python", Config: defaultEnvironmentConfig()}, now, &now)
	if err := repo.CreateEnvironment(ctx, principal.TeamID, environment, &now, now); err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	agent := buildAgentObject("agent_123", 1, "claude", CreateAgentRequest{Name: "Claude Agent", Model: "claude-sonnet-4-5"}, now, nil)
	if err := repo.CreateAgent(ctx, principal.TeamID, "claude", 1, agent, now); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	_, err := createTestSession(ctx, service, principal, CreateSessionParams{
		Agent:         "agent_123",
		EnvironmentID: environment.ID,
	})
	if err == nil || !strings.Contains(err.Error(), "archived environments cannot create new sessions") {
		t.Fatalf("CreateSession error = %v, want archived-environment rejection", err)
	}
}

func TestCreateEnvironmentRejectsDuplicateName(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	principal := Principal{TeamID: "team_123"}
	ctx := context.Background()

	first, err := service.CreateEnvironment(ctx, principal, RequestCredential{Token: "token_123"}, CreateEnvironmentRequest{Name: "python"})
	if err != nil {
		t.Fatalf("CreateEnvironment first: %v", err)
	}
	if first == nil {
		t.Fatal("expected first environment")
	}
	_, err = service.CreateEnvironment(ctx, principal, RequestCredential{Token: "token_123"}, CreateEnvironmentRequest{Name: " python "})
	if err == nil || !strings.Contains(err.Error(), "environment already exists") {
		t.Fatalf("CreateEnvironment duplicate error = %v, want conflict", err)
	}
}

func TestCreateEnvironmentRejectsUnsupportedManagedMetadata(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	principal := Principal{TeamID: "team_123"}
	ctx := context.Background()

	key := ManagedAgentsMetadataPrefix + "future_environment_key"
	_, err := service.CreateEnvironment(ctx, principal, RequestCredential{Token: "token_123"}, CreateEnvironmentRequest{
		Name: "python",
		Metadata: map[string]string{
			key: "enabled",
		},
	})
	if err == nil || !strings.Contains(err.Error(), key) {
		t.Fatalf("CreateEnvironment error = %v, want reserved metadata rejection", err)
	}
}

func TestCreateEnvironmentAllowsCustomMetadata(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	principal := Principal{TeamID: "team_123"}
	ctx := context.Background()

	created, err := service.CreateEnvironment(ctx, principal, RequestCredential{Token: "token_123"}, CreateEnvironmentRequest{
		Name: "python",
		Metadata: map[string]string{
			"backend.llm_host": "https://llm.example.com",
		},
	})
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	if got := created.Metadata["backend.llm_host"]; got != "https://llm.example.com" {
		t.Fatalf("environment metadata = %#v", created.Metadata)
	}
}

func TestCreateAndUpdateEnvironmentBuildArtifactsSynchronously(t *testing.T) {
	repo := newTestRepository(t)
	runtime := &buildRuntimeManager{calls: make(chan buildEnvironmentCall, 2)}
	service := NewService(repo, runtime, nil)
	principal := Principal{TeamID: "team_123"}
	ctx := context.Background()

	created, err := service.CreateEnvironment(ctx, principal, RequestCredential{Token: "token_create"}, CreateEnvironmentRequest{
		Name: "python",
		Config: map[string]any{
			"type": "cloud",
			"packages": map[string]any{
				"type": "packages",
				"pip":  []any{"ruff==0.9.0"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	createCall := waitBuildEnvironmentCall(t, runtime.calls)
	if createCall.Credential.Token != "token_create" {
		t.Fatalf("create build credential = %#v", createCall.Credential)
	}
	if createCall.TeamID != principal.TeamID || createCall.EnvironmentID != created.ID {
		t.Fatalf("create build call = %#v", createCall)
	}
	if !reflect.DeepEqual(createCall.PipPackages, []string{"ruff==0.9.0"}) {
		t.Fatalf("create build pip packages = %#v", createCall.PipPackages)
	}
	artifact, err := repo.GetLatestEnvironmentArtifact(ctx, principal.TeamID, created.ID)
	if err != nil {
		t.Fatalf("GetLatestEnvironmentArtifact after create: %v", err)
	}
	if artifact.Status != EnvironmentArtifactStatusReady || artifact.Assets.PipVolumeID == "" {
		t.Fatalf("create artifact = status:%q assets:%#v, want ready pip volume", artifact.Status, artifact.Assets)
	}

	updated, err := service.UpdateEnvironment(ctx, principal, RequestCredential{Token: "token_update"}, created.ID, UpdateEnvironmentRequest{
		Config: map[string]any{
			"packages": map[string]any{
				"pip": []any{"ruff==0.10.0"},
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateEnvironment: %v", err)
	}
	updateCall := waitBuildEnvironmentCall(t, runtime.calls)
	if updateCall.Credential.Token != "token_update" {
		t.Fatalf("update build credential = %#v", updateCall.Credential)
	}
	if updateCall.TeamID != principal.TeamID || updateCall.EnvironmentID != updated.ID {
		t.Fatalf("update build call = %#v", updateCall)
	}
	if !reflect.DeepEqual(updateCall.PipPackages, []string{"ruff==0.10.0"}) {
		t.Fatalf("update build pip packages = %#v", updateCall.PipPackages)
	}
}

func TestCreateEnvironmentBuildFailureDoesNotCreateEnvironment(t *testing.T) {
	repo := newTestRepository(t)
	runtime := &buildRuntimeManager{calls: make(chan buildEnvironmentCall, 1), buildErr: errors.New("pip install failed")}
	service := NewService(repo, runtime, nil)
	principal := Principal{TeamID: "team_123"}
	ctx := context.Background()

	_, err := service.CreateEnvironment(ctx, principal, RequestCredential{Token: "token_create"}, CreateEnvironmentRequest{
		Name: "python",
		Config: map[string]any{
			"type": "cloud",
			"packages": map[string]any{
				"type": "packages",
				"pip":  []any{"does-not-exist==0.0.1"},
			},
		},
	})
	if !errors.Is(err, ErrEnvironmentBuildFailed) {
		t.Fatalf("CreateEnvironment error = %v, want ErrEnvironmentBuildFailed", err)
	}
	items, _, err := service.ListEnvironments(ctx, principal, 10, "", true)
	if err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("environments after failed build = %#v, want none", items)
	}
}

func TestUpdateEnvironmentRejectsUnsupportedManagedMetadata(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	principal := Principal{TeamID: "team_123"}
	ctx := context.Background()

	environment, err := service.CreateEnvironment(ctx, principal, RequestCredential{Token: "token_123"}, CreateEnvironmentRequest{Name: "python"})
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	value := "enabled"
	key := ManagedAgentsMetadataPrefix + "future_environment_key"
	_, err = service.UpdateEnvironment(ctx, principal, RequestCredential{Token: "token_123"}, environment.ID, UpdateEnvironmentRequest{
		Metadata: MetadataPatchField{Set: true, Values: map[string]*string{key: &value}},
	})
	if err == nil || !strings.Contains(err.Error(), key) {
		t.Fatalf("UpdateEnvironment error = %v, want reserved metadata rejection", err)
	}
}

func TestCreateEnvironmentRoundTripsAllPackageFields(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, &buildRuntimeManager{calls: make(chan buildEnvironmentCall, 1)}, nil)
	principal := Principal{TeamID: "team_123"}
	ctx := context.Background()

	environment, err := service.CreateEnvironment(ctx, principal, RequestCredential{Token: "token_123"}, CreateEnvironmentRequest{
		Name: "toolbox",
		Config: map[string]any{
			"type":       "cloud",
			"networking": map[string]any{"type": "unrestricted"},
			"packages": map[string]any{
				"type":  "packages",
				"apt":   []any{"ripgrep", "git"},
				"cargo": []any{"bat"},
				"gem":   []any{"bundler"},
				"go":    []any{"golang.org/x/tools/cmd/stringer@latest"},
				"npm":   []any{"typescript"},
				"pip":   []any{"ruff==0.9.0"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}

	stored, err := service.GetEnvironment(ctx, principal, environment.ID)
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}

	if !reflect.DeepEqual(stored.Config.Packages.Apt, []string{"ripgrep", "git"}) {
		t.Fatalf("packages.apt = %#v", stored.Config.Packages.Apt)
	}
	if !reflect.DeepEqual(stored.Config.Packages.Cargo, []string{"bat"}) {
		t.Fatalf("packages.cargo = %#v", stored.Config.Packages.Cargo)
	}
	if !reflect.DeepEqual(stored.Config.Packages.Gem, []string{"bundler"}) {
		t.Fatalf("packages.gem = %#v", stored.Config.Packages.Gem)
	}
	if !reflect.DeepEqual(stored.Config.Packages.Go, []string{"golang.org/x/tools/cmd/stringer@latest"}) {
		t.Fatalf("packages.go = %#v", stored.Config.Packages.Go)
	}
	if !reflect.DeepEqual(stored.Config.Packages.NPM, []string{"typescript"}) {
		t.Fatalf("packages.npm = %#v", stored.Config.Packages.NPM)
	}
	if !reflect.DeepEqual(stored.Config.Packages.Pip, []string{"ruff==0.9.0"}) {
		t.Fatalf("packages.pip = %#v", stored.Config.Packages.Pip)
	}
}

func TestEnvironmentLifecycleFlow(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, &buildRuntimeManager{calls: make(chan buildEnvironmentCall, 1)}, nil)
	principal := Principal{TeamID: "team_123"}
	ctx := context.Background()

	created, err := service.CreateEnvironment(ctx, principal, RequestCredential{Token: "token_123"}, CreateEnvironmentRequest{
		Name: "python",
		Config: map[string]any{
			"type":       "cloud",
			"networking": map[string]any{"type": "unrestricted"},
		},
	})
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}

	fetched, err := service.GetEnvironment(ctx, principal, created.ID)
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	if fetched.ID != created.ID {
		t.Fatalf("GetEnvironment ID = %q, want %q", fetched.ID, created.ID)
	}

	active, nextPage, err := service.ListEnvironments(ctx, principal, 10, "", false)
	if err != nil {
		t.Fatalf("ListEnvironments active: %v", err)
	}
	if nextPage != nil {
		t.Fatalf("ListEnvironments active next_page = %v, want nil", *nextPage)
	}
	if len(active) != 1 || active[0].ID != created.ID {
		t.Fatalf("ListEnvironments active = %#v, want [%s]", active, created.ID)
	}

	name := "python tools"
	updated, err := service.UpdateEnvironment(ctx, principal, RequestCredential{Token: "token_123"}, created.ID, UpdateEnvironmentRequest{
		Name: &name,
		Config: map[string]any{
			"packages": map[string]any{
				"pip": []any{"ruff==0.9.0"},
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateEnvironment: %v", err)
	}
	if updated.Name != "python tools" {
		t.Fatalf("updated name = %q, want python tools", updated.Name)
	}
	if !reflect.DeepEqual(updated.Config.Packages.Pip, []string{"ruff==0.9.0"}) {
		t.Fatalf("updated packages.pip = %#v", updated.Config.Packages.Pip)
	}

	archived, err := service.ArchiveEnvironment(ctx, principal, created.ID)
	if err != nil {
		t.Fatalf("ArchiveEnvironment: %v", err)
	}
	if archived.ArchivedAt == nil {
		t.Fatal("ArchiveEnvironment did not set archived_at")
	}

	active, _, err = service.ListEnvironments(ctx, principal, 10, "", false)
	if err != nil {
		t.Fatalf("ListEnvironments active after archive: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active environments after archive = %#v, want none", active)
	}

	all, _, err := service.ListEnvironments(ctx, principal, 10, "", true)
	if err != nil {
		t.Fatalf("ListEnvironments includeArchived: %v", err)
	}
	if len(all) != 1 || all[0].ID != created.ID {
		t.Fatalf("ListEnvironments includeArchived = %#v, want archived environment", all)
	}

	deleted, err := service.DeleteEnvironment(ctx, principal, RequestCredential{Token: "token_123"}, created.ID)
	if err != nil {
		t.Fatalf("DeleteEnvironment: %v", err)
	}
	if got := stringValue(deleted["id"]); got != created.ID {
		t.Fatalf("DeleteEnvironment id = %q, want %q", got, created.ID)
	}
	if _, err := service.GetEnvironment(ctx, principal, created.ID); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Fatalf("GetEnvironment after delete error = %v, want ErrEnvironmentNotFound", err)
	}
}

func TestEnvironmentUpdateOnlyAffectsNewSessionsAndReusesArtifactsForStableConfig(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, &buildRuntimeManager{calls: make(chan buildEnvironmentCall, 2)}, nil)
	principal := Principal{TeamID: "team_123"}
	ctx := context.Background()
	now := time.Now().UTC()

	environment, err := service.CreateEnvironment(ctx, principal, RequestCredential{Token: "token_123"}, CreateEnvironmentRequest{
		Name: "python",
		Config: map[string]any{
			"type":       "cloud",
			"networking": map[string]any{"type": "unrestricted"},
			"packages": map[string]any{
				"type": "packages",
				"pip":  []any{"ruff==0.9.0"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	agent := buildAgentObject("agent_123", 1, "claude", CreateAgentRequest{Name: "Claude Agent", Model: "claude-sonnet-4-5"}, now, nil)
	if err := repo.CreateAgent(ctx, principal.TeamID, "claude", 1, agent, now); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	first, err := createTestSession(ctx, service, principal, CreateSessionParams{
		Agent:         "agent_123",
		EnvironmentID: environment.ID,
	})
	if err != nil {
		t.Fatalf("CreateSession first: %v", err)
	}
	second, err := createTestSession(ctx, service, principal, CreateSessionParams{
		Agent:         "agent_123",
		EnvironmentID: environment.ID,
	})
	if err != nil {
		t.Fatalf("CreateSession second: %v", err)
	}

	storedFirst, _, err := repo.GetSession(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetSession first: %v", err)
	}
	storedSecond, _, err := repo.GetSession(ctx, second.ID)
	if err != nil {
		t.Fatalf("GetSession second: %v", err)
	}
	if storedFirst.EnvironmentArtifactID == "" {
		t.Fatal("expected first session to pin an environment artifact")
	}
	if storedFirst.EnvironmentArtifactID != storedSecond.EnvironmentArtifactID {
		t.Fatalf("artifact ids = %q and %q, want identical artifact reuse", storedFirst.EnvironmentArtifactID, storedSecond.EnvironmentArtifactID)
	}

	updated, err := service.UpdateEnvironment(ctx, principal, RequestCredential{Token: "token_123"}, environment.ID, UpdateEnvironmentRequest{
		Config: map[string]any{
			"packages": map[string]any{
				"pip": []any{"ruff==0.10.0"},
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateEnvironment: %v", err)
	}
	if !reflect.DeepEqual(updated.Config.Packages.Pip, []string{"ruff==0.10.0"}) {
		t.Fatalf("updated packages.pip = %#v", updated.Config.Packages.Pip)
	}

	third, err := createTestSession(ctx, service, principal, CreateSessionParams{
		Agent:         "agent_123",
		EnvironmentID: environment.ID,
	})
	if err != nil {
		t.Fatalf("CreateSession third: %v", err)
	}
	storedThird, _, err := repo.GetSession(ctx, third.ID)
	if err != nil {
		t.Fatalf("GetSession third: %v", err)
	}
	if storedThird.EnvironmentArtifactID == "" {
		t.Fatal("expected third session to pin an environment artifact")
	}
	if storedThird.EnvironmentArtifactID == storedFirst.EnvironmentArtifactID {
		t.Fatalf("third session artifact id = %q, want new artifact after environment update", storedThird.EnvironmentArtifactID)
	}

	reloadedFirst, _, err := repo.GetSession(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetSession first after update: %v", err)
	}
	if reloadedFirst.EnvironmentArtifactID != storedFirst.EnvironmentArtifactID {
		t.Fatalf("first session artifact id changed from %q to %q", storedFirst.EnvironmentArtifactID, reloadedFirst.EnvironmentArtifactID)
	}
}

func TestDeleteEnvironmentRejectsReferencedSessions(t *testing.T) {
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
	if _, err := createTestSession(ctx, service, principal, CreateSessionParams{
		Agent:         "agent_123",
		EnvironmentID: environment.ID,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	_, err := service.DeleteEnvironment(ctx, principal, RequestCredential{Token: "token_123"}, environment.ID)
	if err == nil || !strings.Contains(err.Error(), "referenced by existing sessions") {
		t.Fatalf("DeleteEnvironment error = %v, want in-use rejection", err)
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

	session, err := createTestSession(ctx, service, principal, CreateSessionParams{
		Agent:         "agent_123",
		EnvironmentID: "env_123",
		VaultIDs:      []string{"vlt_a"},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := repo.UpsertRuntime(ctx, &RuntimeRecord{
		SessionID:         session.ID,
		Vendor:            "claude",
		RegionID:          "test-region",
		SandboxID:         "sbx_123",
		WorkspaceVolumeID: "vol_workspace",
		ControlToken:      "ctl_123",
		RuntimeGeneration: 1,
		CreatedAt:         now,
		UpdatedAt:         now,
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

func TestUpdateSessionAllowsVaultIDsChangeWhileRunIsActive(t *testing.T) {
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

	session, err := createTestSession(ctx, service, principal, CreateSessionParams{
		Agent:         "agent_123",
		EnvironmentID: "env_123",
		VaultIDs:      []string{"vlt_a"},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	activeRunID := "srun_123"
	if err := repo.UpsertRuntime(ctx, &RuntimeRecord{
		SessionID:         session.ID,
		Vendor:            "claude",
		RegionID:          "test-region",
		SandboxID:         "sbx_123",
		WorkspaceVolumeID: "vol_workspace",
		ControlToken:      "ctl_123",
		RuntimeGeneration: 1,
		ActiveRunID:       &activeRunID,
		CreatedAt:         now,
		UpdatedAt:         now,
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

func TestUpdateSessionRejectsLegacyHardTTLMetadata(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	principal := Principal{TeamID: "team_123"}
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
	session, err := createTestSession(ctx, service, principal, CreateSessionParams{
		Agent:         "agent_123",
		EnvironmentID: environment.ID,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	key := ManagedAgentsMetadataPrefix + "hard_ttl_seconds"
	newValue := "3600"
	_, err = service.UpdateSession(ctx, principal, credential, session.ID, UpdateSessionParams{
		Metadata: MetadataPatchField{Set: true, Values: map[string]*string{key: &newValue}},
	})
	if err == nil || !strings.Contains(err.Error(), key) {
		t.Fatalf("UpdateSession error = %v, want legacy hard TTL metadata rejection", err)
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

type failingWorkspaceBaseRuntime struct {
	noopRuntimeManager
	called chan Agent
}

func (m *failingWorkspaceBaseRuntime) PrepareWorkspaceBase(_ context.Context, _ string, agent Agent, _ string) (*WorkspaceBaseRecord, error) {
	if m.called != nil {
		m.called <- agent
	}
	return nil, errors.New("workspace base preparation failed")
}

func createTestSession(ctx context.Context, service *Service, principal Principal, params CreateSessionParams) (*Session, error) {
	return service.CreateSession(ctx, principal, RequestCredential{Token: "token_123"}, params, "http://gateway.test")
}

func readyTestEnvironmentArtifact(t *testing.T, teamID string, environment *Environment, assets EnvironmentArtifactAssets) *EnvironmentArtifact {
	t.Helper()
	compatibility := DefaultEnvironmentArtifactCompatibility()
	digest, err := EnvironmentArtifactDigest(environment.Config, compatibility)
	if err != nil {
		t.Fatalf("EnvironmentArtifactDigest: %v", err)
	}
	now := time.Now().UTC()
	return &EnvironmentArtifact{
		ID:             NewID("envart"),
		TeamID:         teamID,
		EnvironmentID:  environment.ID,
		Digest:         digest,
		Status:         EnvironmentArtifactStatusReady,
		ConfigSnapshot: EnvironmentConfigSnapshotForArtifact(environment.Config),
		Compatibility:  compatibility,
		Assets:         assets,
		BuildLog:       "test ready artifact\n",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

type buildEnvironmentCall struct {
	Credential    RequestCredential
	TeamID        string
	EnvironmentID string
	PipPackages   []string
}

type buildRuntimeManager struct {
	noopRuntimeManager
	calls       chan buildEnvironmentCall
	buildErr    error
	cleanedUp   []EnvironmentArtifactAssets
	buildLog    string
	pipVolumeID string
}

func (m *buildRuntimeManager) BuildEnvironmentArtifact(_ context.Context, credential RequestCredential, teamID string, environment *Environment) (*EnvironmentArtifactBuildResult, error) {
	call := buildEnvironmentCall{
		Credential: credential,
		TeamID:     teamID,
	}
	if environment != nil {
		call.EnvironmentID = environment.ID
		call.PipPackages = append([]string(nil), environment.Config.Packages.Pip...)
	}
	if m.calls != nil {
		m.calls <- call
	}
	if m.buildErr != nil {
		return nil, m.buildErr
	}
	pipVolumeID := m.pipVolumeID
	if pipVolumeID == "" {
		pipVolumeID = "vol_pip_" + call.EnvironmentID
	}
	assets := EnvironmentArtifactAssets{}
	if environment != nil {
		for _, manager := range ConfiguredEnvironmentPackageManagers(environment.Config) {
			volumeID := "vol_" + manager + "_" + call.EnvironmentID
			if manager == "pip" {
				volumeID = pipVolumeID
			}
			assets.SetVolumeIDForManager(manager, volumeID)
		}
	}
	buildLog := m.buildLog
	if buildLog == "" {
		buildLog = "built test environment artifact\n"
	}
	return &EnvironmentArtifactBuildResult{
		Assets:   assets,
		BuildLog: buildLog,
	}, nil
}

func (m *buildRuntimeManager) CleanupEnvironmentArtifactAssets(_ context.Context, _ RequestCredential, _ string, assets EnvironmentArtifactAssets) error {
	m.cleanedUp = append(m.cleanedUp, assets)
	return nil
}

func waitBuildEnvironmentCall(t *testing.T, calls <-chan buildEnvironmentCall) buildEnvironmentCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for environment artifact build")
		return buildEnvironmentCall{}
	}
}

type ensureRuntimeCall struct {
	Credential     RequestCredential
	SessionID      string
	Vendor         string
	GatewayBaseURL string
}

type createSessionRuntimeManager struct {
	runtime       *RuntimeRecord
	ensureErr     error
	bootstrapErr  error
	ensureCalls   []ensureRuntimeCall
	bootstrapReqs []*WrapperSessionBootstrapRequest
	callOrder     []string
}

func (m *createSessionRuntimeManager) EnsureRuntime(_ context.Context, _ Principal, credential RequestCredential, session *SessionRecord, _ map[string]any, gatewayBaseURL string) (*RuntimeRecord, error) {
	m.ensureCalls = append(m.ensureCalls, ensureRuntimeCall{Credential: credential, SessionID: session.ID, Vendor: session.Vendor, GatewayBaseURL: gatewayBaseURL})
	m.callOrder = append(m.callOrder, "ensure")
	if m.ensureErr != nil {
		return nil, m.ensureErr
	}
	runtime := &RuntimeRecord{SessionID: session.ID, Vendor: session.Vendor}
	if m.runtime != nil {
		cloned := *m.runtime
		runtime = &cloned
		if runtime.SessionID == "" {
			runtime.SessionID = session.ID
		}
		if runtime.Vendor == "" {
			runtime.Vendor = session.Vendor
		}
	}
	return runtime, nil
}

func (m *createSessionRuntimeManager) BootstrapSession(_ context.Context, _ RequestCredential, _ *RuntimeRecord, req *WrapperSessionBootstrapRequest) error {
	m.bootstrapReqs = append(m.bootstrapReqs, req)
	m.callOrder = append(m.callOrder, "bootstrap")
	return m.bootstrapErr
}

func (m *createSessionRuntimeManager) StartRun(context.Context, RequestCredential, *RuntimeRecord, *WrapperRunRequest) error {
	return nil
}

func (m *createSessionRuntimeManager) ResolveActions(context.Context, RequestCredential, *RuntimeRecord, *WrapperResolveActionsRequest) (*WrapperResolveActionsResponse, error) {
	return nil, nil
}

func (m *createSessionRuntimeManager) InterruptRun(context.Context, RequestCredential, *RuntimeRecord, string) error {
	return nil
}

func (m *createSessionRuntimeManager) DeleteWrapperSession(context.Context, RequestCredential, *RuntimeRecord, string) error {
	return nil
}

func (m *createSessionRuntimeManager) DestroyRuntime(context.Context, RequestCredential, *RuntimeRecord) error {
	return nil
}

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

type testAssetStore struct {
	createStoreCalls int
	nextStoreID      int
	objects          map[string][]byte
	deleteStoreErr   error
	deleteObjectErr  error
}

func newTestAssetStore() *testAssetStore {
	return &testAssetStore{objects: map[string][]byte{}}
}

func testAssetStoreKey(volumeID, path string) string {
	return volumeID + ":" + path
}

func (s *testAssetStore) CreateStore(_ context.Context, _ RequestCredential, req AssetStoreCreateStoreRequest) (AssetStoreVolume, error) {
	s.createStoreCalls++
	s.nextStoreID++
	return AssetStoreVolume{VolumeID: fmt.Sprintf("vol_%s_%d", req.TeamID, s.nextStoreID)}, nil
}

func (s *testAssetStore) DeleteStore(_ context.Context, _ RequestCredential, req AssetStoreDeleteStoreRequest) error {
	if s.deleteStoreErr != nil {
		return s.deleteStoreErr
	}
	for key := range s.objects {
		if strings.HasPrefix(key, req.VolumeID+":") {
			delete(s.objects, key)
		}
	}
	return nil
}

func (s *testAssetStore) PutObject(_ context.Context, _ RequestCredential, req AssetStorePutObjectRequest) (AssetStoreObject, error) {
	data, err := io.ReadAll(req.Content)
	if err != nil {
		return AssetStoreObject{}, err
	}
	s.objects[testAssetStoreKey(req.VolumeID, req.Path)] = append([]byte(nil), data...)
	return AssetStoreObject{
		Path:      req.Path,
		SizeBytes: int64(len(data)),
		SHA256:    "test-sha",
	}, nil
}

func (s *testAssetStore) ReadObject(_ context.Context, _ RequestCredential, req AssetStoreReadObjectRequest) ([]byte, error) {
	if data, ok := s.objects[testAssetStoreKey(req.VolumeID, req.Path)]; ok {
		return append([]byte(nil), data...), nil
	}
	return nil, ErrFileNotFound
}

func (s *testAssetStore) DeleteObject(_ context.Context, _ RequestCredential, req AssetStoreDeleteObjectRequest) error {
	if s.deleteObjectErr != nil {
		return s.deleteObjectErr
	}
	delete(s.objects, testAssetStoreKey(req.VolumeID, req.Path))
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
