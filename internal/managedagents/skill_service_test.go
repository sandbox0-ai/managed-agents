package managedagents

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicSkillRegistryListSkills(t *testing.T) {
	service := NewService(&Repository{}, nil, nil)
	items, nextPage, hasMore, err := service.ListSkills(t.Context(), Principal{TeamID: "team_123"}, 2, "", "anthropic")
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	if !hasMore || nextPage == nil {
		t.Fatal("expected anthropic skills pagination")
	}
	if got := items[0].Source; got != "anthropic" {
		t.Fatalf("source = %q, want anthropic", got)
	}
	if got := items[0].ID; got == "" {
		t.Fatal("expected skill id")
	}
}

func TestAnthropicSkillRegistryGetSkillVersionDefaultsToLatest(t *testing.T) {
	service := NewService(&Repository{}, nil, nil)
	version, err := service.GetSkillVersion(t.Context(), Principal{TeamID: "team_123"}, "xlsx", "")
	if err != nil {
		t.Fatalf("GetSkillVersion: %v", err)
	}
	if got := version.Version; got != "1" {
		t.Fatalf("version = %q, want 1", got)
	}
	if got := version.Directory; got != "xlsx" {
		t.Fatalf("directory = %q, want xlsx", got)
	}
}

func TestAnthropicSkillRegistryGetSkillVersionAcceptsLatestAlias(t *testing.T) {
	service := NewService(&Repository{}, nil, nil)
	version, err := service.GetSkillVersion(t.Context(), Principal{TeamID: "team_123"}, "xlsx", "latest")
	if err != nil {
		t.Fatalf("GetSkillVersion: %v", err)
	}
	if got := version.Version; got != "1" {
		t.Fatalf("version = %q, want 1", got)
	}
	if got := version.ID; got != "skillver_anthropic_xlsx_1" {
		t.Fatalf("id = %q, want stable anthropic skill version id", got)
	}
}

func TestNormalizeAgentSkillsRejectsUnknownAnthropicSkill(t *testing.T) {
	service := NewService(&Repository{}, nil, nil)
	_, err := service.normalizeAgentSkills(t.Context(), Principal{TeamID: "team_123"}, []any{map[string]any{
		"type":     "anthropic",
		"skill_id": "unknown-skill",
	}})
	if err == nil {
		t.Fatal("expected unknown anthropic skill error")
	}
}

func TestListSkillsWithoutSourceIncludesCustomAndAnthropic(t *testing.T) {
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
	if len(items) < 2 {
		t.Fatalf("len(items) = %d, want at least 2", len(items))
	}
	if got := items[0].Source; got != "custom" {
		t.Fatalf("first source = %q, want custom", got)
	}
	foundAnthropic := false
	for _, item := range items {
		if item.Source == "anthropic" {
			foundAnthropic = true
			break
		}
	}
	if !foundAnthropic {
		t.Fatal("expected anthropic skill in merged results")
	}
	if hasMore && nextPage == nil {
		t.Fatal("expected next_page when has_more is true")
	}
}

func TestAnthropicRemoteSkillCatalogListSkills(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Api-Key"); got != "test-key" {
			t.Fatalf("x-api-key = %q, want test-key", got)
		}
		if got := r.Header.Get("Anthropic-Beta"); got != managedAgentsBetaHeader {
			t.Fatalf("anthropic-beta = %q, want %q", got, managedAgentsBetaHeader)
		}
		if r.URL.Path != "/v1/skills" {
			t.Fatalf("path = %q, want /v1/skills", r.URL.Path)
		}
		if got := r.URL.Query().Get("source"); got != "anthropic" {
			t.Fatalf("source = %q, want anthropic", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"type":"skill","id":"xlsx","display_title":"Excel","latest_version":"2","source":"anthropic","created_at":"2026-04-10T00:00:00Z","updated_at":"2026-04-10T00:00:00Z"}],"has_more":false,"next_page":null}`))
	}))
	defer server.Close()

	catalog, err := NewAnthropicRemoteSkillCatalog(AnthropicRemoteSkillCatalogConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatalf("NewAnthropicRemoteSkillCatalog: %v", err)
	}
	items, nextPage, hasMore, err := catalog.ListSkills(t.Context(), 20, "")
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(items) != 1 || items[0].ID != "xlsx" {
		t.Fatalf("items = %#v, want xlsx", items)
	}
	if hasMore || nextPage != nil {
		t.Fatalf("hasMore/nextPage = %v/%v, want false/nil", hasMore, nextPage)
	}
}

func TestAnthropicRemoteSkillCatalogResolveVersionUsesUpstreamLatestVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/skills/xlsx" {
			t.Fatalf("path = %q, want /v1/skills/xlsx", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"type":"skill","id":"xlsx","display_title":"Excel","latest_version":"7","source":"anthropic","created_at":"2026-04-10T00:00:00Z","updated_at":"2026-04-10T00:00:00Z"}`))
	}))
	defer server.Close()

	catalog, err := NewAnthropicRemoteSkillCatalog(AnthropicRemoteSkillCatalogConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatalf("NewAnthropicRemoteSkillCatalog: %v", err)
	}
	version, err := catalog.ResolveVersion(t.Context(), "xlsx", "latest")
	if err != nil {
		t.Fatalf("ResolveVersion: %v", err)
	}
	if version != "7" {
		t.Fatalf("version = %q, want 7", version)
	}
}
