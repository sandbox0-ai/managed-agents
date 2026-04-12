package managedagentsruntime

import (
	"fmt"
	"net/url"
	"strings"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
)

type managedLLMCredential struct {
	VaultID      string
	CredentialID string
	Token        string
	BaseURL      string
}

const (
	managedAnthropicDefaultBaseURL = "https://api.anthropic.com"
	managedAnthropicFakeAPIKey     = "managed-agent-sandbox0-fake-key"
	managedAnthropicFakeAuthToken  = "managed-agent-sandbox0-fake-token"
)

func applyManagedLLMEnv(vendor string, engine map[string]any, vaults []managedVaultCredentials) (map[string]any, *managedLLMCredential, error) {
	out := cloneMap(engine)
	if normalizeManagedRuntimeMetadataValue(vendor) != gatewaymanagedagents.ManagedAgentsEngineClaude {
		return out, nil, nil
	}
	credential, err := selectManagedLLMCredential(vendor, vaults)
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

func selectManagedLLMCredential(vendor string, vaults []managedVaultCredentials) (*managedLLMCredential, error) {
	var selected *managedVaultCredentials
	var selectedConfig gatewaymanagedagents.ManagedVaultConfig
	for i := range vaults {
		config := gatewaymanagedagents.ManagedVaultConfigFromMetadata(vaults[i].vault.Metadata)
		if config.Role != gatewaymanagedagents.ManagedAgentsVaultRoleLLM {
			continue
		}
		if selected != nil {
			return nil, errorsForMultipleLLMVaults(selected.vault.ID, vaults[i].vault.ID)
		}
		selected = &vaults[i]
		selectedConfig = config
	}
	if selected == nil {
		return nil, nil
	}
	return managedLLMCredentialFromVault(vendor, *selected, selectedConfig)
}

func errorsForMultipleLLMVaults(firstID, secondID string) error {
	return fmt.Errorf("session can attach exactly one %s=%q vault, got %s and %s", gatewaymanagedagents.ManagedAgentsVaultRoleKey, gatewaymanagedagents.ManagedAgentsVaultRoleLLM, strings.TrimSpace(firstID), strings.TrimSpace(secondID))
}

func managedLLMCredentialFromVault(vendor string, vault managedVaultCredentials, config gatewaymanagedagents.ManagedVaultConfig) (*managedLLMCredential, error) {
	resolvedVendor := normalizeManagedRuntimeMetadataValue(vendor)
	if config.Engine == "" {
		return nil, fmt.Errorf("llm vault %s is missing %s", vault.vault.ID, gatewaymanagedagents.ManagedAgentsVaultEngineKey)
	}
	if config.Engine != resolvedVendor {
		return nil, fmt.Errorf("llm vault %s uses engine %q but session vendor is %q", vault.vault.ID, config.Engine, vendor)
	}
	if len(vault.credentials) == 0 {
		return nil, fmt.Errorf("llm vault %s has no active credentials", vault.vault.ID)
	}
	if len(vault.credentials) > 1 {
		return nil, fmt.Errorf("llm vault %s must contain exactly one active credential", vault.vault.ID)
	}
	credential := vault.credentials[0]
	if strings.TrimSpace(credential.Snapshot.Auth.Type) != "static_bearer" {
		return nil, fmt.Errorf("llm vault credential %s must use static_bearer auth", credential.Snapshot.ID)
	}
	if credentialMCPServerURL(credential) != "" {
		return nil, fmt.Errorf("llm vault credential %s must not set mcp_server_url", credential.Snapshot.ID)
	}
	token, err := managedLLMTokenFromCredential(credential)
	if err != nil {
		return nil, err
	}
	baseURL := strings.TrimSpace(config.LLMBaseURL)
	if baseURL != "" {
		canonicalBaseURL, err := canonicalManagedRuntimeURL(baseURL)
		if err != nil {
			return nil, fmt.Errorf("llm vault %s has invalid %s", vault.vault.ID, gatewaymanagedagents.ManagedAgentsVaultLLMBaseURLKey)
		}
		baseURL = canonicalBaseURL
	}
	return &managedLLMCredential{
		VaultID:      vault.vault.ID,
		CredentialID: credential.Snapshot.ID,
		Token:        token,
		BaseURL:      baseURL,
	}, nil
}

func normalizeManagedRuntimeMetadataValue(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func managedLLMTokenFromCredential(credential gatewaymanagedagents.StoredCredential) (string, error) {
	token := strings.TrimSpace(stringValue(credential.Secret["token"]))
	if token == "" {
		return "", fmt.Errorf("vault credential %s is missing token", credential.Snapshot.ID)
	}
	return token, nil
}

func resolvedManagedLLMBaseURL(vendor string, credential *managedLLMCredential) string {
	if credential != nil && strings.TrimSpace(credential.BaseURL) != "" {
		return strings.TrimSpace(credential.BaseURL)
	}
	if normalizeManagedRuntimeMetadataValue(vendor) == gatewaymanagedagents.ManagedAgentsEngineClaude {
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
