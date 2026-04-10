package managedagentsruntime

import (
	"fmt"
	"net/url"
	"strings"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
)

type managedLLMCredential struct {
	CredentialID string
	Token        string
	BaseURL      string
}

const (
	managedAnthropicDefaultBaseURL = "https://api.anthropic.com"
	managedAnthropicFakeAPIKey     = "managed-agent-sandbox0-fake-key"
	managedAnthropicFakeAuthToken  = "managed-agent-sandbox0-fake-token"
)

func applyManagedLLMEnv(vendor string, engine map[string]any, credentials []gatewaymanagedagents.StoredCredential) (map[string]any, *managedLLMCredential, error) {
	out := cloneMap(engine)
	if normalizeManagedRuntimeMetadataValue(vendor) != gatewaymanagedagents.ManagedAgentCredentialVendorClaude {
		return out, nil, nil
	}
	credential, err := selectManagedLLMCredential(vendor, credentials)
	if err != nil || credential == nil {
		return out, credential, err
	}
	env := cloneMap(mapValue(out["env"]))
	resolvedBaseURL := resolvedManagedLLMBaseURL(vendor, credential)
	if existingBaseURL := strings.TrimSpace(stringValue(env["ANTHROPIC_BASE_URL"])); existingBaseURL != "" {
		canonicalExisting, err := canonicalManagedRuntimeURL(existingBaseURL)
		if err != nil {
			return nil, nil, fmt.Errorf("engine ANTHROPIC_BASE_URL is invalid: %w", err)
		}
		canonicalResolved, err := canonicalManagedRuntimeURL(resolvedBaseURL)
		if err != nil {
			return nil, nil, err
		}
		if canonicalExisting != canonicalResolved {
			return nil, nil, fmt.Errorf("managed-agent llm credential %s base URL %q conflicts with engine ANTHROPIC_BASE_URL %q", credential.CredentialID, resolvedBaseURL, existingBaseURL)
		}
	}
	env["ANTHROPIC_API_KEY"] = managedAnthropicFakeAPIKey
	env["ANTHROPIC_AUTH_TOKEN"] = managedAnthropicFakeAuthToken
	env["ANTHROPIC_BASE_URL"] = resolvedBaseURL
	out["env"] = env
	extraArgs := cloneMap(mapValue(out["extra_args"]))
	// Claude Code only honors env-based Anthropic auth reliably in bare mode.
	extraArgs["bare"] = nil
	out["extra_args"] = extraArgs
	credentialCopy := *credential
	credentialCopy.BaseURL = resolvedBaseURL
	return out, &credentialCopy, nil
}

func selectManagedLLMCredential(vendor string, credentials []gatewaymanagedagents.StoredCredential) (*managedLLMCredential, error) {
	var selected *managedLLMCredential
	for _, credential := range credentials {
		candidate, err := managedLLMCredentialFromVault(vendor, credential)
		if err != nil {
			return nil, err
		}
		if candidate == nil {
			continue
		}
		if selected != nil {
			return nil, fmt.Errorf("multiple vault credentials are tagged as managed-agent llm credentials for vendor %s", strings.TrimSpace(vendor))
		}
		selected = candidate
	}
	return selected, nil
}

