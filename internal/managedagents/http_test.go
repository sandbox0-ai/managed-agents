package managedagents

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func TestWriteErrorIncludesRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("GET", "/v1/sessions", nil)
	req.Header.Set("X-Request-Id", "req_test123")
	c.Request = req

	writeError(c, 400, "invalid_request_error", "bad request")

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if got := body["request_id"]; got != "req_test123" {
		t.Fatalf("request_id = %v, want req_test123", got)
	}
}

func TestWriteServiceErrorMapsBuildingArtifactToConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v1/environments/env_123", nil)

	handler := &Handler{logger: zap.NewNop()}
	handler.writeServiceError(c, ErrEnvironmentArtifactBuilding)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	errBody, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error body = %#v, want object", body["error"])
	}
	if got := errBody["type"]; got != "invalid_request_error" {
		t.Fatalf("error.type = %v, want invalid_request_error", got)
	}
}
