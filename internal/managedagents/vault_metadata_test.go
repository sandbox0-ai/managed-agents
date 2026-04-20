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

func TestValidateManagedVaultMetadataAcceptsHTTPHeaderCredentialVault(t *testing.T) {
	err := ValidateManagedVaultMetadata(map[string]string{
		ManagedAgentsVaultRoleKey:          ManagedAgentsVaultRoleCredential,
		ManagedAgentsVaultKindKey:          ManagedAgentsVaultKindHTTPHeaders,
		ManagedAgentsVaultVersionKey:       "1",
		ManagedAgentsVaultTargetDomainsKey: "https://App.Example.com/api,app.example.com",
		ManagedAgentsVaultProtocolKey:      "https",
		ManagedAgentsVaultTLSModeKey:       "terminate_reoriginate",
		ManagedAgentsVaultFailurePolicyKey: "fail_closed",
		ManagedAgentsVaultHeadersJSONKey:   `{"x-api-key":"{{ .token }}"}`,
	})
	if err != nil {
		t.Fatalf("ValidateManagedVaultMetadata: %v", err)
	}
	config := ManagedVaultConfigFromMetadata(map[string]string{
		ManagedAgentsVaultRoleKey:          ManagedAgentsVaultRoleCredential,
		ManagedAgentsVaultKindKey:          ManagedAgentsVaultKindHTTPHeaders,
		ManagedAgentsVaultTargetDomainsKey: "https://App.Example.com/api,app.example.com",
		ManagedAgentsVaultFailurePolicyKey: "fail_closed",
		ManagedAgentsVaultHeadersJSONKey:   `{"x-api-key":"{{ .token }}"}`,
	})
	if len(config.TargetDomains) != 1 || config.TargetDomains[0] != "app.example.com" {
		t.Fatalf("target domains = %#v, want normalized unique domain", config.TargetDomains)
	}
	if config.FailurePolicy != "fail-closed" {
		t.Fatalf("failure policy = %q, want fail-closed", config.FailurePolicy)
	}
}

func TestValidateManagedVaultMetadataRejectsIncompleteCredentialVault(t *testing.T) {
	err := ValidateManagedVaultMetadata(map[string]string{
		ManagedAgentsVaultRoleKey:        ManagedAgentsVaultRoleCredential,
		ManagedAgentsVaultKindKey:        ManagedAgentsVaultKindHTTPHeaders,
		ManagedAgentsVaultHeadersJSONKey: `{"x-api-key":"{{ .token }}"}`,
	})
	if err == nil || !strings.Contains(err.Error(), ManagedAgentsVaultTargetDomainsKey) {
		t.Fatalf("ValidateManagedVaultMetadata error = %v, want target domain rejection", err)
	}
}

func TestValidateManagedVaultMetadataRejectsLLMFieldsOnCredentialVault(t *testing.T) {
	err := ValidateManagedVaultMetadata(map[string]string{
		ManagedAgentsVaultRoleKey:          ManagedAgentsVaultRoleCredential,
		ManagedAgentsVaultKindKey:          ManagedAgentsVaultKindHTTPHeaders,
		ManagedAgentsVaultTargetDomainsKey: "api.example.com",
		ManagedAgentsVaultHeadersJSONKey:   `{"authorization":"{{ .authorization }}"}`,
		ManagedAgentsVaultEngineKey:        ManagedAgentsEngineClaude,
	})
	if err == nil || !strings.Contains(err.Error(), ManagedAgentsVaultEngineKey) {
		t.Fatalf("ValidateManagedVaultMetadata error = %v, want engine rejection", err)
	}
}

func TestValidateManagedVaultMetadataRejectsCredentialFieldsOnLLMVault(t *testing.T) {
	err := ValidateManagedVaultMetadata(map[string]string{
		ManagedAgentsVaultRoleKey:    ManagedAgentsVaultRoleLLM,
		ManagedAgentsVaultEngineKey:  ManagedAgentsEngineClaude,
		ManagedAgentsVaultVersionKey: "1",
	})
	if err == nil || !strings.Contains(err.Error(), "only supports") {
		t.Fatalf("ValidateManagedVaultMetadata error = %v, want unsupported credential field rejection", err)
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
