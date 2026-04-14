package managedagents

import (
	"strings"
	"testing"
)

func TestManagedSessionConfigFromMetadataParsesHardTTL(t *testing.T) {
	config, err := ManagedSessionConfigFromMetadata(map[string]string{
		ManagedAgentsSessionHardTTLSecondsKey: " 3600 ",
	})
	if err != nil {
		t.Fatalf("ManagedSessionConfigFromMetadata: %v", err)
	}
	if config.SandboxHardTTLSeconds == nil || *config.SandboxHardTTLSeconds != 3600 {
		t.Fatalf("SandboxHardTTLSeconds = %v, want 3600", config.SandboxHardTTLSeconds)
	}
}

func TestManagedSessionConfigFromMetadataAllowsZeroHardTTL(t *testing.T) {
	config, err := ManagedSessionConfigFromMetadata(map[string]string{
		ManagedAgentsSessionHardTTLSecondsKey: "0",
	})
	if err != nil {
		t.Fatalf("ManagedSessionConfigFromMetadata: %v", err)
	}
	if config.SandboxHardTTLSeconds == nil || *config.SandboxHardTTLSeconds != 0 {
		t.Fatalf("SandboxHardTTLSeconds = %v, want 0", config.SandboxHardTTLSeconds)
	}
}

func TestManagedSessionConfigFromMetadataRejectsInvalidHardTTL(t *testing.T) {
	tests := []string{"", "-1", "ten", "2147483648"}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			_, err := ManagedSessionConfigFromMetadata(map[string]string{
				ManagedAgentsSessionHardTTLSecondsKey: value,
			})
			if err == nil || !strings.Contains(err.Error(), ManagedAgentsSessionHardTTLSecondsKey) {
				t.Fatalf("ManagedSessionConfigFromMetadata error = %v, want hard_ttl rejection", err)
			}
		})
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

func TestValidateManagedSessionMetadataPatchRejectsHardTTLUpdate(t *testing.T) {
	newValue := "0"
	err := validateManagedSessionMetadataPatch(map[string]string{}, MetadataPatchField{
		Set: true,
		Values: map[string]*string{
			" " + ManagedAgentsSessionHardTTLSecondsKey + " ": &newValue,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be changed") {
		t.Fatalf("validateManagedSessionMetadataPatch error = %v, want immutable hard_ttl rejection", err)
	}
}
