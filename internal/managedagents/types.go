package managedagents

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Principal identifies the caller for session ownership checks.
type Principal struct {
	TeamID string
	UserID string
}

// RequestCredential carries the bearer or API key used to reach the regional gateway.
type RequestCredential struct {
	Token string
}

// Usage captures cumulative token usage for a session.
type Usage struct {
	InputTokens          int64               `json:"input_tokens,omitempty"`
	OutputTokens         int64               `json:"output_tokens,omitempty"`
	CacheReadInputTokens int64               `json:"cache_read_input_tokens,omitempty"`
	CacheCreation        *CacheCreationUsage `json:"cache_creation,omitempty"`
}

// CacheCreationUsage captures prompt-cache creation tokens by TTL bucket.
type CacheCreationUsage struct {
	Ephemeral1HInputTokens int64 `json:"ephemeral_1h_input_tokens,omitempty"`
	Ephemeral5MInputTokens int64 `json:"ephemeral_5m_input_tokens,omitempty"`
}

// Stats captures timing statistics for a session.
type Stats struct {
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	ActiveSeconds   float64 `json:"active_seconds,omitempty"`
}

type SessionModelConfig struct {
	ID    string `json:"id"`
	Speed string `json:"speed,omitempty"`
}

type PermissionPolicy struct {
	Type string `json:"type"`
}

type ToolsetDefaultConfig struct {
	Enabled          bool             `json:"enabled"`
	PermissionPolicy PermissionPolicy `json:"permission_policy"`
}

type AgentToolConfig struct {
	Name             string           `json:"name"`
	Enabled          bool             `json:"enabled"`
	PermissionPolicy PermissionPolicy `json:"permission_policy"`
}

type CustomToolInputSchema struct {
	Type       string         `json:"type,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
	Required   []string       `json:"required,omitempty"`
}

type AgentTool struct {
	Type          string                 `json:"type"`
	DefaultConfig *ToolsetDefaultConfig  `json:"default_config,omitempty"`
	Configs       []AgentToolConfig      `json:"configs,omitempty"`
	MCPServerName string                 `json:"mcp_server_name,omitempty"`
	Name          string                 `json:"name,omitempty"`
	Description   string                 `json:"description,omitempty"`
	InputSchema   *CustomToolInputSchema `json:"input_schema,omitempty"`
}

type MCPServer struct {
	Type string `json:"type"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type AgentSkill struct {
	Type    string `json:"type"`
	SkillID string `json:"skill_id"`
	Version string `json:"version"`
}

type SessionAgent struct {
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
}

type RepositoryCheckout struct {
	Type string  `json:"type"`
	Name *string `json:"name,omitempty"`
	SHA  *string `json:"sha,omitempty"`
}

type SessionResource struct {
	Type      string              `json:"type"`
	ID        string              `json:"id"`
	FileID    *string             `json:"file_id,omitempty"`
	URL       *string             `json:"url,omitempty"`
	MountPath string              `json:"mount_path"`
	Checkout  *RepositoryCheckout `json:"checkout,omitempty"`
	CreatedAt string              `json:"created_at"`
	UpdatedAt string              `json:"updated_at"`
}

// Session represents the public managed-agent session object.
type Session struct {
	Type          string            `json:"type"`
	ID            string            `json:"id"`
	Status        string            `json:"status"`
	CreatedAt     string            `json:"created_at"`
	UpdatedAt     string            `json:"updated_at"`
	EnvironmentID string            `json:"environment_id"`
	Title         *string           `json:"title"`
	Metadata      map[string]string `json:"metadata"`
	Agent         SessionAgent      `json:"agent"`
	Resources     []SessionResource `json:"resources"`
	VaultIDs      []string          `json:"vault_ids"`
	Usage         Usage             `json:"usage"`
	Stats         Stats             `json:"stats"`
	ArchivedAt    *string           `json:"archived_at"`
}

// CreateSessionParams is the public create-session request body.
type CreateSessionParams struct {
	Agent         any               `json:"agent"`
	EnvironmentID string            `json:"environment_id"`
	Title         *string           `json:"title"`
	Metadata      map[string]string `json:"metadata"`
	Resources     []map[string]any  `json:"resources"`
	VaultIDs      []string          `json:"vault_ids"`
}

// UpdateSessionParams is the public update-session request body.
type UpdateSessionParams struct {
	Title    NullableStringField `json:"title"`
	Metadata MetadataPatchField  `json:"metadata"`
	VaultIDs StringSliceField    `json:"vault_ids"`
}

// NullableStringField preserves omitted-vs-null semantics for update requests.
type NullableStringField struct {
	Set   bool
	Value *string
}

func (f *NullableStringField) UnmarshalJSON(data []byte) error {
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
		return errors.New("title must be a non-empty string")
	}
	f.Value = &trimmed
	return nil
}

