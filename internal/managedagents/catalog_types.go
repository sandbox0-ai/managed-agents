package managedagents

import (
	"strings"
	"time"
)

// CreateAgentRequest is the public create-agent payload.
type CreateAgentRequest struct {
	Name        string            `json:"name"`
	Model       any               `json:"model"`
	Description *string           `json:"description"`
	System      *string           `json:"system"`
	Tools       []any             `json:"tools"`
	MCPServers  []any             `json:"mcp_servers"`
	Skills      []any             `json:"skills"`
	Metadata    map[string]string `json:"metadata"`
}

// UpdateAgentRequest is the public update-agent payload.
type UpdateAgentRequest struct {
	Name        *string           `json:"name"`
	Model       any               `json:"model"`
	Description *string           `json:"description"`
	System      *string           `json:"system"`
	Tools       []any             `json:"tools"`
	MCPServers  []any             `json:"mcp_servers"`
	Skills      []any             `json:"skills"`
	Metadata    map[string]string `json:"metadata"`
}

// CreateEnvironmentRequest is the public create-environment payload.
type CreateEnvironmentRequest struct {
	Name        string            `json:"name"`
	Description *string           `json:"description"`
	Config      map[string]any    `json:"config"`
	Metadata    map[string]string `json:"metadata"`
}

// UpdateEnvironmentRequest is the public update-environment payload.
type UpdateEnvironmentRequest struct {
	Name        *string            `json:"name"`
	Description *string            `json:"description"`
	Config      map[string]any     `json:"config"`
	Metadata    map[string]*string `json:"metadata"`
}

// CreateVaultRequest is the public create-vault payload.
type CreateVaultRequest struct {
	DisplayName string            `json:"display_name"`
	Metadata    map[string]string `json:"metadata"`
}

// UpdateVaultRequest is the public update-vault payload.
type UpdateVaultRequest struct {
	DisplayName *string            `json:"display_name"`
	Metadata    map[string]*string `json:"metadata"`
}

// CreateCredentialRequest is the public create-credential payload.
type CreateCredentialRequest struct {
	Auth        map[string]any    `json:"auth"`
	DisplayName *string           `json:"display_name"`
	Metadata    map[string]string `json:"metadata"`
}

// UpdateCredentialRequest is the public update-credential payload.
type UpdateCredentialRequest struct {
	Auth        map[string]any     `json:"auth"`
	DisplayName *string            `json:"display_name"`
	Metadata    map[string]*string `json:"metadata"`
}

// AddSessionResourceRequest is the public add-resource payload.
type AddSessionResourceRequest map[string]any

// UpdateSessionResourceRequest is the public update-resource payload.
type UpdateSessionResourceRequest map[string]any

type managedFileRecord struct {
	ID        string
	TeamID    string
	Filename  string
	MimeType  string
	SizeBytes int64
	ScopeType string
	ScopeID   string
	Content   []byte
	CreatedAt time.Time
	UpdatedAt time.Time
}

func nowRFC3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func normalizeModelConfig(input any, vendorHint string) (string, map[string]any) {
	vendor := strings.TrimSpace(vendorHint)
	switch value := input.(type) {
	case string:
		if vendor == "" {
			vendor = inferVendorFromModel(value)
		}
		return vendor, map[string]any{"id": value, "speed": "standard"}
	case map[string]any:
		model := cloneMap(value)
		if vendor == "" {
			vendor = inferVendorFromModel(stringValue(model["id"]))
		}
		if strings.TrimSpace(stringValue(model["id"])) == "" {
			model["id"] = defaultModelForVendor(vendor)
		}
		if _, ok := model["speed"]; !ok {
			model["speed"] = "standard"
		}
		return vendor, model
	default:
		if vendor == "" {
			vendor = "claude"
		}
		return vendor, map[string]any{"id": defaultModelForVendor(vendor), "speed": "standard"}
	}
}

func buildAgentObject(id string, version int, vendor string, req CreateAgentRequest, createdAt time.Time, archivedAt *time.Time) map[string]any {
	_, model := normalizeModelConfig(req.Model, vendor)
	return map[string]any{
		"type":        "agent",
		"id":          id,
		"version":     version,
		"name":        strings.TrimSpace(req.Name),
		"description": nullableText(req.Description),
		"model":       model,
		"system":      nullableText(req.System),
		"tools":       cloneSlice(req.Tools),
		"mcp_servers": cloneSlice(req.MCPServers),
		"skills":      cloneSlice(req.Skills),
		"metadata":    cloneStringMap(req.Metadata),
		"created_at":  nowRFC3339(createdAt),
		"updated_at":  nowRFC3339(createdAt),
		"archived_at": timestampOrNil(archivedAt),
	}
}

