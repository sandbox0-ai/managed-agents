package managedagents

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const managedAgentsBetaHeader = "managed-agents-2026-04-01"

// RequireManagedAgentsBeta enforces the Anthropic beta header expected by managed-agent clients.
func RequireManagedAgentsBeta() gin.HandlerFunc {
	return func(c *gin.Context) {
		if hasManagedAgentsBeta(c.GetHeader("Anthropic-Beta")) {
			c.Next()
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
