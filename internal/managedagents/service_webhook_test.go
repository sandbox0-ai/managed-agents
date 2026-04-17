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
	runtimeManager := &recordingRuntimeManager{}
	service := NewService(repo, runtimeManager, nil)
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
		SessionID:         record.ID,
		Vendor:            "claude",
		RegionID:          "test-region",
		SandboxID:         "sbx_123",
		WrapperURL:        "https://wrapper.example.test",
		WorkspaceVolumeID: "vol_workspace",
		ControlToken:      "secret_123",
		RuntimeGeneration: 1,
		ActiveRunID:       &activeRunID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := repo.UpsertRuntime(context.Background(), runtime); err != nil {
		t.Fatalf("UpsertRuntime: %v", err)
	}
	payloadJSON, _ := json.Marshal(RuntimeCallbackPayload{
		SessionID:       record.ID,
		RunID:           activeRunID,
		VendorSessionID: "vendor_123",
		Events: []SessionEvent{
			{Type: "agent.message", Content: []UserContentBlock{{Type: "text", Text: "done"}}},
			{Type: "session.status_idle", StopReason: &SessionStopReason{Type: "end_turn"}},
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
	eventsBeforeProcessing, _, err := repo.ListEvents(context.Background(), record.ID, EventListOptions{Limit: 10, Order: "asc"})
	if err != nil {
		t.Fatalf("ListEvents before processing: %v", err)
	}
	if len(eventsBeforeProcessing) != 0 {
		t.Fatalf("events before processing = %d, want 0", len(eventsBeforeProcessing))
	}
	processed, err := service.ProcessNextRuntimeWebhookJob(context.Background(), "test_worker")
	if err != nil || !processed {
		t.Fatalf("ProcessNextRuntimeWebhookJob processed=%v err=%v, want processed", processed, err)
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

func TestHandleSandboxWebhookKeepsRequiresActionRunActive(t *testing.T) {
	repo := newTestRepository(t)
	runtimeManager := &recordingRuntimeManager{}
	service := NewService(repo, runtimeManager, nil)
	now := time.Date(2026, 4, 10, 6, 30, 0, 0, time.UTC)
	record := &SessionRecord{
		ID:               "sesn_webhook_requires_action_123",
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
	activeRunID := "run_requires_action_123"
	runtime := &RuntimeRecord{
		SessionID:         record.ID,
		Vendor:            "claude",
		RegionID:          "test-region",
		SandboxID:         "sbx_requires_action_123",
		WrapperURL:        "https://wrapper.example.test",
		WorkspaceVolumeID: "vol_workspace",
		ControlToken:      "secret_456",
		RuntimeGeneration: 1,
		ActiveRunID:       &activeRunID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := repo.UpsertRuntime(context.Background(), runtime); err != nil {
		t.Fatalf("UpsertRuntime: %v", err)
	}
	payloadJSON, _ := json.Marshal(RuntimeCallbackPayload{
		SessionID: record.ID,
		RunID:     activeRunID,
		Events: []SessionEvent{{
			Type:       "session.status_idle",
			StopReason: &SessionStopReason{Type: "requires_action", EventIDs: []string{"tool_1"}},
		}},
	})
	body, _ := json.Marshal(map[string]any{
		"event_id":   "evt_hook_requires_action_123",
		"event_type": "agent.event",
		"sandbox_id": runtime.SandboxID,
		"payload":    json.RawMessage(payloadJSON),
	})

	if err := service.HandleSandboxWebhook(context.Background(), body, webhookSignature(runtime.ControlToken, body)); err != nil {
		t.Fatalf("HandleSandboxWebhook: %v", err)
	}
	processed, err := service.ProcessNextRuntimeWebhookJob(context.Background(), "test_worker")
	if err != nil || !processed {
		t.Fatalf("ProcessNextRuntimeWebhookJob processed=%v err=%v, want processed", processed, err)
	}
	updatedRuntime, err := repo.GetRuntime(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetRuntime: %v", err)
	}
	if updatedRuntime.ActiveRunID == nil || *updatedRuntime.ActiveRunID != activeRunID {
		t.Fatalf("active_run_id = %v, want %q", updatedRuntime.ActiveRunID, activeRunID)
	}
}

func TestHandleSandboxWebhookSkipsStaleRun(t *testing.T) {
	repo := newTestRepository(t)
	runtimeManager := &recordingRuntimeManager{}
	service := NewService(repo, runtimeManager, nil)
	now := time.Date(2026, 4, 10, 7, 0, 0, 0, time.UTC)
	record := &SessionRecord{
		ID:               "sesn_webhook_stale_123",
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
	activeRunID := "run_new_123"
	runtime := &RuntimeRecord{
		SessionID:         record.ID,
		Vendor:            "claude",
		RegionID:          "test-region",
		SandboxID:         "sbx_stale_123",
		WrapperURL:        "https://wrapper.example.test",
		WorkspaceVolumeID: "vol_workspace",
		ControlToken:      "secret_stale",
		RuntimeGeneration: 1,
		ActiveRunID:       &activeRunID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := repo.UpsertRuntime(context.Background(), runtime); err != nil {
		t.Fatalf("UpsertRuntime: %v", err)
	}
	payloadJSON, _ := json.Marshal(RuntimeCallbackPayload{
		SessionID: record.ID,
		RunID:     "run_old_123",
		Events: []SessionEvent{{
			Type:       "session.status_idle",
			StopReason: &SessionStopReason{Type: "end_turn"},
		}},
	})
	body, _ := json.Marshal(map[string]any{
		"event_id":   "evt_hook_stale_123",
		"event_type": "agent.event",
		"sandbox_id": runtime.SandboxID,
		"payload":    json.RawMessage(payloadJSON),
	})
	if err := service.HandleSandboxWebhook(context.Background(), body, webhookSignature(runtime.ControlToken, body)); err != nil {
		t.Fatalf("HandleSandboxWebhook: %v", err)
	}
	processed, err := service.ProcessNextRuntimeWebhookJob(context.Background(), "test_worker")
	if err != nil || !processed {
		t.Fatalf("ProcessNextRuntimeWebhookJob processed=%v err=%v, want processed", processed, err)
	}
	updatedRuntime, err := repo.GetRuntime(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetRuntime: %v", err)
	}
	if updatedRuntime.ActiveRunID == nil || *updatedRuntime.ActiveRunID != activeRunID {
		t.Fatalf("active_run_id = %v, want %q", updatedRuntime.ActiveRunID, activeRunID)
	}
	events, _, err := repo.ListEvents(context.Background(), record.ID, EventListOptions{Limit: 10, Order: "asc"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events len = %d, want 0", len(events))
	}
}

func TestHandleSandboxWebhookMarksRunningSandboxKilled(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, &recordingRuntimeManager{}, nil)
	startedAt := time.Date(2026, 4, 10, 7, 30, 0, 0, time.UTC)
	record := &SessionRecord{
		ID:                  "sesn_webhook_sandbox_killed",
		TeamID:              "team_123",
		CreatedByUserID:     "user_123",
		Vendor:              "claude",
		EnvironmentID:       "env_123",
		WorkingDirectory:    "/workspace",
		Agent:               map[string]any{"id": "agent_123", "type": "agent"},
		Status:              "running",
		LastStatusStartedAt: &startedAt,
		CreatedAt:           startedAt,
		UpdatedAt:           startedAt,
	}
	if err := repo.CreateSession(context.Background(), record, map[string]any{}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	activeRunID := "run_killed_123"
	runtime := &RuntimeRecord{
		SessionID:         record.ID,
		Vendor:            "claude",
		RegionID:          "test-region",
		SandboxID:         "sbx_killed_123",
		WrapperURL:        "https://wrapper.example.test",
		WorkspaceVolumeID: "vol_workspace",
		ControlToken:      "secret_killed",
		RuntimeGeneration: 1,
		ActiveRunID:       &activeRunID,
		CreatedAt:         startedAt,
		UpdatedAt:         startedAt,
	}
	if err := repo.UpsertRuntime(context.Background(), runtime); err != nil {
		t.Fatalf("UpsertRuntime: %v", err)
	}
	body, _ := json.Marshal(map[string]any{
		"event_id":   "evt_sandbox_killed_123",
		"event_type": "sandbox.killed",
		"sandbox_id": runtime.SandboxID,
	})

	if err := service.HandleSandboxWebhook(context.Background(), body, webhookSignature(runtime.ControlToken, body)); err != nil {
		t.Fatalf("HandleSandboxWebhook: %v", err)
	}
	updatedRuntime, err := repo.GetRuntime(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetRuntime: %v", err)
	}
	if updatedRuntime.SandboxID != "" || updatedRuntime.ActiveRunID != nil {
		t.Fatalf("runtime = sandbox:%q active:%v, want cleared sandbox and active run", updatedRuntime.SandboxID, updatedRuntime.ActiveRunID)
	}
	updatedSession, _, err := repo.GetSession(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if updatedSession.Status != "terminated" {
		t.Fatalf("session status = %q, want terminated", updatedSession.Status)
	}
	events, _, err := repo.ListEvents(context.Background(), record.ID, EventListOptions{Limit: 10, Order: "asc"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 || stringValue(events[0]["type"]) != "session.error" || stringValue(events[1]["type"]) != "session.status_terminated" {
		t.Fatalf("events = %#v, want terminal sandbox lost events", events)
	}
}

func TestHandleSandboxWebhookPreservesDeletedRequiresActionRun(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, &recordingRuntimeManager{}, nil)
	now := time.Date(2026, 4, 10, 8, 0, 0, 0, time.UTC)
	record := &SessionRecord{
		ID:               "sesn_webhook_sandbox_deleted_requires_action",
		TeamID:           "team_123",
		CreatedByUserID:  "user_123",
		Vendor:           "claude",
		EnvironmentID:    "env_123",
		WorkingDirectory: "/workspace",
		Agent:            map[string]any{"id": "agent_123", "type": "agent"},
		Status:           "idle",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := repo.CreateSession(context.Background(), record, map[string]any{}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	activeRunID := "run_deleted_requires_action_123"
	runtime := &RuntimeRecord{
		SessionID:         record.ID,
		Vendor:            "claude",
		RegionID:          "test-region",
		SandboxID:         "sbx_deleted_requires_action_123",
		WrapperURL:        "https://wrapper.example.test",
		WorkspaceVolumeID: "vol_workspace",
		ControlToken:      "secret_deleted",
		RuntimeGeneration: 1,
		ActiveRunID:       &activeRunID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := repo.UpsertRuntime(context.Background(), runtime); err != nil {
		t.Fatalf("UpsertRuntime: %v", err)
	}
	body, _ := json.Marshal(map[string]any{
		"event_id":   "evt_sandbox_deleted_123",
		"event_type": "sandbox.deleted",
		"sandbox_id": runtime.SandboxID,
	})

	if err := service.HandleSandboxWebhook(context.Background(), body, webhookSignature(runtime.ControlToken, body)); err != nil {
		t.Fatalf("HandleSandboxWebhook: %v", err)
	}
	updatedRuntime, err := repo.GetRuntime(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetRuntime: %v", err)
	}
	if updatedRuntime.SandboxID != "" || updatedRuntime.ActiveRunID == nil || *updatedRuntime.ActiveRunID != activeRunID {
		t.Fatalf("runtime = sandbox:%q active:%v, want cleared sandbox and preserved active run", updatedRuntime.SandboxID, updatedRuntime.ActiveRunID)
	}
	updatedSession, _, err := repo.GetSession(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if updatedSession.Status != "idle" {
		t.Fatalf("session status = %q, want idle", updatedSession.Status)
	}
	events, _, err := repo.ListEvents(context.Background(), record.ID, EventListOptions{Limit: 10, Order: "asc"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events len = %d, want 0", len(events))
	}
}

func webhookSignature(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
