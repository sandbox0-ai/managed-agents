package managedagents

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const managedAgentsUpstreamBetaHeader = skillsAPIBetaHeader

type AnthropicSkillCatalog interface {
	Has(skillID string) bool
	ResolveVersion(ctx context.Context, skillID, version string) (string, error)
	ListSkills(ctx context.Context, limit int, page string) ([]Skill, *string, bool, error)
	GetSkill(ctx context.Context, skillID string) (*Skill, error)
	ListSkillVersions(ctx context.Context, skillID string, limit int, page string) ([]SkillVersion, *string, bool, error)
	GetSkillVersion(ctx context.Context, skillID, version string) (*SkillVersion, error)
}

type AnthropicRemoteSkillCatalogConfig struct {
	BaseURL    string
	APIKey     string
	APIVersion string
	Timeout    time.Duration
}

type anthropicSkillDescriptor struct {
	ID            string
	DisplayTitle  string
	Name          string
	Description   string
	Directory     string
	LatestVersion string
	ReleasedAt    time.Time
}

type anthropicSkillRegistry struct {
	skills []anthropicSkillDescriptor
	byID   map[string]anthropicSkillDescriptor
}

type anthropicRemoteSkillCatalog struct {
	baseURL    string
	apiKey     string
	apiVersion string
	client     *http.Client
	fallback   *anthropicSkillRegistry
}

func defaultAnthropicSkillRegistry() *anthropicSkillRegistry {
	releasedAt := time.Date(2025, 10, 16, 0, 0, 0, 0, time.UTC)
	skills := []anthropicSkillDescriptor{
		{ID: "pptx", DisplayTitle: "PowerPoint", Name: "pptx", Description: "Create and edit PowerPoint presentations.", Directory: "pptx", LatestVersion: "1", ReleasedAt: releasedAt},
		{ID: "xlsx", DisplayTitle: "Excel", Name: "xlsx", Description: "Create and analyze spreadsheets.", Directory: "xlsx", LatestVersion: "1", ReleasedAt: releasedAt},
		{ID: "docx", DisplayTitle: "Word", Name: "docx", Description: "Create and edit Word documents.", Directory: "docx", LatestVersion: "1", ReleasedAt: releasedAt},
		{ID: "pdf", DisplayTitle: "PDF", Name: "pdf", Description: "Generate and process PDF documents.", Directory: "pdf", LatestVersion: "1", ReleasedAt: releasedAt},
	}
	byID := make(map[string]anthropicSkillDescriptor, len(skills))
	for _, skill := range skills {
		byID[skill.ID] = skill
	}
	return &anthropicSkillRegistry{skills: skills, byID: byID}
}

func NewAnthropicRemoteSkillCatalog(cfg AnthropicRemoteSkillCatalogConfig) (AnthropicSkillCatalog, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, errors.New("anthropic api key is required")
	}
	apiVersion := strings.TrimSpace(cfg.APIVersion)
	if apiVersion == "" {
		apiVersion = "2023-06-01"
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &anthropicRemoteSkillCatalog{
		baseURL:    baseURL,
		apiKey:     apiKey,
		apiVersion: apiVersion,
		client:     &http.Client{Timeout: timeout},
		fallback:   defaultAnthropicSkillRegistry(),
	}, nil
}

func (r *anthropicSkillRegistry) Has(skillID string) bool {
	if r == nil {
		return false
	}
	_, ok := r.byID[strings.TrimSpace(skillID)]
	return ok
}

func (r *anthropicSkillRegistry) ResolveVersion(_ context.Context, skillID, version string) (string, error) {
	if r == nil {
		return "", ErrSkillNotFound
	}
	skill, ok := r.byID[strings.TrimSpace(skillID)]
	if !ok {
		return "", ErrSkillNotFound
	}
	trimmedVersion := strings.TrimSpace(version)
	if trimmedVersion == "" || strings.EqualFold(trimmedVersion, "latest") {
		return skill.LatestVersion, nil
	}
	if _, err := normalizeRequiredText(trimmedVersion, "version", 64); err != nil {
		return "", err
	}
	if trimmedVersion != skill.LatestVersion {
		return "", ErrSkillVersionNotFound
	}
	return trimmedVersion, nil
}

