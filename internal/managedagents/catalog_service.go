package managedagents

import (
	"context"
	"errors"
	"mime"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (s *Service) CreateAgent(ctx context.Context, principal Principal, req CreateAgentRequest) (map[string]any, error) {
	if strings.TrimSpace(principal.TeamID) == "" {
		return nil, errors.New("team id is required")
	}
	if strings.TrimSpace(req.Name) == "" {
		return nil, errors.New("name is required")
	}
	vendor, _ := normalizeModelConfig(req.Model, "")
	if err := ensureClaudeVendor(vendor); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	agent := buildAgentObject(NewID("agent"), 1, vendor, req, now, nil)
	if err := s.repo.CreateAgent(ctx, principal.TeamID, vendor, 1, agent, now); err != nil {
		return nil, err
	}
	return agent, nil
}

func (s *Service) ListAgents(ctx context.Context, principal Principal, opts AgentListOptions) ([]map[string]any, *string, error) {
	return s.repo.ListAgents(ctx, principal.TeamID, opts)
}

func (s *Service) GetAgent(ctx context.Context, principal Principal, agentID string, version int) (map[string]any, error) {
	agent, _, err := s.repo.GetAgent(ctx, principal.TeamID, agentID, version)
	return agent, err
}

func (s *Service) UpdateAgent(ctx context.Context, principal Principal, agentID string, req UpdateAgentRequest) (map[string]any, error) {
	agent, vendor, err := s.repo.GetAgent(ctx, principal.TeamID, agentID, 0)
	if err != nil {
		return nil, err
	}
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" {
			return nil, errors.New("name is required")
		}
		agent["name"] = trimmed
	}
	if req.Description != nil {
		agent["description"] = nullableText(req.Description)
	}
	if req.System != nil {
		agent["system"] = nullableText(req.System)
	}
	if req.Model != nil {
		vendor, agent["model"] = normalizeModelConfig(req.Model, vendor)
	}
	if req.Tools != nil {
		agent["tools"] = cloneSlice(req.Tools)
	}
	if req.MCPServers != nil {
		agent["mcp_servers"] = cloneSlice(req.MCPServers)
	}
	if req.Skills != nil {
		agent["skills"] = cloneSlice(req.Skills)
	}
	if req.Metadata != nil {
		agent["metadata"] = cloneStringMap(req.Metadata)
	}
	version := intValue(agent["version"]) + 1
	if version <= 1 {
		version = 2
	}
	updatedAt := time.Now().UTC()
	agent["version"] = version
	agent["updated_at"] = nowRFC3339(updatedAt)
	if err := s.repo.UpdateAgent(ctx, principal.TeamID, agentID, vendor, version, agent, timePointerFromValue(agent["archived_at"]), updatedAt); err != nil {
		return nil, err
	}
	return agent, nil
}

func (s *Service) ArchiveAgent(ctx context.Context, principal Principal, agentID string) (map[string]any, error) {
	agent, vendor, err := s.repo.GetAgent(ctx, principal.TeamID, agentID, 0)
	if err != nil {
		return nil, err
	}
	version := intValue(agent["version"])
	if version <= 0 {
		version = 1
	}
	now := time.Now().UTC()
	agent["archived_at"] = nowRFC3339(now)
	agent["updated_at"] = nowRFC3339(now)
	if err := s.repo.UpdateAgent(ctx, principal.TeamID, agentID, vendor, version, agent, &now, now); err != nil {
		return nil, err
	}
	return agent, nil
}

func (s *Service) ListAgentVersions(ctx context.Context, principal Principal, agentID string, limit int, page string) ([]map[string]any, *string, error) {
	return s.repo.ListAgentVersions(ctx, principal.TeamID, agentID, limit, page)
}

func (s *Service) CreateEnvironment(ctx context.Context, principal Principal, req CreateEnvironmentRequest) (map[string]any, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, errors.New("name is required")
	}
	req.Config = ensureEnvironmentConfig(req.Config)
	now := time.Now().UTC()
	environment := buildEnvironmentObject(NewID("env"), req, now, nil)
	if err := s.repo.CreateEnvironment(ctx, principal.TeamID, environment, nil, now); err != nil {
		return nil, err
	}
	return environment, nil
}

