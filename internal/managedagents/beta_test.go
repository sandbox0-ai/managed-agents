package managedagents

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequireManagedAgentsBetaAcceptsFilesAPIBetaForFilesRoutes(t *testing.T) {
	router := betaTestRouter("/v1/files")
	req := httptest.NewRequest(http.MethodGet, "/v1/files", nil)
	req.Header.Set("Anthropic-Beta", filesAPIBetaHeader)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

func TestRequireManagedAgentsBetaRejectsFilesAPIBetaForManagedAgentRoutes(t *testing.T) {
	router := betaTestRouter("/v1/agents")
	req := httptest.NewRequest(http.MethodGet, "/v1/agents", nil)
	req.Header.Set("Anthropic-Beta", filesAPIBetaHeader)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestRequireManagedAgentsBetaAcceptsManagedAgentsBetaForFilesRoutes(t *testing.T) {
	router := betaTestRouter("/v1/files/:file_id/content")
	req := httptest.NewRequest(http.MethodGet, "/v1/files/file_123/content", nil)
	req.Header.Set("Anthropic-Beta", managedAgentsBetaHeader)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

func betaTestRouter(path string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequireManagedAgentsBeta())
	router.GET(path, func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	return router
}
