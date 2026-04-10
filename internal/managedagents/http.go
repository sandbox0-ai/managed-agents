package managedagents

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	contract "github.com/sandbox0-ai/managed-agent/internal/apicontract/generated"
	"github.com/sandbox0-ai/managed-agent/internal/apicontractutil"
	"github.com/sandbox0-ai/managed-agent/internal/httpauth"
	"go.uber.org/zap"
)

var (
	streamPollInterval = 1 * time.Second
	streamPollTimeout  = 30 * time.Second
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
	var req contract.BetaManagedAgentsCreateSessionParams
	if err := decodeContractJSONBody(c, "BetaManagedAgentsCreateSessionParams", &req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	params, err := createSessionParamsFromContract(req)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	session, err := h.service.CreateSession(c.Request.Context(), principal, params)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := sessionToContract(session)
	if err != nil {
		h.logger.Error("failed to encode create-session response", zap.Error(err))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) ListSessions(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req contract.BetaListSessionsParams
	if err := c.ShouldBindQuery(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	sessions, nextPage, err := h.service.ListSessions(c.Request.Context(), principal, SessionListOptions{
		Limit:           int32QueryValue(req.Limit),
		Page:            stringQueryValue(req.Page),
		Order:           listOrderQueryValue(req.Order),
		IncludeArchived: boolQueryValue(req.IncludeArchived),
		AgentID:         stringQueryValue(req.AgentId),
		AgentVersion:    int32QueryValue(req.AgentVersion),
		CreatedAt: TimeFilter{
			GTE: timestampQueryValue(req.CreatedAtGte),
			GT:  timestampQueryValue(req.CreatedAtGt),
			LTE: timestampQueryValue(req.CreatedAtLte),
			LT:  timestampQueryValue(req.CreatedAtLt),
		},
	})
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := listSessionsToContract(sessions, nextPage)
	if err != nil {
		h.logger.Error("failed to encode list-sessions response", zap.Error(err))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
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
	response, err := sessionToContract(session)
	if err != nil {
		h.logger.Error("failed to encode get-session response", zap.Error(err), zap.String("session_id", c.Param("session_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) UpdateSession(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	body, err := readValidatedContractJSONBody(c, "BetaManagedAgentsUpdateSessionParams")
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	req, err := updateSessionParamsFromJSON(body)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	session, err := h.service.UpdateSession(c.Request.Context(), principal, c.Param("session_id"), req)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := sessionToContract(session)
	if err != nil {
		h.logger.Error("failed to encode update-session response", zap.Error(err), zap.String("session_id", c.Param("session_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
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
	contractResponse, err := deletedSessionToContract(response)
	if err != nil {
		h.logger.Error("failed to encode delete-session response", zap.Error(err), zap.String("session_id", c.Param("session_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, contractResponse)
}

func (h *Handler) ListEvents(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req contract.BetaListEventsParams
	if err := c.ShouldBindQuery(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	events, nextPage, err := h.service.ListEvents(c.Request.Context(), principal, c.Param("session_id"), EventListOptions{
		Limit: int32QueryValue(req.Limit),
		Page:  stringQueryValue(req.Page),
		Order: listOrderQueryValue(req.Order),
	})
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := listSessionEventsToContract(events, nextPage)
	if err != nil {
		h.logger.Error("failed to encode event list response", zap.Error(err), zap.String("session_id", c.Param("session_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) SendEvents(c *gin.Context) {
	principal, credential, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req contract.BetaManagedAgentsSendSessionEventsParams
	if err := decodeContractJSONBody(c, "BetaManagedAgentsSendSessionEventsParams", &req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	params, err := sendEventsParamsFromContract(req)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	events, err := h.service.SendEvents(c.Request.Context(), principal, credential, c.Param("session_id"), params, requestBaseURL(c))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := sendSessionEventsToContract(events)
	if err != nil {
		h.logger.Error("failed to encode send-events response", zap.Error(err), zap.String("session_id", c.Param("session_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
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
	deadline := time.Now().Add(streamPollTimeout)
	for {
		events, err := h.service.repo.ListEventsAfterID(c.Request.Context(), sessionID, lastEventID, 1)
		if err != nil {
			h.writeServiceError(c, err)
			return
		}
		if len(events) > 0 {
			payload, err := streamSessionEventToContract(events[0])
			if err != nil {
				h.logger.Error("failed to encode stream event response", zap.Error(err), zap.String("session_id", sessionID))
				writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
				return
			}
			c.JSON(http.StatusOK, payload)
			return
		}
		if time.Now().After(deadline) {
			writeError(c, http.StatusRequestTimeout, "gateway_timeout_error", "stream timed out waiting for the next event")
			return
		}
		select {
		case <-c.Request.Context().Done():
			return
		case <-time.After(streamPollInterval):
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
	c.JSON(status, apicontractutil.ErrorResponse(code, message, &requestID))
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