func (s *Service) ListEnvironments(ctx context.Context, principal Principal, limit int, page string, includeArchived bool) ([]map[string]any, *string, error) {
	return s.repo.ListEnvironments(ctx, principal.TeamID, limit, page, includeArchived)
}

func (s *Service) GetEnvironment(ctx context.Context, principal Principal, environmentID string) (map[string]any, error) {
	return s.repo.GetEnvironment(ctx, principal.TeamID, environmentID)
}

func (s *Service) UpdateEnvironment(ctx context.Context, principal Principal, environmentID string, req UpdateEnvironmentRequest) (map[string]any, error) {
	environment, err := s.repo.GetEnvironment(ctx, principal.TeamID, environmentID)
	if err != nil {
		return nil, err
	}
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed != "" {
			environment["name"] = trimmed
		}
	}
	if req.Description != nil {
		environment["description"] = strings.TrimSpace(*req.Description)
	}
	if req.Config != nil {
		environment["config"] = ensureEnvironmentConfig(req.Config)
	}
	if req.Metadata != nil {
		environment["metadata"] = mergeNullableMetadata(mapFromValue(environment["metadata"]), req.Metadata)
	}
	now := time.Now().UTC()
	environment["updated_at"] = nowRFC3339(now)
	if err := s.repo.UpdateEnvironment(ctx, principal.TeamID, environmentID, environment, timePointerFromValue(environment["archived_at"]), now); err != nil {
		return nil, err
	}
	return environment, nil
}

func (s *Service) DeleteEnvironment(ctx context.Context, principal Principal, environmentID string) (map[string]any, error) {
	if err := s.repo.DeleteEnvironment(ctx, principal.TeamID, environmentID); err != nil {
		return nil, err
	}
	return deletedObject("environment_deleted", environmentID), nil
}

