package e2e

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestRuntimeSessionRoundTrip(t *testing.T) {
	cfg := loadConfig(t)
	client := newClient(cfg)

	env := createEnvironment(t, client, cfg.Suffix)
	agent := createAgent(t, client, cfg.Suffix)
	vault := createLLMVault(t, client, cfg.Suffix)
	createStaticBearerCredential(t, client, requireString(t, vault, "id"))
	envID := requireString(t, env, "id")
	agentID := requireString(t, agent, "id")
	vaultID := requireString(t, vault, "id")
	t.Cleanup(func() {
		_, _, _ = client.post(t.Context(), "/v1/agents/"+agentID+"/archive", map[string]any{})
		_, _, _ = client.delete(t.Context(), "/v1/vaults/"+vaultID)
		_, _, _ = client.delete(t.Context(), "/v1/environments/"+envID)
	})

	session := createSession(t, client, cfg.Suffix, agentID, envID, vaultID)
	sessionID := requireString(t, session, "id")
	t.Cleanup(func() {
		_, _, _ = client.delete(t.Context(), "/v1/sessions/"+sessionID)
	})

	sendUserMessage(t, client, sessionID, "Say hello from managed-agents e2e")
	events := waitForAgentMessage(t, client, sessionID)
	if !hasEventType(events, "session.status_idle") {
		t.Fatalf("events missing session.status_idle: %#v", events)
	}

	deleted, status, err := client.delete(t.Context(), "/v1/sessions/"+sessionID)
	if err != nil || status != http.StatusOK {
		t.Fatalf("delete session status=%d err=%v", status, err)
	}
	if deleted["id"] != sessionID {
		t.Fatalf("deleted session id=%v, want %s", deleted["id"], sessionID)
	}
}

func createSession(t *testing.T, client *apiClient, suffix, agentID, environmentID, vaultID string) map[string]any {
	t.Helper()
	body := map[string]any{
		"agent":          agentID,
		"environment_id": environmentID,
		"title":          fmt.Sprintf("e2e-session-%s", suffix),
		"vault_ids":      []string{vaultID},
		"metadata":       map[string]string{"e2e": "managed-agents"},
	}
	resp, status, err := client.post(t.Context(), "/v1/sessions", body)
	if err != nil || status != http.StatusOK {
		t.Fatalf("create session status=%d err=%v", status, err)
	}
	requireString(t, resp, "id")
	return resp
}

func sendUserMessage(t *testing.T, client *apiClient, sessionID, text string) {
	t.Helper()
	body := map[string]any{
		"events": []map[string]any{{
			"type":    "user.message",
			"content": []map[string]any{{"type": "text", "text": text}},
		}},
	}
	if _, status, err := client.post(t.Context(), "/v1/sessions/"+sessionID+"/events", body); err != nil || status != http.StatusOK {
		t.Fatalf("send events status=%d err=%v", status, err)
	}
}

func waitForAgentMessage(t *testing.T, client *apiClient, sessionID string) []any {
	t.Helper()
	var events []any
	eventually(t, 5*time.Minute, 2*time.Second, func() error {
		resp, status, err := client.get(t.Context(), "/v1/sessions/"+sessionID+"/events?limit=100&order=asc")
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("list events status=%d", status)
		}
		events = listData(resp)
		if !hasEventType(events, "agent.message") {
			return fmt.Errorf("agent.message not observed yet; events=%v", eventTypes(events))
		}
		return nil
	})
	return events
}

func hasEventType(events []any, eventType string) bool {
	for _, item := range events {
		event, _ := item.(map[string]any)
		if event["type"] == eventType {
			return true
		}
	}
	return false
}

func eventTypes(events []any) []string {
	out := make([]string, 0, len(events))
	for _, item := range events {
		event, _ := item.(map[string]any)
		if typ, ok := event["type"].(string); ok {
			out = append(out, typ)
		}
	}
	return out
}
