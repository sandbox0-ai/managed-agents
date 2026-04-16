package managedagents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type recordingRuntimeManager struct {
	resolveResponse        *WrapperResolveActionsResponse
	bootstrapReqs          []*WrapperSessionBootstrapRequest
	startRunReqs           []*WrapperRunRequest
	resolveReqs            []*WrapperResolveActionsRequest
	interruptRunIDs        []string
	pauseReqs              []*RuntimeRecord
	resumeReqs             []*RuntimeRecord
	backgroundPauseEnabled bool
	backgroundPauseReqs    []*RuntimeRecord
}

func (m *recordingRuntimeManager) EnsureRuntime(context.Context, Principal, RequestCredential, *SessionRecord, map[string]any, string) (*RuntimeRecord, error) {
	return nil, nil
}

func (m *recordingRuntimeManager) BootstrapSession(_ context.Context, _ RequestCredential, _ *RuntimeRecord, req *WrapperSessionBootstrapRequest) error {
	m.bootstrapReqs = append(m.bootstrapReqs, req)
	return nil
}

func (m *recordingRuntimeManager) StartRun(_ context.Context, _ RequestCredential, _ *RuntimeRecord, req *WrapperRunRequest) error {
	m.startRunReqs = append(m.startRunReqs, req)
	return nil
}

func (m *recordingRuntimeManager) ResolveActions(_ context.Context, _ RequestCredential, _ *RuntimeRecord, req *WrapperResolveActionsRequest) (*WrapperResolveActionsResponse, error) {
	m.resolveReqs = append(m.resolveReqs, req)
	if m.resolveResponse == nil {
		return &WrapperResolveActionsResponse{}, nil
	}
	return m.resolveResponse, nil
}

func (m *recordingRuntimeManager) InterruptRun(_ context.Context, _ RequestCredential, _ *RuntimeRecord, runID string) error {
	m.interruptRunIDs = append(m.interruptRunIDs, runID)
	return nil
}

func (m *recordingRuntimeManager) PauseRuntime(_ context.Context, _ RequestCredential, runtime *RuntimeRecord) error {
	m.pauseReqs = append(m.pauseReqs, runtime)
	return nil
}

func (m *recordingRuntimeManager) ResumeRuntime(_ context.Context, _ RequestCredential, runtime *RuntimeRecord) error {
	m.resumeReqs = append(m.resumeReqs, runtime)
	return nil
}

func (m *recordingRuntimeManager) DeleteWrapperSession(context.Context, RequestCredential, *RuntimeRecord, string) error {
	return nil
}

func (m *recordingRuntimeManager) DestroyRuntime(context.Context, RequestCredential, *RuntimeRecord) error {
	return nil
}

func (m *recordingRuntimeManager) CanPauseRuntimeInBackground() bool {
	return m.backgroundPauseEnabled
}

func (m *recordingRuntimeManager) PauseRuntimeInBackground(_ context.Context, runtime *RuntimeRecord) error {
	m.backgroundPauseReqs = append(m.backgroundPauseReqs, runtime)
	return nil
}

