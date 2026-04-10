package managedagents

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// StoredCredential carries the public snapshot plus the secret payload.
type StoredCredential struct {
	Snapshot Credential
	Secret   map[string]any
}

type normalizedCredentialAuth struct {
	Public map[string]any
	Secret map[string]any
}

func normalizeCreateCredentialAuth(auth map[string]any) (*normalizedCredentialAuth, error) {
	copyAuth := cloneMap(auth)
	switch strings.TrimSpace(stringValue(copyAuth["type"])) {
	case "static_bearer":
		if err := validateAllowedFields(copyAuth, []string{"type", "token", "mcp_server_url"}); err != nil {
			return nil, err
		}
		token, err := requiredTrimmedString(copyAuth, "token")
		if err != nil {
			return nil, err
		}
		serverURL, err := requiredURLString(copyAuth, "mcp_server_url")
		if err != nil {
			return nil, err
		}
		return &normalizedCredentialAuth{
			Public: map[string]any{
				"type":           "static_bearer",
				"mcp_server_url": serverURL,
			},
			Secret: map[string]any{
				"type":           "static_bearer",
				"token":          token,
				"mcp_server_url": serverURL,
			},
		}, nil
	case "mcp_oauth":
		if err := validateAllowedFields(copyAuth, []string{"type", "mcp_server_url", "access_token", "expires_at", "refresh"}); err != nil {
			return nil, err
		}
		accessToken, err := requiredTrimmedString(copyAuth, "access_token")
		if err != nil {
			return nil, err
		}
		serverURL, err := requiredURLString(copyAuth, "mcp_server_url")
		if err != nil {
			return nil, err
		}
		public := map[string]any{
			"type":           "mcp_oauth",
			"mcp_server_url": serverURL,
		}
		secret := map[string]any{
			"type":           "mcp_oauth",
			"mcp_server_url": serverURL,
			"access_token":   accessToken,
		}
		if value, ok, err := optionalTimestampValue(copyAuth, "expires_at"); err != nil {
			return nil, err
		} else if ok {
			public["expires_at"] = value
			secret["expires_at"] = value
		}
		if raw, ok := copyAuth["refresh"]; ok {
			publicRefresh, secretRefresh, err := normalizeCreateMcpOAuthRefresh(raw)
			if err != nil {
				return nil, err
			}
			if publicRefresh != nil {
				public["refresh"] = publicRefresh
				secret["refresh"] = secretRefresh
			}
		}
		return &normalizedCredentialAuth{Public: public, Secret: secret}, nil
	default:
		return nil, errors.New("auth.type must be static_bearer or mcp_oauth")
	}
}

func normalizeUpdateCredentialAuth(currentPublic map[string]any, currentSecret map[string]any, updates map[string]any) (*normalizedCredentialAuth, error) {
	copyUpdates := cloneMap(updates)
	currentType := strings.TrimSpace(stringValue(currentPublic["type"]))
	if currentType == "" {
		currentType = strings.TrimSpace(stringValue(currentSecret["type"]))
	}
	requestedType := strings.TrimSpace(stringValue(copyUpdates["type"]))
	if requestedType == "" {
		return nil, errors.New("auth.type is required")
	}
	if currentType != "" && requestedType != currentType {
		return nil, errors.New("auth.type cannot be changed")
	}
	public := cloneMap(currentPublic)
	secret := cloneMap(currentSecret)
	if len(public) == 0 {
		public["type"] = requestedType
	}
	if len(secret) == 0 {
		secret["type"] = requestedType
	}
	switch requestedType {
	case "static_bearer":
		if err := validateAllowedFields(copyUpdates, []string{"type", "token"}); err != nil {
			return nil, err
		}
		if value, ok := copyUpdates["token"]; ok {
			if value == nil {
				delete(secret, "token")
			} else {
				token := strings.TrimSpace(stringValue(value))
				if token == "" {
					return nil, errors.New("auth.token must be a non-empty string")
				}
				secret["token"] = token
			}
		}
	case "mcp_oauth":
		if err := validateAllowedFields(copyUpdates, []string{"type", "access_token", "expires_at", "refresh"}); err != nil {
			return nil, err
		}
		if value, ok := copyUpdates["access_token"]; ok {
			if value == nil {
				delete(secret, "access_token")
			} else {
				accessToken := strings.TrimSpace(stringValue(value))
				if accessToken == "" {
					return nil, errors.New("auth.access_token must be a non-empty string")
				}
				secret["access_token"] = accessToken
			}
		}
		if value, ok, err := optionalTimestampValue(copyUpdates, "expires_at"); err != nil {
			return nil, err
		} else if ok {
			if value == nil {
				delete(public, "expires_at")
				delete(secret, "expires_at")
			} else {
				public["expires_at"] = value
				secret["expires_at"] = value
			}
		}
		if raw, ok := copyUpdates["refresh"]; ok {
			if raw == nil {
				delete(public, "refresh")
				delete(secret, "refresh")
			} else {
				currentPublicRefresh, _ := public["refresh"].(map[string]any)
				currentSecretRefresh, _ := secret["refresh"].(map[string]any)
				nextPublicRefresh, nextSecretRefresh, err := normalizeUpdateMcpOAuthRefresh(currentPublicRefresh, currentSecretRefresh, raw)
				if err != nil {
					return nil, err
				}
				public["refresh"] = nextPublicRefresh
				secret["refresh"] = nextSecretRefresh
			}
		}
	default:
		return nil, errors.New("auth.type must be static_bearer or mcp_oauth")
	}
	return &normalizedCredentialAuth{Public: public, Secret: secret}, nil
}

