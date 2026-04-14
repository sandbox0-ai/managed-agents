package managedagents

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	// ManagedAgentsSessionHardTTLSecondsKey lets official SDK callers set the runtime sandbox hard TTL through session metadata.
	ManagedAgentsSessionHardTTLSecondsKey = ManagedAgentsMetadataPrefix + "hard_ttl_seconds"
)

// ManagedSessionConfig is the Sandbox0-specific session runtime policy decoded from official session metadata.
type ManagedSessionConfig struct {
	SandboxHardTTLSeconds *int
}

// ManagedSessionConfigFromMetadata parses Sandbox0 managed-agents session metadata.
func ManagedSessionConfigFromMetadata(metadata map[string]string) (ManagedSessionConfig, error) {
	var config ManagedSessionConfig
	raw, ok := metadata[ManagedAgentsSessionHardTTLSecondsKey]
	if !ok {
		return config, nil
	}
	seconds, err := parseManagedSessionSeconds(ManagedAgentsSessionHardTTLSecondsKey, raw)
	if err != nil {
		return ManagedSessionConfig{}, err
	}
	config.SandboxHardTTLSeconds = &seconds
	return config, nil
}

// ValidateManagedSessionMetadata rejects malformed or unsupported Sandbox0 managed-agents session metadata.
func ValidateManagedSessionMetadata(metadata map[string]string) error {
	if err := validateManagedMetadataScope(metadata, ManagedMetadataScopeSession); err != nil {
		return err
	}
	_, err := ManagedSessionConfigFromMetadata(metadata)
	return err
}

func validateManagedSessionMetadataPatch(existing map[string]string, patch MetadataPatchField) error {
	if !patch.Set {
		return nil
	}
	if patch.Clear {
		if _, ok := existing[ManagedAgentsSessionHardTTLSecondsKey]; ok {
			return fmt.Errorf("%s cannot be changed after session creation", ManagedAgentsSessionHardTTLSecondsKey)
		}
		return nil
	}
	for key := range patch.Values {
		if strings.TrimSpace(key) == ManagedAgentsSessionHardTTLSecondsKey {
			return fmt.Errorf("%s cannot be changed after session creation", ManagedAgentsSessionHardTTLSecondsKey)
		}
	}
	return nil
}

func parseManagedSessionSeconds(key, value string) (int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, fmt.Errorf("%s must be a non-negative integer number of seconds", key)
	}
	seconds, err := strconv.ParseInt(trimmed, 10, 32)
	if err != nil || seconds < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer number of seconds", key)
	}
	return int(seconds), nil
}
