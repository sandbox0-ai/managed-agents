package managedagents

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

func bootstrapStateDigestFor(record *SessionRecord, engine map[string]any, runtime *RuntimeRecord) (string, error) {
	if record == nil || runtime == nil {
		return "", nil
	}
	payload := map[string]any{
		"schema":                  1,
		"session_id":              strings.TrimSpace(record.ID),
		"vendor":                  strings.TrimSpace(record.Vendor),
		"sandbox_id":              strings.TrimSpace(runtime.SandboxID),
		"runtime_generation":      runtime.RuntimeGeneration,
		"vendor_session_id":       strings.TrimSpace(runtime.VendorSessionID),
		"working_directory":       strings.TrimSpace(record.WorkingDirectory),
		"environment_id":          strings.TrimSpace(record.EnvironmentID),
		"environment_artifact_id": strings.TrimSpace(record.EnvironmentArtifactID),
		"agent":                   record.Agent,
		"resources":               record.Resources,
		"vault_ids":               record.VaultIDs,
		"engine":                  engine,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func runtimeBootstrapCurrent(record *SessionRecord, engine map[string]any, runtime *RuntimeRecord) (bool, string, error) {
	digest, err := bootstrapStateDigestFor(record, engine, runtime)
	if err != nil || digest == "" {
		return false, digest, err
	}
	return runtime.BootstrapSyncedAt != nil && strings.TrimSpace(runtime.BootstrapStateDigest) == digest, digest, nil
}
