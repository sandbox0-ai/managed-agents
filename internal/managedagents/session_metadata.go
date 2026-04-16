package managedagents

// ManagedSessionConfig is the Sandbox0-specific session runtime policy decoded from official session metadata.
type ManagedSessionConfig struct {
}

// ManagedSessionConfigFromMetadata parses Sandbox0 managed-agents session metadata.
func ManagedSessionConfigFromMetadata(metadata map[string]string) (ManagedSessionConfig, error) {
	_ = metadata
	var config ManagedSessionConfig
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
	_ = existing
	return nil
}
