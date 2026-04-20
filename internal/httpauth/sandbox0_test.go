package httpauth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func TestSandbox0AuthenticatorUserTokenSingleTeamWithoutHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/me":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"user_123","email":"u@example.com","name":"User","email_verified":true,"is_admin":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}}`))
		case "/teams":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"teams":[{"id":"team_123","name":"Team","slug":"team","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]}}`))
		case "/teams/team_123":
			if got := r.Header.Get(teamIDHeader); got != "team_123" {
				t.Fatalf("x-team-id = %q, want team_123", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"team_123","name":"Team","slug":"team","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authenticator, err := NewSandbox0Authenticator(Sandbox0AuthenticatorConfig{BaseURL: server.URL, Timeout: 5 * time.Second, Logger: zap.NewNop()})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	authCtx, err := authenticator.AuthenticateRequest(t.Context(), "jwt-token", "")
	if err != nil {
		t.Fatalf("authenticate request: %v", err)
	}
	if authCtx.TeamID != "team_123" {
		t.Fatalf("team id = %q, want team_123", authCtx.TeamID)
	}
	if authCtx.UserID != "user_123" {
		t.Fatalf("user id = %q, want user_123", authCtx.UserID)
	}
}

func TestSandbox0AuthenticatorUserTokenAutoSelectsStableTeamForMultipleTeams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/me":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"user_123","email":"u@example.com","name":"User","email_verified":true,"is_admin":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}}`))
		case "/teams":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"teams":[{"id":"team_1","name":"Team 1","slug":"team-1","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"},{"id":"team_2","name":"Team 2","slug":"team-2","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]}}`))
		case "/teams/team_1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"team_1","name":"Team 1","slug":"team-1","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authenticator, err := NewSandbox0Authenticator(Sandbox0AuthenticatorConfig{BaseURL: server.URL, Timeout: 5 * time.Second, Logger: zap.NewNop()})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	authCtx, err := authenticator.AuthenticateRequest(t.Context(), "jwt-token", "")
	if err != nil {
		t.Fatalf("authenticate request: %v", err)
	}
	if authCtx.TeamID != "team_1" {
		t.Fatalf("team id = %q, want team_1", authCtx.TeamID)
	}
}

func TestSandbox0AuthenticatorAPIKeyUsesSDKIntrospection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api-keys/current":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"api_key":{"id":"key_123","team_id":"team_123","created_by":"","scope":"team","roles":["sandbox:create"],"permissions":["sandbox:create"],"is_active":true,"expires_at":"2027-01-01T00:00:00Z"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authenticator, err := NewSandbox0Authenticator(Sandbox0AuthenticatorConfig{BaseURL: server.URL, Timeout: 5 * time.Second, Logger: zap.NewNop()})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	authCtx, err := authenticator.AuthenticateRequest(t.Context(), "s0_region_token", "")
	if err != nil {
		t.Fatalf("authenticate request: %v", err)
	}
	if authCtx.TeamID != "team_123" {
		t.Fatalf("team id = %q, want team_123", authCtx.TeamID)
	}
	if authCtx.UserID != "" {
		t.Fatalf("user id = %q, want empty for api key auth", authCtx.UserID)
	}
}

func TestSandbox0AuthenticatorMiddlewareStoresContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/me":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"user_123","email":"u@example.com","name":"User","email_verified":true,"is_admin":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}}`))
		case "/teams":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"teams":[{"id":"team_123","name":"Team","slug":"team","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]}}`))
		case "/teams/team_123":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"team_123","name":"Team","slug":"team","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authenticator, err := NewSandbox0Authenticator(Sandbox0AuthenticatorConfig{BaseURL: server.URL, Timeout: 5 * time.Second, Logger: zap.NewNop()})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(authenticator.Authenticate())
	router.GET("/protected", func(c *gin.Context) {
		authCtx := GetContext(c)
		if authCtx == nil {
			c.String(http.StatusInternalServerError, "missing auth context")
			return
		}
		c.String(http.StatusOK, fmt.Sprintf("%s:%s", authCtx.TeamID, authCtx.UserID))
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer jwt-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); body != "team_123:user_123" {
		t.Fatalf("body = %q, want team_123:user_123", body)
	}
}

func TestSandbox0AuthenticatorMiddlewareUsesSelectedTeamHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/me":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"user_123","email":"u@example.com","name":"User","email_verified":true,"is_admin":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}}`))
		case "/teams/team_2":
			if got := r.Header.Get(teamIDHeader); got != "team_2" {
				t.Fatalf("x-team-id = %q, want team_2", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"team_2","name":"Team 2","slug":"team-2","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authenticator, err := NewSandbox0Authenticator(Sandbox0AuthenticatorConfig{BaseURL: server.URL, Timeout: 5 * time.Second, Logger: zap.NewNop()})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(authenticator.Authenticate())
	router.GET("/protected", func(c *gin.Context) {
		authCtx := GetContext(c)
		if authCtx == nil {
			c.String(http.StatusInternalServerError, "missing auth context")
			return
		}
		c.String(http.StatusOK, fmt.Sprintf("%s:%s", authCtx.TeamID, authCtx.UserID))
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer jwt-token")
	req.Header.Set(teamIDHeader, "team_2")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); body != "team_2:user_123" {
		t.Fatalf("body = %q, want team_2:user_123", body)
	}
}

func TestSandbox0AuthenticatorMiddlewareRejectsAPIKeyTeamMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api-keys/current":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"api_key":{"id":"key_123","team_id":"team_123","created_by":"","scope":"team","roles":["sandbox:create"],"permissions":["sandbox:create"],"is_active":true,"expires_at":"2027-01-01T00:00:00Z"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	authenticator, err := NewSandbox0Authenticator(Sandbox0AuthenticatorConfig{BaseURL: server.URL, Timeout: 5 * time.Second, Logger: zap.NewNop()})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(authenticator.Authenticate())
	router.GET("/protected", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer s0_region_token")
	req.Header.Set(teamIDHeader, "team_other")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestSandbox0AuthenticatorMiddlewareUsesManagedAgentErrorShape(t *testing.T) {
	authenticator, err := NewSandbox0Authenticator(Sandbox0AuthenticatorConfig{BaseURL: "http://example.invalid", Timeout: 5 * time.Second, Logger: zap.NewNop()})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(authenticator.Authenticate())
	router.GET("/protected", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Request-Id", "req_auth_123")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if rec.Header().Get("Request-Id") != "req_auth_123" {
		t.Fatalf("Request-Id = %q, want req_auth_123", rec.Header().Get("Request-Id"))
	}
	if body := rec.Body.String(); body != `{"error":{"message":"missing authorization header","type":"authentication_error"},"request_id":"req_auth_123","type":"error"}` {
		t.Fatalf("body = %s", body)
	}
}
