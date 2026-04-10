package managedagents

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	ManagedAgentCredentialKindKey     = "managed_agent_kind"
	ManagedAgentCredentialVendorKey   = "managed_agent_vendor"
	ManagedAgentCredentialProviderKey = "managed_agent_provider"
	ManagedAgentCredentialBaseURLKey  = "managed_agent_base_url"

	ManagedAgentCredentialKindLLM           = "llm"
	ManagedAgentCredentialVendorClaude      = "claude"
	ManagedAgentCredentialProviderAnthropic = "anthropic"
	ManagedAgentCredentialProviderClaude    = "claude"
)

func ValidateManagedCredentialMetadata(metadata map[string]string) error {
	kind := normalizeManagedCredentialMetadataValue(metadata[ManagedAgentCredentialKindKey])
	vendor := normalizeManagedCredentialMetadataValue(metadata[ManagedAgentCredentialVendorKey])
	provider := normalizeManagedCredentialMetadataValue(metadata[ManagedAgentCredentialProviderKey])
	baseURL := strings.TrimSpace(metadata[ManagedAgentCredentialBaseURLKey])

	if kind == "" {
		if vendor != "" || provider != "" || baseURL != "" {
			return fmt.Errorf("%s is required when managed-agent credential metadata is set", ManagedAgentCredentialKindKey)
		}
		return nil
	}
	if kind != ManagedAgentCredentialKindLLM {
		return fmt.Errorf("%s must be %q", ManagedAgentCredentialKindKey, ManagedAgentCredentialKindLLM)
	}
	if vendor != "" && vendor != ManagedAgentCredentialVendorClaude {
		return fmt.Errorf("%s must be %q", ManagedAgentCredentialVendorKey, ManagedAgentCredentialVendorClaude)
	}
	if provider != "" && provider != ManagedAgentCredentialProviderAnthropic && provider != ManagedAgentCredentialProviderClaude {
		return fmt.Errorf("%s must be %q or %q", ManagedAgentCredentialProviderKey, ManagedAgentCredentialProviderAnthropic, ManagedAgentCredentialProviderClaude)
	}
	if baseURL == "" {
		return nil
	}
	parsedURL, err := url.Parse(baseURL)
	if err != nil || strings.TrimSpace(parsedURL.Hostname()) == "" {
		return fmt.Errorf("%s must be a valid URL", ManagedAgentCredentialBaseURLKey)
	}
	scheme := strings.ToLower(strings.TrimSpace(parsedURL.Scheme))
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%s must use http or https", ManagedAgentCredentialBaseURLKey)
	}
	return nil
}

func normalizeManagedCredentialMetadataValue(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
