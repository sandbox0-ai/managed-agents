package managedagents

import (
	"reflect"
	"testing"
)

func TestManagedEnvironmentPackageManagersStableOrder(t *testing.T) {
	got := ManagedEnvironmentPackageManagers()
	want := []string{"apt", "cargo", "gem", "go", "npm", "pip"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ManagedEnvironmentPackageManagers() = %#v, want %#v", got, want)
	}
}

func TestEnvironmentArtifactDigestStableAndCompatibilitySensitive(t *testing.T) {
	config := environmentConfigFromMap(map[string]any{
		"type":       "cloud",
		"networking": map[string]any{"type": "unrestricted"},
		"packages": map[string]any{
			"type":  "packages",
			"apt":   []any{"ripgrep"},
			"cargo": []any{"bat"},
			"gem":   []any{"bundler"},
			"go":    []any{"golang.org/x/tools/cmd/stringer@latest"},
			"npm":   []any{"typescript"},
			"pip":   []any{"ruff==0.9.0"},
		},
	})
	compatibilityA := map[string]any{
		"template_family": "managed-agent-claude-shared-v1",
		"os":              "linux",
		"arch":            "amd64",
		"base_image":      "shared-image-v1",
	}
	compatibilityB := cloneMap(compatibilityA)
	compatibilityB["base_image"] = "shared-image-v2"

	digestA1, err := EnvironmentArtifactDigest(config, compatibilityA)
	if err != nil {
		t.Fatalf("EnvironmentArtifactDigest first: %v", err)
	}
	digestA2, err := EnvironmentArtifactDigest(config, compatibilityA)
	if err != nil {
		t.Fatalf("EnvironmentArtifactDigest second: %v", err)
	}
	if digestA1 != digestA2 {
		t.Fatalf("digest not stable: %q != %q", digestA1, digestA2)
	}

	digestB, err := EnvironmentArtifactDigest(config, compatibilityB)
	if err != nil {
		t.Fatalf("EnvironmentArtifactDigest compatibility change: %v", err)
	}
	if digestA1 == digestB {
		t.Fatalf("digest = %q, want compatibility change to affect digest", digestA1)
	}
}
