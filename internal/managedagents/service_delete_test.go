package managedagents

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDeleteSessionClearsSessionResourceSecrets(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, &recordingRuntimeManager{}, nil)
	now := time.Date(2026, 4, 10, 6, 0, 0, 0, time.UTC)
	record := &SessionRecord{
		ID:               "sesn_delete_secrets_123",
		TeamID:           "team_123",
		CreatedByUserID:  "user_123",
		Vendor:           "claude",
		EnvironmentID:    "env_123",
		WorkingDirectory: "/workspace",
		Agent:            map[string]any{"id": "agent_123", "type": "agent"},
		Resources:        []map[string]any{},
		VaultIDs:         []string{},
		Status:           "idle",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := repo.CreateSession(context.Background(), record, map[string]any{}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := repo.UpsertSessionResourceSecret(context.Background(), record.ID, "resource_123", map[string]any{"authorization_token": "secret"}); err != nil {
		t.Fatalf("UpsertSessionResourceSecret: %v", err)
	}

	if _, err := service.DeleteSession(context.Background(), Principal{TeamID: record.TeamID, UserID: record.CreatedByUserID}, RequestCredential{Token: "token_123"}, record.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, _, err := repo.GetSession(context.Background(), record.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("GetSession error = %v, want ErrSessionNotFound", err)
	}
	secret, err := repo.GetSessionResourceSecret(context.Background(), record.ID, "resource_123")
	if err != nil {
		t.Fatalf("GetSessionResourceSecret: %v", err)
	}
	if secret != nil {
		t.Fatalf("session resource secret = %#v, want nil", secret)
	}
}

func TestDeleteSessionKeepsRuntimeWhenDestroyFails(t *testing.T) {
	repo := newTestRepository(t)
	destroyErr := errors.New("sandbox0 delete failed")
	service := NewService(repo, &recordingRuntimeManager{destroyErr: destroyErr}, nil)
	now := time.Date(2026, 4, 10, 6, 0, 0, 0, time.UTC)
	record := &SessionRecord{
		ID:               "sesn_delete_retry_123",
		TeamID:           "team_123",
		CreatedByUserID:  "user_123",
		Vendor:           "claude",
		EnvironmentID:    "env_123",
		WorkingDirectory: "/workspace",
		Agent:            map[string]any{"id": "agent_123", "type": "agent"},
		Resources:        []map[string]any{},
		VaultIDs:         []string{},
		Status:           "idle",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := repo.CreateSession(context.Background(), record, map[string]any{}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := repo.UpsertRuntime(context.Background(), &RuntimeRecord{
		SessionID:         record.ID,
		Vendor:            record.Vendor,
		RegionID:          "default",
		SandboxID:         "sbx_retry",
		WrapperURL:        "https://wrapper.test",
		WorkspaceVolumeID: "vol_retry",
		ControlToken:      "ctl_retry",
		RuntimeGeneration: 1,
		CreatedAt:         now,
		UpdatedAt:         now,
	}); err != nil {
		t.Fatalf("UpsertRuntime: %v", err)
	}

	_, err := service.DeleteSession(context.Background(), Principal{TeamID: record.TeamID, UserID: record.CreatedByUserID}, RequestCredential{Token: "token_123"}, record.ID)
	if err == nil || !strings.Contains(err.Error(), "sandbox0 delete failed") {
		t.Fatalf("DeleteSession error = %v, want destroy failure", err)
	}
	if _, _, err := repo.GetSession(context.Background(), record.ID); err != nil {
		t.Fatalf("GetSession after failed delete: %v", err)
	}
	runtime, err := repo.GetRuntime(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetRuntime after failed delete: %v", err)
	}
	if runtime.SandboxID != "sbx_retry" || runtime.WorkspaceVolumeID != "vol_retry" {
		t.Fatalf("runtime = sandbox:%q volume:%q, want retry identifiers preserved", runtime.SandboxID, runtime.WorkspaceVolumeID)
	}
}
