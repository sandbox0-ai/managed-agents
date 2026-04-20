package managedagents

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const (
	ManagedAgentsVaultRoleKey          = ManagedAgentsMetadataPrefix + "role"
	ManagedAgentsVaultEngineKey        = ManagedAgentsMetadataPrefix + "engine"
	ManagedAgentsVaultLLMBaseURLKey    = ManagedAgentsMetadataPrefix + "llm_base_url"
	ManagedAgentsVaultKindKey          = ManagedAgentsMetadataPrefix + "kind"
	ManagedAgentsVaultVersionKey       = ManagedAgentsMetadataPrefix + "version"
	ManagedAgentsVaultTargetDomainsKey = ManagedAgentsMetadataPrefix + "target_domains"
	ManagedAgentsVaultProtocolKey      = ManagedAgentsMetadataPrefix + "protocol"
	ManagedAgentsVaultTLSModeKey       = ManagedAgentsMetadataPrefix + "tls_mode"
	ManagedAgentsVaultFailurePolicyKey = ManagedAgentsMetadataPrefix + "failure_policy"
	ManagedAgentsVaultHeadersJSONKey   = ManagedAgentsMetadataPrefix + "headers_json"

	ManagedAgentsVaultRoleLLM         = "llm"
	ManagedAgentsVaultRoleCredential  = "credential"
	ManagedAgentsVaultKindHTTPHeaders = "http_headers"
	ManagedAgentsEngineClaude         = "claude"
	ManagedAgentsEngineCodex          = "codex"
)

type ManagedVaultConfig struct {
	Role          string
	Engine        string
	LLMBaseURL    string
	Kind          string
	Version       string
	TargetDomains []string
	Protocol      string
	TLSMode       string
	FailurePolicy string
	Headers       map[string]string
}

func ManagedVaultConfigFromMetadata(metadata map[string]string) ManagedVaultConfig {
	return ManagedVaultConfig{
		Role:          normalizeManagedMetadataValue(metadata[ManagedAgentsVaultRoleKey]),
		Engine:        normalizeManagedMetadataValue(metadata[ManagedAgentsVaultEngineKey]),
		LLMBaseURL:    strings.TrimSpace(metadata[ManagedAgentsVaultLLMBaseURLKey]),
		Kind:          normalizeManagedMetadataValue(metadata[ManagedAgentsVaultKindKey]),
		Version:       strings.TrimSpace(metadata[ManagedAgentsVaultVersionKey]),
		TargetDomains: managedTargetDomainsFromMetadata(metadata[ManagedAgentsVaultTargetDomainsKey]),
		Protocol:      normalizeManagedMetadataValue(metadata[ManagedAgentsVaultProtocolKey]),
		TLSMode:       normalizeManagedMetadataEnumValue(metadata[ManagedAgentsVaultTLSModeKey]),
		FailurePolicy: normalizeManagedMetadataEnumValue(metadata[ManagedAgentsVaultFailurePolicyKey]),
		Headers:       managedHeadersFromMetadata(metadata[ManagedAgentsVaultHeadersJSONKey]),
	}
}

func ValidateManagedVaultMetadata(metadata map[string]string) error {
	if err := validateManagedMetadataScope(metadata, ManagedMetadataScopeVault); err != nil {
		return err
	}

	config := ManagedVaultConfigFromMetadata(metadata)

	if config.Role == "" {
		if config.hasManagedVaultConfig() {
			return fmt.Errorf("%s is required when sandbox0 managed-agents vault metadata is set", ManagedAgentsVaultRoleKey)
		}
		return nil
	}
	switch config.Role {
	case ManagedAgentsVaultRoleLLM:
		return validateManagedLLMVaultConfig(config)
	case ManagedAgentsVaultRoleCredential:
		return validateManagedCredentialVaultConfig(config)
	default:
		return fmt.Errorf("%s must be one of %q or %q", ManagedAgentsVaultRoleKey, ManagedAgentsVaultRoleLLM, ManagedAgentsVaultRoleCredential)
	}
}

func validateManagedLLMVaultConfig(config ManagedVaultConfig) error {
	if config.Kind != "" || config.Version != "" || len(config.TargetDomains) > 0 || len(config.Headers) > 0 || config.Protocol != "" || config.TLSMode != "" || config.FailurePolicy != "" {
		return fmt.Errorf("%s=%q only supports %s, %s, and %s", ManagedAgentsVaultRoleKey, ManagedAgentsVaultRoleLLM, ManagedAgentsVaultRoleKey, ManagedAgentsVaultEngineKey, ManagedAgentsVaultLLMBaseURLKey)
	}
	if config.Engine == "" {
		return fmt.Errorf("%s is required when %s is %q", ManagedAgentsVaultEngineKey, ManagedAgentsVaultRoleKey, ManagedAgentsVaultRoleLLM)
	}
	if !IsSupportedManagedAgentsEngine(config.Engine) {
		return fmt.Errorf("%s must be one of %q or %q", ManagedAgentsVaultEngineKey, ManagedAgentsEngineClaude, ManagedAgentsEngineCodex)
	}
	if config.LLMBaseURL == "" {
		return nil
	}
	parsedURL, err := url.Parse(config.LLMBaseURL)
	if err != nil || strings.TrimSpace(parsedURL.Hostname()) == "" {
		return fmt.Errorf("%s must be a valid URL", ManagedAgentsVaultLLMBaseURLKey)
	}
	scheme := strings.ToLower(strings.TrimSpace(parsedURL.Scheme))
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%s must use http or https", ManagedAgentsVaultLLMBaseURLKey)
	}
	return nil
}

