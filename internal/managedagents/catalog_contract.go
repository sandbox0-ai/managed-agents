package managedagents

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var builtInAgentToolNames = map[string]struct{}{
	"bash":       {},
	"edit":       {},
	"read":       {},
	"write":      {},
	"glob":       {},
	"grep":       {},
	"web_fetch":  {},
	"web_search": {},
}

func normalizeAgentModel(input any, vendorHint string) (string, map[string]any, error) {
	switch typed := input.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return "", nil, errors.New("model is required")
		}
	case map[string]any:
		if strings.TrimSpace(stringValue(typed["id"])) == "" {
			return "", nil, errors.New("model.id is required")
		}
	default:
		return "", nil, errors.New("model must be a string or object")
	}
	vendor, model := normalizeModelConfig(input, vendorHint)
	if err := ensureClaudeVendor(vendor); err != nil {
		return "", nil, err
	}
	if err := validateAllowedFields(model, []string{"id", "speed"}); err != nil {
		return "", nil, err
	}
	modelID := strings.TrimSpace(stringValue(model["id"]))
	if modelID == "" {
		return "", nil, errors.New("model.id is required")
	}
	model["id"] = modelID
	speed := strings.TrimSpace(stringValue(model["speed"]))
	if speed == "" {
		speed = "standard"
	}
	if speed != "standard" && speed != "fast" {
		return "", nil, errors.New("model.speed must be standard or fast")
	}
	model["speed"] = speed
	return vendor, model, nil
}

func normalizeAgentMetadata(input map[string]string) (map[string]string, error) {
	return normalizeMetadataMap(input, 16, 64, 512)
}

func mergeAgentMetadata(existing map[string]string, patch MetadataPatchField) (map[string]string, error) {
	return mergeMetadataPatch(existing, patch, 16, 64, 512)
}

func normalizeMetadataMap(input map[string]string, maxPairs, maxKeyLen, maxValueLen int) (map[string]string, error) {
	if len(input) == 0 {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		normalizedKey := strings.TrimSpace(key)
		if normalizedKey == "" {
			return nil, errors.New("metadata keys must be non-empty")
		}
		if maxKeyLen > 0 && len(normalizedKey) > maxKeyLen {
			return nil, fmt.Errorf("metadata key %q exceeds %d characters", normalizedKey, maxKeyLen)
		}
		normalizedValue := strings.TrimSpace(value)
		if maxValueLen > 0 && len(normalizedValue) > maxValueLen {
			return nil, fmt.Errorf("metadata value for %q exceeds %d characters", normalizedKey, maxValueLen)
		}
		out[normalizedKey] = normalizedValue
	}
	if maxPairs > 0 && len(out) > maxPairs {
		return nil, fmt.Errorf("metadata supports at most %d entries", maxPairs)
	}
	return out, nil
}

func mergeMetadataPatch(existing map[string]string, patch MetadataPatchField, maxPairs, maxKeyLen, maxValueLen int) (map[string]string, error) {
	if !patch.Set {
		return cloneStringMap(existing), nil
	}
	if patch.Clear {
		return map[string]string{}, nil
	}
	out := cloneStringMap(existing)
	for key, value := range patch.Values {
		normalizedKey := strings.TrimSpace(key)
		if normalizedKey == "" {
			return nil, errors.New("metadata keys must be non-empty")
		}
		if maxKeyLen > 0 && len(normalizedKey) > maxKeyLen {
			return nil, fmt.Errorf("metadata key %q exceeds %d characters", normalizedKey, maxKeyLen)
		}
		if value == nil {
			delete(out, normalizedKey)
			continue
		}
		normalizedValue := strings.TrimSpace(*value)
		if normalizedValue == "" {
			delete(out, normalizedKey)
			continue
		}
		if maxValueLen > 0 && len(normalizedValue) > maxValueLen {
			return nil, fmt.Errorf("metadata value for %q exceeds %d characters", normalizedKey, maxValueLen)
		}
		out[normalizedKey] = normalizedValue
	}
	if maxPairs > 0 && len(out) > maxPairs {
		return nil, fmt.Errorf("metadata supports at most %d entries", maxPairs)
	}
	return out, nil
}

func normalizeOptionalText(value *string, field string, maxLen int) (*string, error) {
	if value == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, nil
	}
	if maxLen > 0 && len(trimmed) > maxLen {
		return nil, fmt.Errorf("%s exceeds %d characters", field, maxLen)
	}
	return &trimmed, nil
}

func normalizeRequiredText(value, field string, maxLen int) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	if maxLen > 0 && len(trimmed) > maxLen {
		return "", fmt.Errorf("%s exceeds %d characters", field, maxLen)
	}
	return trimmed, nil
}