func TestSendEventsInterruptClearsActiveRunAndMarksIdle(t *testing.T) {
	repo := newTestRepository(t)
	runtimeManager := &recordingRuntimeManager{}
	service := NewService(repo, runtimeManager, nil)
	now := time.Date(2026, 4, 10, 4, 30, 0, 0, time.UTC)
	record := &SessionRecord{
		ID:               "sesn_interrupt_123",
		TeamID:           "team_123",
		CreatedByUserID:  "user_123",
		Vendor:           "claude",
		EnvironmentID:    "env_123",
		WorkingDirectory: "/workspace",
		Agent:            map[string]any{"id": "agent_123", "type": "agent"},
		Resources:        []map[string]any{},
		VaultIDs:         []string{},
		Status:           "running",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := repo.CreateSession(context.Background(), record, map[string]any{}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	activeRunID := "run_active_123"
	if err := repo.UpsertRuntime(context.Background(), &RuntimeRecord{
		SessionID:         record.ID,
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

	events, err := service.SendEvents(context.Background(), Principal{TeamID: record.TeamID, UserID: record.CreatedByUserID}, RequestCredential{Token: "token_123"}, record.ID, SendEventsParams{Events: []InputEvent{{Type: "user.interrupt"}}}, "http://gateway.test")
	if err != nil {
		t.Fatalf("SendEvents: %v", err)
	}
	if len(events) != 1 || stringValue(events[0]["type"]) != "user.interrupt" {
		t.Fatalf("returned events = %#v, want one user.interrupt", events)
	}
	if len(runtimeManager.interruptRunIDs) != 1 || runtimeManager.interruptRunIDs[0] != activeRunID {
		t.Fatalf("interruptRunIDs = %#v, want %q", runtimeManager.interruptRunIDs, activeRunID)
	}
	if len(runtimeManager.pauseReqs) != 1 {
		t.Fatalf("pause calls = %d, want 1", len(runtimeManager.pauseReqs))
	}
	updatedRuntime, err := repo.GetRuntime(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetRuntime: %v", err)
	}
	if updatedRuntime.ActiveRunID != nil {
		t.Fatalf("active run id = %v, want nil", *updatedRuntime.ActiveRunID)
	}
	updatedSession, _, err := repo.GetSession(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if updatedSession.Status != "idle" {
		t.Fatalf("session status = %q, want idle", updatedSession.Status)
	}
	storedEvents, _, err := repo.ListEvents(context.Background(), record.ID, EventListOptions{Order: "asc"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(storedEvents) != 2 || stringValue(storedEvents[1]["type"]) != "session.status_idle" {
		t.Fatalf("stored events = %#v, want interrupt then status_idle", storedEvents)
	}
}

func TestSendEventsRejectsArchivedSession(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, &recordingRuntimeManager{}, nil)
	now := time.Date(2026, 4, 10, 5, 0, 0, 0, time.UTC)
	archivedAt := now.Add(time.Minute)
	record := &SessionRecord{
		ID:               "sesn_archived_123",
		TeamID:           "team_123",
		CreatedByUserID:  "user_123",
		Vendor:           "claude",
		EnvironmentID:    "env_123",
		WorkingDirectory: "/workspace",
		Agent:            map[string]any{"id": "agent_123", "type": "agent"},
		Resources:        []map[string]any{},
		VaultIDs:         []string{},
		Status:           "idle",
		ArchivedAt:       &archivedAt,
		CreatedAt:        now,
		UpdatedAt:        archivedAt,
	}
	if err := repo.CreateSession(context.Background(), record, map[string]any{}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	_, err := service.SendEvents(context.Background(), Principal{TeamID: record.TeamID, UserID: record.CreatedByUserID}, RequestCredential{Token: "token_123"}, record.ID, SendEventsParams{Events: []InputEvent{{Type: "user.message", Content: []UserContentBlock{{Type: "text", Text: "hello"}}}}}, "http://gateway.test")
	if !errors.Is(err, ErrSessionArchived) {
		t.Fatalf("SendEvents error = %v, want ErrSessionArchived", err)
	}
}

func TestSendEventsQueuesUserMessageWhileRunIsActive(t *testing.T) {
	repo := newTestRepository(t)
	runtimeManager := &recordingRuntimeManager{}
	service := NewService(repo, runtimeManager, nil)
	now := time.Date(2026, 4, 10, 5, 15, 0, 0, time.UTC)
	record := &SessionRecord{
		ID:               "sesn_active_send_123",
		TeamID:           "team_123",
		CreatedByUserID:  "user_123",
		Vendor:           "claude",
		EnvironmentID:    "env_123",
		WorkingDirectory: "/workspace",
		Agent:            map[string]any{"id": "agent_123", "type": "agent"},
		Resources:        []map[string]any{},
		VaultIDs:         []string{},
		Status:           "running",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := repo.CreateSession(context.Background(), record, map[string]any{}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	activeRunID := "run_active_123"
	runtime := &RuntimeRecord{
		SessionID:         record.ID,
		Vendor:            "claude",
		RegionID:          "test-region",
		SandboxID:         "sbx_active_send_123",
		WorkspaceVolumeID: "vol_workspace",
		ControlToken:      "ctl_123",
		RuntimeGeneration: 1,
		ActiveRunID:       &activeRunID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := repo.UpsertRuntime(context.Background(), runtime); err != nil {
		t.Fatalf("UpsertRuntime: %v", err)
	}

	returned, err := service.SendEvents(context.Background(), Principal{TeamID: record.TeamID, UserID: record.CreatedByUserID}, RequestCredential{Token: "token_123"}, record.ID, SendEventsParams{Events: []InputEvent{{Type: "user.message", Content: []UserContentBlock{{Type: "text", Text: "hello"}}}}}, "http://gateway.test")
	if err != nil {
		t.Fatalf("SendEvents: %v", err)
	}
	if len(returned) != 1 || returned[0]["processed_at"] != nil {
		t.Fatalf("returned events = %#v, want one queued event with nil processed_at", returned)
	}
	if len(runtimeManager.startRunReqs) != 0 {
		t.Fatalf("start run calls = %d, want 0", len(runtimeManager.startRunReqs))
	}
	events, _, err := repo.ListEvents(context.Background(), record.ID, EventListOptions{Order: "asc"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 || events[0]["processed_at"] != nil {
		t.Fatalf("stored events = %#v, want one queued event with nil processed_at", events)
	}
	payloadJSON, _ := json.Marshal(RuntimeCallbackPayload{
		SessionID: record.ID,
		RunID:     activeRunID,
		Events: []SessionEvent{{
			Type:       "session.status_idle",
			StopReason: &SessionStopReason{Type: "end_turn"},
		}},
	})
	body, _ := json.Marshal(map[string]any{
		"event_id":   "evt_hook_queue_123",
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
	if len(runtimeManager.startRunReqs) != 1 {
		t.Fatalf("start run calls = %d, want 1", len(runtimeManager.startRunReqs))
	}
	if runtimeManager.startRunReqs[0].RunID == "" || runtimeManager.startRunReqs[0].RunID == activeRunID {
		t.Fatalf("queued run id = %q, want new run id", runtimeManager.startRunReqs[0].RunID)
	}
	if len(runtimeManager.startRunReqs[0].InputEvents) != 1 || runtimeManager.startRunReqs[0].InputEvents[0].ProcessedAt == nil {
		t.Fatalf("queued run input events = %#v, want processed queued event", runtimeManager.startRunReqs[0].InputEvents)
	}
	updatedRuntime, err := repo.GetRuntime(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetRuntime: %v", err)
	}
	if updatedRuntime.ActiveRunID == nil || *updatedRuntime.ActiveRunID != runtimeManager.startRunReqs[0].RunID {
		t.Fatalf("active_run_id = %v, want %q", updatedRuntime.ActiveRunID, runtimeManager.startRunReqs[0].RunID)
	}
}

func TestDeleteSessionRejectsRunningSession(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, &recordingRuntimeManager{}, nil)
	now := time.Date(2026, 4, 10, 5, 30, 0, 0, time.UTC)
	record := &SessionRecord{
		ID:               "sesn_running_delete_123",
		TeamID:           "team_123",
		CreatedByUserID:  "user_123",
		Vendor:           "claude",
		EnvironmentID:    "env_123",
		WorkingDirectory: "/workspace",
		Agent:            map[string]any{"id": "agent_123", "type": "agent"},
		Resources:        []map[string]any{},
		VaultIDs:         []string{},
		Status:           "running",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := repo.CreateSession(context.Background(), record, map[string]any{}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	activeRunID := "run_active_123"
	if err := repo.UpsertRuntime(context.Background(), &RuntimeRecord{
		SessionID:         record.ID,
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

	_, err := service.DeleteSession(context.Background(), Principal{TeamID: record.TeamID, UserID: record.CreatedByUserID}, RequestCredential{Token: "token_123"}, record.ID)
	if !errors.Is(err, ErrSessionRunning) {
		t.Fatalf("DeleteSession error = %v, want ErrSessionRunning", err)
	}
	if _, _, err := repo.GetSession(context.Background(), record.ID); err != nil {
		t.Fatalf("session should still exist: %v", err)
	}
}

func TestSendEventsStartsNewRunWhenResolvedActionsRequireResume(t *testing.T) {
	repo := newTestRepository(t)
	runtime := &recordingRuntimeManager{
		resolveResponse: &WrapperResolveActionsResponse{
			ResolvedCount:      1,
			RemainingActionIDs: nil,
			ResumeRequired:     true,
		},
	}
	service := NewService(repo, runtime, nil)
	now := time.Date(2026, 4, 10, 4, 0, 0, 0, time.UTC)
	record := &SessionRecord{
		ID:               "sesn_resume_123",
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
	activeRunID := "run_pending_123"
	if err := repo.UpsertRuntime(context.Background(), &RuntimeRecord{
		SessionID:         record.ID,
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
	if err := repo.AppendEvents(context.Background(), record.ID, []map[string]any{stampEvent(map[string]any{
		"type": "session.status_idle",
		"stop_reason": map[string]any{
			"type":      "requires_action",
			"event_ids": []any{"tool_1"},
		},
	}, now)}); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}

	events, err := service.SendEvents(context.Background(), Principal{TeamID: record.TeamID, UserID: record.CreatedByUserID}, RequestCredential{Token: "token_123"}, record.ID, SendEventsParams{Events: []InputEvent{{
		Type:      "user.tool_confirmation",
		ToolUseID: "tool_1",
		Result:    "allow",
	}}}, "http://gateway.test")
	if err != nil {
		t.Fatalf("SendEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	if len(runtime.resolveReqs) != 1 {
		t.Fatalf("resolve calls = %d, want 1", len(runtime.resolveReqs))
	}
	if len(runtime.resumeReqs) != 1 {
		t.Fatalf("resume calls = %d, want 1", len(runtime.resumeReqs))
	}
	if len(runtime.bootstrapReqs) != 1 {
		t.Fatalf("bootstrap calls = %d, want 1", len(runtime.bootstrapReqs))
	}
	if runtime.bootstrapReqs[0].SessionID != record.ID {
		t.Fatalf("bootstrap session_id = %q, want %q", runtime.bootstrapReqs[0].SessionID, record.ID)
	}
	if len(runtime.startRunReqs) != 1 {
		t.Fatalf("start run calls = %d, want 1", len(runtime.startRunReqs))
	}
	if runtime.startRunReqs[0].SessionID != record.ID {
		t.Fatalf("start run session_id = %q, want %q", runtime.startRunReqs[0].SessionID, record.ID)
	}
	if runtime.startRunReqs[0].RunID == "" || runtime.startRunReqs[0].RunID == activeRunID {
		t.Fatalf("start run id = %q, expected new run id", runtime.startRunReqs[0].RunID)
	}
	updatedRuntime, err := repo.GetRuntime(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetRuntime: %v", err)
	}
	if updatedRuntime.ActiveRunID == nil || *updatedRuntime.ActiveRunID != runtime.startRunReqs[0].RunID {
		t.Fatalf("active run id = %v, want %q", updatedRuntime.ActiveRunID, runtime.startRunReqs[0].RunID)
	}
}

func TestValidateInputEventsRejectsUnexpectedToolConfirmationFields(t *testing.T) {
	err := validateInputEvents([]map[string]any{{
		"type":        "user.tool_confirmation",
		"tool_use_id": "tool_1",
		"result":      "allow",
		"unexpected":  true,
	}})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateInputEventsAcceptsDocumentContent(t *testing.T) {
	err := validateInputEvents([]map[string]any{{
		"type": "user.message",
		"content": []any{map[string]any{
			"type": "document",
			"source": map[string]any{
				"type":       "text",
				"media_type": "text/plain",
				"data":       "hello",
			},
		}},
	}})
	if err != nil {
		t.Fatalf("validateInputEvents: %v", err)
	}
}

func TestValidateInputEventsRejectsTooLongToolConfirmationID(t *testing.T) {
	err := validateInputEvents([]map[string]any{{
		"type":        "user.tool_confirmation",
		"tool_use_id": strings.Repeat("a", 129),
		"result":      "allow",
	}})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateInputEventsRejectsTooLongDenyMessage(t *testing.T) {
	err := validateInputEvents([]map[string]any{{
		"type":         "user.tool_confirmation",
		"tool_use_id":  "tool_1",
		"result":       "deny",
		"deny_message": strings.Repeat("a", 10001),
	}})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateInputEventsRejectsTooLongCustomToolUseID(t *testing.T) {
	err := validateInputEvents([]map[string]any{{
		"type":               "user.custom_tool_result",
		"custom_tool_use_id": strings.Repeat("a", 129),
	}})
	if err == nil {
		t.Fatal("expected validation error")
	}
}