func managedLLMCredentialFromVault(vendor string, credential gatewaymanagedagents.StoredCredential) (*managedLLMCredential, error) {
	metadata := credential.Snapshot.Metadata
	kind := normalizeManagedRuntimeMetadataValue(metadata[gatewaymanagedagents.ManagedAgentCredentialKindKey])
	if kind == "" {
		return nil, nil
	}
	if kind != gatewaymanagedagents.ManagedAgentCredentialKindLLM {
		return nil, fmt.Errorf("vault credential %s has unsupported %s %q", credential.Snapshot.ID, gatewaymanagedagents.ManagedAgentCredentialKindKey, metadata[gatewaymanagedagents.ManagedAgentCredentialKindKey])
	}
	resolvedVendor := normalizeManagedRuntimeMetadataValue(vendor)
	metadataVendor := normalizeManagedRuntimeMetadataValue(metadata[gatewaymanagedagents.ManagedAgentCredentialVendorKey])
	if metadataVendor != "" && metadataVendor != resolvedVendor {
		return nil, nil
	}
	provider := normalizeManagedRuntimeMetadataValue(metadata[gatewaymanagedagents.ManagedAgentCredentialProviderKey])
	if resolvedVendor == gatewaymanagedagents.ManagedAgentCredentialVendorClaude && provider != "" && provider != gatewaymanagedagents.ManagedAgentCredentialProviderAnthropic && provider != gatewaymanagedagents.ManagedAgentCredentialProviderClaude {
		return nil, fmt.Errorf("vault credential %s has unsupported %s %q for vendor %s", credential.Snapshot.ID, gatewaymanagedagents.ManagedAgentCredentialProviderKey, metadata[gatewaymanagedagents.ManagedAgentCredentialProviderKey], vendor)
	}
	token, err := managedLLMTokenFromCredential(credential)
	if err != nil {
		return nil, err
	}
	baseURL := strings.TrimSpace(metadata[gatewaymanagedagents.ManagedAgentCredentialBaseURLKey])
	if baseURL == "" {
		baseURL = strings.TrimSpace(credential.Snapshot.Auth.MCPServerURL)
	}
	if baseURL == "" {
		baseURL = strings.TrimSpace(stringValue(credential.Secret["mcp_server_url"]))
	}
	if baseURL != "" {
		canonicalBaseURL, err := canonicalManagedRuntimeURL(baseURL)
		if err != nil {
			return nil, fmt.Errorf("vault credential %s has invalid managed-agent llm base URL", credential.Snapshot.ID)
		}
		baseURL = canonicalBaseURL
	}
	return &managedLLMCredential{
		CredentialID: credential.Snapshot.ID,
		Token:        token,
		BaseURL:      baseURL,
	}, nil
}

func normalizeManagedRuntimeMetadataValue(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func managedLLMTokenFromCredential(credential gatewaymanagedagents.StoredCredential) (string, error) {
	switch strings.TrimSpace(credential.Snapshot.Auth.Type) {
	case "static_bearer":
		token := strings.TrimSpace(stringValue(credential.Secret["token"]))
		if token == "" {
			return "", fmt.Errorf("vault credential %s is missing token", credential.Snapshot.ID)
		}
		return token, nil
	case "mcp_oauth":
		accessToken := strings.TrimSpace(stringValue(credential.Secret["access_token"]))
		if accessToken == "" {
			return "", fmt.Errorf("vault credential %s is missing access_token", credential.Snapshot.ID)
		}
		return accessToken, nil
	default:
		return "", fmt.Errorf("vault credential %s has unsupported auth type %q for managed-agent llm projection", credential.Snapshot.ID, credential.Snapshot.Auth.Type)
	}
}

func resolvedManagedLLMBaseURL(vendor string, credential *managedLLMCredential) string {
	if credential != nil && strings.TrimSpace(credential.BaseURL) != "" {
		return strings.TrimSpace(credential.BaseURL)
	}
	if normalizeManagedRuntimeMetadataValue(vendor) == gatewaymanagedagents.ManagedAgentCredentialVendorClaude {
		return managedAnthropicDefaultBaseURL
	}
	return ""
}

func canonicalManagedRuntimeURL(raw string) (string, error) {
	parsedURL, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || strings.TrimSpace(parsedURL.Hostname()) == "" {
		return "", fmt.Errorf("invalid URL %q", raw)
	}
	parsedURL.Scheme = strings.ToLower(strings.TrimSpace(parsedURL.Scheme))
	parsedURL.Host = strings.ToLower(strings.TrimSpace(parsedURL.Host))
	if parsedURL.Path == "/" {
		parsedURL.Path = ""
	}
	return strings.TrimRight(parsedURL.String(), "/"), nil
}