func (r *anthropicSkillRegistry) ListSkills(_ context.Context, limit int, page string) ([]Skill, *string, bool, error) {
	if r == nil {
		return []Skill{}, nil, false, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	offset, err := decodeAnthropicSkillCursor(page)
	if err != nil {
		return nil, nil, false, err
	}
	if offset >= len(r.skills) {
		return []Skill{}, nil, false, nil
	}
	end := offset + limit
	if end > len(r.skills) {
		end = len(r.skills)
	}
	items := make([]Skill, 0, end-offset)
	for _, skill := range r.skills[offset:end] {
		items = append(items, anthropicSkillObject(skill))
	}
	var nextPage *string
	if end < len(r.skills) {
		nextPage = encodeAnthropicSkillCursor(end)
	}
	return items, nextPage, nextPage != nil, nil
}

func (r *anthropicSkillRegistry) GetSkill(_ context.Context, skillID string) (*Skill, error) {
	if r == nil {
		return nil, ErrSkillNotFound
	}
	skill, ok := r.byID[strings.TrimSpace(skillID)]
	if !ok {
		return nil, ErrSkillNotFound
	}
	item := anthropicSkillObject(skill)
	return &item, nil
}

func (r *anthropicSkillRegistry) ListSkillVersions(ctx context.Context, skillID string, limit int, page string) ([]SkillVersion, *string, bool, error) {
	if limit <= 0 || limit > 1000 {
		limit = 20
	}
	if _, ok := r.byID[strings.TrimSpace(skillID)]; !ok {
		return nil, nil, false, ErrSkillNotFound
	}
	offset, err := decodeAnthropicSkillCursor(page)
	if err != nil {
		return nil, nil, false, err
	}
	if offset > 0 || limit == 0 {
		return []SkillVersion{}, nil, false, nil
	}
	version, err := r.GetSkillVersion(ctx, skillID, "")
	if err != nil {
		return nil, nil, false, err
	}
	return []SkillVersion{*version}, nil, false, nil
}

func (r *anthropicSkillRegistry) GetSkillVersion(ctx context.Context, skillID, version string) (*SkillVersion, error) {
	if r == nil {
		return nil, ErrSkillVersionNotFound
	}
	skill, ok := r.byID[strings.TrimSpace(skillID)]
	if !ok {
		return nil, ErrSkillNotFound
	}
	resolvedVersion, err := r.ResolveVersion(ctx, skillID, version)
	if err != nil {
		return nil, err
	}
	item := anthropicSkillVersionObject(skill, resolvedVersion)
	return &item, nil
}

func (c *anthropicRemoteSkillCatalog) Has(skillID string) bool {
	if c == nil {
		return false
	}
	return c.fallback != nil && c.fallback.Has(skillID)
}

func (c *anthropicRemoteSkillCatalog) ResolveVersion(ctx context.Context, skillID, version string) (string, error) {
	trimmedVersion := strings.TrimSpace(version)
	if trimmedVersion != "" && !strings.EqualFold(trimmedVersion, "latest") {
		if _, err := normalizeRequiredText(trimmedVersion, "version", 64); err != nil {
			return "", err
		}
		path := "/v1/skills/" + url.PathEscape(strings.TrimSpace(skillID)) + "/versions/" + url.PathEscape(trimmedVersion)
		if err := c.doJSON(ctx, http.MethodGet, path, nil, nil, nil); err != nil {
			return "", err
		}
		return trimmedVersion, nil
	}
	skill, err := c.GetSkill(ctx, skillID)
	if err != nil {
		return "", err
	}
	if skill == nil || skill.LatestVersion == nil || strings.TrimSpace(*skill.LatestVersion) == "" {
		return "", ErrSkillVersionNotFound
	}
	return strings.TrimSpace(*skill.LatestVersion), nil
}

func (c *anthropicRemoteSkillCatalog) ListSkills(ctx context.Context, limit int, page string) ([]Skill, *string, bool, error) {
	query := url.Values{}
	query.Set("source", "anthropic")
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if trimmed := strings.TrimSpace(page); trimmed != "" {
		query.Set("page", trimmed)
	}
	response := ListSkillsResponse{}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/skills", query, nil, &response); err != nil {
		return nil, nil, false, err
	}
	return response.Data, response.NextPage, response.HasMore, nil
}

