package managedagents

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	InputTokens          int64 `json:"input_tokens,omitempty"`
	OutputTokens         int64 `json:"output_tokens,omitempty"`
	CacheReadInputTokens int64 `json:"cache_read_input_tokens,omitempty"`
}

// Stats captures timing statistics for a session.
type Stats struct {
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	ActiveSeconds   float64 `json:"active_seconds,omitempty"`
}

// Session represents the public managed-agent session object.
type Session struct {
	Type             string            `json:"type"`
	ID               string            `json:"id"`
	Status           string            `json:"status"`
	CreatedAt        string            `json:"created_at"`
	UpdatedAt        string            `json:"updated_at"`
	EnvironmentID    string            `json:"environment_id"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
	Title            *string           `json:"title"`
	Metadata         map[string]string `json:"metadata"`
	Agent            map[string]any    `json:"agent"`
	Resources        []map[string]any  `json:"resources"`
	VaultIDs         []string          `json:"vault_ids"`
	Usage            Usage             `json:"usage"`
	Stats            Stats             `json:"stats"`
	ArchivedAt       *string           `json:"archived_at"`
}

// CreateSessionParams is the public create-session request body.
type CreateSessionParams struct {
	Agent            any               `json:"agent"`
	EnvironmentID    string            `json:"environment_id"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
	Title            *string           `json:"title"`
	Metadata         map[string]string `json:"metadata"`
	Resources        []map[string]any  `json:"resources"`
	VaultIDs         []string          `json:"vault_ids"`
	Vendor           string            `json:"vendor,omitempty"`
	Engine           map[string]any    `json:"engine,omitempty"`
}

// UpdateSessionParams is the public update-session request body.
type UpdateSessionParams struct {
	Title            *string           `json:"title"`
	Metadata         map[string]string `json:"metadata"`
	WorkingDirectory *string           `json:"working_directory,omitempty"`
}

// SendEventsParams is the public send-events request body.
type SendEventsParams struct {
	Events []map[string]any `json:"events"`
}

// RuntimeCallbackPayload is the internal runtime event payload published by the wrapper.
type RuntimeCallbackPayload struct {
	SessionID       string           `json:"session_id"`
	RunID           string           `json:"run_id"`
	VendorSessionID string           `json:"vendor_session_id,omitempty"`
	UsageDelta      Usage            `json:"usage_delta,omitempty"`
	Events          []map[string]any `json:"events"`
}

// WrapperSessionBootstrapRequest provisions or refreshes wrapper-side runtime state.
type WrapperSessionBootstrapRequest struct {
	SessionID        string           `json:"session_id"`
	Vendor           string           `json:"vendor"`
	VendorSessionID  string           `json:"vendor_session_id,omitempty"`
	WorkingDirectory string           `json:"working_directory,omitempty"`
	EnvironmentID    string           `json:"environment_id"`
	Agent            map[string]any   `json:"agent"`
	Resources        []map[string]any `json:"resources,omitempty"`
	VaultIDs         []string         `json:"vault_ids,omitempty"`
	Engine           map[string]any   `json:"engine,omitempty"`
}

// WrapperRunRequest starts a new wrapper-side turn.
type WrapperRunRequest struct {
	SessionID   string           `json:"session_id"`
	RunID       string           `json:"run_id"`
	InputEvents []map[string]any `json:"input_events"`
}

// WrapperResolveActionsRequest resolves runtime-blocking tool confirmations.
type WrapperResolveActionsRequest struct {
	SessionID string           `json:"session_id"`
	Events    []map[string]any `json:"events"`
}

type WrapperResolveActionsResponse struct {
	ResolvedCount      int      `json:"resolved_count"`
	RemainingActionIDs []string `json:"remaining_action_ids"`
	ResumeRequired     bool     `json:"resume_required,omitempty"`
}

