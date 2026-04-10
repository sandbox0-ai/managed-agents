package httpauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	contractutil "github.com/sandbox0-ai/managed-agent/internal/apicontractutil"
	sandbox0sdk "github.com/sandbox0-ai/sdk-go"
	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
	"go.uber.org/zap"
)

const teamIDHeader = "X-Team-ID"

// Sandbox0AuthenticatorConfig configures sandbox0-backed request authentication.
type Sandbox0AuthenticatorConfig struct {
	BaseURL string
	Timeout time.Duration
	Logger  *zap.Logger
}

// Sandbox0Authenticator resolves tenant and user identity through sandbox0 APIs.
type Sandbox0Authenticator struct {
	baseURL string
	timeout time.Duration
	logger  *zap.Logger
}

// NewSandbox0Authenticator creates a sandbox0-backed authenticator.
func NewSandbox0Authenticator(cfg Sandbox0AuthenticatorConfig) (*Sandbox0Authenticator, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		return nil, errors.New("sandbox0 base url is required")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Sandbox0Authenticator{baseURL: baseURL, timeout: timeout, logger: logger}, nil
}

// Authenticate resolves the current caller via sandbox0 and stores the auth context on gin.Context.
func (a *Sandbox0Authenticator) Authenticate() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractRequestToken(c)
		if token == "" {
			writeAuthError(c, newAuthError(http.StatusUnauthorized, "missing authorization header", nil))
			return
		}

		authCtx, err := a.AuthenticateRequest(c.Request.Context(), token, "")
		if err != nil {
			writeAuthError(c, err)
			return
		}

		SetContext(c, authCtx)
		c.Next()
	}
}

// AuthenticateRequest resolves the current caller via sandbox0.
func (a *Sandbox0Authenticator) AuthenticateRequest(ctx context.Context, token, selectedTeamID string) (*Context, error) {
	if strings.TrimSpace(token) == "" {
		return nil, newAuthError(http.StatusUnauthorized, "missing authorization header", nil)
	}
	if strings.HasPrefix(token, "s0_") {
		return a.authenticateAPIKey(ctx, token, selectedTeamID)
	}
	return a.authenticateUserToken(ctx, token, selectedTeamID)
}

func (a *Sandbox0Authenticator) authenticateUserToken(ctx context.Context, token, selectedTeamID string) (*Context, error) {
	client, err := a.newClient(token, "")
	if err != nil {
		return nil, newAuthError(http.StatusBadGateway, "failed to initialize sandbox0 client", err)
	}

	user, err := a.currentUser(ctx, client)
	if err != nil {
		return nil, err
	}

	teamID := strings.TrimSpace(selectedTeamID)
	if teamID == "" {
		teamID, err = a.resolveUserTeamID(ctx, client)
		if err != nil {
			return nil, err
		}
	}

	teamClient, err := a.newClient(token, teamID)
	if err != nil {
		return nil, newAuthError(http.StatusBadGateway, "failed to initialize sandbox0 team client", err)
	}
	if err := a.verifyUserTeamAccess(ctx, teamClient, teamID); err != nil {
		return nil, err
	}

	return &Context{TeamID: teamID, UserID: user.ID}, nil
}

func (a *Sandbox0Authenticator) authenticateAPIKey(ctx context.Context, token, selectedTeamID string) (*Context, error) {
	client, err := a.newClient(token, "")
	if err != nil {
		return nil, newAuthError(http.StatusBadGateway, "failed to initialize sandbox0 client", err)
	}
	apiKey, err := client.GetCurrentAPIKey(ctx)
	if err != nil {
		return nil, a.wrapSandbox0Error(err)
	}
	teamID := strings.TrimSpace(apiKey.TeamID)
	if teamID == "" {
		return nil, newAuthError(http.StatusBadGateway, "sandbox0 api key response missing team id", nil)
	}
	if selected := strings.TrimSpace(selectedTeamID); selected != "" && selected != teamID {
		return nil, newAuthError(http.StatusForbidden, "x-team-id does not match the sandbox0 api key team", nil)
	}

	return &Context{TeamID: teamID}, nil
}

func (a *Sandbox0Authenticator) currentUser(ctx context.Context, client *sandbox0sdk.Client) (*apispec.User, error) {
	resp, err := client.API().UsersMeGet(ctx)
	if err != nil {
		return nil, a.wrapSandbox0Error(err)
	}
	response, ok := resp.(*apispec.SuccessUserResponse)
	if !ok {
		return nil, newAuthError(http.StatusBadGateway, "unexpected sandbox0 users/me response", nil)
	}
	user, ok := response.Data.Get()
	if !ok {
		return nil, newAuthError(http.StatusBadGateway, "sandbox0 users/me response missing user", nil)
	}
	return &user, nil
}