func (s *Service) ArchiveEnvironment(ctx context.Context, principal Principal, environmentID string) (map[string]any, error) {
	environment, err := s.repo.GetEnvironment(ctx, principal.TeamID, environmentID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	environment["archived_at"] = nowRFC3339(now)
	environment["updated_at"] = nowRFC3339(now)
	if err := s.repo.UpdateEnvironment(ctx, principal.TeamID, environmentID, environment, &now, now); err != nil {
		return nil, err
	}
	return environment, nil
}

func (s *Service) CreateVault(ctx context.Context, principal Principal, req CreateVaultRequest) (map[string]any, error) {
	if strings.TrimSpace(req.DisplayName) == "" {
		return nil, errors.New("display_name is required")
	}
	now := time.Now().UTC()
	vault := buildVaultObject(NewID("vlt"), req, now, nil)
	if err := s.repo.CreateVault(ctx, principal.TeamID, vault, nil, now); err != nil {
		return nil, err
	}
	return vault, nil
}

func (s *Service) ListVaults(ctx context.Context, principal Principal, limit int, page string, includeArchived bool) ([]map[string]any, *string, error) {
	return s.repo.ListVaults(ctx, principal.TeamID, limit, page, includeArchived)
}

func (s *Service) GetVault(ctx context.Context, principal Principal, vaultID string) (map[string]any, error) {
	return s.repo.GetVault(ctx, principal.TeamID, vaultID)
}

func (s *Service) UpdateVault(ctx context.Context, principal Principal, vaultID string, req UpdateVaultRequest) (map[string]any, error) {
	vault, err := s.repo.GetVault(ctx, principal.TeamID, vaultID)
	if err != nil {
		return nil, err
	}
	if req.DisplayName != nil {
		trimmed := strings.TrimSpace(*req.DisplayName)
		if trimmed != "" {
			vault["display_name"] = trimmed
		}
	}
	if req.Metadata != nil {
		vault["metadata"] = mergeNullableMetadata(mapFromValue(vault["metadata"]), req.Metadata)
	}
	now := time.Now().UTC()
	vault["updated_at"] = nowRFC3339(now)
	if err := s.repo.UpdateVault(ctx, principal.TeamID, vaultID, vault, timePointerFromValue(vault["archived_at"]), now); err != nil {
		return nil, err
	}
	return vault, nil
}

func (s *Service) DeleteVault(ctx context.Context, principal Principal, vaultID string) (map[string]any, error) {
	if err := s.repo.DeleteVault(ctx, principal.TeamID, vaultID); err != nil {
		return nil, err
	}
	return deletedObject("vault_deleted", vaultID), nil
}

func (s *Service) ArchiveVault(ctx context.Context, principal Principal, vaultID string) (map[string]any, error) {
	vault, err := s.repo.GetVault(ctx, principal.TeamID, vaultID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	vault["archived_at"] = nowRFC3339(now)
	vault["updated_at"] = nowRFC3339(now)
	if err := s.repo.UpdateVault(ctx, principal.TeamID, vaultID, vault, &now, now); err != nil {
		return nil, err
	}
	return vault, nil
}

func (s *Service) CreateCredential(ctx context.Context, principal Principal, vaultID string, req CreateCredentialRequest) (map[string]any, error) {
	if len(req.Auth) == 0 {
		return nil, errors.New("auth is required")
	}
	if _, err := s.repo.GetVault(ctx, principal.TeamID, vaultID); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	credential := buildCredentialObject(NewID("vcrd"), vaultID, req, now, nil)
	if err := s.repo.CreateCredential(ctx, principal.TeamID, vaultID, credential, cloneMap(req.Auth), nil, now); err != nil {
		return nil, err
	}
	return credential, nil
}

func (s *Service) ListCredentials(ctx context.Context, principal Principal, vaultID string, limit int, page string, includeArchived bool) ([]map[string]any, *string, error) {
	if _, err := s.repo.GetVault(ctx, principal.TeamID, vaultID); err != nil {
		return nil, nil, err
	}
	return s.repo.ListCredentials(ctx, principal.TeamID, vaultID, limit, page, includeArchived)
}

func (s *Service) GetCredential(ctx context.Context, principal Principal, vaultID, credentialID string) (map[string]any, error) {
	credential, _, err := s.repo.GetCredential(ctx, principal.TeamID, vaultID, credentialID)
	return credential, err
}

func (s *Service) UpdateCredential(ctx context.Context, principal Principal, vaultID, credentialID string, req UpdateCredentialRequest) (map[string]any, error) {
	credential, secret, err := s.repo.GetCredential(ctx, principal.TeamID, vaultID, credentialID)
	if err != nil {
		return nil, err
	}
	if req.DisplayName != nil {
		credential["display_name"] = nullableText(req.DisplayName)
	}
	if req.Metadata != nil {
		credential["metadata"] = mergeNullableMetadata(mapFromValue(credential["metadata"]), req.Metadata)
	}
	if req.Auth != nil {
		secret = cloneMap(req.Auth)
		credential["auth"] = redactCredentialAuth(req.Auth)
	}
	now := time.Now().UTC()
	credential["updated_at"] = nowRFC3339(now)
	if err := s.repo.UpdateCredential(ctx, principal.TeamID, vaultID, credentialID, credential, secret, timePointerFromValue(credential["archived_at"]), now); err != nil {
		return nil, err
	}
	return credential, nil
}

func (s *Service) DeleteCredential(ctx context.Context, principal Principal, vaultID, credentialID string) (map[string]any, error) {
	if err := s.repo.DeleteCredential(ctx, principal.TeamID, vaultID, credentialID); err != nil {
		return nil, err
	}
	return deletedObject("vault_credential_deleted", credentialID), nil
}

func (s *Service) ArchiveCredential(ctx context.Context, principal Principal, vaultID, credentialID string) (map[string]any, error) {
	credential, secret, err := s.repo.GetCredential(ctx, principal.TeamID, vaultID, credentialID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	credential["archived_at"] = nowRFC3339(now)
	credential["updated_at"] = nowRFC3339(now)
	if err := s.repo.UpdateCredential(ctx, principal.TeamID, vaultID, credentialID, credential, secret, &now, now); err != nil {
		return nil, err
	}
	return credential, nil
}

func (s *Service) UploadFile(ctx context.Context, principal Principal, filename, mimeType string, content []byte) (map[string]any, error) {
	trimmedName := strings.TrimSpace(filename)
	if trimmedName == "" {
		return nil, errors.New("filename is required")
	}
	resolvedMimeType := strings.TrimSpace(mimeType)
	if resolvedMimeType == "" {
		resolvedMimeType = mime.TypeByExtension(filepath.Ext(trimmedName))
	}
	if resolvedMimeType == "" {
		resolvedMimeType = "application/octet-stream"
	}
	now := time.Now().UTC()
	record := &managedFileRecord{
		ID:        NewID("file"),
		TeamID:    principal.TeamID,
		Filename:  trimmedName,
		MimeType:  resolvedMimeType,
		SizeBytes: int64(len(content)),
		Content:   append([]byte(nil), content...),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.repo.CreateFile(ctx, record); err != nil {
		return nil, err
	}
	return buildFileObject(record), nil
}

func (s *Service) ListFiles(ctx context.Context, principal Principal, opts FileListOptions) (map[string]any, error) {
	files, hasMore, err := s.repo.ListFiles(ctx, principal.TeamID, opts)
	if err != nil {
		return nil, err
	}
	var firstID any
	var lastID any
	if len(files) > 0 {
		firstID = files[0]["id"]
		lastID = files[len(files)-1]["id"]
	}
	return map[string]any{
		"data":     files,
		"first_id": firstID,
		"last_id":  lastID,
		"has_more": hasMore,
	}, nil
}

func (s *Service) GetFileMetadata(ctx context.Context, principal Principal, fileID string) (map[string]any, error) {
	record, err := s.repo.GetFile(ctx, principal.TeamID, fileID)
	if err != nil {
		return nil, err
	}
	return buildFileObject(record), nil
}

func (s *Service) GetFileContent(ctx context.Context, principal Principal, fileID string) (*managedFileRecord, error) {
	return s.repo.GetFile(ctx, principal.TeamID, fileID)
}

func (s *Service) DeleteFile(ctx context.Context, principal Principal, fileID string) (map[string]any, error) {
	if err := s.repo.DeleteFile(ctx, principal.TeamID, fileID); err != nil {
		return nil, err
	}
	return deletedObject("file_deleted", fileID), nil
}

func (s *Service) ArchiveSession(ctx context.Context, principal Principal, sessionID string) (*Session, error) {
	record, engine, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := ensureSessionAccess(record, principal); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	archivedEvent := stampEvent(map[string]any{"type": "session.archived"}, now)
	if err := s.repo.AppendEvents(ctx, sessionID, []map[string]any{archivedEvent}); err != nil {
		return nil, err
	}
	record = applySessionBatch(record, now, Usage{}, []map[string]any{archivedEvent})
	if err := s.repo.UpdateSession(ctx, record, engine); err != nil {
		return nil, err
	}
	return record.toAPI(now), nil
}

func (s *Service) ListSessionResources(ctx context.Context, principal Principal, sessionID string, limit int, page string) ([]map[string]any, *string, error) {
	record, _, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	if err := ensureSessionAccess(record, principal); err != nil {
		return nil, nil, err
	}
	resources := cloneMapSlice(record.Resources)
	sort.SliceStable(resources, func(i, j int) bool {
		leftTime := parseResourceCreatedAt(resources[i])
		rightTime := parseResourceCreatedAt(resources[j])
		if leftTime.Equal(rightTime) {
			return stringValue(resources[i]["id"]) < stringValue(resources[j]["id"])
		}
		return leftTime.Before(rightTime)
	})
	cursor, err := decodePageCursor(page)
	if err != nil {
		return nil, nil, err
	}
	if cursor != nil {
		cursorTime, _ := time.Parse(time.RFC3339, cursor.CreatedAt)
		filtered := make([]map[string]any, 0, len(resources))
		for _, resource := range resources {
			resourceTime := parseResourceCreatedAt(resource)
			resourceID := stringValue(resource["id"])
			if resourceTime.After(cursorTime) || (resourceTime.Equal(cursorTime) && resourceID > cursor.ID) {
				filtered = append(filtered, resource)
			}
		}
		resources = filtered
	}
	if limit <= 0 || limit > 1000 {
		limit = len(resources)
	}
	var nextPage *string
	if limit < len(resources) {
		last := resources[limit-1]
		nextPage = encodePageCursor(parseResourceCreatedAt(last), stringValue(last["id"]))
		resources = resources[:limit]
	}
	return resources, nextPage, nil
}

func (s *Service) AddSessionResource(ctx context.Context, principal Principal, sessionID string, req AddSessionResourceRequest) (map[string]any, error) {
	record, engine, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := ensureSessionAccess(record, principal); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	resource, err := s.validateAndNormalizeResource(ctx, principal, normalizeSessionResource(req, now))
	if err != nil {
		return nil, err
	}
	record.Resources = append(cloneMapSlice(record.Resources), resource)
	record.UpdatedAt = now
	if err := s.repo.UpdateSession(ctx, record, engine); err != nil {
		return nil, err
	}
	return resource, nil
}

func (s *Service) GetSessionResource(ctx context.Context, principal Principal, sessionID, resourceID string) (map[string]any, error) {
	record, _, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := ensureSessionAccess(record, principal); err != nil {
		return nil, err
	}
	resource, _, found := findResource(record.Resources, resourceID)
	if !found {
		return nil, ErrResourceNotFound
	}
	return cloneMap(resource), nil
}

func (s *Service) UpdateSessionResource(ctx context.Context, principal Principal, sessionID, resourceID string, req UpdateSessionResourceRequest) (map[string]any, error) {
	record, engine, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := ensureSessionAccess(record, principal); err != nil {
		return nil, err
	}
	current, index, found := findResource(record.Resources, resourceID)
	if !found {
		return nil, ErrResourceNotFound
	}
	now := time.Now().UTC()
	updated := cloneMap(req)
	updated["id"] = resourceID
	updated["created_at"] = current["created_at"]
	updated = normalizeSessionResource(updated, now)
	updated, err = s.validateAndNormalizeResource(ctx, principal, updated)
	if err != nil {
		return nil, err
	}
	resources := cloneMapSlice(record.Resources)
	resources[index] = updated
	record.Resources = resources
	record.UpdatedAt = now
	if err := s.repo.UpdateSession(ctx, record, engine); err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *Service) DeleteSessionResource(ctx context.Context, principal Principal, sessionID, resourceID string) (map[string]any, error) {
	record, engine, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := ensureSessionAccess(record, principal); err != nil {
		return nil, err
	}
	_, index, found := findResource(record.Resources, resourceID)
	if !found {
		return nil, ErrResourceNotFound
	}
	resources := cloneMapSlice(record.Resources)
	record.Resources = append(resources[:index], resources[index+1:]...)
	record.UpdatedAt = time.Now().UTC()
	if err := s.repo.UpdateSession(ctx, record, engine); err != nil {
		return nil, err
	}
	return deletedObject("session_resource_deleted", resourceID), nil
}

func (s *Service) validateAndNormalizeResource(ctx context.Context, principal Principal, resource map[string]any) (map[string]any, error) {
	resourceType := strings.TrimSpace(stringValue(resource["type"]))
	switch resourceType {
	case "file":
		fileID := strings.TrimSpace(stringValue(resource["file_id"]))
		if fileID == "" {
			return nil, errors.New("file_id is required")
		}
		if strings.TrimSpace(stringValue(resource["mount_path"])) == "" {
			return nil, errors.New("mount_path is required")
		}
		if _, err := s.repo.GetFile(ctx, principal.TeamID, fileID); err != nil {
			return nil, err
		}
	case "github_repository":
		if strings.TrimSpace(stringValue(resource["url"])) == "" {
			return nil, errors.New("url is required")
		}
		if strings.TrimSpace(stringValue(resource["mount_path"])) == "" {
			return nil, errors.New("mount_path is required")
		}
	default:
		return nil, errors.New("invalid resource type")
	}
	return resource, nil
}

func ensureEnvironmentConfig(config map[string]any) map[string]any {
	if len(config) != 0 {
		return cloneMap(config)
	}
	return map[string]any{
		"type": "cloud",
		"networking": map[string]any{
			"type":                   "limited",
			"allowed_hosts":          []any{},
			"allow_package_managers": false,
			"allow_mcp_servers":      false,
		},
		"packages": map[string]any{
			"type":  "packages",
			"pip":   []any{},
			"npm":   []any{},
			"apt":   []any{},
			"cargo": []any{},
			"gem":   []any{},
			"go":    []any{},
		},
	}
}

func mapFromValue(value any) map[string]string {
	if value == nil {
		return map[string]string{}
	}
	result := map[string]string{}
	switch typed := value.(type) {
	case map[string]string:
		return cloneStringMap(typed)
	case map[string]any:
		for key, entry := range typed {
			if text, ok := entry.(string); ok {
				result[key] = text
			}
		}
	}
	return result
}

func mergeNullableMetadata(existing map[string]string, updates map[string]*string) map[string]string {
	out := cloneStringMap(existing)
	for key, value := range updates {
		if value == nil || strings.TrimSpace(*value) == "" {
			delete(out, key)
			continue
		}
		out[key] = strings.TrimSpace(*value)
	}
	return out
}

func findResource(resources []map[string]any, resourceID string) (map[string]any, int, bool) {
	for index, resource := range resources {
		if strings.TrimSpace(stringValue(resource["id"])) == strings.TrimSpace(resourceID) {
			return resource, index, true
		}
	}
	return nil, -1, false
}

func parseResourceCreatedAt(resource map[string]any) time.Time {
	text := stringValue(resource["created_at"])
	if text == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func deletedObject(objectType, id string) map[string]any {
	return map[string]any{"type": objectType, "id": strings.TrimSpace(id)}
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func timePointerFromValue(value any) *time.Time {
	text := strings.TrimSpace(stringValue(value))
	if text == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return nil
	}
	return &parsed
}

func (s *Service) resolveSessionAgentReference(ctx context.Context, principal Principal, input any) (string, map[string]any, error) {
	switch value := input.(type) {
	case string:
		agent, vendor, err := s.repo.GetAgent(ctx, principal.TeamID, value, 0)
		if err != nil {
			return "", nil, err
		}
		if err := ensureClaudeVendor(vendor); err != nil {
			return "", nil, err
		}
		return vendor, agent, err
	case map[string]any:
		if strings.TrimSpace(stringValue(value["id"])) == "" {
			return "", nil, errors.New("agent.id is required")
		}
		agent, vendor, err := s.repo.GetAgent(ctx, principal.TeamID, stringValue(value["id"]), intValue(value["version"]))
		if err != nil {
			return "", nil, err
		}
		if err := ensureClaudeVendor(vendor); err != nil {
			return "", nil, err
		}
		return vendor, agent, err
	default:
		return "", nil, errors.New("agent is required")
	}
}

func ensureClaudeVendor(vendor string) error {
	if strings.EqualFold(strings.TrimSpace(vendor), "claude") {
		return nil
	}
	return errors.New("only claude managed agents are supported")
}

func (s *Service) validateSessionDependencies(ctx context.Context, principal Principal, environmentID string, vaultIDs []string, resources []map[string]any) ([]map[string]any, error) {
	if _, err := s.repo.GetEnvironment(ctx, principal.TeamID, environmentID); err != nil {
		return nil, err
	}
	for _, vaultID := range vaultIDs {
		if _, err := s.repo.GetVault(ctx, principal.TeamID, vaultID); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	normalized := make([]map[string]any, 0, len(resources))
	for _, resource := range resources {
		validated, err := s.validateAndNormalizeResource(ctx, principal, normalizeSessionResource(resource, now))
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, validated)
	}
	return normalized, nil
}
