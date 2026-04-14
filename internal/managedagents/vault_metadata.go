package managedagents

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	ManagedAgentsVaultRoleKey       = ManagedAgentsMetadataPrefix + "role"
	ManagedAgentsVaultEngineKey     = ManagedAgentsMetadataPrefix + "engine"
	ManagedAgentsVaultLLMBaseURLKey = ManagedAgentsMetadataPrefix + "llm_base_url"

	ManagedAgentsVaultRoleLLM = "llm"
	ManagedAgentsEngineClaude = "claude"
)

type ManagedVaultConfig struct {
	Role       string
	Engine     string
	LLMBaseURL string
}

func ManagedVaultConfigFromMetadata(metadata map[string]string) ManagedVaultConfig {
	return ManagedVaultConfig{
		Role:       normalizeManagedVaultMetadataValue(metadata[ManagedAgentsVaultRoleKey]),
		Engine:     normalizeManagedVaultMetadataValue(metadata[ManagedAgentsVaultEngineKey]),
		LLMBaseURL: strings.TrimSpace(metadata[ManagedAgentsVaultLLMBaseURLKey]),
	}
}

func ValidateManagedVaultMetadata(metadata map[string]string) error {
	if err := validateManagedMetadataScope(metadata, ManagedMetadataScopeVault); err != nil {
		return err
	}

	config := ManagedVaultConfigFromMetadata(metadata)

	if config.Role == "" {
		if config.Engine != "" || config.LLMBaseURL != "" {
			return fmt.Errorf("%s is required when sandbox0 managed-agents vault metadata is set", ManagedAgentsVaultRoleKey)
		}
		return nil
	}
	if config.Role != ManagedAgentsVaultRoleLLM {
		return fmt.Errorf("%s must be %q", ManagedAgentsVaultRoleKey, ManagedAgentsVaultRoleLLM)
	}
	if config.Engine == "" {
		return fmt.Errorf("%s is required when %s is %q", ManagedAgentsVaultEngineKey, ManagedAgentsVaultRoleKey, ManagedAgentsVaultRoleLLM)
	}
	if config.Engine != ManagedAgentsEngineClaude {
		return fmt.Errorf("%s must be %q", ManagedAgentsVaultEngineKey, ManagedAgentsEngineClaude)
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

func normalizeManagedVaultMetadataValue(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
