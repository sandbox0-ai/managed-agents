package managedagents

import (
	"strings"
	"testing"
)

func TestValidateManagedSessionMetadataRejectsLegacyHardTTL(t *testing.T) {
	const key = ManagedAgentsMetadataPrefix + "hard_ttl_seconds"
	err := ValidateManagedSessionMetadata(map[string]string{
		key: "3600",
	})
	if err == nil || !strings.Contains(err.Error(), key) {
		t.Fatalf("ValidateManagedSessionMetadata error = %v, want legacy hard TTL key rejection", err)
	}
}

func TestValidateManagedSessionMetadataRejectsUnknownReservedKey(t *testing.T) {
	key := ManagedAgentsMetadataPrefix + "engine"
	err := ValidateManagedSessionMetadata(map[string]string{
		key: ManagedAgentsEngineClaude,
	})
	if err == nil || !strings.Contains(err.Error(), key) {
		t.Fatalf("ValidateManagedSessionMetadata error = %v, want unknown reserved key rejection", err)
	}
}

func TestValidateManagedSessionMetadataAllowsCustomMetadata(t *testing.T) {
	err := ValidateManagedSessionMetadata(map[string]string{
		"backend.llm_host": "https://llm.example.com",
	})
	if err != nil {
		t.Fatalf("ValidateManagedSessionMetadata error = %v, want custom metadata allowed", err)
	}
}
