package managedagentsruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sandbox0-ai/managed-agent/internal/dbpool"
	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
	managedagentmigrations "github.com/sandbox0-ai/managed-agent/internal/managedagents/migrations"
	"github.com/sandbox0-ai/managed-agent/internal/migrate"
	"go.uber.org/zap"
)

func TestCleanupEnvironmentArtifactsWaitsForShortBuildRace(t *testing.T) {
	repo := newRuntimeTestRepository(t)
	ctx := context.Background()
	artifact := testEnvironmentArtifact(gatewaymanagedagents.EnvironmentArtifactStatusBuilding)
	if err := repo.CreateEnvironmentArtifact(ctx, artifact); err != nil {
		t.Fatalf("CreateEnvironmentArtifact: %v", err)
	}

	updateDone := make(chan error, 1)
	go func() {
		time.Sleep(150 * time.Millisecond)
		stored, err := repo.GetEnvironmentArtifact(ctx, artifact.TeamID, artifact.ID)
		if err != nil {
			updateDone <- err
			return
		}
		stored.Status = gatewaymanagedagents.EnvironmentArtifactStatusFailed
		stored.UpdatedAt = time.Now().UTC()
		updateDone <- repo.UpdateEnvironmentArtifact(ctx, stored)
	}()

	mgr := &SDKRuntimeManager{
		repo:   repo,
		cfg:    Config{Enabled: true, SandboxBaseURL: "http://127.0.0.1:1", SandboxRequestTimeout: time.Second, SandboxAdminAPIKey: "runtime_123"},
		logger: zap.NewNop(),
	}
	if err := mgr.CleanupEnvironmentArtifacts(ctx, gatewaymanagedagents.RequestCredential{}, artifact.TeamID, artifact.EnvironmentID); err != nil {
		t.Fatalf("CleanupEnvironmentArtifacts: %v", err)
	}
	if err := <-updateDone; err != nil {
		t.Fatalf("finish build update: %v", err)
	}

	stored, err := repo.GetEnvironmentArtifact(ctx, artifact.TeamID, artifact.ID)
	if err != nil {
		t.Fatalf("GetEnvironmentArtifact after cleanup: %v", err)
	}
	if stored.Status != gatewaymanagedagents.EnvironmentArtifactStatusArchived {
		t.Fatalf("artifact status = %q, want archived", stored.Status)
	}
	if stored.ArchivedAt == nil {
		t.Fatal("artifact archived_at = nil, want timestamp")
	}
}

func TestCleanupEnvironmentArtifactsReturnsConflictWhenBuildKeepsRunning(t *testing.T) {
	repo := newRuntimeTestRepository(t)
	ctx := context.Background()
	artifact := testEnvironmentArtifact(gatewaymanagedagents.EnvironmentArtifactStatusBuilding)
	if err := repo.CreateEnvironmentArtifact(ctx, artifact); err != nil {
		t.Fatalf("CreateEnvironmentArtifact: %v", err)
	}

	mgr := &SDKRuntimeManager{
		repo:   repo,
		cfg:    Config{Enabled: true, SandboxBaseURL: "http://127.0.0.1:1", SandboxRequestTimeout: 250 * time.Millisecond, SandboxAdminAPIKey: "runtime_123"},
		logger: zap.NewNop(),
	}
	err := mgr.CleanupEnvironmentArtifacts(ctx, gatewaymanagedagents.RequestCredential{}, artifact.TeamID, artifact.EnvironmentID)
	if !errors.Is(err, gatewaymanagedagents.ErrEnvironmentArtifactBuilding) {
		t.Fatalf("CleanupEnvironmentArtifacts error = %v, want ErrEnvironmentArtifactBuilding", err)
	}
	if !strings.Contains(err.Error(), artifact.ID) {
		t.Fatalf("CleanupEnvironmentArtifacts error = %v, want artifact id", err)
	}

	stored, getErr := repo.GetEnvironmentArtifact(ctx, artifact.TeamID, artifact.ID)
	if getErr != nil {
		t.Fatalf("GetEnvironmentArtifact after cleanup failure: %v", getErr)
	}
	if stored.Status != gatewaymanagedagents.EnvironmentArtifactStatusBuilding {
		t.Fatalf("artifact status = %q, want still building", stored.Status)
	}
}

func testEnvironmentArtifact(status string) *gatewaymanagedagents.EnvironmentArtifact {
	now := time.Now().UTC()
	return &gatewaymanagedagents.EnvironmentArtifact{
		ID:             "envart_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16],
		TeamID:         "team_123",
		EnvironmentID:  "env_123",
		Digest:         strings.ReplaceAll(uuid.NewString(), "-", ""),
		Status:         status,
		ConfigSnapshot: map[string]any{"type": "cloud"},
		Compatibility:  gatewaymanagedagents.DefaultEnvironmentArtifactCompatibility(),
		Assets:         gatewaymanagedagents.EnvironmentArtifactAssets{},
		BuildLog:       "",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

func newRuntimeTestRepository(t *testing.T) *gatewaymanagedagents.Repository {
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

	schema := fmt.Sprintf("managed_agents_runtime_test_%s", strings.ReplaceAll(uuid.NewString(), "-", ""))
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

	return gatewaymanagedagents.NewRepository(pool)
}
