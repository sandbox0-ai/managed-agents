package httpauth

import "github.com/gin-gonic/gin"

const contextKey = "auth_context"

// Context carries the authenticated tenant and user identity for a request.
type Context struct {
	TeamID string
	UserID string
}

// Authenticator adds authentication to a gin router and stores Context in gin.Context.
type Authenticator interface {
	Authenticate() gin.HandlerFunc
}

// SetContext stores an auth context on the request.
func SetContext(c *gin.Context, authCtx *Context) {
	if c == nil || authCtx == nil {
		return
	}
	c.Set(contextKey, authCtx)
}

// GetContext extracts the auth context from gin.Context.
func GetContext(c *gin.Context) *Context {
	if c == nil {
		return nil
	}
	v, exists := c.Get(contextKey)
	if !exists || v == nil {
		return nil
	}
	authCtx, _ := v.(*Context)
	return authCtx
}
