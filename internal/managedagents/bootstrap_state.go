package managedagents

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

type runtimeBootstrapState struct {
	Vendor                string           `json:"vendor"`
	EnvironmentID         string           `json:"environment_id"`
	EnvironmentArtifactID string           `json:"environment_artifact_id,omitempty"`
	WorkingDirectory      string           `json:"working_directory"`
	Agent                 map[string]any   `json:"agent,omitempty"`
	Resources             []map[string]any `json:"resources,omitempty"`
	VaultIDs              []string         `json:"vault_ids,omitempty"`
	Engine                map[string]any   `json:"engine,omitempty"`
}

func runtimeBootstrapStateHash(record *SessionRecord, engine map[string]any) (string, error) {
	if record == nil {
		return "", fmt.Errorf("runtime bootstrap state requires session record")
	}
	state := runtimeBootstrapState{
		Vendor:                record.Vendor,
		EnvironmentID:         record.EnvironmentID,
		EnvironmentArtifactID: record.EnvironmentArtifactID,
		WorkingDirectory:      record.WorkingDirectory,
		Agent:                 cloneMap(record.Agent),
		Resources:             cloneMapSlice(record.Resources),
		VaultIDs:              append([]string(nil), record.VaultIDs...),
		Engine:                cloneMap(engine),
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("marshal runtime bootstrap state: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func runtimeBootstrapStateCurrent(runtime *RuntimeRecord, stateHash string) bool {
	if runtime == nil {
		return false
	}
	return runtime.BootstrappedRuntimeGeneration == runtime.RuntimeGeneration &&
		runtime.BootstrapStateHash == stateHash
}