func buildEnvironmentObject(id string, req CreateEnvironmentRequest, now time.Time, archivedAt *time.Time) map[string]any {
	description := ""
	if req.Description != nil {
		description = strings.TrimSpace(*req.Description)
	}
	return map[string]any{
		"type":        "environment",
		"id":          id,
		"name":        strings.TrimSpace(req.Name),
		"description": description,
		"config":      cloneMap(req.Config),
		"metadata":    cloneStringMap(req.Metadata),
		"created_at":  nowRFC3339(now),
		"updated_at":  nowRFC3339(now),
		"archived_at": timestampOrNil(archivedAt),
	}
}

func buildVaultObject(id string, req CreateVaultRequest, now time.Time, archivedAt *time.Time) map[string]any {
	return map[string]any{
		"type":         "vault",
		"id":           id,
		"display_name": strings.TrimSpace(req.DisplayName),
		"metadata":     cloneStringMap(req.Metadata),
		"created_at":   nowRFC3339(now),
		"updated_at":   nowRFC3339(now),
		"archived_at":  timestampOrNil(archivedAt),
	}
}

func buildCredentialObject(id, vaultID string, req CreateCredentialRequest, now time.Time, archivedAt *time.Time) map[string]any {
	return map[string]any{
		"type":         "vault_credential",
		"id":           id,
		"vault_id":     vaultID,
		"display_name": nullableText(req.DisplayName),
		"metadata":     cloneStringMap(req.Metadata),
		"created_at":   nowRFC3339(now),
		"updated_at":   nowRFC3339(now),
		"archived_at":  timestampOrNil(archivedAt),
		"auth":         redactCredentialAuth(req.Auth),
	}
}

func buildFileObject(record *managedFileRecord) map[string]any {
	if record == nil {
		return map[string]any{}
	}
	var scope any
	if strings.TrimSpace(record.ScopeType) != "" && strings.TrimSpace(record.ScopeID) != "" {
		scope = map[string]any{"type": record.ScopeType, "id": record.ScopeID}
	}
	return map[string]any{
		"type":         "file",
		"id":           record.ID,
		"filename":     record.Filename,
		"mime_type":    record.MimeType,
		"size_bytes":   record.SizeBytes,
		"downloadable": true,
		"scope":        scope,
		"created_at":   nowRFC3339(record.CreatedAt),
	}
}

func normalizeSessionResource(resource map[string]any, when time.Time) map[string]any {
	out := cloneMap(resource)
	if strings.TrimSpace(stringValue(out["id"])) == "" {
		out["id"] = NewID("sesrsc")
	}
	if strings.TrimSpace(stringValue(out["created_at"])) == "" {
		out["created_at"] = nowRFC3339(when)
	}
	out["updated_at"] = nowRFC3339(when)
	return out
}

func redactCredentialAuth(auth map[string]any) map[string]any {
	redacted := cloneMap(auth)
	delete(redacted, "token")
	delete(redacted, "access_token")
	delete(redacted, "client_secret")
	if refresh, ok := redacted["refresh"].(map[string]any); ok {
		refreshCopy := cloneMap(refresh)
		delete(refreshCopy, "refresh_token")
		if tokenEndpointAuth, ok := refreshCopy["token_endpoint_auth"].(map[string]any); ok {
			tokenEndpointAuthCopy := cloneMap(tokenEndpointAuth)
			delete(tokenEndpointAuthCopy, "client_secret")
			refreshCopy["token_endpoint_auth"] = tokenEndpointAuthCopy
		}
		redacted["refresh"] = refreshCopy
	}
	return redacted
}

func nullableText(value *string) any {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return trimmed
}

func timestampOrNil(value *time.Time) any {
	if value == nil {
		return nil
	}
	return nowRFC3339(*value)
}

func cloneSlice(in []any) []any {
	if len(in) == 0 {
		return []any{}
	}
	out := make([]any, 0, len(in))
	for _, item := range in {
		switch value := item.(type) {
		case map[string]any:
			out = append(out, cloneMap(value))
		default:
			out = append(out, value)
		}
	}
	return out
}
