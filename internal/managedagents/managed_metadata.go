package managedagents

import (
	"fmt"
	"strings"
)

// ManagedAgentsMetadataPrefix is reserved for Sandbox0 built-in compatibility metadata.
const ManagedAgentsMetadataPrefix = "sandbox0.managed_agents."

// ManagedMetadataScope names metadata-bearing resources that can own reserved managed-agents keys.
type ManagedMetadataScope string

const (
	ManagedMetadataScopeAgent       ManagedMetadataScope = "agent"
	ManagedMetadataScopeCredential  ManagedMetadataScope = "credential"
	ManagedMetadataScopeEnvironment ManagedMetadataScope = "environment"
	ManagedMetadataScopeSession     ManagedMetadataScope = "session"
	ManagedMetadataScopeVault       ManagedMetadataScope = "vault"
)

var managedMetadataScopes = map[string]map[ManagedMetadataScope]struct{}{
	ManagedAgentsVaultRoleKey:          {ManagedMetadataScopeVault: {}},
	ManagedAgentsVaultEngineKey:        {ManagedMetadataScopeVault: {}},
	ManagedAgentsVaultLLMBaseURLKey:    {ManagedMetadataScopeVault: {}},
	ManagedAgentsVaultKindKey:          {ManagedMetadataScopeVault: {}},
	ManagedAgentsVaultVersionKey:       {ManagedMetadataScopeVault: {}},
	ManagedAgentsVaultTargetDomainsKey: {ManagedMetadataScopeVault: {}},
	ManagedAgentsVaultProtocolKey:      {ManagedMetadataScopeVault: {}},
	ManagedAgentsVaultTLSModeKey:       {ManagedMetadataScopeVault: {}},
	ManagedAgentsVaultFailurePolicyKey: {ManagedMetadataScopeVault: {}},
	ManagedAgentsVaultHeadersJSONKey:   {ManagedMetadataScopeVault: {}},
}

func ValidateManagedAgentMetadata(metadata map[string]string) error {
	return validateManagedMetadataScope(metadata, ManagedMetadataScopeAgent)
}

func ValidateManagedCredentialMetadata(metadata map[string]string) error {
	return validateManagedMetadataScope(metadata, ManagedMetadataScopeCredential)
}

func ValidateManagedEnvironmentMetadata(metadata map[string]string) error {
	return validateManagedMetadataScope(metadata, ManagedMetadataScopeEnvironment)
}

func validateManagedMetadataScope(metadata map[string]string, scope ManagedMetadataScope) error {
	for rawKey := range metadata {
		key := strings.TrimSpace(rawKey)
		if !strings.HasPrefix(key, ManagedAgentsMetadataPrefix) {
			continue
		}
		if !managedMetadataKeySupportsScope(key, scope) {
			return fmt.Errorf("%s is a reserved sandbox0 managed-agents key and is not supported as %s metadata", key, scope)
		}
	}
	return nil
}

func managedMetadataKeySupportsScope(key string, scope ManagedMetadataScope) bool {
	scopes, ok := managedMetadataScopes[key]
	if !ok {
		return false
	}
	_, ok = scopes[scope]
	return ok
}

func normalizeManagedMetadataValue(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
