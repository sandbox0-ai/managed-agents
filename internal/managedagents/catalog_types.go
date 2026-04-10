package managedagents

import (
	"encoding/json"
	"errors"
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
	Version     int                    `json:"version"`
	Name        OptionalStringField    `json:"name"`
	Model       OptionalValueField     `json:"model"`
	Description NullableTextPatchField `json:"description"`
	System      NullableTextPatchField `json:"system"`
	Tools       OptionalJSONArrayField `json:"tools"`
	MCPServers  OptionalJSONArrayField `json:"mcp_servers"`
	Skills      OptionalJSONArrayField `json:"skills"`
	Metadata    MetadataPatchField     `json:"metadata"`
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
	Name        *string                `json:"name"`
	Description NullableTextPatchField `json:"description"`
	Config      map[string]any         `json:"config"`
	Metadata    MetadataPatchField     `json:"metadata"`
}

// CreateVaultRequest is the public create-vault payload.
type CreateVaultRequest struct {
	DisplayName string            `json:"display_name"`
	Metadata    map[string]string `json:"metadata"`
}

// UpdateVaultRequest is the public update-vault payload.
type UpdateVaultRequest struct {
	DisplayName NullableTextPatchField `json:"display_name"`
	Metadata    MetadataPatchField     `json:"metadata"`
}

// CreateCredentialRequest is the public create-credential payload.
type CreateCredentialRequest struct {
	Auth        map[string]any    `json:"auth"`
	DisplayName *string           `json:"display_name"`
	Metadata    map[string]string `json:"metadata"`
}

// UpdateCredentialRequest is the public update-credential payload.
type UpdateCredentialRequest struct {
	Auth        map[string]any         `json:"auth"`
	DisplayName NullableTextPatchField `json:"display_name"`
	Metadata    MetadataPatchField     `json:"metadata"`
}

// AddSessionResourceRequest is the public add-resource payload.
type AddSessionResourceRequest map[string]any

// UpdateSessionResourceRequest is the public update-resource payload.
type UpdateSessionResourceRequest map[string]any

// NullableTextPatchField preserves omitted-vs-null semantics for nullable text updates.
type NullableTextPatchField struct {
	Set   bool
	Value *string
}

// OptionalStringField preserves whether a non-null string field was explicitly provided.
type OptionalStringField struct {
	Set   bool
	Value string
}

func (f *OptionalStringField) UnmarshalJSON(data []byte) error {
	f.Set = true
	if strings.TrimSpace(string(data)) == "null" {
		return errors.New("value must be a string")
	}
	return json.Unmarshal(data, &f.Value)
}

func (f *NullableTextPatchField) UnmarshalJSON(data []byte) error {
	f.Set = true
	if strings.TrimSpace(string(data)) == "null" {
		f.Value = nil
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		f.Value = nil
		return nil
	}
	f.Value = &trimmed
	return nil
}

// OptionalJSONArrayField preserves omitted-vs-null semantics for array replacements.
type OptionalJSONArrayField struct {
	Set    bool
	Values []any
}

func (f *OptionalJSONArrayField) UnmarshalJSON(data []byte) error {
	f.Set = true
	if strings.TrimSpace(string(data)) == "null" {
		f.Values = []any{}
		return nil
	}
	var values []any
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	f.Values = values
	return nil
}

// OptionalValueField preserves whether an arbitrary JSON value was explicitly provided.
type OptionalValueField struct {
	Set   bool
	Value any
}

func (f *OptionalValueField) UnmarshalJSON(data []byte) error {
	f.Set = true
	if strings.TrimSpace(string(data)) == "null" {
		return errors.New("value must not be null")
	}
	return json.Unmarshal(data, &f.Value)
}

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

type Agent struct {
	Type        string             `json:"type"`
	ID          string             `json:"id"`
	Version     int                `json:"version"`
	Name        string             `json:"name"`
	Description *string            `json:"description"`
	Model       SessionModelConfig `json:"model"`
	System      *string            `json:"system"`
	Tools       []AgentTool        `json:"tools"`
	MCPServers  []MCPServer        `json:"mcp_servers"`
	Skills      []AgentSkill       `json:"skills"`
	Metadata    map[string]string  `json:"metadata"`
	CreatedAt   string             `json:"created_at"`
	UpdatedAt   string             `json:"updated_at"`
	ArchivedAt  *string            `json:"archived_at"`
}