func (c *anthropicRemoteSkillCatalog) GetSkill(ctx context.Context, skillID string) (*Skill, error) {
	response := Skill{}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/skills/"+url.PathEscape(strings.TrimSpace(skillID)), nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *anthropicRemoteSkillCatalog) ListSkillVersions(ctx context.Context, skillID string, limit int, page string) ([]SkillVersion, *string, bool, error) {
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if trimmed := strings.TrimSpace(page); trimmed != "" {
		query.Set("page", trimmed)
	}
	response := ListSkillVersionsResponse{}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/skills/"+url.PathEscape(strings.TrimSpace(skillID))+"/versions", query, nil, &response); err != nil {
		return nil, nil, false, err
	}
	return response.Data, response.NextPage, response.HasMore, nil
}

func (c *anthropicRemoteSkillCatalog) GetSkillVersion(ctx context.Context, skillID, version string) (*SkillVersion, error) {
	resolvedVersion := strings.TrimSpace(version)
	if resolvedVersion == "" || strings.EqualFold(resolvedVersion, "latest") {
		var err error
		resolvedVersion, err = c.ResolveVersion(ctx, skillID, version)
		if err != nil {
			return nil, err
		}
	} else if _, err := normalizeRequiredText(resolvedVersion, "version", 64); err != nil {
		return nil, err
	}
	response := SkillVersion{}
	path := "/v1/skills/" + url.PathEscape(strings.TrimSpace(skillID)) + "/versions/" + url.PathEscape(resolvedVersion)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *anthropicRemoteSkillCatalog) doJSON(ctx context.Context, method, requestPath string, query url.Values, body io.Reader, out any) error {
	if c == nil {
		return errors.New("anthropic skill catalog is not configured")
	}
	endpoint := c.baseURL + requestPath
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("build anthropic skill request: %w", err)
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Anthropic-Version", c.apiVersion)
	req.Header.Set("Anthropic-Beta", managedAgentsUpstreamBetaHeader)
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("request anthropic skill api: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read anthropic skill api response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		if strings.Contains(requestPath, "/versions/") {
			return ErrSkillVersionNotFound
		}
		return ErrSkillNotFound
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("anthropic skill api %s: status %d: %s", requestPath, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if out == nil || len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode anthropic skill api response: %w", err)
	}
	return nil
}

func anthropicSkillObject(skill anthropicSkillDescriptor) Skill {
	return Skill{
		Type:          "skill",
		ID:            skill.ID,
		DisplayTitle:  normalizeNullableString(&skill.DisplayTitle),
		LatestVersion: normalizeNullableString(&skill.LatestVersion),
		Source:        "anthropic",
		CreatedAt:     nowRFC3339(skill.ReleasedAt),
		UpdatedAt:     nowRFC3339(skill.ReleasedAt),
	}
}

func anthropicSkillVersionObject(skill anthropicSkillDescriptor, version string) SkillVersion {
	return SkillVersion{
		Type:        "skill_version",
		ID:          anthropicSkillVersionID(skill.ID, version),
		SkillID:     skill.ID,
		Version:     version,
		Name:        skill.Name,
		Description: skill.Description,
		Directory:   skill.Directory,
		CreatedAt:   nowRFC3339(skill.ReleasedAt),
	}
}

func anthropicSkillVersionID(skillID, version string) string {
	toSafe := func(value string) string {
		return strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				return r
			}
			return '_'
		}, value)
	}
	return "skillver_anthropic_" + toSafe(strings.TrimSpace(skillID)) + "_" + toSafe(strings.TrimSpace(version))
}

const anthropicSkillCursorPrefix = "askills:"

func encodeAnthropicSkillCursor(offset int) *string {
	payload, _ := json.Marshal(map[string]int{"offset": offset})
	encoded := anthropicSkillCursorPrefix + base64.RawURLEncoding.EncodeToString(payload)
	return &encoded
}

func decodeAnthropicSkillCursor(raw string) (int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, nil
	}
	if !strings.HasPrefix(trimmed, anthropicSkillCursorPrefix) {
		return 0, errors.New("invalid page cursor")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(trimmed, anthropicSkillCursorPrefix))
	if err != nil {
		return 0, errors.New("invalid page cursor")
	}
	decoded := map[string]any{}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return 0, errors.New("invalid page cursor")
	}
	offset := intValue(decoded["offset"])
	if offset < 0 {
		return 0, fmt.Errorf("invalid page cursor: %s", strconv.Itoa(offset))
	}
	return offset, nil
}