func normalizeAgentTools(input []any) ([]any, error) {
	if len(input) == 0 {
		return []any{}, nil
	}
	if len(input) > 128 {
		return nil, errors.New("tools supports at most 128 entries")
	}
	out := make([]any, 0, len(input))
	customNames := make(map[string]struct{})
	for _, raw := range input {
		tool, err := normalizeAgentTool(raw)
		if err != nil {
			return nil, err
		}
		if stringValue(tool["type"]) == "custom" {
			name := stringValue(tool["name"])
			if _, exists := customNames[name]; exists {
				return nil, fmt.Errorf("duplicate custom tool name %q", name)
			}
			customNames[name] = struct{}{}
		}
		out = append(out, tool)
	}
	return out, nil
}

func normalizeAgentTool(raw any) (map[string]any, error) {
	tool, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("tool entries must be objects")
	}
	tool = cloneMap(tool)
	switch strings.TrimSpace(stringValue(tool["type"])) {
	case "agent_toolset_20260401":
		return normalizeAgentToolset(tool)
	case "mcp_toolset":
		return normalizeMCPToolset(tool)
	case "custom":
		return normalizeCustomTool(tool)
	default:
		return nil, errors.New("invalid tool type")
	}
}

func normalizeAgentToolset(tool map[string]any) (map[string]any, error) {
	if err := validateAllowedFields(tool, []string{"type", "default_config", "configs"}); err != nil {
		return nil, err
	}
	defaultConfig, err := normalizeToolsetDefaultConfig(mapValue(tool["default_config"]), "always_allow")
	if err != nil {
		return nil, err
	}
	configs, err := normalizeBuiltInToolConfigs(tool["configs"], defaultConfig)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"type":           "agent_toolset_20260401",
		"default_config": defaultConfig,
		"configs":        configs,
	}, nil
}

func normalizeMCPToolset(tool map[string]any) (map[string]any, error) {
	if err := validateAllowedFields(tool, []string{"type", "mcp_server_name", "default_config", "configs"}); err != nil {
		return nil, err
	}
	serverName, err := normalizeRequiredText(stringValue(tool["mcp_server_name"]), "mcp_server_name", 255)
	if err != nil {
		return nil, err
	}
	defaultConfig, err := normalizeToolsetDefaultConfig(mapValue(tool["default_config"]), "always_ask")
	if err != nil {
		return nil, err
	}
	configs, err := normalizeNamedToolConfigs(tool["configs"], defaultConfig)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"type":            "mcp_toolset",
		"mcp_server_name": serverName,
		"default_config":  defaultConfig,
		"configs":         configs,
	}, nil
}

func normalizeCustomTool(tool map[string]any) (map[string]any, error) {
	if err := validateAllowedFields(tool, []string{"type", "name", "description", "input_schema"}); err != nil {
		return nil, err
	}
	name, err := normalizeRequiredText(stringValue(tool["name"]), "tool.name", 128)
	if err != nil {
		return nil, err
	}
	description, err := normalizeRequiredText(stringValue(tool["description"]), "tool.description", 1024)
	if err != nil {
		return nil, err
	}
	inputSchema, err := normalizeCustomToolInputSchema(tool["input_schema"])
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"type":         "custom",
		"name":         name,
		"description":  description,
		"input_schema": inputSchema,
	}, nil
}

func normalizeCustomToolInputSchema(raw any) (map[string]any, error) {
	schema, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("tool.input_schema must be an object")
	}
	schema = cloneMap(schema)
	if err := validateAllowedFields(schema, []string{"type", "properties", "required"}); err != nil {
		return nil, err
	}
	if schemaType := strings.TrimSpace(stringValue(schema["type"])); schemaType != "" && schemaType != "object" {
		return nil, errors.New("tool.input_schema.type must be object")
	}
	out := map[string]any{"type": "object"}
	if properties := schema["properties"]; properties != nil {
		props, ok := properties.(map[string]any)
		if !ok {
			return nil, errors.New("tool.input_schema.properties must be an object")
		}
		out["properties"] = cloneMap(props)
	}
	if required := schema["required"]; required != nil {
		items, err := normalizeStringSliceAny(required, "tool.input_schema.required")
		if err != nil {
			return nil, err
		}
		out["required"] = items
	}
	return out, nil
}

