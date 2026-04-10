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

func TestListAgentVersionsSupportsCursorPagination(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	principal := Principal{TeamID: "team_123"}
	baseTime := time.Date(2026, 4, 10, 1, 0, 0, 0, time.UTC)

	agent := map[string]any{
		"id":          "agent_123",
		"type":        "agent",
		"version":     1,
		"name":        "Claude Agent",
		"description": nil,
		"model":       map[string]any{"id": "claude-sonnet-4-20250514", "speed": "standard"},
		"system":      nil,
		"tools":       []any{},
		"mcp_servers": []any{},
		"skills":      []any{},
		"created_at":  baseTime.Format(time.RFC3339),
		"updated_at":  baseTime.Format(time.RFC3339),
	}
	if err := repo.CreateAgent(context.Background(), principal.TeamID, "claude", 1, agent, baseTime); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	for version := 2; version <= 3; version++ {
		when := baseTime.Add(time.Duration(version-1) * time.Minute)
		snapshot := cloneMap(agent)
		snapshot["version"] = version
		snapshot["updated_at"] = when.Format(time.RFC3339)
		if err := repo.UpdateAgent(context.Background(), principal.TeamID, "agent_123", "claude", version, snapshot, nil, when); err != nil {
			t.Fatalf("UpdateAgent version %d: %v", version, err)
		}
	}

	page1, nextPage, err := service.ListAgentVersions(context.Background(), principal, "agent_123", 2, "")
	if err != nil {
		t.Fatalf("ListAgentVersions page1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page1 len = %d, want 2", len(page1))
	}
	if got := intValue(page1[0]["version"]); got != 3 {
		t.Fatalf("page1[0].version = %d, want 3", got)
	}
	if got := intValue(page1[1]["version"]); got != 2 {
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
	if got := intValue(page2[0]["version"]); got != 1 {
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
