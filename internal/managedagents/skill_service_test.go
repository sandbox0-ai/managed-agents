package managedagents

import (
	"context"
	"strings"
	"testing"
)

func TestListAnthropicSkillsReturnsEmpty(t *testing.T) {
	service := NewService(&Repository{}, nil, nil)
	items, nextPage, hasMore, err := service.ListSkills(t.Context(), Principal{TeamID: "team_123"}, 2, "", "anthropic")
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0", len(items))
	}
	if hasMore || nextPage != nil {
		t.Fatalf("hasMore/nextPage = %v/%v, want false/nil", hasMore, nextPage)
	}
}

func TestNormalizeAgentSkillsRejectsAnthropicPrebuiltSkill(t *testing.T) {
	service := NewService(&Repository{}, nil, nil)
	_, err := service.normalizeAgentSkills(t.Context(), Principal{TeamID: "team_123"}, []any{map[string]any{
		"type":     "anthropic",
		"skill_id": "xlsx",
	}})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("error = %v, want unsupported anthropic pre-built skill", err)
	}
}

func TestListSkillsWithoutSourceIncludesOnlyCustomSkills(t *testing.T) {
	repo := newTestRepository(t)
	service := NewService(repo, noopRuntimeManager{}, nil)
	principal := Principal{TeamID: "team_123"}
	_, err := service.CreateSkill(context.Background(), principal, nil, []uploadedSkillFile{{
		Path:    "demo-skill/SKILL.md",
		Content: []byte("---\nname: demo-skill\ndescription: >-\n  Demo skill\n---\n\n# Demo Skill\n"),
	}})
	if err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}
	items, nextPage, hasMore, err := service.ListSkills(context.Background(), principal, 5, "", "")
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if got := items[0].Source; got != "custom" {
		t.Fatalf("first source = %q, want custom", got)
	}
	if hasMore && nextPage == nil {
		t.Fatal("expected next_page when has_more is true")
	}
}
