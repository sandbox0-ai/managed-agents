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

func TestServiceUploadFileStoresBytesOutsidePostgres(t *testing.T) {
	repo := newTestRepository(t)
	store := newTestFileStore()
	service := NewService(repo, noopRuntimeManager{}, nil, WithFileStore(store))
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
	if record.FileStoreVolumeID == "" || record.FileStorePath == "" {
		t.Fatalf("file-store location missing: %#v", record)
	}
	file, err := service.GetFileContent(ctx, principal, credential, metadata.ID)
	if err != nil {
		t.Fatalf("GetFileContent: %v", err)
	}
	if string(file.Content) != "%PDF-1.7" {
		t.Fatalf("file content = %q", string(file.Content))
	}
}

func TestResolveFileBackedInputEventsValidatesMIMEAndInlinesBase64(t *testing.T) {
	repo := newTestRepository(t)
	store := newTestFileStore()
	service := NewService(repo, noopRuntimeManager{}, nil, WithFileStore(store))
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

func TestCreateSessionRejectsInvalidHardTTLMetadata(t *testing.T) {
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
			ManagedAgentsSessionHardTTLSecondsKey: "-1",
		},
	})
	if err == nil || !strings.Contains(err.Error(), ManagedAgentsSessionHardTTLSecondsKey) {
		t.Fatalf("CreateSession error = %v, want hard_ttl metadata rejection", err)
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
	artifact, err := service.ensureEnvironmentArtifactRecord(ctx, principal.TeamID, &environment)
	if err != nil {
		t.Fatalf("ensureEnvironmentArtifactRecord: %v", err)
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

func TestCreateSessionBootstrapsAndDelaysIdlePause(t *testing.T) {
	repo := newTestRepository(t)
	runtime := &createSessionRuntimeManager{runtime: &RuntimeRecord{
		Vendor:              "claude",
		RegionID:            "test-region",
		SandboxID:           "sbx_create",
		WrapperURL:          "https://wrapper.example.test",
		WorkspaceVolumeID:   "vol_workspace",
		EngineStateVolumeID: "vol_state",
		ControlToken:        "ctl_123",
		RuntimeGeneration:   1,
	}, pauseCh: make(chan *RuntimeRecord, 1)}
	service := NewService(repo, runtime, nil, WithCreateIdlePauseDelay(0))
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
	var paused *RuntimeRecord
	select {
	case paused = <-runtime.pauseCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delayed create pause")
	}
	if paused.SandboxID != "sbx_create" {
		t.Fatalf("PauseRuntime sandbox_id = %q, want sbx_create", paused.SandboxID)
	}
	if !reflect.DeepEqual(runtime.callOrder, []string{"ensure", "bootstrap", "pause"}) {
		t.Fatalf("call order = %#v", runtime.callOrder)
	}
}

func TestCreateSessionDelayedIdlePauseSkipsActiveRuntime(t *testing.T) {
	repo := newTestRepository(t)
	runtime := &createSessionRuntimeManager{runtime: &RuntimeRecord{
		Vendor:              "claude",
		RegionID:            "test-region",
		SandboxID:           "sbx_create",
		WrapperURL:          "https://wrapper.example.test",
		WorkspaceVolumeID:   "vol_workspace",
		EngineStateVolumeID: "vol_state",
		ControlToken:        "ctl_123",
		RuntimeGeneration:   1,
	}, pauseCh: make(chan *RuntimeRecord, 1)}
	service := NewService(repo, runtime, nil, WithCreateIdlePauseDelay(100*time.Millisecond))
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
	activeRunID := "srun_active"
	if err := repo.UpsertRuntime(ctx, &RuntimeRecord{
		SessionID:           session.ID,
		Vendor:              "claude",
		RegionID:            "test-region",
		SandboxID:           "sbx_create",
		WrapperURL:          "https://wrapper.example.test",
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

	select {
	case paused := <-runtime.pauseCh:
		t.Fatalf("unexpected delayed create pause for active runtime: %#v", paused)
	case <-time.After(250 * time.Millisecond):
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

	first, err := service.CreateEnvironment(ctx, principal, CreateEnvironmentRequest{Name: "python"})
	if err != nil {
		t.Fatalf("CreateEnvironment first: %v", err)
	}
	if first == nil {
		t.Fatal("expected first environment")
	}
	_, err = service.CreateEnvironment(ctx, principal, CreateEnvironmentRequest{Name: " python "})
	if err == nil || !strings.Contains(err.Error(), "environment already exists") {
		t.Fatalf("CreateEnvironment duplicate error = %v, want conflict", err)
	}
}

func TestCreateEnvironmentRejectsUnsupportedManagedMetadata(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	principal := Principal{TeamID: "team_123"}
	ctx := context.Background()

	_, err := service.CreateEnvironment(ctx, principal, CreateEnvironmentRequest{
		Name: "python",
		Metadata: map[string]string{
			ManagedAgentsVaultLLMBaseURLKey: "https://llm.example.com",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "environment metadata") {
		t.Fatalf("CreateEnvironment error = %v, want managed metadata scope rejection", err)
	}
}

func TestUpdateEnvironmentRejectsUnsupportedManagedMetadata(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	principal := Principal{TeamID: "team_123"}
	ctx := context.Background()

	environment, err := service.CreateEnvironment(ctx, principal, CreateEnvironmentRequest{Name: "python"})
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	value := "0"
	_, err = service.UpdateEnvironment(ctx, principal, environment.ID, UpdateEnvironmentRequest{
		Metadata: MetadataPatchField{Set: true, Values: map[string]*string{ManagedAgentsSessionHardTTLSecondsKey: &value}},
	})
	if err == nil || !strings.Contains(err.Error(), "environment metadata") {
		t.Fatalf("UpdateEnvironment error = %v, want managed metadata scope rejection", err)
	}
}

func TestCreateEnvironmentRoundTripsAllPackageFields(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	principal := Principal{TeamID: "team_123"}
	ctx := context.Background()

	environment, err := service.CreateEnvironment(ctx, principal, CreateEnvironmentRequest{
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
	service := NewService(repo, noopRuntimeManager{}, nil)
	principal := Principal{TeamID: "team_123"}
	ctx := context.Background()

	created, err := service.CreateEnvironment(ctx, principal, CreateEnvironmentRequest{
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
	updated, err := service.UpdateEnvironment(ctx, principal, created.ID, UpdateEnvironmentRequest{
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

	deleted, err := service.DeleteEnvironment(ctx, principal, created.ID)
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
	service := NewService(repo, noopRuntimeManager{}, nil)
	principal := Principal{TeamID: "team_123"}
	ctx := context.Background()
	now := time.Now().UTC()

	environment, err := service.CreateEnvironment(ctx, principal, CreateEnvironmentRequest{
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

	updated, err := service.UpdateEnvironment(ctx, principal, environment.ID, UpdateEnvironmentRequest{
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
	if _, err := service.ensureEnvironmentArtifactRecord(ctx, principal.TeamID, &environment); err != nil {
		t.Fatalf("ensureEnvironmentArtifactRecord: %v", err)
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

	_, err := service.DeleteEnvironment(ctx, principal, environment.ID)
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
	if len(runtime.resumeReqs) != 1 {
		t.Fatalf("resume calls = %d, want 1", len(runtime.resumeReqs))
	}
	if len(runtime.pauseReqs) != 1 {
		t.Fatalf("pause calls = %d, want 1", len(runtime.pauseReqs))
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

func TestUpdateSessionRejectsHardTTLMetadataChange(t *testing.T) {
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
		Metadata: map[string]string{
			ManagedAgentsSessionHardTTLSecondsKey: "3600",
		},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	newValue := "0"
	_, err = service.UpdateSession(ctx, principal, credential, session.ID, UpdateSessionParams{
		Metadata: MetadataPatchField{Set: true, Values: map[string]*string{ManagedAgentsSessionHardTTLSecondsKey: &newValue}},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be changed") {
		t.Fatalf("UpdateSession error = %v, want immutable hard_ttl rejection", err)
	}
	stored, _, err := repo.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got := stored.Metadata[ManagedAgentsSessionHardTTLSecondsKey]; got != "3600" {
		t.Fatalf("stored hard_ttl metadata = %q, want 3600", got)
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

func createTestSession(ctx context.Context, service *Service, principal Principal, params CreateSessionParams) (*Session, error) {
	return service.CreateSession(ctx, principal, RequestCredential{Token: "token_123"}, params, "http://gateway.test")
}

type ensureRuntimeCall struct {
	Credential     RequestCredential
	SessionID      string
	GatewayBaseURL string
}

type createSessionRuntimeManager struct {
	runtime       *RuntimeRecord
	ensureCalls   []ensureRuntimeCall
	bootstrapReqs []*WrapperSessionBootstrapRequest
	pauseCalls    []*RuntimeRecord
	pauseCh       chan *RuntimeRecord
	callOrder     []string
}

func (m *createSessionRuntimeManager) EnsureRuntime(_ context.Context, _ Principal, credential RequestCredential, session *SessionRecord, _ map[string]any, gatewayBaseURL string) (*RuntimeRecord, error) {
	m.ensureCalls = append(m.ensureCalls, ensureRuntimeCall{Credential: credential, SessionID: session.ID, GatewayBaseURL: gatewayBaseURL})
	m.callOrder = append(m.callOrder, "ensure")
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
	return nil
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

func (m *createSessionRuntimeManager) PauseRuntime(_ context.Context, _ RequestCredential, runtime *RuntimeRecord) error {
	m.pauseCalls = append(m.pauseCalls, runtime)
	m.callOrder = append(m.callOrder, "pause")
	if m.pauseCh != nil {
		m.pauseCh <- runtime
	}
	return nil
}

func (m *createSessionRuntimeManager) ResumeRuntime(context.Context, RequestCredential, *RuntimeRecord) error {
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

func (noopRuntimeManager) PauseRuntime(context.Context, RequestCredential, *RuntimeRecord) error {
	return nil
}

func (noopRuntimeManager) ResumeRuntime(context.Context, RequestCredential, *RuntimeRecord) error {
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
	resumeReqs    []*RuntimeRecord
	pauseReqs     []*RuntimeRecord
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

func (m *updateSessionRuntimeManager) PauseRuntime(_ context.Context, _ RequestCredential, runtime *RuntimeRecord) error {
	m.pauseReqs = append(m.pauseReqs, runtime)
	return nil
}

func (m *updateSessionRuntimeManager) ResumeRuntime(_ context.Context, _ RequestCredential, runtime *RuntimeRecord) error {
	m.resumeReqs = append(m.resumeReqs, runtime)
	return nil
}

func (m *updateSessionRuntimeManager) DeleteWrapperSession(context.Context, RequestCredential, *RuntimeRecord, string) error {
	return nil
}

func (m *updateSessionRuntimeManager) DestroyRuntime(context.Context, RequestCredential, *RuntimeRecord) error {
	return nil
}

type testFileStore struct {
	content map[string][]byte
}

func newTestFileStore() *testFileStore {
	return &testFileStore{content: map[string][]byte{}}
}

func (s *testFileStore) PutFile(_ context.Context, _ RequestCredential, req FileStorePutRequest) (FileStoreObject, error) {
	data, err := io.ReadAll(req.Content)
	if err != nil {
		return FileStoreObject{}, err
	}
	s.content[req.FileID] = append([]byte(nil), data...)
	return FileStoreObject{
		VolumeID:  "vol_" + req.FileID,
		Path:      "/files/" + req.FileID,
		SizeBytes: int64(len(data)),
		SHA256:    "test-sha",
	}, nil
}

func (s *testFileStore) ReadFile(_ context.Context, _ RequestCredential, req FileStoreReadRequest) ([]byte, error) {
	if data, ok := s.content[req.FileID]; ok {
		return append([]byte(nil), data...), nil
	}
	if len(req.FallbackContent) > 0 {
		return append([]byte(nil), req.FallbackContent...), nil
	}
	return nil, ErrFileNotFound
}

func (s *testFileStore) DeleteFile(_ context.Context, _ RequestCredential, req FileStoreDeleteRequest) error {
	delete(s.content, req.FileID)
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