func (a *Sandbox0Authenticator) resolveUserTeamID(ctx context.Context, client *sandbox0sdk.Client) (string, error) {
	resp, err := client.API().TeamsGet(ctx)
	if err != nil {
		return "", a.wrapSandbox0Error(err)
	}
	response, ok := resp.(*apispec.SuccessTeamListResponse)
	if !ok {
		return "", newAuthError(http.StatusBadGateway, "unexpected sandbox0 teams response", nil)
	}
	data, ok := response.Data.Get()
	if !ok {
		return "", newAuthError(http.StatusBadGateway, "sandbox0 teams response missing data", nil)
	}
	teams := data.Teams
	if len(teams) == 0 {
		return "", newAuthError(http.StatusForbidden, "authenticated user is not a member of any team", nil)
	}
	if len(teams) == 1 {
		return strings.TrimSpace(teams[0].ID), nil
	}
	teamIDs := make([]string, 0, len(teams))
	for _, team := range teams {
		if teamID := strings.TrimSpace(team.ID); teamID != "" {
			teamIDs = append(teamIDs, teamID)
		}
	}
	if len(teamIDs) == 0 {
		return "", newAuthError(http.StatusBadGateway, "sandbox0 teams response missing team ids", nil)
	}
	slices.Sort(teamIDs)
	return teamIDs[0], nil
}

func (a *Sandbox0Authenticator) verifyUserTeamAccess(ctx context.Context, client *sandbox0sdk.Client, teamID string) error {
	resp, err := client.API().TeamsIDGet(ctx, apispec.TeamsIDGetParams{ID: teamID})
	if err != nil {
		return a.wrapSandbox0Error(err)
	}
	response, ok := resp.(*apispec.SuccessTeamResponse)
	if !ok {
		return newAuthError(http.StatusBadGateway, "unexpected sandbox0 team response", nil)
	}
	team, ok := response.Data.Get()
	if !ok {
		return newAuthError(http.StatusBadGateway, "sandbox0 team response missing team", nil)
	}
	if strings.TrimSpace(team.ID) != teamID {
		return newAuthError(http.StatusForbidden, "sandbox0 returned a mismatched team context", nil)
	}
	return nil
}

func (a *Sandbox0Authenticator) newClient(token, teamID string) (*sandbox0sdk.Client, error) {
	opts := []sandbox0sdk.Option{
		sandbox0sdk.WithBaseURL(a.baseURL),
		sandbox0sdk.WithToken(token),
		sandbox0sdk.WithTimeout(a.timeout),
	}
	if teamID != "" {
		opts = append(opts, sandbox0sdk.WithRequestEditor(func(_ context.Context, req *http.Request) error {
			req.Header.Set(teamIDHeader, teamID)
			return nil
		}))
	}
	return sandbox0sdk.NewClient(opts...)
}

func (a *Sandbox0Authenticator) wrapSandbox0Error(err error) error {
	var apiErr *sandbox0sdk.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusUnauthorized:
			return newAuthError(http.StatusUnauthorized, "sandbox0 rejected the request credentials", err)
		case http.StatusForbidden:
			return newAuthError(http.StatusForbidden, apiErr.Message, err)
		case http.StatusBadRequest:
			return newAuthError(http.StatusBadRequest, apiErr.Message, err)
		default:
			return newAuthError(http.StatusBadGateway, fmt.Sprintf("sandbox0 auth probe failed: %s", apiErr.Message), err)
		}
	}
	return newAuthError(http.StatusBadGateway, "sandbox0 auth probe failed", err)
}

type authError struct {
	status int
	msg    string
	err    error
}

func newAuthError(status int, msg string, err error) *authError {
	return &authError{status: status, msg: strings.TrimSpace(msg), err: err}
}

func (e *authError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.msg != "" {
		return e.msg
	}
	if e.err != nil {
		return e.err.Error()
	}
	return "authentication failed"
}

func (e *authError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func writeAuthError(c *gin.Context, err error) {
	status := http.StatusUnauthorized
	message := "authentication failed"
	var authErr *authError
	if errors.As(err, &authErr) {
		status = authErr.status
		if strings.TrimSpace(authErr.msg) != "" {
			message = authErr.msg
		}
	}
	requestID := authRequestID(c)
	c.Header("Request-Id", requestID)
	c.AbortWithStatusJSON(status, contractutil.ErrorResponse(contractutil.ErrorCodeForStatus(status), message, &requestID))
}

func authRequestID(c *gin.Context) string {
	if c == nil {
		return "req_auth"
	}
	if requestID := strings.TrimSpace(c.GetHeader("Request-Id")); requestID != "" {
		return requestID
	}
	if requestID := strings.TrimSpace(c.GetHeader("X-Request-Id")); requestID != "" {
		return requestID
	}
	return "req_auth"
}

func extractRequestToken(c *gin.Context) string {
	if c == nil {
		return ""
	}
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
