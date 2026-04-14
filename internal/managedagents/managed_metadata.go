package managedagents

import (
	"fmt"
	"strings"
)

const ManagedAgentsMetadataPrefix = "sandbox0.managed_agents."

type ManagedMetadataScope string

const (
	ManagedMetadataScopeEnvironment ManagedMetadataScope = "environment"
	ManagedMetadataScopeSession     ManagedMetadataScope = "session"
	ManagedMetadataScopeVault       ManagedMetadataScope = "vault"
)

var managedMetadataScopes = map[string]map[ManagedMetadataScope]struct{}{
	ManagedAgentsSessionHardTTLSecondsKey: {ManagedMetadataScopeSession: {}},
	ManagedAgentsVaultRoleKey:             {ManagedMetadataScopeVault: {}},
	ManagedAgentsVaultEngineKey:           {ManagedMetadataScopeVault: {}},
	ManagedAgentsVaultLLMBaseURLKey:       {ManagedMetadataScopeVault: {}},
}

func validateManagedMetadataScope(metadata map[string]string, scope ManagedMetadataScope) error {
	for rawKey := range metadata {
		key := strings.TrimSpace(rawKey)
		if !strings.HasPrefix(key, ManagedAgentsMetadataPrefix) {
			continue
		}
		if !managedMetadataKeySupportsScope(key, scope) {
			return fmt.Errorf("%s is not supported sandbox0 managed-agents %s metadata", key, scope)
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

func ValidateManagedEnvironmentMetadata(metadata map[string]string) error {
	return validateManagedMetadataScope(metadata, ManagedMetadataScopeEnvironment)
}
