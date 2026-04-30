package managedagents

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

type bootstrapVaultCredentialState struct {
	VaultID     string           `json:"vault_id"`
	Credentials []map[string]any `json:"credentials"`
}

func bootstrapStateDigestFor(record *SessionRecord, engine map[string]any, runtime *RuntimeRecord, vaultCredentials []bootstrapVaultCredentialState) (string, error) {
	if record == nil || runtime == nil {
		return "", nil
	}
	payload := map[string]any{
		"schema":                  2,
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
		"vault_credentials":       vaultCredentials,
		"engine":                  engine,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func runtimeBootstrapCurrent(record *SessionRecord, engine map[string]any, runtime *RuntimeRecord, vaultCredentials []bootstrapVaultCredentialState, forceSync bool) (bool, string, error) {
	digest, err := bootstrapStateDigestFor(record, engine, runtime, vaultCredentials)
	if err != nil || digest == "" {
		return false, digest, err
	}
	return !forceSync && runtime.BootstrapSyncedAt != nil && strings.TrimSpace(runtime.BootstrapStateDigest) == digest, digest, nil
}