// MetadataPatchField preserves omitted-vs-null semantics for metadata patch requests.
type MetadataPatchField struct {
	Set    bool
	Clear  bool
	Values map[string]*string
}

func (f *MetadataPatchField) UnmarshalJSON(data []byte) error {
	f.Set = true
	if strings.TrimSpace(string(data)) == "null" {
		f.Clear = true
		f.Values = nil
		return nil
	}
	var values map[string]*string
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	f.Values = values
	return nil
}

// StringSliceField preserves whether a slice field was explicitly provided.
type StringSliceField struct {
	Set    bool
	Values []string
}

func (f *StringSliceField) UnmarshalJSON(data []byte) error {
	f.Set = true
	if strings.TrimSpace(string(data)) == "null" {
		return errors.New("vault_ids must be an array")
	}
	var values []string
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	f.Values = values
	return nil
}

// SendEventsParams is the public send-events request body.
type SendEventsParams struct {
	Events []InputEvent `json:"events"`
}

// RuntimeCallbackPayload is the internal runtime event payload published by the wrapper.
type RuntimeCallbackPayload struct {
	SessionID       string         `json:"session_id"`
	RunID           string         `json:"run_id"`
	VendorSessionID string         `json:"vendor_session_id,omitempty"`
	UsageDelta      Usage          `json:"usage_delta,omitempty"`
	Events          []SessionEvent `json:"events"`
}