type Environment struct {
	Type        string            `json:"type"`
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Config      CloudConfig       `json:"config"`
	Metadata    map[string]string `json:"metadata"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
	ArchivedAt  *string           `json:"archived_at"`
}

type Vault struct {
	Type        string            `json:"type"`
	ID          string            `json:"id"`
	DisplayName string            `json:"display_name"`
	Metadata    map[string]string `json:"metadata"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
	ArchivedAt  *string           `json:"archived_at"`
}

type Credential struct {
	Type        string            `json:"type"`
	ID          string            `json:"id"`
	VaultID     string            `json:"vault_id"`
	DisplayName *string           `json:"display_name"`
	Metadata    map[string]string `json:"metadata"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
	ArchivedAt  *string           `json:"archived_at"`
	Auth        CredentialAuth    `json:"auth"`
}

type CloudConfig struct {
	Type       string                `json:"type"`
	Networking EnvironmentNetworking `json:"networking"`
	Packages   EnvironmentPackages   `json:"packages"`
}

type EnvironmentNetworking struct {
	Type                 string   `json:"type"`
	AllowedHosts         []string `json:"allowed_hosts,omitempty"`
	AllowPackageManagers bool     `json:"allow_package_managers,omitempty"`
	AllowMCPServers      bool     `json:"allow_mcp_servers,omitempty"`
}

type EnvironmentPackages struct {
	Type  string   `json:"type"`
	Apt   []string `json:"apt"`
	Cargo []string `json:"cargo"`
	Gem   []string `json:"gem"`
	Go    []string `json:"go"`
	NPM   []string `json:"npm"`
	Pip   []string `json:"pip"`
}

type CredentialAuth struct {
	Type         string                  `json:"type"`
	MCPServerURL string                  `json:"mcp_server_url"`
	ExpiresAt    *string                 `json:"expires_at,omitempty"`
	Refresh      *CredentialOAuthRefresh `json:"refresh,omitempty"`
}

type CredentialOAuthRefresh struct {
	TokenEndpoint     string                      `json:"token_endpoint"`
	ClientID          string                      `json:"client_id"`
	Scope             *string                     `json:"scope,omitempty"`
	Resource          *string                     `json:"resource,omitempty"`
	TokenEndpointAuth CredentialTokenEndpointAuth `json:"token_endpoint_auth"`
}

type CredentialTokenEndpointAuth struct {
	Type string `json:"type"`
}

type FileScope struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type FileMetadata struct {
	Type         string     `json:"type"`
	ID           string     `json:"id"`
	Filename     string     `json:"filename"`
	MimeType     string     `json:"mime_type"`
	SizeBytes    int64      `json:"size_bytes"`
	Downloadable bool       `json:"downloadable"`
	Scope        *FileScope `json:"scope"`
	CreatedAt    string     `json:"created_at"`
}

type FileListResponse struct {
	Data    []FileMetadata `json:"data"`
	FirstID *string        `json:"first_id"`
	LastID  *string        `json:"last_id"`
	HasMore bool           `json:"has_more"`
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

func buildAgentObject(id string, version int, vendor string, req CreateAgentRequest, createdAt time.Time, archivedAt *time.Time) Agent {
	_, model := normalizeModelConfig(req.Model, vendor)
	return Agent{
		Type:        "agent",
		ID:          id,
		Version:     version,
		Name:        strings.TrimSpace(req.Name),
		Description: normalizeNullableString(req.Description),
		Model:       modelConfigFromMap(model),
		System:      normalizeNullableString(req.System),
		Tools:       agentToolsFromAny(req.Tools),
		MCPServers:  mcpServersFromAny(req.MCPServers),
		Skills:      agentSkillsFromAny(req.Skills),
		Metadata:    cloneStringMap(req.Metadata),
		CreatedAt:   nowRFC3339(createdAt),
		UpdatedAt:   nowRFC3339(createdAt),
		ArchivedAt:  timestampPointerOrNil(archivedAt),
	}
}

func buildEnvironmentObject(id string, req CreateEnvironmentRequest, now time.Time, archivedAt *time.Time) Environment {
	description := ""
	if req.Description != nil {
		description = strings.TrimSpace(*req.Description)
	}
	return Environment{
		Type:        "environment",
		ID:          id,
		Name:        strings.TrimSpace(req.Name),
		Description: description,
		Config:      environmentConfigFromMap(req.Config),
		Metadata:    cloneStringMap(req.Metadata),
		CreatedAt:   nowRFC3339(now),
		UpdatedAt:   nowRFC3339(now),
		ArchivedAt:  timestampPointerOrNil(archivedAt),
	}
}

func buildVaultObject(id string, req CreateVaultRequest, now time.Time, archivedAt *time.Time) Vault {
	return Vault{
		Type:        "vault",
		ID:          id,
		DisplayName: strings.TrimSpace(req.DisplayName),
		Metadata:    cloneStringMap(req.Metadata),
		CreatedAt:   nowRFC3339(now),
		UpdatedAt:   nowRFC3339(now),
		ArchivedAt:  timestampPointerOrNil(archivedAt),
	}
}

func buildCredentialObject(id, vaultID string, req CreateCredentialRequest, now time.Time, archivedAt *time.Time) Credential {
	return Credential{
		Type:        "vault_credential",
		ID:          id,
		VaultID:     vaultID,
		DisplayName: normalizeNullableString(req.DisplayName),
		Metadata:    cloneStringMap(req.Metadata),
		CreatedAt:   nowRFC3339(now),
		UpdatedAt:   nowRFC3339(now),
		ArchivedAt:  timestampPointerOrNil(archivedAt),
		Auth:        credentialAuthFromMap(redactCredentialAuth(req.Auth)),
	}
}

func buildFileObject(record *managedFileRecord) FileMetadata {
	if record == nil {
		return FileMetadata{}
	}
	var scope *FileScope
	if strings.TrimSpace(record.ScopeType) != "" && strings.TrimSpace(record.ScopeID) != "" {
		scope = &FileScope{Type: record.ScopeType, ID: record.ScopeID}
	}
	return FileMetadata{
		Type:         "file",
		ID:           record.ID,
		Filename:     record.Filename,
		MimeType:     record.MimeType,
		SizeBytes:    record.SizeBytes,
		Downloadable: true,
		Scope:        scope,
		CreatedAt:    nowRFC3339(record.CreatedAt),
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

func modelConfigFromMap(model map[string]any) SessionModelConfig {
	return SessionModelConfig{ID: stringValue(model["id"]), Speed: stringValue(model["speed"])}
}

func modelConfigToMap(model SessionModelConfig) map[string]any {
	result := map[string]any{"id": strings.TrimSpace(model.ID)}
	if speed := strings.TrimSpace(model.Speed); speed != "" {
		result["speed"] = speed
	}
	return result
}

func environmentConfigFromMap(config map[string]any) CloudConfig {
	packages := mapValue(config["packages"])
	networking := mapValue(config["networking"])
	return CloudConfig{
		Type: strings.TrimSpace(stringValue(config["type"])),
		Networking: EnvironmentNetworking{
			Type:                 strings.TrimSpace(stringValue(networking["type"])),
			AllowedHosts:         stringSliceFromAny(networking["allowed_hosts"]),
			AllowPackageManagers: boolValue(networking["allow_package_managers"]),
			AllowMCPServers:      boolValue(networking["allow_mcp_servers"]),
		},
		Packages: EnvironmentPackages{
			Type:  defaultIfEmpty(strings.TrimSpace(stringValue(packages["type"])), "packages"),
			Apt:   stringSliceFromAny(packages["apt"]),
			Cargo: stringSliceFromAny(packages["cargo"]),
			Gem:   stringSliceFromAny(packages["gem"]),
			Go:    stringSliceFromAny(packages["go"]),
			NPM:   stringSliceFromAny(packages["npm"]),
			Pip:   stringSliceFromAny(packages["pip"]),
		},
	}
}

func environmentConfigToMap(config CloudConfig) map[string]any {
	networking := map[string]any{"type": strings.TrimSpace(config.Networking.Type)}
	if networking["type"] == "limited" {
		networking["allowed_hosts"] = stringSliceValuesToAny(config.Networking.AllowedHosts)
		networking["allow_package_managers"] = config.Networking.AllowPackageManagers
		networking["allow_mcp_servers"] = config.Networking.AllowMCPServers
	}
	return map[string]any{
		"type":       strings.TrimSpace(config.Type),
		"networking": networking,
		"packages": map[string]any{
			"type":  defaultIfEmpty(strings.TrimSpace(config.Packages.Type), "packages"),
			"apt":   stringSliceValuesToAny(config.Packages.Apt),
			"cargo": stringSliceValuesToAny(config.Packages.Cargo),
			"gem":   stringSliceValuesToAny(config.Packages.Gem),
			"go":    stringSliceValuesToAny(config.Packages.Go),
			"npm":   stringSliceValuesToAny(config.Packages.NPM),
			"pip":   stringSliceValuesToAny(config.Packages.Pip),
		},
	}
}

func credentialAuthFromMap(auth map[string]any) CredentialAuth {
	refreshMap := mapValue(auth["refresh"])
	var refresh *CredentialOAuthRefresh
	if len(refreshMap) > 0 {
		refresh = &CredentialOAuthRefresh{
			TokenEndpoint: strings.TrimSpace(stringValue(refreshMap["token_endpoint"])),
			ClientID:      strings.TrimSpace(stringValue(refreshMap["client_id"])),
			Scope:         nullableStringFromAny(refreshMap["scope"]),
			Resource:      nullableStringFromAny(refreshMap["resource"]),
			TokenEndpointAuth: CredentialTokenEndpointAuth{
				Type: strings.TrimSpace(stringValue(mapValue(refreshMap["token_endpoint_auth"])["type"])),
			},
		}
	}
	return CredentialAuth{
		Type:         strings.TrimSpace(stringValue(auth["type"])),
		MCPServerURL: strings.TrimSpace(stringValue(auth["mcp_server_url"])),
		ExpiresAt:    nullableStringFromAny(auth["expires_at"]),
		Refresh:      refresh,
	}
}

func CredentialAuthFromMapForRuntime(auth map[string]any) CredentialAuth {
	return credentialAuthFromMap(auth)
}

func credentialAuthToMap(auth CredentialAuth) map[string]any {
	out := map[string]any{
		"type":           strings.TrimSpace(auth.Type),
		"mcp_server_url": strings.TrimSpace(auth.MCPServerURL),
	}
	if auth.ExpiresAt != nil && strings.TrimSpace(*auth.ExpiresAt) != "" {
		out["expires_at"] = strings.TrimSpace(*auth.ExpiresAt)
	}
	if auth.Refresh != nil {
		refresh := map[string]any{
			"token_endpoint": auth.Refresh.TokenEndpoint,
			"client_id":      auth.Refresh.ClientID,
			"token_endpoint_auth": map[string]any{
				"type": auth.Refresh.TokenEndpointAuth.Type,
			},
		}
		if auth.Refresh.Scope != nil && strings.TrimSpace(*auth.Refresh.Scope) != "" {
			refresh["scope"] = strings.TrimSpace(*auth.Refresh.Scope)
		}
		if auth.Refresh.Resource != nil && strings.TrimSpace(*auth.Refresh.Resource) != "" {
			refresh["resource"] = strings.TrimSpace(*auth.Refresh.Resource)
		}
		out["refresh"] = refresh
	}
	return out
}

func CredentialAuthToMapForRuntime(auth CredentialAuth) map[string]any {
	return credentialAuthToMap(auth)
}

func stringSliceFromAny(raw any) []string {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text := strings.TrimSpace(stringValue(item)); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func stringSliceValuesToAny(values []string) []any {
	if len(values) == 0 {
		return []any{}
	}
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, strings.TrimSpace(value))
	}
	return out
}

func defaultIfEmpty(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func boolValue(raw any) bool {
	value, _ := raw.(bool)
	return value
}

func timestampPointerOrNil(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := nowRFC3339(*value)
	return &formatted
}

func parseTimestampPointer(value *string) *time.Time {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*value))
	if err != nil {
		return nil
	}
	return &parsed
}

func agentToSnapshot(agent Agent) map[string]any {
	return valueToSnapshotMap(agent)
}

func environmentToSnapshot(environment Environment) map[string]any {
	return valueToSnapshotMap(environment)
}

func vaultToSnapshot(vault Vault) map[string]any {
	return valueToSnapshotMap(vault)
}

func credentialToSnapshot(credential Credential) map[string]any {
	return valueToSnapshotMap(credential)
}

func valueToSnapshotMap(value any) map[string]any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := json.Unmarshal(encoded, &out); err != nil {
		return map[string]any{}
	}
	return out
}