func normalizeCreateMcpOAuthRefresh(raw any) (map[string]any, map[string]any, error) {
	if raw == nil {
		return nil, nil, nil
	}
	refresh, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, errors.New("auth.refresh must be an object")
	}
	refresh = cloneMap(refresh)
	if err := validateAllowedFields(refresh, []string{"refresh_token", "token_endpoint", "client_id", "scope", "resource", "token_endpoint_auth"}); err != nil {
		return nil, nil, err
	}
	refreshToken, err := requiredTrimmedString(refresh, "refresh_token")
	if err != nil {
		return nil, nil, err
	}
	tokenEndpoint, err := requiredURLString(refresh, "token_endpoint")
	if err != nil {
		return nil, nil, err
	}
	clientID, err := requiredTrimmedString(refresh, "client_id")
	if err != nil {
		return nil, nil, err
	}
	publicAuth, secretAuth, err := normalizeCreateTokenEndpointAuth(refresh["token_endpoint_auth"])
	if err != nil {
		return nil, nil, err
	}
	public := map[string]any{
		"token_endpoint":      tokenEndpoint,
		"client_id":           clientID,
		"token_endpoint_auth": publicAuth,
	}
	secret := map[string]any{
		"refresh_token":       refreshToken,
		"token_endpoint":      tokenEndpoint,
		"client_id":           clientID,
		"token_endpoint_auth": secretAuth,
	}
	if value, ok := optionalCredentialString(refresh, "scope"); ok {
		public["scope"] = value
		secret["scope"] = value
	}
	if value, ok := optionalCredentialString(refresh, "resource"); ok {
		public["resource"] = value
		secret["resource"] = value
	}
	return public, secret, nil
}

func normalizeUpdateMcpOAuthRefresh(currentPublic map[string]any, currentSecret map[string]any, raw any) (map[string]any, map[string]any, error) {
	refresh, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, errors.New("auth.refresh must be an object")
	}
	refresh = cloneMap(refresh)
	if err := validateAllowedFields(refresh, []string{"refresh_token", "scope", "token_endpoint_auth"}); err != nil {
		return nil, nil, err
	}
	public := cloneMap(currentPublic)
	secret := cloneMap(currentSecret)
	if value, ok := refresh["refresh_token"]; ok {
		if value == nil {
			delete(secret, "refresh_token")
		} else {
			refreshToken := strings.TrimSpace(stringValue(value))
			if refreshToken == "" {
				return nil, nil, errors.New("auth.refresh.refresh_token must be a non-empty string")
			}
			secret["refresh_token"] = refreshToken
		}
	}
	if value, ok := refresh["scope"]; ok {
		if value == nil {
			delete(public, "scope")
			delete(secret, "scope")
		} else {
			scope := strings.TrimSpace(stringValue(value))
			if scope == "" {
				delete(public, "scope")
				delete(secret, "scope")
			} else {
				public["scope"] = scope
				secret["scope"] = scope
			}
		}
	}
	if rawAuth, ok := refresh["token_endpoint_auth"]; ok {
		currentPublicAuth, _ := public["token_endpoint_auth"].(map[string]any)
		currentSecretAuth, _ := secret["token_endpoint_auth"].(map[string]any)
		nextPublicAuth, nextSecretAuth, err := normalizeUpdateTokenEndpointAuth(currentPublicAuth, currentSecretAuth, rawAuth)
		if err != nil {
			return nil, nil, err
		}
		public["token_endpoint_auth"] = nextPublicAuth
		secret["token_endpoint_auth"] = nextSecretAuth
	}
	return public, secret, nil
}

