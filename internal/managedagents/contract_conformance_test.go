package managedagents

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sandbox0-ai/managed-agent/internal/httpauth"
)

type stubAuthenticator struct {
	handler gin.HandlerFunc
}

func (a stubAuthenticator) Authenticate() gin.HandlerFunc {
	return a.handler
}

func newContractTestRouter(t *testing.T, authHandler gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	MountRoutes(router, stubAuthenticator{handler: authHandler}, &Handler{})
	return router
}

func readErrorBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response body: %v", err)
	}
	return body
}

func requireAuthorizationAndSetPrincipal(recordedAuth *string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if recordedAuth != nil {
			*recordedAuth = c.GetHeader("Authorization")
		}
		if strings.TrimSpace(c.GetHeader("Authorization")) == "" {
			writeError(c, http.StatusUnauthorized, "authentication_error", "authorization header required")
			c.Abort()
			return
		}
		httpauth.SetContext(c, &httpauth.Context{TeamID: "team_123", UserID: "user_123"})
		c.Next()
	}
}

func TestMountRoutesUsesSpecUpdateMethodsOnly(t *testing.T) {
	router := newContractTestRouter(t, requireAuthorizationAndSetPrincipal(nil))

	routesByPath := map[string]map[string]bool{}
	for _, route := range router.Routes() {
		if _, exists := routesByPath[route.Path]; !exists {
			routesByPath[route.Path] = map[string]bool{}
		}
		routesByPath[route.Path][route.Method] = true
	}

	updateOnlyRoutes := []string{
		"/v1/agents/:agent_id",
		"/v1/environments/:environment_id",
		"/v1/sessions/:session_id",
		"/v1/sessions/:session_id/resources/:resource_id",
		"/v1/vaults/:vault_id",
		"/v1/vaults/:vault_id/credentials/:credential_id",
	}
	for _, path := range updateOnlyRoutes {
		methods := routesByPath[path]
		if !methods[http.MethodPut] {
			t.Fatalf("route %s missing PUT method", path)
		}
		if methods[http.MethodPost] {
			t.Fatalf("route %s unexpectedly exposes POST method", path)
		}
	}
	if !routesByPath[InternalWebhookPath][http.MethodPost] {
		t.Fatalf("internal webhook path %s missing POST method", InternalWebhookPath)
	}
}

func TestCreateSessionRejectsUnknownFields(t *testing.T) {
	router := newContractTestRouter(t, requireAuthorizationAndSetPrincipal(nil))

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewBufferString(`{
		"environment_id":"env_123",
		"agent":"agent_123",
		"unexpected":true
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer token_123")
	req.Header.Set("Anthropic-Beta", managedAgentsBetaHeader)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	body := readErrorBody(t, rec)
	if body["type"] != "error" {
		t.Fatalf("type = %v, want error", body["type"])
	}
	errBody, _ := body["error"].(map[string]any)
	if errBody["type"] != "invalid_request_error" {
		t.Fatalf("error.type = %v, want invalid_request_error", errBody["type"])
	}
}

func TestListSkillsRejectsInvalidLimitQuery(t *testing.T) {
	router := newContractTestRouter(t, requireAuthorizationAndSetPrincipal(nil))

	req := httptest.NewRequest(http.MethodGet, "/v1/skills?limit=bad", nil)
	req.Header.Set("Authorization", "Bearer token_123")
	req.Header.Set("Anthropic-Beta", managedAgentsBetaHeader)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	body := readErrorBody(t, rec)
	errBody, _ := body["error"].(map[string]any)
	if errBody["type"] != "invalid_request_error" {
		t.Fatalf("error.type = %v, want invalid_request_error", errBody["type"])
	}
}

func TestMissingBetaHeaderIsRejected(t *testing.T) {
	authCalled := false
	router := newContractTestRouter(t, func(c *gin.Context) {
		authCalled = true
		httpauth.SetContext(c, &httpauth.Context{TeamID: "team_123", UserID: "user_123"})
		c.Next()
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/skills", nil)
	req.Header.Set("Authorization", "Bearer token_123")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if authCalled {
		t.Fatal("auth middleware ran before beta header check")
	}
	body := readErrorBody(t, rec)
	errBody, _ := body["error"].(map[string]any)
	if errBody["type"] != "invalid_request_error" {
		t.Fatalf("error.type = %v, want invalid_request_error", errBody["type"])
	}
}

func TestAPIKeyHeaderIsNormalizedBeforeAuthentication(t *testing.T) {
	var recordedAuthorization string
	router := newContractTestRouter(t, requireAuthorizationAndSetPrincipal(&recordedAuthorization))

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewBufferString(`{
		"environment_id":"env_123",
		"agent":"agent_123",
		"unexpected":true
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", "api_key_123")
	req.Header.Set("Anthropic-Beta", managedAgentsBetaHeader)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if recordedAuthorization != "Bearer api_key_123" {
		t.Fatalf("authorization header = %q, want %q", recordedAuthorization, "Bearer api_key_123")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
