package managedagents

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

const combinedSkillCursorPrefix = "skills:"

type combinedSkillCursor struct {
	Phase  string `json:"phase"`
	Cursor string `json:"cursor,omitempty"`
}

func (s *Service) listAllSkills(ctx context.Context, principal Principal, limit int, page string) ([]Skill, *string, bool, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	cursor, err := decodeCombinedSkillCursor(page)
	if err != nil {
		return nil, nil, false, err
	}
	phase := "custom"
	phaseCursor := ""
	if cursor != nil {
		phase = cursor.Phase
		phaseCursor = cursor.Cursor
	}
	switch phase {
	case "custom":
		items, nextPage, hasMore, err := s.repo.ListSkills(ctx, principal.TeamID, limit, phaseCursor, "custom")
		if err != nil {
			return nil, nil, false, err
		}
		if hasMore && nextPage != nil {
			return items, encodeCombinedSkillCursor("custom", *nextPage), true, nil
		}
		remaining := limit - len(items)
		if remaining <= 0 {
			probe, _, anthropicHasMore, err := s.anthropicSkills.ListSkills(ctx, 1, "")
			if err != nil {
				return nil, nil, false, err
			}
			if anthropicHasMore || len(probe) > 0 {
				return items, encodeCombinedSkillCursor("anthropic", ""), true, nil
			}
			return items, nil, false, nil
		}
		anthropicItems, anthropicNext, anthropicHasMore, err := s.anthropicSkills.ListSkills(ctx, remaining, "")
		if err != nil {
			return nil, nil, false, err
		}
		merged := append(items, anthropicItems...)
		if anthropicHasMore && anthropicNext != nil {
			return merged, encodeCombinedSkillCursor("anthropic", *anthropicNext), true, nil
		}
		return merged, nil, false, nil
	case "anthropic":
		items, nextPage, hasMore, err := s.anthropicSkills.ListSkills(ctx, limit, phaseCursor)
		if err != nil {
			return nil, nil, false, err
		}
		if hasMore && nextPage != nil {
			return items, encodeCombinedSkillCursor("anthropic", *nextPage), true, nil
		}
		return items, nil, false, nil
	default:
		return nil, nil, false, errors.New("invalid page cursor")
	}
}

func encodeCombinedSkillCursor(phase, cursor string) *string {
	payload, _ := json.Marshal(combinedSkillCursor{Phase: phase, Cursor: cursor})
	encoded := combinedSkillCursorPrefix + base64.RawURLEncoding.EncodeToString(payload)
	return &encoded
}

func decodeCombinedSkillCursor(raw string) (*combinedSkillCursor, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	if !strings.HasPrefix(trimmed, combinedSkillCursorPrefix) {
		return nil, errors.New("invalid page cursor")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(trimmed, combinedSkillCursorPrefix))
	if err != nil {
		return nil, errors.New("invalid page cursor")
	}
	var cursor combinedSkillCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return nil, errors.New("invalid page cursor")
	}
	cursor.Phase = strings.TrimSpace(cursor.Phase)
	if cursor.Phase != "custom" && cursor.Phase != "anthropic" {
		return nil, errors.New("invalid page cursor")
	}
	return &cursor, nil
}
