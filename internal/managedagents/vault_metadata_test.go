package managedagents

import (
	"strings"
	"testing"
)

func TestValidateManagedVaultMetadataAcceptsLLMVault(t *testing.T) {
	err := ValidateManagedVaultMetadata(map[string]string{
		ManagedAgentsVaultRoleKey:       ManagedAgentsVaultRoleLLM,
		ManagedAgentsVaultEngineKey:     ManagedAgentsEngineClaude,
		ManagedAgentsVaultLLMBaseURLKey: "https://api.anthropic.com",
	})
	if err != nil {
		t.Fatalf("ValidateManagedVaultMetadata: %v", err)
	}
}

func TestValidateManagedVaultMetadataAcceptsCodexLLMVault(t *testing.T) {
	err := ValidateManagedVaultMetadata(map[string]string{
		ManagedAgentsVaultRoleKey:       ManagedAgentsVaultRoleLLM,
		ManagedAgentsVaultEngineKey:     ManagedAgentsEngineCodex,
		ManagedAgentsVaultLLMBaseURLKey: "https://api.openai.com/v1",
	})
	if err != nil {
		t.Fatalf("ValidateManagedVaultMetadata: %v", err)
	}
}

func TestValidateManagedVaultMetadataRejectsUnknownReservedKey(t *testing.T) {
	key := ManagedAgentsMetadataPrefix + "provider"
	err := ValidateManagedVaultMetadata(map[string]string{
		key: "anthropic",
	})
	if err == nil || !strings.Contains(err.Error(), key) {
		t.Fatalf("ValidateManagedVaultMetadata error = %v, want unknown reserved key rejection", err)
	}
}

func TestValidateManagedVaultMetadataAllowsCustomMetadata(t *testing.T) {
	err := ValidateManagedVaultMetadata(map[string]string{
		"backend.provider": "anthropic",
	})
	if err != nil {
		t.Fatalf("ValidateManagedVaultMetadata error = %v, want custom metadata allowed", err)
	}
}
