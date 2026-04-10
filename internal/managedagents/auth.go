package managedagents

import (
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
)

// NormalizeAPIKeyHeader aliases x-api-key into Authorization for shared auth middleware.
func NormalizeAPIKeyHeader() gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.TrimSpace(c.GetHeader("Authorization")) == "" {
			if apiKey := strings.TrimSpace(c.GetHeader("X-Api-Key")); apiKey != "" {
				c.Request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
			}
		}
		c.Next()
	}
}
