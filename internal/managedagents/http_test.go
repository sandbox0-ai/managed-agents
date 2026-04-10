package managedagents

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
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
