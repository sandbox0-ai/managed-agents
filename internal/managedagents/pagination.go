package managedagents

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type FileListOptions struct {
	Limit    int
	ScopeID  string
	BeforeID string
	AfterID  string
}

type TimeFilter struct {
	GTE *time.Time
	GT  *time.Time
	LTE *time.Time
	LT  *time.Time
}

type SessionListOptions struct {
	Limit           int
	Page            string
	Order           string
	IncludeArchived bool
	AgentID         string
	AgentVersion    int
	CreatedAt       TimeFilter
}

type EventListOptions struct {
	Limit int
	Page  string
	Order string
}

type AgentListOptions struct {
	Limit           int
	Page            string
	Order           string
	IncludeArchived bool
	CreatedAt       TimeFilter
}

type pageCursor struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
	Position  int64  `json:"position,omitempty"`
}

func normalizeListOrder(order string) string {
	if strings.EqualFold(strings.TrimSpace(order), "asc") {
		return "asc"
	}
	return "desc"
}

func decodePageCursor(raw string) (*pageCursor, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "page_") {
		trimmed = strings.TrimPrefix(trimmed, "page_")
	}
	decoded, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return nil, errors.New("invalid page cursor")
	}
	var cursor pageCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil {
		return nil, errors.New("invalid page cursor")
	}
	if strings.TrimSpace(cursor.ID) == "" || strings.TrimSpace(cursor.CreatedAt) == "" {
		return nil, errors.New("invalid page cursor")
	}
	if _, err := time.Parse(time.RFC3339, cursor.CreatedAt); err != nil {
		return nil, errors.New("invalid page cursor")
	}
	return &cursor, nil
}

func encodePageCursor(createdAt time.Time, id string) *string {
	if strings.TrimSpace(id) == "" || createdAt.IsZero() {
		return nil
	}
	payload, err := json.Marshal(pageCursor{CreatedAt: createdAt.UTC().Format(time.RFC3339), ID: id})
	if err != nil {
		return nil
	}
	value := "page_" + base64.StdEncoding.EncodeToString(payload)
	return &value
}

func encodePositionPageCursor(createdAt time.Time, id string, position int64) *string {
	if position <= 0 {
		return encodePageCursor(createdAt, id)
	}
	if strings.TrimSpace(id) == "" || createdAt.IsZero() {
		return nil
	}
	payload, err := json.Marshal(pageCursor{CreatedAt: createdAt.UTC().Format(time.RFC3339), ID: id, Position: position})
	if err != nil {
		return nil
	}
	value := "page_" + base64.StdEncoding.EncodeToString(payload)
	return &value
}

func parseTimestampQuery(value string) (*time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return nil, errors.New("invalid timestamp")
	}
	return &parsed, nil
}
