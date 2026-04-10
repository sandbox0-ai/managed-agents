package managedagents

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

func TestHandleSandboxWebhookAppliesAgentEvents(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, &recordingRuntimeManager{}, nil)
	now := time.Date(2026, 4, 10, 6, 0, 0, 0, time.UTC)
	record := &SessionRecord{
		ID:               "sesn_webhook_123",
		TeamID:           "team_123",
		CreatedByUserID:  "user_123",
		Vendor:           "claude",
		EnvironmentID:    "env_123",
		WorkingDirectory: "/workspace",
		Agent:            map[string]any{"id": "agent_123", "type": "agent"},
		Status:           "running",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := repo.CreateSession(context.Background(), record, map[string]any{}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	activeRunID := "run_123"
	runtime := &RuntimeRecord{
		SessionID:           record.ID,
		Vendor:              "claude",
		RegionID:            "test-region",
		SandboxID:           "sbx_123",
		WrapperURL:          "https://wrapper.example.test",
		WorkspaceVolumeID:   "vol_workspace",
		EngineStateVolumeID: "vol_state",
		ControlToken:        "secret_123",
		RuntimeGeneration:   1,
		ActiveRunID:         &activeRunID,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if err := repo.UpsertRuntime(context.Background(), runtime); err != nil {
		t.Fatalf("UpsertRuntime: %v", err)
	}
	payloadJSON, _ := json.Marshal(RuntimeCallbackPayload{
		SessionID:       record.ID,
		RunID:           activeRunID,
		VendorSessionID: "vendor_123",
		Events: []map[string]any{
			{"type": "agent.message", "content": []any{map[string]any{"type": "text", "text": "done"}}},
			{"type": "session.status_idle", "stop_reason": map[string]any{"type": "end_turn"}},
		},
	})
	body, _ := json.Marshal(map[string]any{
		"event_id":   "evt_hook_123",
		"event_type": "agent.event",
		"sandbox_id": runtime.SandboxID,
		"payload":    json.RawMessage(payloadJSON),
	})
	if err := service.HandleSandboxWebhook(context.Background(), body, webhookSignature(runtime.ControlToken, body)); err != nil {
		t.Fatalf("HandleSandboxWebhook: %v", err)
	}
	updatedRuntime, err := repo.GetRuntime(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetRuntime: %v", err)
	}
	if updatedRuntime.VendorSessionID != "vendor_123" {
		t.Fatalf("vendor_session_id = %q, want vendor_123", updatedRuntime.VendorSessionID)
	}
	if updatedRuntime.ActiveRunID != nil {
		t.Fatalf("active_run_id = %v, want nil", updatedRuntime.ActiveRunID)
	}
	events, _, err := repo.ListEvents(context.Background(), record.ID, EventListOptions{Limit: 10, Order: "asc"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if got := stringValue(events[0]["type"]); got != "agent.message" {
		t.Fatalf("first event type = %q, want agent.message", got)
	}
	updatedSession, _, err := repo.GetSession(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if updatedSession.Status != "idle" {
		t.Fatalf("session status = %q, want idle", updatedSession.Status)
	}
}

func webhookSignature(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
