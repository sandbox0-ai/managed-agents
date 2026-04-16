package managedagentsruntime

import (
	"path"
	"strings"
)

const defaultRuntimeStateSubdir = ".sandbox0/agent-wrapper"

func runtimeStateMountPath(workspaceMountPath string) string {
	workspaceMountPath = cleanMountPath(workspaceMountPath)
	if workspaceMountPath == "" {
		workspaceMountPath = "/workspace"
	}
	return path.Join(workspaceMountPath, defaultRuntimeStateSubdir)
}

func uniqueVolumeIDs(values ...string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