func normalizeToolsetDefaultConfig(input map[string]any, defaultPermissionPolicy string) (map[string]any, error) {
	if len(input) == 0 {
		return map[string]any{
			"enabled":           true,
			"permission_policy": map[string]any{"type": defaultPermissionPolicy},
		}, nil
	}
	if err := validateAllowedFields(input, []string{"enabled", "permission_policy"}); err != nil {
		return nil, err
	}
	defaultConfig := map[string]any{"enabled": true, "permission_policy": map[string]any{"type": defaultPermissionPolicy}}
	if enabled, ok := input["enabled"]; ok && enabled != nil {
		enabledValue, ok := enabled.(bool)
		if !ok {
			return nil, errors.New("default_config.enabled must be a boolean")
		}
		defaultConfig["enabled"] = enabledValue
	}
	if policy, ok := input["permission_policy"]; ok && policy != nil {
		normalizedPolicy, err := normalizePermissionPolicy(policy)
		if err != nil {
			return nil, err
		}
		defaultConfig["permission_policy"] = normalizedPolicy
	}
	return defaultConfig, nil
}

func normalizeBuiltInToolConfigs(raw any, defaults map[string]any) ([]any, error) {
	if raw == nil {
		return []any{}, nil
	}
	entries, ok := raw.([]any)
	if !ok {
		return nil, errors.New("configs must be an array")
	}
	out := make([]any, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, rawEntry := range entries {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			return nil, errors.New("tool config entries must be objects")
		}
		if err := validateAllowedFields(entry, []string{"name", "enabled", "permission_policy"}); err != nil {
			return nil, err
		}
		name := strings.TrimSpace(stringValue(entry["name"]))
		if _, exists := builtInAgentToolNames[name]; !exists {
			return nil, fmt.Errorf("invalid tool config name %q", name)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("duplicate tool config name %q", name)
		}
		seen[name] = struct{}{}
		resolved, err := resolveToolConfig(name, entry, defaults)
		if err != nil {
			return nil, err
		}
		out = append(out, resolved)
	}
	return out, nil
}

func normalizeNamedToolConfigs(raw any, defaults map[string]any) ([]any, error) {
	if raw == nil {
		return []any{}, nil
	}
	entries, ok := raw.([]any)
	if !ok {
		return nil, errors.New("configs must be an array")
	}
	out := make([]any, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, rawEntry := range entries {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			return nil, errors.New("tool config entries must be objects")
		}
		if err := validateAllowedFields(entry, []string{"name", "enabled", "permission_policy"}); err != nil {
			return nil, err
		}
		name, err := normalizeRequiredText(stringValue(entry["name"]), "tool config name", 128)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("duplicate tool config name %q", name)
		}
		seen[name] = struct{}{}
		resolved, err := resolveToolConfig(name, entry, defaults)
		if err != nil {
			return nil, err
		}
		out = append(out, resolved)
	}
	return out, nil
}

func resolveToolConfig(name string, input map[string]any, defaults map[string]any) (map[string]any, error) {
	resolved := map[string]any{
		"name":              name,
		"enabled":           defaults["enabled"],
		"permission_policy": cloneMap(mapValue(defaults["permission_policy"])),
	}
	if enabled, ok := input["enabled"]; ok && enabled != nil {
		enabledValue, ok := enabled.(bool)
		if !ok {
			return nil, fmt.Errorf("tool config %q enabled must be a boolean", name)
		}
		resolved["enabled"] = enabledValue
	}
	if policy, ok := input["permission_policy"]; ok && policy != nil {
		normalizedPolicy, err := normalizePermissionPolicy(policy)
		if err != nil {
			return nil, err
		}
		resolved["permission_policy"] = normalizedPolicy
	}
	return resolved, nil
}

func normalizePermissionPolicy(raw any) (map[string]any, error) {
	policy, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("permission_policy must be an object")
	}
	policy = cloneMap(policy)
	if err := validateAllowedFields(policy, []string{"type"}); err != nil {
		return nil, err
	}
	switch strings.TrimSpace(stringValue(policy["type"])) {
	case "always_allow":
		return map[string]any{"type": "always_allow"}, nil
	case "always_ask":
		return map[string]any{"type": "always_ask"}, nil
	default:
		return nil, errors.New("invalid permission_policy.type")
	}
}

func normalizeMCPServers(input []any) ([]any, map[string]struct{}, error) {
	if len(input) == 0 {
		return []any{}, map[string]struct{}{}, nil
	}
	if len(input) > 20 {
		return nil, nil, errors.New("mcp_servers supports at most 20 entries")
	}
	out := make([]any, 0, len(input))
	names := make(map[string]struct{}, len(input))
	for _, raw := range input {
		server, err := normalizeMCPServer(raw)
		if err != nil {
			return nil, nil, err
		}
		name := stringValue(server["name"])
		if _, exists := names[name]; exists {
			return nil, nil, fmt.Errorf("duplicate mcp server name %q", name)
		}
		names[name] = struct{}{}
		out = append(out, server)
	}
	return out, names, nil
}

