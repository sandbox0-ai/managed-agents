package managedagents

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	contract "github.com/sandbox0-ai/managed-agent/internal/apicontract/generated"
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
		if !methods[http.MethodPost] {
			t.Fatalf("route %s missing POST method", path)
		}
		if methods[http.MethodPut] {
			t.Fatalf("route %s unexpectedly exposes PUT method", path)
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

func TestCreateCredentialAcceptsUnboundStaticBearer(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	MountRoutes(router, stubAuthenticator{handler: requireAuthorizationAndSetPrincipal(nil)}, NewHandler(service, nil))

	now := time.Now().UTC()
	vault := buildVaultObject("vlt_123", CreateVaultRequest{DisplayName: "llm runtime"}, now, nil)
	if err := repo.CreateVault(t.Context(), "team_123", vault, nil, now); err != nil {
		t.Fatalf("CreateVault: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/vaults/vlt_123/credentials", bytes.NewBufferString(`{
		"auth":{"type":"static_bearer","token":"secret-token"}
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer token_123")
	req.Header.Set("Anthropic-Beta", managedAgentsBetaHeader)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response Credential
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response body: %v", err)
	}
	if response.Auth.MCPServerURL != "" {
		t.Fatalf("response mcp_server_url = %q, want empty", response.Auth.MCPServerURL)
	}
	_, secret, err := repo.GetCredential(t.Context(), "team_123", "vlt_123", response.ID)
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if _, ok := secret["mcp_server_url"]; ok {
		t.Fatalf("secret mcp_server_url = %#v, want omitted", secret["mcp_server_url"])
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

func TestCreateSkillRejectsUnknownMultipartField(t *testing.T) {
	router := newContractTestRouter(t, requireAuthorizationAndSetPrincipal(nil))

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("display_title", "Demo Skill"); err != nil {
		t.Fatalf("write display_title: %v", err)
	}
	if err := writer.WriteField("unexpected", "value"); err != nil {
		t.Fatalf("write unexpected field: %v", err)
	}
	part, err := writer.CreateFormFile("files", "demo-skill/SKILL.md")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte("# Demo Skill")); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/skills", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer token_123")
	req.Header.Set("Anthropic-Beta", managedAgentsBetaHeader)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	bodyMap := readErrorBody(t, rec)
	errBody, _ := bodyMap["error"].(map[string]any)
	if errBody["type"] != "invalid_request_error" {
		t.Fatalf("error.type = %v, want invalid_request_error", errBody["type"])
	}
}

func TestCreateSkillVersionRejectsDisplayTitleField(t *testing.T) {
	router := newContractTestRouter(t, requireAuthorizationAndSetPrincipal(nil))

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("display_title", "Demo Skill"); err != nil {
		t.Fatalf("write display_title: %v", err)
	}
	part, err := writer.CreateFormFile("files", "demo-skill/SKILL.md")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte("# Demo Skill")); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/skills/skill_123/versions", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer token_123")
	req.Header.Set("Anthropic-Beta", managedAgentsBetaHeader)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	bodyMap := readErrorBody(t, rec)
	errBody, _ := bodyMap["error"].(map[string]any)
	if errBody["type"] != "invalid_request_error" {
		t.Fatalf("error.type = %v, want invalid_request_error", errBody["type"])
	}
}

func TestUploadFileRejectsUnknownMultipartField(t *testing.T) {
	router := newContractTestRouter(t, requireAuthorizationAndSetPrincipal(nil))

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("unexpected", "value"); err != nil {
		t.Fatalf("write unexpected field: %v", err)
	}
	part, err := writer.CreateFormFile("file", "hello.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte("hello")); err != nil {
		t.Fatalf("write file content: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/files", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer token_123")
	req.Header.Set("Anthropic-Beta", managedAgentsBetaHeader)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	bodyMap := readErrorBody(t, rec)
	errBody, _ := bodyMap["error"].(map[string]any)
	if errBody["type"] != "invalid_request_error" {
		t.Fatalf("error.type = %v, want invalid_request_error", errBody["type"])
	}
}

func TestStreamEventsReturnsSSEPayload(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
	record := &SessionRecord{
		ID:               "sesn_stream_123",
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
	if err := repo.CreateSession(t.Context(), record, map[string]any{}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := repo.AppendEvents(t.Context(), record.ID, []map[string]any{{
		"id":           "evt_stream_123",
		"type":         "session.status_running",
		"processed_at": now.Format(time.RFC3339Nano),
	}}); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}

	service := NewService(repo, noopRuntimeManager{}, nil)
	handler := NewHandler(service, nil)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	MountRoutes(router, stubAuthenticator{handler: requireAuthorizationAndSetPrincipal(nil)}, handler)

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+record.ID+"/events/stream", nil)
	req.Header.Set("Authorization", "Bearer token_123")
	req.Header.Set("Anthropic-Beta", managedAgentsBetaHeader)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", contentType)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "id: evt_stream_123\n") {
		t.Fatalf("stream body missing event id: %q", body)
	}
	if !strings.Contains(body, "data: ") {
		t.Fatalf("stream body missing data field: %q", body)
	}
	payloadJSON := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(body, "data: ", 2)[1], ""))
	var payload contract.BetaManagedAgentsStreamSessionEvents
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("unmarshal stream payload: %v", err)
	}
	discriminator, err := payload.Discriminator()
	if err != nil {
		t.Fatalf("payload discriminator: %v", err)
	}
	if discriminator != "session.status_running" {
		t.Fatalf("stream event type = %q, want session.status_running", discriminator)
	}
}
