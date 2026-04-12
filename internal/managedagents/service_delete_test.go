package managedagents

import (
	"context"
	"errors"
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