type runtimeWebhookJob struct {
	ID                string
	SessionID         string
	SandboxID         string
	RuntimeGeneration int64
	RunID             string
	EventType         string
	Payload           RuntimeCallbackPayload
	Status            string
	Attempts          int
	LeaseOwner        string
	LeaseExpiresAt    *time.Time
	LastError         string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type runtimeInputEventBatch struct {
	ID                 string
	SessionID          string
	EventIDs           []string
	RuntimeInputEvents []map[string]any
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// WrapperSessionBootstrapRequest provisions or refreshes wrapper-side runtime state.
type WrapperSessionBootstrapRequest struct {
	SessionID           string           `json:"session_id"`
	Vendor              string           `json:"vendor"`
	VendorSessionID     string           `json:"vendor_session_id,omitempty"`
	SandboxID           string           `json:"sandbox_id,omitempty"`
	CallbackURL         string           `json:"callback_url,omitempty"`
	ControlToken        string           `json:"control_token,omitempty"`
	WorkingDirectory    string           `json:"working_directory,omitempty"`
	EnvironmentID       string           `json:"environment_id"`
	Environment         map[string]any   `json:"environment,omitempty"`
	EnvironmentArtifact map[string]any   `json:"environment_artifact,omitempty"`
	Agent               map[string]any   `json:"agent"`
	Resources           []map[string]any `json:"resources,omitempty"`
	VaultIDs            []string         `json:"vault_ids,omitempty"`
	BootstrapEvents     []map[string]any `json:"bootstrap_events,omitempty"`
	SkillNames          []string         `json:"skill_names,omitempty"`
	Engine              map[string]any   `json:"engine,omitempty"`
}

// WrapperRunRequest starts a new wrapper-side turn.
type WrapperRunRequest struct {
	SessionID   string       `json:"session_id"`
	RunID       string       `json:"run_id"`
	InputEvents []InputEvent `json:"input_events"`
}

// WrapperResolveActionsRequest resolves runtime-blocking tool confirmations.
type WrapperResolveActionsRequest struct {
	SessionID string       `json:"session_id"`
	Events    []InputEvent `json:"events"`
}

type WrapperResolveActionsResponse struct {
	ResolvedCount      int      `json:"resolved_count"`
	RemainingActionIDs []string `json:"remaining_action_ids"`
	ResumeRequired     bool     `json:"resume_required,omitempty"`
}

type SessionRecord struct {
	ID                    string
	TeamID                string
	CreatedByUserID       string
	Vendor                string
	EnvironmentID         string
	EnvironmentArtifactID string
	WorkingDirectory      string
	Title                 *string
	Metadata              map[string]string
	Agent                 map[string]any
	Resources             []map[string]any
	VaultIDs              []string
	Status                string
	Usage                 Usage
	StatsActiveSeconds    float64
	LastStatusStartedAt   *time.Time
	ArchivedAt            *time.Time
	DeletedAt             *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type RuntimeRecord struct {
	SessionID             string
	Vendor                string
	RegionID              string
	SandboxID             string
	WrapperURL            string
	WorkspaceVolumeID     string
	WorkspaceBaseDigest   string
	WorkspaceBaseVolumeID string
	EnvironmentVolumeIDs  map[string]string
	ControlToken          string
	VendorSessionID       string
	RuntimeGeneration     int64
	ActiveRunID           *string
	SandboxDeletedAt      *time.Time
	BootstrapStateDigest  string
	BootstrapSyncedAt     *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type WorkspaceBaseRecord struct {
	ID            string
	TeamID        string
	Digest        string
	Status        string
	VolumeID      string
	InputSnapshot map[string]any
	FailureReason string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type TeamAssetStore struct {
	TeamID    string
	RegionID  string
	VolumeID  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type EnvironmentArtifact struct {
	ID             string
	TeamID         string
	EnvironmentID  string
	Digest         string
	Status         string
	ConfigSnapshot map[string]any
	Compatibility  map[string]any
	Assets         EnvironmentArtifactAssets
	BuildLog       string
	FailureReason  *string
	ArchivedAt     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type EnvironmentArtifactAssets struct {
	AptVolumeID   string `json:"apt_volume_id,omitempty"`
	CargoVolumeID string `json:"cargo_volume_id,omitempty"`
	GemVolumeID   string `json:"gem_volume_id,omitempty"`
	GoVolumeID    string `json:"go_volume_id,omitempty"`
	NPMVolumeID   string `json:"npm_volume_id,omitempty"`
	PipVolumeID   string `json:"pip_volume_id,omitempty"`
}

type ContentSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
	FileID    string `json:"file_id,omitempty"`
}

type UserContentBlock struct {
	Type    string         `json:"type"`
	Text    string         `json:"text,omitempty"`
	Source  *ContentSource `json:"source,omitempty"`
	Title   *string        `json:"title,omitempty"`
	Context *string        `json:"context,omitempty"`
}

type InputEvent struct {
	Type            string             `json:"type"`
	ID              string             `json:"id,omitempty"`
	Content         []UserContentBlock `json:"content,omitempty"`
	ToolUseID       string             `json:"tool_use_id,omitempty"`
	Result          string             `json:"result,omitempty"`
	DenyMessage     *string            `json:"deny_message,omitempty"`
	CustomToolUseID string             `json:"custom_tool_use_id,omitempty"`
	IsError         *bool              `json:"is_error,omitempty"`
	ProcessedAt     *string            `json:"processed_at,omitempty"`
}

type SessionStopReason struct {
	Type     string   `json:"type"`
	EventIDs []string `json:"event_ids,omitempty"`
}

type SessionErrorDetail struct {
	Type          string `json:"type"`
	Message       string `json:"message,omitempty"`
	RetryStatus   any    `json:"retry_status,omitempty"`
	MCPServerName string `json:"mcp_server_name,omitempty"`
}

type SpanModelUsage struct {
	InputTokens              int64   `json:"input_tokens"`
	OutputTokens             int64   `json:"output_tokens"`
	CacheCreationInputTokens int64   `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64   `json:"cache_read_input_tokens"`
	Speed                    *string `json:"speed,omitempty"`
}

type SessionEvent struct {
	Type                string              `json:"type"`
	ID                  string              `json:"id,omitempty"`
	ProcessedAt         *string             `json:"processed_at,omitempty"`
	Content             []UserContentBlock  `json:"content,omitempty"`
	Name                string              `json:"name,omitempty"`
	Input               map[string]any      `json:"input,omitempty"`
	MCPServerName       string              `json:"mcp_server_name,omitempty"`
	EvaluatedPermission string              `json:"evaluated_permission,omitempty"`
	ToolUseID           string              `json:"tool_use_id,omitempty"`
	MCPToolUseID        string              `json:"mcp_tool_use_id,omitempty"`
	CustomToolUseID     string              `json:"custom_tool_use_id,omitempty"`
	IsError             *bool               `json:"is_error,omitempty"`
	StopReason          *SessionStopReason  `json:"stop_reason,omitempty"`
	Error               *SessionErrorDetail `json:"error,omitempty"`
	ModelUsage          *SpanModelUsage     `json:"model_usage,omitempty"`
	ModelRequestStartID string              `json:"model_request_start_id,omitempty"`
}

func inputEventsToMaps(events []InputEvent) []map[string]any {
	if len(events) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(events))
	for _, event := range events {
		mapped := valueToSnapshotMap(event)
		if mapped == nil {
			mapped = map[string]any{}
		}
		out = append(out, mapped)
	}
	return out
}

func inputEventsFromMaps(events []map[string]any) []InputEvent {
	if len(events) == 0 {
		return []InputEvent{}
	}
	out := make([]InputEvent, 0, len(events))
	for _, event := range events {
		out = append(out, inputEventFromMap(event))
	}
	return out
}

func sessionEventsToMaps(events []SessionEvent) []map[string]any {
	if len(events) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(events))
	for _, event := range events {
		mapped := valueToSnapshotMap(event)
		if mapped == nil {
			mapped = map[string]any{}
		}
		out = append(out, mapped)
	}
	return out
}

func inputEventFromMap(event map[string]any) InputEvent {
	mapped := cloneMap(event)
	result := InputEvent{
		Type:            stringValue(mapped["type"]),
		ID:              stringValue(mapped["id"]),
		ToolUseID:       stringValue(mapped["tool_use_id"]),
		Result:          stringValue(mapped["result"]),
		DenyMessage:     nullableStringFromAny(mapped["deny_message"]),
		CustomToolUseID: stringValue(mapped["custom_tool_use_id"]),
		IsError:         nullableBoolFromAny(mapped["is_error"]),
		ProcessedAt:     nullableStringFromAny(mapped["processed_at"]),
	}
	if content, ok := mapped["content"].([]any); ok {
		result.Content = userContentBlocksFromAny(content)
	}
	return result
}

func userContentBlocksFromAny(items []any) []UserContentBlock {
	if len(items) == 0 {
		return []UserContentBlock{}
	}
	out := make([]UserContentBlock, 0, len(items))
	for _, item := range items {
		block := mapValue(item)
		result := UserContentBlock{
			Type:    stringValue(block["type"]),
			Text:    stringValue(block["text"]),
			Title:   nullableStringFromAny(block["title"]),
			Context: nullableStringFromAny(block["context"]),
		}
		if source := contentSourceFromAny(block["source"]); source != nil {
			result.Source = source
		}
		out = append(out, result)
	}
	return out
}

func contentSourceFromAny(raw any) *ContentSource {
	source := mapValue(raw)
	if len(source) == 0 {
		return nil
	}
	return &ContentSource{
		Type:      stringValue(source["type"]),
		MediaType: stringValue(source["media_type"]),
		Data:      stringValue(source["data"]),
		URL:       stringValue(source["url"]),
		FileID:    stringValue(source["file_id"]),
	}
}

func nullableBoolFromAny(raw any) *bool {
	value, ok := raw.(bool)
	if !ok {
		return nil
	}
	return &value
}

func (r *SessionRecord) toAPI(now time.Time) *Session {
	if r == nil {
		return nil
	}
	updatedAt := r.UpdatedAt.UTC().Format(time.RFC3339)
	createdAt := r.CreatedAt.UTC().Format(time.RFC3339)
	duration := r.UpdatedAt.Sub(r.CreatedAt).Seconds()
	if r.Status != "terminated" {
		duration = now.Sub(r.CreatedAt).Seconds()
	}
	stats := Stats{
		DurationSeconds: duration,
		ActiveSeconds:   r.StatsActiveSeconds,
	}
	if r.Status == "running" && r.LastStatusStartedAt != nil {
		stats.ActiveSeconds += now.Sub(r.LastStatusStartedAt.UTC()).Seconds()
	}
	var archivedAt *string
	if r.ArchivedAt != nil {
		value := r.ArchivedAt.UTC().Format(time.RFC3339)
		archivedAt = &value
	}
	return &Session{
		Type:          "session",
		ID:            r.ID,
		Status:        r.Status,
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
		EnvironmentID: r.EnvironmentID,
		Title:         r.Title,
		Metadata:      cloneStringMap(r.Metadata),
		Agent:         sessionAgentFromSnapshot(r.Agent),
		Resources:     sessionResourcesFromSnapshots(r.Resources),
		VaultIDs:      append([]string(nil), r.VaultIDs...),
		Usage:         normalizeUsageValue(r.Usage),
		Stats:         stats,
		ArchivedAt:    archivedAt,
	}
}

func normalizeAgentSnapshot(agent any, explicitVendor string) (string, map[string]any) {
	vendor := strings.TrimSpace(explicitVendor)
	if vendor == "" {
		vendor = "claude"
	}
	switch value := agent.(type) {
	case string:
		return vendor, map[string]any{
			"type":        "agent",
			"id":          value,
			"version":     1,
			"name":        value,
			"description": nil,
			"model": map[string]any{
				"id":    defaultClaudeModel,
				"speed": "standard",
			},
			"system":      nil,
			"tools":       []any{},
			"mcp_servers": []any{},
			"skills":      []any{},
		}
	case map[string]any:
		copyValue := cloneMap(value)
		copyValue["type"] = "agent"
		if _, ok := copyValue["version"]; !ok {
			copyValue["version"] = 1
		}
		if _, ok := copyValue["name"]; !ok {
			copyValue["name"] = stringValue(copyValue["id"])
		}
		if _, ok := copyValue["description"]; !ok {
			copyValue["description"] = nil
		}
		if _, ok := copyValue["system"]; !ok {
			copyValue["system"] = nil
		}
		if _, ok := copyValue["tools"]; !ok {
			copyValue["tools"] = []any{}
		}
		if _, ok := copyValue["mcp_servers"]; !ok {
			copyValue["mcp_servers"] = []any{}
		}
		if _, ok := copyValue["skills"]; !ok {
			copyValue["skills"] = []any{}
		}
		if model, ok := copyValue["model"].(string); ok {
			copyValue["model"] = map[string]any{"id": model, "speed": "standard"}
		}
		if _, ok := copyValue["model"]; !ok {
			copyValue["model"] = map[string]any{"id": defaultClaudeModel, "speed": "standard"}
		}
		return vendor, copyValue
	default:
		return vendor, map[string]any{
			"type":        "agent",
			"id":          NewID("agent"),
			"version":     1,
			"name":        "sandbox0 managed agent",
			"description": nil,
			"model": map[string]any{
				"id":    defaultClaudeModel,
				"speed": "standard",
			},
			"system":      nil,
			"tools":       []any{},
			"mcp_servers": []any{},
			"skills":      []any{},
		}
	}
}

const defaultClaudeModel = "claude-sonnet-4-20250514"

func ensureResourceIDs(resources []map[string]any) []map[string]any {
	for i := range resources {
		if strings.TrimSpace(stringValue(resources[i]["id"])) == "" {
			resources[i]["id"] = NewID("rsc")
		}
	}
	return resources
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
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

func normalizeUsageValue(usage Usage) Usage {
	if usage.CacheCreation != nil && usage.CacheCreation.Ephemeral1HInputTokens == 0 && usage.CacheCreation.Ephemeral5MInputTokens == 0 {
		usage.CacheCreation = nil
	}
	return usage
}

func cloneMapSlice(in []map[string]any) []map[string]any {
	if len(in) == 0 {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(in))
	for _, entry := range in {
		out = append(out, cloneMap(entry))
	}
	return out
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func sessionAgentFromSnapshot(snapshot map[string]any) SessionAgent {
	copyValue := cloneMap(snapshot)
	model := mapValue(copyValue["model"])
	return SessionAgent{
		Type:        "agent",
		ID:          stringValue(copyValue["id"]),
		Version:     intValue(copyValue["version"]),
		Name:        stringValue(copyValue["name"]),
		Description: nullableStringFromAny(copyValue["description"]),
		Model: SessionModelConfig{
			ID:    stringValue(model["id"]),
			Speed: stringValue(model["speed"]),
		},
		System:     nullableStringFromAny(copyValue["system"]),
		Tools:      agentToolsFromAny(copyValue["tools"]),
		MCPServers: mcpServersFromAny(copyValue["mcp_servers"]),
		Skills:     agentSkillsFromAny(copyValue["skills"]),
	}
}

func agentToolsFromAny(raw any) []AgentTool {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return []AgentTool{}
	}
	out := make([]AgentTool, 0, len(items))
	for _, item := range items {
		tool := mapValue(item)
		resolved := AgentTool{Type: stringValue(tool["type"])}
		if defaultConfig := toolsetDefaultConfigFromMap(mapValue(tool["default_config"])); defaultConfig != nil {
			resolved.DefaultConfig = defaultConfig
		}
		resolved.Configs = toolConfigsFromAny(tool["configs"])
		resolved.MCPServerName = stringValue(tool["mcp_server_name"])
		resolved.Name = stringValue(tool["name"])
		resolved.Description = stringValue(tool["description"])
		if inputSchema := customToolInputSchemaFromMap(mapValue(tool["input_schema"])); inputSchema != nil {
			resolved.InputSchema = inputSchema
		}
		out = append(out, resolved)
	}
	return out
}

func toolsetDefaultConfigFromMap(value map[string]any) *ToolsetDefaultConfig {
	if len(value) == 0 {
		return nil
	}
	return &ToolsetDefaultConfig{
		Enabled: boolValue(value["enabled"]),
		PermissionPolicy: PermissionPolicy{
			Type: stringValue(mapValue(value["permission_policy"])["type"]),
		},
	}
}

func toolConfigsFromAny(raw any) []AgentToolConfig {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return []AgentToolConfig{}
	}
	out := make([]AgentToolConfig, 0, len(items))
	for _, item := range items {
		config := mapValue(item)
		out = append(out, AgentToolConfig{
			Name:    stringValue(config["name"]),
			Enabled: boolValue(config["enabled"]),
			PermissionPolicy: PermissionPolicy{
				Type: stringValue(mapValue(config["permission_policy"])["type"]),
			},
		})
	}
	return out
}

func customToolInputSchemaFromMap(value map[string]any) *CustomToolInputSchema {
	if len(value) == 0 {
		return nil
	}
	return &CustomToolInputSchema{
		Type:       stringValue(value["type"]),
		Properties: cloneMap(mapValue(value["properties"])),
		Required:   stringSliceFromAny(value["required"]),
	}
}

func mcpServersFromAny(raw any) []MCPServer {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return []MCPServer{}
	}
	out := make([]MCPServer, 0, len(items))
	for _, item := range items {
		server := mapValue(item)
		out = append(out, MCPServer{Type: stringValue(server["type"]), Name: stringValue(server["name"]), URL: stringValue(server["url"])})
	}
	return out
}

func agentSkillsFromAny(raw any) []AgentSkill {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return []AgentSkill{}
	}
	out := make([]AgentSkill, 0, len(items))
	for _, item := range items {
		skill := mapValue(item)
		out = append(out, AgentSkill{Type: stringValue(skill["type"]), SkillID: stringValue(skill["skill_id"]), Version: stringValue(skill["version"])})
	}
	return out
}

func valueToJSONArray(value any) []any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []any{}
	}
	var out []any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return []any{}
	}
	if len(out) == 0 {
		return []any{}
	}
	return out
}

func sessionResourcesFromSnapshots(resources []map[string]any) []SessionResource {
	if len(resources) == 0 {
		return []SessionResource{}
	}
	out := make([]SessionResource, 0, len(resources))
	for _, resource := range resources {
		out = append(out, sessionResourceFromSnapshot(resource))
	}
	return out
}

func sessionResourceFromSnapshot(snapshot map[string]any) SessionResource {
	copyValue := cloneMap(snapshot)
	resource := SessionResource{
		Type:      stringValue(copyValue["type"]),
		ID:        stringValue(copyValue["id"]),
		MountPath: stringValue(copyValue["mount_path"]),
		CreatedAt: stringValue(copyValue["created_at"]),
		UpdatedAt: stringValue(copyValue["updated_at"]),
	}
	if fileID := nullableStringFromAny(copyValue["file_id"]); fileID != nil {
		resource.FileID = fileID
	}
	if rawURL := nullableStringFromAny(copyValue["url"]); rawURL != nil {
		resource.URL = rawURL
	}
	if checkout := repositoryCheckoutFromSnapshot(copyValue["checkout"]); checkout != nil {
		resource.Checkout = checkout
	}
	return resource
}

func repositoryCheckoutFromSnapshot(value any) *RepositoryCheckout {
	checkout := mapValue(value)
	if len(checkout) == 0 {
		return nil
	}
	result := &RepositoryCheckout{Type: stringValue(checkout["type"])}
	if name := nullableStringFromAny(checkout["name"]); name != nil {
		result.Name = name
	}
	if sha := nullableStringFromAny(checkout["sha"]); sha != nil {
		result.SHA = sha
	}
	return result
}

func cloneJSONArray(value any) []any {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return []any{}
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return []any{}
	}
	var out []any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return []any{}
	}
	return out
}

func nullableStringFromAny(value any) *string {
	text := strings.TrimSpace(stringValue(value))
	if text == "" {
		return nil
	}
	return &text
}

func mapValue(value any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	if typed, ok := value.(map[string]any); ok {
		return cloneMap(typed)
	}
	return map[string]any{}
}

// NewID returns a prefixed random identifier.
func NewID(prefix string) string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return prefix + "_fallback"
	}
	return prefix + "_" + hex.EncodeToString(raw[:])
}
