package managedagents

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sandbox0-ai/managed-agent/internal/httpauth"
	"go.uber.org/zap"
)

// Handler serves the managed-agent HTTP contract.
type Handler struct {
	service *Service
	logger  *zap.Logger
}

// NewHandler creates a new managed-agent handler.
func NewHandler(service *Service, logger *zap.Logger) *Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Handler{service: service, logger: logger}
}

func (h *Handler) CreateSession(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req CreateSessionParams
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	session, err := h.service.CreateSession(c.Request.Context(), principal, req)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

func (h *Handler) ListSessions(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	createdAtGTE, err := parseTimestampQuery(c.Query("created_at[gte]"))
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	createdAtGT, err := parseTimestampQuery(c.Query("created_at[gt]"))
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	createdAtLTE, err := parseTimestampQuery(c.Query("created_at[lte]"))
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	createdAtLT, err := parseTimestampQuery(c.Query("created_at[lt]"))
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	agentVersion, _ := strconv.Atoi(strings.TrimSpace(c.Query("agent_version")))
	sessions, nextPage, err := h.service.ListSessions(c.Request.Context(), principal, SessionListOptions{
		Limit:           limit,
		Page:            c.Query("page"),
		Order:           c.Query("order"),
		IncludeArchived: strings.EqualFold(strings.TrimSpace(c.Query("include_archived")), "true"),
		AgentID:         c.Query("agent_id"),
		AgentVersion:    agentVersion,
		CreatedAt: TimeFilter{
			GTE: createdAtGTE,
			GT:  createdAtGT,
			LTE: createdAtLTE,
			LT:  createdAtLT,
		},
	})
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": sessions, "next_page": nextPage})
}

func (h *Handler) GetSession(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	session, err := h.service.GetSession(c.Request.Context(), principal, c.Param("session_id"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

func (h *Handler) UpdateSession(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req UpdateSessionParams
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	session, err := h.service.UpdateSession(c.Request.Context(), principal, c.Param("session_id"), req)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

func (h *Handler) DeleteSession(c *gin.Context) {
	principal, credential, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	response, err := h.service.DeleteSession(c.Request.Context(), principal, credential, c.Param("session_id"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) ListEvents(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	events, nextPage, err := h.service.ListEvents(c.Request.Context(), principal, c.Param("session_id"), EventListOptions{
		Limit: limit,
		Page:  c.Query("page"),
		Order: c.Query("order"),
	})
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": events, "next_page": nextPage})
}

func (h *Handler) SendEvents(c *gin.Context) {
	principal, credential, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req SendEventsParams
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	events, err := h.service.SendEvents(c.Request.Context(), principal, credential, c.Param("session_id"), req, requestBaseURL(c))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": events})
}

func (h *Handler) StreamEvents(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	sessionID := c.Param("session_id")
	if _, err := h.service.GetSession(c.Request.Context(), principal, sessionID); err != nil {
		h.writeServiceError(c, err)
		return
	}
	lastEventID := strings.TrimSpace(c.GetHeader("Last-Event-ID"))
	initialEvents, err := h.service.repo.ListEventsAfterID(c.Request.Context(), sessionID, lastEventID, 200)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	writer := c.Writer
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(c, http.StatusInternalServerError, "api_error", "streaming is unavailable")
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	poll := time.NewTicker(1 * time.Second)
	defer heartbeat.Stop()
	defer poll.Stop()

	sendEvents := func(events []map[string]any) (bool, error) {
		for _, event := range events {
			payload, err := json.Marshal(event)
			if err != nil {
				return false, err
			}
			if eventID := stringValue(event["id"]); eventID != "" {
				if _, err := fmt.Fprintf(writer, "id: %s\n", eventID); err != nil {
					return false, err
				}
			}
			if _, err := io.WriteString(writer, "event: message\n"); err != nil {
				return false, err
			}
			if _, err := fmt.Fprintf(writer, "data: %s\n\n", payload); err != nil {
				return false, err
			}
			flusher.Flush()
			if stringValue(event["type"]) == "session.deleted" {
				return true, nil
			}
		}
		return false, nil
	}

	fetch := func(events []map[string]any) (bool, error) {
		if len(events) == 0 {
			fetched, err := h.service.repo.ListEventsAfterID(c.Request.Context(), sessionID, lastEventID, 200)
			if err != nil {
				return false, err
			}
			events = fetched
		}
		if len(events) == 0 {
			return false, nil
		}
		terminated, err := sendEvents(events)
		if err != nil {
			return false, err
		}
		last := events[len(events)-1]
		if eventID := stringValue(last["id"]); eventID != "" {
			lastEventID = eventID
		}
		return terminated, nil
	}

	terminated, err := sendEvents(initialEvents)
	if err != nil {
		h.logger.Warn("managed-agent stream initial fetch failed", zap.Error(err), zap.String("session_id", sessionID))
		return
	}
	if len(initialEvents) > 0 {
		last := initialEvents[len(initialEvents)-1]
		if eventID := stringValue(last["id"]); eventID != "" {
			lastEventID = eventID
		}
	}
	if terminated {
		return
	}
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := io.WriteString(writer, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-poll.C:
			terminated, err := fetch(nil)
			if err != nil {
				h.logger.Warn("managed-agent stream poll failed", zap.Error(err), zap.String("session_id", sessionID))
				return
			}
			if terminated {
				return
			}
		}
	}
}

func (h *Handler) RuntimeSandboxWebhook(c *gin.Context) {
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid webhook body")
		return
	}
	if err := h.service.HandleSandboxWebhook(c.Request.Context(), rawBody, c.GetHeader("X-Sandbox0-Signature")); err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) requirePrincipal(c *gin.Context) (Principal, RequestCredential, bool) {
	authCtx := httpauth.GetContext(c)
	if authCtx == nil || strings.TrimSpace(authCtx.TeamID) == "" {
		writeError(c, http.StatusUnauthorized, "authentication_error", "authentication required")
		return Principal{}, RequestCredential{}, false
	}
	token := extractRequestToken(c)
	if strings.TrimSpace(token) == "" {
		writeError(c, http.StatusUnauthorized, "authentication_error", "request credential required")
		return Principal{}, RequestCredential{}, false
	}
	return Principal{TeamID: authCtx.TeamID, UserID: authCtx.UserID}, RequestCredential{Token: token}, true
}

func (h *Handler) writeServiceError(c *gin.Context, err error) {
	if err == nil {
		return
	}
	switch {
	case strings.Contains(err.Error(), "forbidden"):
		writeError(c, http.StatusForbidden, "permission_error", err.Error())
	case strings.Contains(err.Error(), "not found"):
		writeError(c, http.StatusNotFound, "not_found_error", err.Error())
	case strings.Contains(err.Error(), "invalid webhook signature"):
		writeError(c, http.StatusUnauthorized, "authentication_error", err.Error())
	case strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "invalid"):
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
	default:
		h.logger.Error("managed-agent handler error", zap.Error(err))
		writeError(c, http.StatusInternalServerError, "api_error", err.Error())
	}
}

func writeError(c *gin.Context, status int, code, message string) {
	requestID := managedAgentsRequestID(c)
	c.Header("Request-Id", requestID)
	c.JSON(status, gin.H{
		"type":       "error",
		"request_id": requestID,
		"error": gin.H{
			"type":    code,
			"message": message,
		},
	})
}

func requestBaseURL(c *gin.Context) string {
	scheme := c.GetHeader("X-Forwarded-Proto")
	if scheme == "" {
		if c.Request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := strings.TrimSpace(c.GetHeader("X-Forwarded-Host"))
	if host == "" {
		host = c.Request.Host
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

func managedAgentsRequestID(c *gin.Context) string {
	if requestID := strings.TrimSpace(c.GetHeader("Request-Id")); requestID != "" {
		return requestID
	}
	if requestID := strings.TrimSpace(c.GetHeader("X-Request-Id")); requestID != "" {
		return requestID
	}
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return NewID("req")
	}
	return "req_" + hex.EncodeToString(bytes)
}

func extractRequestToken(c *gin.Context) string {
	if token := bearerToken(c.GetHeader("Authorization")); token != "" {
		return token
	}
	return strings.TrimSpace(c.GetHeader("X-Api-Key"))
}

func bearerToken(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	parts := strings.SplitN(trimmed, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
