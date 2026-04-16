package managedagentsruntime

import (
	"path"
	"strings"
)

const defaultRuntimeStateSubdir = ".sandbox0/agent-wrapper"

func defaultEngineStateMountPath(workspaceMountPath string) string {
	workspaceMountPath = cleanMountPath(workspaceMountPath)
	if workspaceMountPath == "" {
		workspaceMountPath = "/workspace"
	}
	return path.Join(workspaceMountPath, defaultRuntimeStateSubdir)
}

func normalizeEngineStateMountPath(workspaceMountPath, engineStateMountPath string) string {
	workspaceMountPath = cleanMountPath(workspaceMountPath)
	if workspaceMountPath == "" {
		workspaceMountPath = "/workspace"
	}
	engineStateMountPath = cleanMountPath(engineStateMountPath)
	if engineStateMountPath == "" || engineStateMountPath == workspaceMountPath || !mountPathContains(workspaceMountPath, engineStateMountPath) {
		return defaultEngineStateMountPath(workspaceMountPath)
	}
	return engineStateMountPath
}

func mountPathContains(root, candidate string) bool {
	root = cleanMountPath(root)
	candidate = cleanMountPath(candidate)
	if root == "" || candidate == "" {
		return false
	}
	if root == "/" {
		return true
	}
	return candidate == root || strings.HasPrefix(candidate, root+"/")
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
