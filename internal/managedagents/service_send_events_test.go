package managedagents

import (
	"context"
	"testing"
	"time"
)

type recordingRuntimeManager struct {
	resolveResponse *WrapperResolveActionsResponse
	bootstrapReqs   []*WrapperSessionBootstrapRequest
	startRunReqs    []*WrapperRunRequest
	resolveReqs     []*WrapperResolveActionsRequest
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

func (m *recordingRuntimeManager) InterruptRun(context.Context, RequestCredential, *RuntimeRecord, string) error {
	return nil
}

func (m *recordingRuntimeManager) DeleteWrapperSession(context.Context, RequestCredential, *RuntimeRecord, string) error {
	return nil
}

func (m *recordingRuntimeManager) DestroyRuntime(context.Context, RequestCredential, *RuntimeRecord) error {
	return nil
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
		SessionID:           record.ID,
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
	if err := repo.AppendEvents(context.Background(), record.ID, []map[string]any{stampEvent(map[string]any{
		"type": "session.status_idle",
		"stop_reason": map[string]any{
			"type":      "requires_action",
			"event_ids": []any{"tool_1"},
		},
	}, now)}); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}

	events, err := service.SendEvents(context.Background(), Principal{TeamID: record.TeamID, UserID: record.CreatedByUserID}, RequestCredential{Token: "token_123"}, record.ID, SendEventsParams{Events: []map[string]any{{
		"type":        "user.tool_confirmation",
		"tool_use_id": "tool_1",
		"result":      "allow",
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
