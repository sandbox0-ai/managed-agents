package e2e

import (
	"net/http"
	"testing"
)

func TestHealthEndpoints(t *testing.T) {
	cfg := loadConfig(t)
	client := newClient(cfg)

	if _, status, err := client.get(t.Context(), "/healthz"); err != nil || status != http.StatusOK {
		t.Fatalf("GET /healthz status=%d err=%v, want 200", status, err)
	}
	if _, status, err := client.get(t.Context(), "/readyz"); err != nil || status != http.StatusOK {
		t.Fatalf("GET /readyz status=%d err=%v, want 200", status, err)
	}
}

func TestAuthenticationRequired(t *testing.T) {
	cfg := loadConfig(t)
	client := newClient(cfg)
	client.token = ""

	_, status, err := client.get(t.Context(), "/v1/environments?limit=1")
	if err == nil || status != http.StatusUnauthorized {
		t.Fatalf("GET /v1/environments without token status=%d err=%v, want 401", status, err)
	}
}