type SessionRecord struct {
	ID                  string
	TeamID              string
	CreatedByUserID     string
	Vendor              string
	EnvironmentID       string
	WorkingDirectory    string
	Title               *string
	Metadata            map[string]string
	Agent               map[string]any
	Resources           []map[string]any
	VaultIDs            []string
	Status              string
	Usage               Usage
	StatsActiveSeconds  float64
	LastStatusStartedAt *time.Time
	ArchivedAt          *time.Time
	DeletedAt           *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type RuntimeRecord struct {
	SessionID           string
	Vendor              string
	RegionID            string
	SandboxID           string
	WrapperURL          string
	WorkspaceVolumeID   string
	EngineStateVolumeID string
	ControlToken        string
	VendorSessionID     string
	RuntimeGeneration   int64
	ActiveRunID         *string
	CreatedAt           time.Time
	UpdatedAt           time.Time
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
		Type:             "session",
		ID:               r.ID,
		Status:           r.Status,
		CreatedAt:        createdAt,
		UpdatedAt:        updatedAt,
		EnvironmentID:    r.EnvironmentID,
		WorkingDirectory: r.WorkingDirectory,
		Title:            r.Title,
		Metadata:         cloneStringMap(r.Metadata),
		Agent:            cloneMap(r.Agent),
		Resources:        cloneMapSlice(r.Resources),
		VaultIDs:         append([]string(nil), r.VaultIDs...),
		Usage:            r.Usage,
		Stats:            stats,
		ArchivedAt:       archivedAt,
	}
}

func normalizeAgentSnapshot(agent any, explicitVendor string) (string, map[string]any) {
	vendor := strings.TrimSpace(explicitVendor)
	switch value := agent.(type) {
	case string:
		if vendor == "" {
			vendor = inferVendorFromAgentID(value)
		}
		return vendor, map[string]any{
			"type":        "agent",
			"id":          value,
			"version":     1,
			"name":        value,
			"description": nil,
			"model": map[string]any{
				"id":    defaultModelForVendor(vendor),
				"speed": "standard",
			},
			"system":      nil,
			"tools":       []any{},
			"mcp_servers": []any{},
			"skills":      []any{},
		}
	case map[string]any:
		copyValue := cloneMap(value)
		if vendor == "" {
			if model, ok := copyValue["model"].(string); ok {
				vendor = inferVendorFromModel(model)
			} else if model, ok := copyValue["model"].(map[string]any); ok {
				vendor = inferVendorFromModel(stringValue(model["id"]))
			}
		}
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
			copyValue["model"] = map[string]any{"id": defaultModelForVendor(vendor), "speed": "standard"}
		}
		return vendor, copyValue
	default:
		if vendor == "" {
			vendor = "claude"
		}
		return vendor, map[string]any{
			"type":        "agent",
			"id":          NewID("agent"),
			"version":     1,
			"name":        "sandbox0 managed agent",
			"description": nil,
			"model": map[string]any{
				"id":    defaultModelForVendor(vendor),
				"speed": "standard",
			},
			"system":      nil,
			"tools":       []any{},
			"mcp_servers": []any{},
			"skills":      []any{},
		}
	}
}

func inferVendorFromAgentID(id string) string {
	trimmed := strings.ToLower(strings.TrimSpace(id))
	if strings.Contains(trimmed, "codex") || strings.HasPrefix(trimmed, "oai_") {
		return "codex"
	}
	return "claude"
}

func inferVendorFromModel(model string) string {
	trimmed := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(trimmed, "gpt") || strings.Contains(trimmed, "codex") || strings.Contains(trimmed, "o4") {
		return "codex"
	}
	return "claude"
}

func defaultModelForVendor(vendor string) string {
	if strings.EqualFold(strings.TrimSpace(vendor), "codex") {
		return "gpt-5-codex"
	}
	return "claude-sonnet-4-20250514"
}

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

// NewID returns a prefixed random identifier.
func NewID(prefix string) string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return prefix + "_fallback"
	}
	return prefix + "_" + hex.EncodeToString(raw[:])
}
