package managedagents

import (
	"context"
	"testing"
	"time"
)

func TestListEventsAfterIDUsesInsertionPosition(t *testing.T) {
	repo := newTestRepository(t)
	record := createRepositoryEventTestSession(t, repo, "sesn_events_after_position")

	if err := repo.AppendEvents(context.Background(), record.ID, []map[string]any{
		{"id": "evt_z", "type": "user.message"},
		{"id": "evt_a", "type": "session.status_running"},
	}); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}
	forceSameEventTimestamp(t, repo, record.ID)

	events, err := repo.ListEventsAfterID(context.Background(), record.ID, "evt_z", 10)
	if err != nil {
		t.Fatalf("ListEventsAfterID: %v", err)
	}
	if len(events) != 1 || stringValue(events[0]["id"]) != "evt_a" {
		t.Fatalf("events after evt_z = %#v, want evt_a", events)
	}
}

func TestListEventsPaginationUsesInsertionPosition(t *testing.T) {
	repo := newTestRepository(t)
	record := createRepositoryEventTestSession(t, repo, "sesn_events_page_position")

	if err := repo.AppendEvents(context.Background(), record.ID, []map[string]any{
		{"id": "evt_z", "type": "user.message"},
		{"id": "evt_a", "type": "session.status_running"},
	}); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}
	forceSameEventTimestamp(t, repo, record.ID)

	firstPage, nextPage, err := repo.ListEvents(context.Background(), record.ID, EventListOptions{Limit: 1, Order: "asc"})
	if err != nil {
		t.Fatalf("ListEvents first page: %v", err)
	}
	if len(firstPage) != 1 || stringValue(firstPage[0]["id"]) != "evt_z" || nextPage == nil {
		t.Fatalf("first page = %#v next=%v, want evt_z with next page", firstPage, nextPage)
	}

	secondPage, _, err := repo.ListEvents(context.Background(), record.ID, EventListOptions{Limit: 1, Order: "asc", Page: *nextPage})
	if err != nil {
		t.Fatalf("ListEvents second page: %v", err)
	}
	if len(secondPage) != 1 || stringValue(secondPage[0]["id"]) != "evt_a" {
		t.Fatalf("second page = %#v, want evt_a", secondPage)
	}
}

func createRepositoryEventTestSession(t *testing.T, repo *Repository, sessionID string) *SessionRecord {
	t.Helper()
	now := time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC)
	record := &SessionRecord{
		ID:              sessionID,
		TeamID:          "team_123",
		CreatedByUserID: "user_123",
		Vendor:          "claude",
		EnvironmentID:   "env_123",
		Agent:           map[string]any{"id": "agent_123", "type": "agent"},
		Status:          "running",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := repo.CreateSession(context.Background(), record, map[string]any{}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return record
}

func forceSameEventTimestamp(t *testing.T, repo *Repository, sessionID string) {
	t.Helper()
	when := time.Date(2026, 4, 12, 12, 1, 0, 0, time.UTC)
	if _, err := repo.db(context.Background()).Exec(context.Background(), `
		UPDATE managed_agent_session_events
		SET created_at = $1
		WHERE session_id = $2
	`, when, sessionID); err != nil {
		t.Fatalf("force same event timestamp: %v", err)
	}
}