func normalizeMCPServer(raw any) (map[string]any, error) {
	server, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("mcp_servers entries must be objects")
	}
	server = cloneMap(server)
	if err := validateAllowedFields(server, []string{"type", "name", "url"}); err != nil {
		return nil, err
	}
	if strings.TrimSpace(stringValue(server["type"])) != "url" {
		return nil, errors.New("invalid mcp server type")
	}
	name, err := normalizeRequiredText(stringValue(server["name"]), "mcp server name", 255)
	if err != nil {
		return nil, err
	}
	serverURL, err := normalizeRequiredText(stringValue(server["url"]), "mcp server url", 2048)
	if err != nil {
		return nil, err
	}
	if _, err := CanonicalMCPServerURL(serverURL); err != nil {
		return nil, errors.New("mcp server url must be a valid http or https url")
	}
	return map[string]any{"type": "url", "name": name, "url": serverURL}, nil
}

func validateToolReferences(tools []any, mcpServerNames map[string]struct{}) error {
	referenced := make(map[string]struct{}, len(mcpServerNames))
	for _, raw := range tools {
		tool := mapValue(raw)
		if stringValue(tool["type"]) != "mcp_toolset" {
			continue
		}
		name := stringValue(tool["mcp_server_name"])
		if _, ok := mcpServerNames[name]; !ok {
			return fmt.Errorf("mcp_toolset references unknown mcp server %q", name)
		}
		if _, ok := referenced[name]; ok {
			return fmt.Errorf("mcp server %q must not be referenced by more than one mcp_toolset", name)
		}
		referenced[name] = struct{}{}
	}
	for name := range mcpServerNames {
		if _, ok := referenced[name]; !ok {
			return fmt.Errorf("mcp server %q must be referenced by exactly one mcp_toolset", name)
		}
	}
	return nil
}

func anySliceValue(raw any) []any {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return []any{}
	}
	return cloneSlice(items)
}

func mcpServerNames(input []any) map[string]struct{} {
	names := make(map[string]struct{}, len(input))
	for _, raw := range input {
		server := mapValue(raw)
		name := strings.TrimSpace(stringValue(server["name"]))
		if name == "" {
			continue
		}
		names[name] = struct{}{}
	}
	return names
}

func (s *Service) normalizeAgentSkills(ctx context.Context, principal Principal, input []any) ([]any, error) {
	if len(input) == 0 {
		return []any{}, nil
	}
	if len(input) > 20 {
		return nil, errors.New("skills supports at most 20 entries")
	}
	out := make([]any, 0, len(input))
	for _, raw := range input {
		skill, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("skills entries must be objects")
		}
		skill = cloneMap(skill)
		if err := validateAllowedFields(skill, []string{"type", "skill_id", "version"}); err != nil {
			return nil, err
		}
		skillType := strings.TrimSpace(stringValue(skill["type"]))
		skillID, err := normalizeRequiredText(stringValue(skill["skill_id"]), "skill_id", 64)
		if err != nil {
			return nil, err
		}
		version := strings.TrimSpace(stringValue(skill["version"]))
		switch skillType {
		case "custom":
			if version == "" {
				resolvedSkill, err := s.repo.GetSkill(ctx, principal.TeamID, skillID)
				if err != nil {
					return nil, err
				}
				if resolvedSkill.LatestVersion != nil {
					version = strings.TrimSpace(*resolvedSkill.LatestVersion)
				}
				if version == "" {
					return nil, fmt.Errorf("skill %q has no available version", skillID)
				}
			}
			out = append(out, map[string]any{"type": "custom", "skill_id": skillID, "version": version})
		case "anthropic":
			version, err = s.anthropicSkills.ResolveVersion(ctx, skillID, version)
			if err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"type": "anthropic", "skill_id": skillID, "version": version})
		default:
			return nil, errors.New("invalid skill type")
		}
	}
	return out, nil
}

func normalizeCreateEnvironmentConfig(config map[string]any) (map[string]any, error) {
	if len(config) == 0 {
		return defaultEnvironmentConfig(), nil
	}
	return resolveCloudConfig(defaultEnvironmentConfig(), config)
}

func normalizeUpdateEnvironmentConfig(existing map[string]any, patch map[string]any) (map[string]any, error) {
	if len(patch) == 0 {
		return cloneMap(existing), nil
	}
	return resolveCloudConfig(existing, patch)
}

