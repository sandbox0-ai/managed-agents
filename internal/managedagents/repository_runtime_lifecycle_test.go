package managedagents

import (
	"context"
	"testing"
	"time"
)

func TestRuntimeSandboxDeletionKeepsRebuildState(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-20 * time.Minute)
	session := &SessionRecord{
		ID:               "sesn_lifecycle",
		TeamID:           "team_123",
		Vendor:           ManagedAgentsEngineClaude,
		EnvironmentID:    "env_123",
		WorkingDirectory: "/workspace",
		Metadata:         map[string]string{},
		Agent:            map[string]any{"type": "agent"},
		Resources:        []map[string]any{},
		VaultIDs:         []string{},
		Status:           "idle",
		CreatedAt:        old,
		UpdatedAt:        old,
	}
	if err := repo.CreateSession(ctx, session, nil); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	activeRunID := "srun_requires_action"
	runtime := &RuntimeRecord{
		SessionID:         session.ID,
		Vendor:            session.Vendor,
		RegionID:          "default",
		SandboxID:         "sbx_123",
		WrapperURL:        "https://wrapper.test",
		WorkspaceVolumeID: "vol_workspace",
		ControlToken:      "ctl_123",
		VendorSessionID:   "vendor_session",
		RuntimeGeneration: 3,
		ActiveRunID:       &activeRunID,
		CreatedAt:         old,
		UpdatedAt:         old,
	}
	if err := repo.UpsertRuntime(ctx, runtime); err != nil {
		t.Fatalf("UpsertRuntime: %v", err)
	}

	candidates, err := repo.ListIdleRuntimesForSandboxDeletion(ctx, now.Add(-10*time.Minute), 10)
	if err != nil {
		t.Fatalf("ListIdleRuntimesForSandboxDeletion: %v", err)
	}
	if len(candidates) != 1 || candidates[0].SandboxID != "sbx_123" {
		t.Fatalf("candidates = %#v, want runtime with sandbox sbx_123", candidates)
	}

	deletedAt := now
	if err := repo.MarkRuntimeSandboxDeleted(ctx, session.ID, "sbx_123", deletedAt); err != nil {
		t.Fatalf("MarkRuntimeSandboxDeleted: %v", err)
	}
	stored, err := repo.GetRuntime(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetRuntime: %v", err)
	}
	if stored.SandboxID != "" || stored.WrapperURL != "" || stored.ControlToken != "" {
		t.Fatalf("runtime sandbox fields = sandbox:%q wrapper:%q token:%q, want cleared", stored.SandboxID, stored.WrapperURL, stored.ControlToken)
	}
	if stored.WorkspaceVolumeID != "vol_workspace" || stored.VendorSessionID != "vendor_session" {
		t.Fatalf("runtime rebuild fields = volume:%q vendor_session:%q, want preserved", stored.WorkspaceVolumeID, stored.VendorSessionID)
	}
	if stored.ActiveRunID == nil || *stored.ActiveRunID != activeRunID {
		t.Fatalf("active_run_id = %v, want %q preserved", stored.ActiveRunID, activeRunID)
	}
	if stored.SandboxDeletedAt == nil || !stored.SandboxDeletedAt.Equal(deletedAt) {
		t.Fatalf("sandbox_deleted_at = %v, want %v", stored.SandboxDeletedAt, deletedAt)
	}
}

func TestListRunningRuntimesForTTLRefresh(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()
	startedAt := now.Add(-time.Minute)
	session := &SessionRecord{
		ID:                  "sesn_running",
		TeamID:              "team_123",
		Vendor:              ManagedAgentsEngineClaude,
		EnvironmentID:       "env_123",
		WorkingDirectory:    "/workspace",
		Metadata:            map[string]string{},
		Agent:               map[string]any{"type": "agent"},
		Resources:           []map[string]any{},
		VaultIDs:            []string{},
		Status:              "running",
		LastStatusStartedAt: &startedAt,
		CreatedAt:           startedAt,
		UpdatedAt:           startedAt,
	}
	if err := repo.CreateSession(ctx, session, nil); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := repo.UpsertRuntime(ctx, &RuntimeRecord{
		SessionID:         session.ID,
		Vendor:            session.Vendor,
		RegionID:          "default",
		SandboxID:         "sbx_running",
		WrapperURL:        "https://wrapper.test",
		WorkspaceVolumeID: "vol_workspace",
		ControlToken:      "ctl_123",
		RuntimeGeneration: 1,
		CreatedAt:         startedAt,
		UpdatedAt:         startedAt,
	}); err != nil {
		t.Fatalf("UpsertRuntime: %v", err)
	}

	runtimes, err := repo.ListRunningRuntimes(ctx, 10)
	if err != nil {
		t.Fatalf("ListRunningRuntimes: %v", err)
	}
	if len(runtimes) != 1 || runtimes[0].SandboxID != "sbx_running" {
		t.Fatalf("running runtimes = %#v, want sbx_running", runtimes)
	}
}
