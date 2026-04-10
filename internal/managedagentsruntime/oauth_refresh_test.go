package managedagentsruntime

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
)

func TestRefreshMcpOAuthCredentialUsesClientSecretPostAndRotatesTokens(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "refresh_token" {
			t.Fatalf("grant_type = %q", got)
		}
		if got := r.Form.Get("client_secret"); got != "client-secret" {
			t.Fatalf("client_secret = %q", got)
		}
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":120}`))
	}))
	defer server.Close()

	updated, changed, err := refreshMcpOAuthCredential(t.Context(), server.Client(), gatewaymanagedagents.StoredCredential{
		Snapshot: gatewaymanagedagents.Credential{
			ID: "cred_123",
			Auth: gatewaymanagedagents.CredentialAuth{
				Type:         "mcp_oauth",
				MCPServerURL: "https://mcp.example.com/sse",
				ExpiresAt:    stringPointer(now.Add(5 * time.Second).Format(time.RFC3339)),
				Refresh: &gatewaymanagedagents.CredentialOAuthRefresh{
					TokenEndpoint: server.URL,
					ClientID:      "client-123",
					TokenEndpointAuth: gatewaymanagedagents.CredentialTokenEndpointAuth{
						Type: "client_secret_post",
					},
				},
			},
		},
		Secret: map[string]any{
			"type":           "mcp_oauth",
			"mcp_server_url": "https://mcp.example.com/sse",
			"access_token":   "old-access",
			"expires_at":     now.Add(5 * time.Second).Format(time.RFC3339),
			"refresh": map[string]any{
				"refresh_token":  "old-refresh",
				"token_endpoint": server.URL,
				"client_id":      "client-123",
				"token_endpoint_auth": map[string]any{
					"type":          "client_secret_post",
					"client_secret": "client-secret",
				},
			},
		},
	}, now)
	if err != nil {
		t.Fatalf("refreshMcpOAuthCredential: %v", err)
	}
	if !changed {
		t.Fatal("expected refresh to report changed")
	}
	if got := stringValue(updated.Secret["access_token"]); got != "new-access" {
		t.Fatalf("access_token = %q", got)
	}
	if got := stringValue(mapValue(updated.Secret["refresh"])["refresh_token"]); got != "new-refresh" {
		t.Fatalf("refresh_token = %q", got)
	}
}

func TestExecuteMcpOAuthRefreshUsesClientSecretBasic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("client-123:secret-123"))
		if got := r.Header.Get("Authorization"); got != expected {
			t.Fatalf("Authorization = %q", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.Form.Get("client_id"); got != "" {
			t.Fatalf("client_id = %q, want empty for basic auth", got)
		}
		_, _ = w.Write([]byte(`{"access_token":"new-access"}`))
	}))
	defer server.Close()

	_, err := executeMcpOAuthRefresh(t.Context(), server.Client(), &mcpOAuthRefreshConfig{
		CredentialID:  "cred_123",
		TokenEndpoint: server.URL,
		ClientID:      "client-123",
		RefreshToken:  "refresh-123",
		Auth:          tokenEndpointAuthConfig{Type: "client_secret_basic", ClientSecret: "secret-123"},
	})
	if err != nil {
		t.Fatalf("executeMcpOAuthRefresh: %v", err)
	}
}

func TestCredentialNeedsMcpOAuthRefreshRejectsExpiredTokenWithoutRefresh(t *testing.T) {
	_, err := credentialNeedsMcpOAuthRefresh(gatewaymanagedagents.StoredCredential{
		Snapshot: gatewaymanagedagents.Credential{
			ID: "cred_123",
			Auth: gatewaymanagedagents.CredentialAuth{
				Type:      "mcp_oauth",
				ExpiresAt: stringPointer(time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)),
			},
		},
		Secret: map[string]any{"type": "mcp_oauth"},
	}, time.Now().UTC())
	if err == nil {
		t.Fatal("expected refresh error")
	}
}

func TestParseMcpOAuthRefreshConfigReadsResourceAndScope(t *testing.T) {
	config, err := parseMcpOAuthRefreshConfig(gatewaymanagedagents.StoredCredential{
		Snapshot: gatewaymanagedagents.Credential{
			ID: "cred_123",
			Auth: gatewaymanagedagents.CredentialAuth{
				Type: "mcp_oauth",
				Refresh: &gatewaymanagedagents.CredentialOAuthRefresh{
					TokenEndpoint: "https://auth.example.com/token",
					ClientID:      "client-123",
					Scope:         stringPointer("scope-a"),
					Resource:      stringPointer("https://resource.example.com"),
					TokenEndpointAuth: gatewaymanagedagents.CredentialTokenEndpointAuth{
						Type: "none",
					},
				},
			},
		},
		Secret: map[string]any{
			"refresh": map[string]any{
				"refresh_token": "refresh-123",
			},
		},
	})
	if err != nil {
		t.Fatalf("parseMcpOAuthRefreshConfig: %v", err)
	}
	if config.Scope != "scope-a" || config.Resource != "https://resource.example.com" {
		t.Fatalf("scope/resource = %q %q", config.Scope, config.Resource)
	}
	if _, err := url.Parse(config.TokenEndpoint); err != nil {
		t.Fatalf("token endpoint parse: %v", err)
	}
}

func stringPointer(value string) *string {
	return &value
}
