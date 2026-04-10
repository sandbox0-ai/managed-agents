package managedagentsruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
)

const mcpOAuthRefreshLeadTime = 30 * time.Second

type mcpOAuthRefreshConfig struct {
	CredentialID  string
	TokenEndpoint string
	ClientID      string
	RefreshToken  string
	Scope         string
	Resource      string
	Auth          tokenEndpointAuthConfig
}

type tokenEndpointAuthConfig struct {
	Type         string
	ClientSecret string
}

type oauthRefreshResponse struct {
	AccessToken  string          `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	ExpiresIn    json.Number     `json:"expires_in"`
	TokenType    string          `json:"token_type"`
	RawExpiresAt json.RawMessage `json:"expires_at"`
}

func (m *SDKRuntimeManager) maybeRefreshVaultCredential(ctx context.Context, teamID, vaultID string, credential gatewaymanagedagents.StoredCredential, now time.Time) (gatewaymanagedagents.StoredCredential, error) {
	needsRefresh, err := credentialNeedsMcpOAuthRefresh(credential, now)
	if err != nil || !needsRefresh {
		return credential, err
	}
	updated, changed, err := refreshMcpOAuthCredential(ctx, m.httpClient, credential, now)
	if err != nil {
		return credential, err
	}
	if !changed {
		return credential, nil
	}
	if err := m.repo.UpdateCredential(ctx, teamID, vaultID, updated.Snapshot.ID, &updated.Snapshot, updated.Secret, archivedAtPointer(updated.Snapshot.ArchivedAt), now); err != nil {
		return credential, err
	}
	return updated, nil
}

func credentialNeedsMcpOAuthRefresh(credential gatewaymanagedagents.StoredCredential, now time.Time) (bool, error) {
	publicAuth := gatewaymanagedagents.CredentialAuthToMapForRuntime(credential.Snapshot.Auth)
	if stringValue(publicAuth["type"]) != "mcp_oauth" {
		return false, nil
	}
	expiresAt := timePointer(stringValue(publicAuth["expires_at"]))
	if expiresAt == nil {
		expiresAt = timePointer(stringValue(credential.Secret["expires_at"]))
	}
	if expiresAt == nil {
		return false, nil
	}
	if expiresAt.After(now.Add(mcpOAuthRefreshLeadTime)) {
		return false, nil
	}
	if mapValue(credential.Secret["refresh"])["refresh_token"] == nil {
		return false, fmt.Errorf("vault credential %s access token is expired and cannot be refreshed", credential.Snapshot.ID)
	}
	return true, nil
}

func refreshMcpOAuthCredential(ctx context.Context, httpClient *http.Client, credential gatewaymanagedagents.StoredCredential, now time.Time) (gatewaymanagedagents.StoredCredential, bool, error) {
	refreshConfig, err := parseMcpOAuthRefreshConfig(credential)
	if err != nil {
		return credential, false, err
	}
	response, err := executeMcpOAuthRefresh(ctx, httpClient, refreshConfig)
	if err != nil {
		return credential, false, err
	}
	updated := gatewaymanagedagents.StoredCredential{
		Snapshot: credential.Snapshot,
		Secret:   cloneMap(credential.Secret),
	}
	publicAuth := gatewaymanagedagents.CredentialAuthToMapForRuntime(updated.Snapshot.Auth)
	secretAuth := cloneMap(updated.Secret)
	secretAuth["access_token"] = response.AccessToken
	if expiresAt := response.expiresAt(now); expiresAt != nil {
		formatted := expiresAt.UTC().Format(time.RFC3339)
		publicAuth["expires_at"] = formatted
		secretAuth["expires_at"] = formatted
	} else {
		delete(publicAuth, "expires_at")
		delete(secretAuth, "expires_at")
	}
	if response.RefreshToken != "" {
		refreshSecret := cloneMap(mapValue(secretAuth["refresh"]))
		refreshSecret["refresh_token"] = response.RefreshToken
		secretAuth["refresh"] = refreshSecret
	}
	updated.Snapshot.Auth = gatewaymanagedagents.CredentialAuthFromMapForRuntime(publicAuth)
	updated.Snapshot.UpdatedAt = now.UTC().Format(time.RFC3339)
	updated.Secret = secretAuth
	return updated, true, nil
}

func parseMcpOAuthRefreshConfig(credential gatewaymanagedagents.StoredCredential) (*mcpOAuthRefreshConfig, error) {
	publicAuth := gatewaymanagedagents.CredentialAuthToMapForRuntime(credential.Snapshot.Auth)
	if stringValue(publicAuth["type"]) != "mcp_oauth" {
		return nil, errors.New("credential is not an mcp_oauth credential")
	}
	refreshSecret := mapValue(credential.Secret["refresh"])
	refreshPublic := mapValue(publicAuth["refresh"])
	refreshToken := strings.TrimSpace(stringValue(refreshSecret["refresh_token"]))
	if refreshToken == "" {
		return nil, fmt.Errorf("vault credential %s is missing refresh_token", credential.Snapshot.ID)
	}
	tokenEndpoint := strings.TrimSpace(stringValue(refreshSecret["token_endpoint"]))
	if tokenEndpoint == "" {
		tokenEndpoint = strings.TrimSpace(stringValue(refreshPublic["token_endpoint"]))
	}
	clientID := strings.TrimSpace(stringValue(refreshSecret["client_id"]))
	if clientID == "" {
		clientID = strings.TrimSpace(stringValue(refreshPublic["client_id"]))
	}
	authConfig, err := parseTokenEndpointAuthConfig(refreshPublic, refreshSecret)
	if err != nil {
		return nil, err
	}
	if tokenEndpoint == "" || clientID == "" {
		return nil, fmt.Errorf("vault credential %s refresh config is incomplete", credential.Snapshot.ID)
	}
	return &mcpOAuthRefreshConfig{
		CredentialID:  credential.Snapshot.ID,
		TokenEndpoint: tokenEndpoint,
		ClientID:      clientID,
		RefreshToken:  refreshToken,
		Scope:         firstNonEmptyString(stringValue(refreshSecret["scope"]), stringValue(refreshPublic["scope"])),
		Resource:      firstNonEmptyString(stringValue(refreshSecret["resource"]), stringValue(refreshPublic["resource"])),
		Auth:          authConfig,
	}, nil
}

func executeMcpOAuthRefresh(ctx context.Context, httpClient *http.Client, cfg *mcpOAuthRefreshConfig) (*oauthRefreshResponse, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", cfg.RefreshToken)
	if cfg.Auth.Type != "client_secret_basic" {
		form.Set("client_id", cfg.ClientID)
	}
	if cfg.Auth.Type == "client_secret_post" {
		form.Set("client_secret", cfg.Auth.ClientSecret)
	}
	if strings.TrimSpace(cfg.Scope) != "" {
		form.Set("scope", cfg.Scope)
	}
	if strings.TrimSpace(cfg.Resource) != "" {
		form.Set("resource", cfg.Resource)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if cfg.Auth.Type == "client_secret_basic" {
		encoded := base64.StdEncoding.EncodeToString([]byte(cfg.ClientID + ":" + cfg.Auth.ClientSecret))
		req.Header.Set("Authorization", "Basic "+encoded)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh token for credential %s: %w", cfg.CredentialID, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read token refresh response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("refresh token for credential %s failed: %s", cfg.CredentialID, strings.TrimSpace(string(body)))
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var parsed oauthRefreshResponse
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode token refresh response: %w", err)
	}
	if strings.TrimSpace(parsed.AccessToken) == "" {
		return nil, fmt.Errorf("refresh token for credential %s returned an empty access_token", cfg.CredentialID)
	}
	return &parsed, nil
}

func parseTokenEndpointAuthConfig(publicRefresh map[string]any, secretRefresh map[string]any) (tokenEndpointAuthConfig, error) {
	secretAuth := mapValue(secretRefresh["token_endpoint_auth"])
	publicAuth := mapValue(publicRefresh["token_endpoint_auth"])
	typeName := firstNonEmptyString(stringValue(secretAuth["type"]), stringValue(publicAuth["type"]))
	switch typeName {
	case "none":
		return tokenEndpointAuthConfig{Type: "none"}, nil
	case "client_secret_basic", "client_secret_post":
		clientSecret := strings.TrimSpace(stringValue(secretAuth["client_secret"]))
		if clientSecret == "" {
			return tokenEndpointAuthConfig{}, errors.New("refresh token endpoint auth is missing client_secret")
		}
		return tokenEndpointAuthConfig{Type: typeName, ClientSecret: clientSecret}, nil
	default:
		return tokenEndpointAuthConfig{}, errors.New("refresh token endpoint auth type is invalid")
	}
}

func (r *oauthRefreshResponse) expiresAt(now time.Time) *time.Time {
	if r == nil {
		return nil
	}
	if expiresIn := strings.TrimSpace(r.ExpiresIn.String()); expiresIn != "" {
		if seconds, err := r.ExpiresIn.Int64(); err == nil && seconds > 0 {
			expiresAt := now.Add(time.Duration(seconds) * time.Second)
			return &expiresAt
		}
	}
	if len(r.RawExpiresAt) != 0 && string(r.RawExpiresAt) != "null" {
		var text string
		if err := json.Unmarshal(r.RawExpiresAt, &text); err == nil {
			return timePointer(text)
		}
	}
	return nil
}

func archivedAtPointer(value *string) *time.Time {
	if value == nil {
		return nil
	}
	return timePointer(*value)
}

func timePointer(value string) *time.Time {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return nil
	}
	return &parsed
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	encoded, err := json.Marshal(in)
	if err != nil {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := json.Unmarshal(encoded, &out); err != nil {
		return map[string]any{}
	}
	return out
}
