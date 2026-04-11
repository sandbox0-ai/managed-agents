package managedagents

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const managedAgentsBetaHeader = "managed-agents-2026-04-01"
const filesAPIBetaHeader = "files-api-2025-04-14"

// RequireManagedAgentsBeta enforces the Anthropic beta header expected by managed-agent clients.
func RequireManagedAgentsBeta() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Anthropic-Beta")
		if hasManagedAgentsBeta(header) || (isFilesAPIPath(c.Request.URL.Path) && hasFilesAPIBeta(header)) {
			c.Next()
			return
		}
		if isFilesAPIPath(c.Request.URL.Path) {
			writeError(c, http.StatusBadRequest, "invalid_request_error", "anthropic-beta must include managed-agents-2026-04-01 or files-api-2025-04-14")
			c.Abort()
			return
		}
		writeError(c, http.StatusBadRequest, "invalid_request_error", "anthropic-beta must include managed-agents-2026-04-01")
		c.Abort()
	}
}

func hasManagedAgentsBeta(value string) bool {
	for _, item := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(item), managedAgentsBetaHeader) {
			return true
		}
	}
	return false
}

func hasFilesAPIBeta(value string) bool {
	for _, item := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(item), filesAPIBetaHeader) {
			return true
		}
	}
	return false
}

func isFilesAPIPath(path string) bool {
	normalized := strings.TrimSpace(path)
	return normalized == "/v1/files" || strings.HasPrefix(normalized, "/v1/files/")
}
