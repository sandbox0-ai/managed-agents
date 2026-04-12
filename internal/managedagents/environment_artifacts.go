package managedagents

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"runtime"
	"strings"
)

const (
	EnvironmentArtifactStatusPending  = "pending"
	EnvironmentArtifactStatusBuilding = "building"
	EnvironmentArtifactStatusReady    = "ready"
	EnvironmentArtifactStatusFailed   = "failed"
	EnvironmentArtifactStatusArchived = "archived"

	managedEnvironmentMountRoot      = "/opt/managed-env"
	ManagedEnvironmentAptMountPath   = managedEnvironmentMountRoot + "/apt"
	ManagedEnvironmentCargoMountPath = managedEnvironmentMountRoot + "/cargo"
	ManagedEnvironmentGemMountPath   = managedEnvironmentMountRoot + "/gem"
	ManagedEnvironmentGoMountPath    = managedEnvironmentMountRoot + "/go"
	ManagedEnvironmentNPMMountPath   = managedEnvironmentMountRoot + "/npm"
	ManagedEnvironmentPipMountPath   = managedEnvironmentMountRoot + "/pip"
)

var managedEnvironmentPackageManagers = []string{"apt", "cargo", "gem", "go", "npm", "pip"}

func defaultEnvironmentArtifactCompatibility() map[string]any {
	return map[string]any{
		"template_family": "managed-agent-claude-warm-v1",
		"os":              runtime.GOOS,
		"arch":            runtime.GOARCH,
		"base_image":      "managed-agent-claude-warm-v1",
	}
}

func DefaultEnvironmentArtifactCompatibility() map[string]any {
	return defaultEnvironmentArtifactCompatibility()
}

func environmentArtifactDigest(config CloudConfig, compatibility map[string]any) (string, error) {
	payload := map[string]any{
		"config":         environmentConfigToMap(config),
		"compatibility":  cloneMap(compatibility),
		"package_order":  append([]string(nil), managedEnvironmentPackageManagers...),
		"mount_contract": managedEnvironmentMountSnapshot(),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func EnvironmentArtifactDigest(config CloudConfig, compatibility map[string]any) (string, error) {
	return environmentArtifactDigest(config, compatibility)
}

func EnvironmentConfigSnapshotForArtifact(config CloudConfig) map[string]any {
	return environmentConfigToMap(config)
}

func managedEnvironmentMountPath(manager string) string {
	switch strings.TrimSpace(manager) {
	case "apt":
		return ManagedEnvironmentAptMountPath
	case "cargo":
		return ManagedEnvironmentCargoMountPath
	case "gem":
		return ManagedEnvironmentGemMountPath
	case "go":
		return ManagedEnvironmentGoMountPath
	case "npm":
		return ManagedEnvironmentNPMMountPath
	case "pip":
		return ManagedEnvironmentPipMountPath
	default:
		return ""
	}
}

func ManagedEnvironmentMountPath(manager string) string {
	return managedEnvironmentMountPath(manager)
}

func ManagedEnvironmentPackageManagers() []string {
	return append([]string(nil), managedEnvironmentPackageManagers...)
}

func managedEnvironmentMountSnapshot() map[string]any {
	return map[string]any{
		"apt":   managedEnvironmentAssetSnapshot("apt", ""),
		"cargo": managedEnvironmentAssetSnapshot("cargo", ""),
		"gem":   managedEnvironmentAssetSnapshot("gem", ""),
		"go":    managedEnvironmentAssetSnapshot("go", ""),
		"npm":   managedEnvironmentAssetSnapshot("npm", ""),
		"pip":   managedEnvironmentAssetSnapshot("pip", ""),
	}
}

func managedEnvironmentAssetSnapshot(manager, volumeID string) map[string]any {
	return map[string]any{
		"manager":    manager,
		"volume_id":  strings.TrimSpace(volumeID),
		"mount_path": managedEnvironmentMountPath(manager),
	}
}

func environmentArtifactSnapshot(artifact *EnvironmentArtifact) map[string]any {
	if artifact == nil {
		return nil
	}
	return map[string]any{
		"id":            artifact.ID,
		"digest":        artifact.Digest,
		"status":        artifact.Status,
		"compatibility": cloneMap(artifact.Compatibility),
		"assets": map[string]any{
			"apt":   managedEnvironmentAssetSnapshot("apt", artifact.Assets.AptVolumeID),
			"cargo": managedEnvironmentAssetSnapshot("cargo", artifact.Assets.CargoVolumeID),
			"gem":   managedEnvironmentAssetSnapshot("gem", artifact.Assets.GemVolumeID),
			"go":    managedEnvironmentAssetSnapshot("go", artifact.Assets.GoVolumeID),
			"npm":   managedEnvironmentAssetSnapshot("npm", artifact.Assets.NPMVolumeID),
			"pip":   managedEnvironmentAssetSnapshot("pip", artifact.Assets.PipVolumeID),
		},
	}
}

func EnvironmentArtifactSnapshotForRuntime(artifact *EnvironmentArtifact) map[string]any {
	return environmentArtifactSnapshot(artifact)
}

func (a EnvironmentArtifactAssets) VolumeIDForManager(manager string) string {
	switch strings.TrimSpace(manager) {
	case "apt":
		return strings.TrimSpace(a.AptVolumeID)
	case "cargo":
		return strings.TrimSpace(a.CargoVolumeID)
	case "gem":
		return strings.TrimSpace(a.GemVolumeID)
	case "go":
		return strings.TrimSpace(a.GoVolumeID)
	case "npm":
		return strings.TrimSpace(a.NPMVolumeID)
	case "pip":
		return strings.TrimSpace(a.PipVolumeID)
	default:
		return ""
	}
}

func (a *EnvironmentArtifactAssets) SetVolumeIDForManager(manager, volumeID string) {
	if a == nil {
		return
	}
	switch strings.TrimSpace(manager) {
	case "apt":
		a.AptVolumeID = strings.TrimSpace(volumeID)
	case "cargo":
		a.CargoVolumeID = strings.TrimSpace(volumeID)
	case "gem":
		a.GemVolumeID = strings.TrimSpace(volumeID)
	case "go":
		a.GoVolumeID = strings.TrimSpace(volumeID)
	case "npm":
		a.NPMVolumeID = strings.TrimSpace(volumeID)
	case "pip":
		a.PipVolumeID = strings.TrimSpace(volumeID)
	}
}

func (a EnvironmentArtifactAssets) VolumeIDs() []string {
	out := make([]string, 0, len(managedEnvironmentPackageManagers))
	for _, manager := range managedEnvironmentPackageManagers {
		if volumeID := a.VolumeIDForManager(manager); volumeID != "" {
			out = append(out, volumeID)
		}
	}
	return out
}