func normalizeCreateTokenEndpointAuth(raw any) (map[string]any, map[string]any, error) {
	auth, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, errors.New("auth.refresh.token_endpoint_auth must be an object")
	}
	auth = cloneMap(auth)
	switch strings.TrimSpace(stringValue(auth["type"])) {
	case "none":
		if err := validateAllowedFields(auth, []string{"type"}); err != nil {
			return nil, nil, err
		}
		return map[string]any{"type": "none"}, map[string]any{"type": "none"}, nil
	case "client_secret_basic", "client_secret_post":
		if err := validateAllowedFields(auth, []string{"type", "client_secret"}); err != nil {
			return nil, nil, err
		}
		clientSecret, err := requiredTrimmedString(auth, "client_secret")
		if err != nil {
			return nil, nil, err
		}
		typeName := strings.TrimSpace(stringValue(auth["type"]))
		return map[string]any{"type": typeName}, map[string]any{"type": typeName, "client_secret": clientSecret}, nil
	default:
		return nil, nil, errors.New("auth.refresh.token_endpoint_auth.type is invalid")
	}
}

func normalizeUpdateTokenEndpointAuth(currentPublic map[string]any, currentSecret map[string]any, raw any) (map[string]any, map[string]any, error) {
	auth, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, errors.New("auth.refresh.token_endpoint_auth must be an object")
	}
	auth = cloneMap(auth)
	typeName := strings.TrimSpace(stringValue(auth["type"]))
	if typeName != "client_secret_basic" && typeName != "client_secret_post" {
		return nil, nil, errors.New("auth.refresh.token_endpoint_auth.type is invalid")
	}
	if err := validateAllowedFields(auth, []string{"type", "client_secret"}); err != nil {
		return nil, nil, err
	}
	public := cloneMap(currentPublic)
	secret := cloneMap(currentSecret)
	public["type"] = typeName
	secret["type"] = typeName
	if value, ok := auth["client_secret"]; ok {
		if value == nil {
			delete(secret, "client_secret")
		} else {
			clientSecret := strings.TrimSpace(stringValue(value))
			if clientSecret == "" {
				return nil, nil, errors.New("auth.refresh.token_endpoint_auth.client_secret must be a non-empty string")
			}
			secret["client_secret"] = clientSecret
		}
	}
	return public, secret, nil
}

func requiredTrimmedString(input map[string]any, field string) (string, error) {
	value := strings.TrimSpace(stringValue(input[field]))
	if value == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	return value, nil
}

func optionalCredentialString(input map[string]any, field string) (string, bool) {
	value, ok := input[field]
	if !ok || value == nil {
		return "", false
	}
	trimmed := strings.TrimSpace(stringValue(value))
	if trimmed == "" {
		return "", false
	}
	return trimmed, true
}

func requiredURLString(input map[string]any, field string) (string, error) {
	value, err := requiredTrimmedString(input, field)
	if err != nil {
		return "", err
	}
	parsed, parseErr := url.Parse(value)
	if parseErr != nil || strings.TrimSpace(parsed.Scheme) == "" || strings.TrimSpace(parsed.Hostname()) == "" {
		return "", fmt.Errorf("%s must be a valid url", field)
	}
	return value, nil
}

func optionalTimestampValue(input map[string]any, field string) (any, bool, error) {
	value, ok := input[field]
	if !ok {
		return nil, false, nil
	}
	if value == nil {
		return nil, true, nil
	}
	trimmed := strings.TrimSpace(stringValue(value))
	if trimmed == "" {
		return nil, false, fmt.Errorf("%s must be a valid RFC3339 timestamp", field)
	}
	if _, err := time.Parse(time.RFC3339, trimmed); err != nil {
		return nil, false, fmt.Errorf("%s must be a valid RFC3339 timestamp", field)
	}
	return trimmed, true, nil
}