func defaultEnvironmentConfig() map[string]any {
	return map[string]any{
		"type": "cloud",
		"networking": map[string]any{
			"type": "unrestricted",
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

func resolveCloudConfig(existing map[string]any, patch map[string]any) (map[string]any, error) {
	patch = cloneMap(patch)
	if err := validateAllowedFields(patch, []string{"type", "networking", "packages"}); err != nil {
		return nil, err
	}
	if strings.TrimSpace(stringValue(patch["type"])) != "cloud" {
		return nil, errors.New("config.type must be cloud")
	}
	base := defaultEnvironmentConfig()
	if len(existing) != 0 {
		base = cloneMap(existing)
	}
	networking, err := normalizeEnvironmentNetworking(mapValue(base["networking"]), patch["networking"])
	if err != nil {
		return nil, err
	}
	packages, err := normalizeEnvironmentPackages(mapValue(base["packages"]), patch["packages"])
	if err != nil {
		return nil, err
	}
	return map[string]any{"type": "cloud", "networking": networking, "packages": packages}, nil
}

func normalizeEnvironmentNetworking(existing map[string]any, raw any) (map[string]any, error) {
	if raw == nil {
		if len(existing) == 0 {
			return map[string]any{"type": "unrestricted"}, nil
		}
		return cloneMap(existing), nil
	}
	networking, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("config.networking must be an object")
	}
	networking = cloneMap(networking)
	if err := validateAllowedFields(networking, []string{"type", "allowed_hosts", "allow_package_managers", "allow_mcp_servers"}); err != nil {
		return nil, err
	}
	switch strings.TrimSpace(stringValue(networking["type"])) {
	case "unrestricted":
		return map[string]any{"type": "unrestricted"}, nil
	case "limited":
		base := map[string]any{"type": "limited", "allowed_hosts": []any{}, "allow_package_managers": false, "allow_mcp_servers": false}
		if strings.TrimSpace(stringValue(existing["type"])) == "limited" {
			base = cloneMap(existing)
		}
		if allowedHosts, ok := networking["allowed_hosts"]; ok && allowedHosts != nil {
			hosts, err := normalizeStringSliceAny(allowedHosts, "config.networking.allowed_hosts")
			if err != nil {
				return nil, err
			}
			base["allowed_hosts"] = hosts
		}
		if value, ok := networking["allow_package_managers"]; ok && value != nil {
			flag, ok := value.(bool)
			if !ok {
				return nil, errors.New("config.networking.allow_package_managers must be a boolean")
			}
			base["allow_package_managers"] = flag
		}
		if value, ok := networking["allow_mcp_servers"]; ok && value != nil {
			flag, ok := value.(bool)
			if !ok {
				return nil, errors.New("config.networking.allow_mcp_servers must be a boolean")
			}
			base["allow_mcp_servers"] = flag
		}
		return base, nil
	default:
		return nil, errors.New("config.networking.type must be limited or unrestricted")
	}
}

func normalizeEnvironmentPackages(existing map[string]any, raw any) (map[string]any, error) {
	if raw == nil {
		if len(existing) == 0 {
			return cloneMap(defaultEnvironmentConfig()["packages"].(map[string]any)), nil
		}
		return cloneMap(existing), nil
	}
	packages, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("config.packages must be an object")
	}
	packages = cloneMap(packages)
	if err := validateAllowedFields(packages, []string{"type", "pip", "npm", "apt", "cargo", "gem", "go"}); err != nil {
		return nil, err
	}
	base := cloneMap(defaultEnvironmentConfig()["packages"].(map[string]any))
	if len(existing) != 0 {
		base = cloneMap(existing)
	}
	base["type"] = "packages"
	for _, field := range []string{"pip", "npm", "apt", "cargo", "gem", "go"} {
		if value, ok := packages[field]; ok && value != nil {
			items, err := normalizeStringSliceAny(value, "config.packages."+field)
			if err != nil {
				return nil, err
			}
			base[field] = items
		}
		if _, ok := base[field]; !ok {
			base[field] = []any{}
		}
	}
	return base, nil
}

func normalizeStringSliceAny(raw any, field string) ([]any, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array", field)
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s entries must be strings", field)
		}
		out = append(out, strings.TrimSpace(text))
	}
	return out, nil
}

func normalizeStringSlice(values []string, field string, maxLen int) ([]string, error) {
	if len(values) == 0 {
		return []string{}, nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil, fmt.Errorf("%s entries must be non-empty", field)
		}
		if maxLen > 0 && len(trimmed) > maxLen {
			return nil, fmt.Errorf("%s entry exceeds %d characters", field, maxLen)
		}
		out = append(out, trimmed)
	}
	return out, nil
}