func validateManagedCredentialVaultConfig(config ManagedVaultConfig) error {
	if config.Engine != "" || config.LLMBaseURL != "" {
		return fmt.Errorf("%s=%q does not support %s or %s", ManagedAgentsVaultRoleKey, ManagedAgentsVaultRoleCredential, ManagedAgentsVaultEngineKey, ManagedAgentsVaultLLMBaseURLKey)
	}
	if config.Version != "" && config.Version != "1" {
		return fmt.Errorf("%s must be %q", ManagedAgentsVaultVersionKey, "1")
	}
	if config.Kind != ManagedAgentsVaultKindHTTPHeaders {
		return fmt.Errorf("%s must be %q when %s is %q", ManagedAgentsVaultKindKey, ManagedAgentsVaultKindHTTPHeaders, ManagedAgentsVaultRoleKey, ManagedAgentsVaultRoleCredential)
	}
	if len(config.TargetDomains) == 0 {
		return fmt.Errorf("%s is required when %s is %q", ManagedAgentsVaultTargetDomainsKey, ManagedAgentsVaultRoleKey, ManagedAgentsVaultRoleCredential)
	}
	if config.Protocol != "" && config.Protocol != "http" && config.Protocol != "https" {
		return fmt.Errorf("%s must be %q or %q", ManagedAgentsVaultProtocolKey, "http", "https")
	}
	if config.TLSMode != "" && config.TLSMode != "passthrough" && config.TLSMode != "terminate-reoriginate" {
		return fmt.Errorf("%s must be %q or %q", ManagedAgentsVaultTLSModeKey, "passthrough", "terminate-reoriginate")
	}
	if config.FailurePolicy != "" && config.FailurePolicy != "fail-closed" && config.FailurePolicy != "fail-open" {
		return fmt.Errorf("%s must be %q or %q", ManagedAgentsVaultFailurePolicyKey, "fail-closed", "fail-open")
	}
	if len(config.Headers) == 0 {
		return fmt.Errorf("%s is required when %s is %q", ManagedAgentsVaultHeadersJSONKey, ManagedAgentsVaultKindKey, ManagedAgentsVaultKindHTTPHeaders)
	}
	for name, value := range config.Headers {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s must be a JSON object with non-empty header names and values", ManagedAgentsVaultHeadersJSONKey)
		}
	}
	return nil
}

func IsSupportedManagedAgentsEngine(engine string) bool {
	switch normalizeManagedMetadataValue(engine) {
	case ManagedAgentsEngineClaude, ManagedAgentsEngineCodex:
		return true
	default:
		return false
	}
}

func (c ManagedVaultConfig) hasManagedVaultConfig() bool {
	return c.Engine != "" ||
		c.LLMBaseURL != "" ||
		c.Kind != "" ||
		c.Version != "" ||
		len(c.TargetDomains) > 0 ||
		c.Protocol != "" ||
		c.TLSMode != "" ||
		c.FailurePolicy != "" ||
		len(c.Headers) > 0
}

func managedTargetDomainsFromMetadata(value string) []string {
	out := make([]string, 0)
	seen := make(map[string]struct{})
	for _, item := range strings.Split(value, ",") {
		domain := managedTargetDomain(strings.TrimSpace(item))
		if domain == "" {
			continue
		}
		if _, ok := seen[domain]; ok {
			continue
		}
		seen[domain] = struct{}{}
		out = append(out, domain)
	}
	sort.Strings(out)
	return out
}

func managedTargetDomain(value string) string {
	if value == "" {
		return ""
	}
	if parsedURL, err := url.Parse(value); err == nil && strings.TrimSpace(parsedURL.Hostname()) != "" {
		return strings.ToLower(strings.TrimSpace(parsedURL.Hostname()))
	}
	trimmed := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "https://")
	trimmed = strings.TrimPrefix(trimmed, "http://")
	trimmed = strings.Trim(trimmed, "/")
	if index := strings.Index(trimmed, "/"); index >= 0 {
		trimmed = trimmed[:index]
	}
	return strings.TrimSpace(trimmed)
}

func managedHeadersFromMetadata(value string) map[string]string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(trimmed), &headers); err != nil {
		return map[string]string{"": ""}
	}
	return headers
}

func normalizeManagedMetadataEnumValue(value string) string {
	return strings.ReplaceAll(normalizeManagedMetadataValue(value), "_", "-")
}
