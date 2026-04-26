package managedagentsruntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
	sandbox0sdk "github.com/sandbox0-ai/sdk-go"
	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
	"go.uber.org/zap"
)

type resolvedWorkspaceBaseSkill struct {
	SkillID      string `json:"skill_id"`
	Version      string `json:"version"`
	VersionID    string `json:"version_id"`
	Directory    string `json:"directory"`
	BundlePath   string `json:"bundle_path"`
	BundleSHA256 string `json:"bundle_sha256"`
}

func (m *SDKRuntimeManager) PrepareWorkspaceBase(ctx context.Context, teamID string, agent gatewaymanagedagents.Agent, workingDirectory string) (*gatewaymanagedagents.WorkspaceBaseRecord, error) {
	digest, inputSnapshot, skills, err := m.workspaceBaseInputForAgent(ctx, teamID, agentSkillMapsFromAgent(agent), workingDirectory)
	if err != nil {
		return nil, err
	}
	if digest == "" {
		return nil, nil
	}
	if existing, err := m.repo.GetWorkspaceBase(ctx, teamID, digest); err == nil && strings.TrimSpace(existing.Status) == gatewaymanagedagents.WorkspaceBaseStatusReady && strings.TrimSpace(existing.VolumeID) != "" {
		return existing, nil
	} else if err != nil && !errors.Is(err, gatewaymanagedagents.ErrWorkspaceBaseNotFound) {
		return nil, err
	}
	client, err := m.runtimeSandboxClient()
	if err != nil {
		return nil, err
	}
	regionID, err := m.repo.ResolveRuntimeRegionID(ctx, teamID)
	if err != nil {
		return nil, err
	}
	teamStore, err := m.repo.GetTeamAssetStore(ctx, teamID, regionID)
	if err != nil {
		return nil, fmt.Errorf("resolve team asset store: %w", err)
	}
	now := time.Now().UTC()
	building := gatewaymanagedagents.NewWorkspaceBaseRecord(teamID, digest, gatewaymanagedagents.WorkspaceBaseStatusBuilding, "", inputSnapshot, now)
	if err := m.repo.UpsertWorkspaceBase(ctx, building); err != nil {
		return nil, err
	}
	tempVolume, err := client.CreateVolume(ctx, apispec.CreateSandboxVolumeRequest{})
	if err != nil {
		return nil, fmt.Errorf("create workspace base temp volume: %w", err)
	}
	tempVolumeID := tempVolume.ID
	cleanupTemp := true
	defer func() {
		if cleanupTemp && strings.TrimSpace(tempVolumeID) != "" {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.cfg.SandboxRequestTimeout)
			defer cancel()
			if _, err := client.DeleteVolumeWithOptions(cleanupCtx, tempVolumeID, &sandbox0sdk.DeleteVolumeOptions{Force: true}); err != nil && !isSandboxNotFound(err) {
				m.logger.Warn("delete workspace base temp volume failed", zap.Error(err), zap.String("volume_id", tempVolumeID))
			}
		}
	}()
	if err := m.materializeWorkspaceBaseSkills(ctx, client, tempVolumeID, teamStore.VolumeID, skills, workingDirectory); err != nil {
		failed := gatewaymanagedagents.NewWorkspaceBaseRecord(teamID, digest, gatewaymanagedagents.WorkspaceBaseStatusFailed, "", inputSnapshot, now)
		failed.FailureReason = err.Error()
		_ = m.repo.UpsertWorkspaceBase(context.WithoutCancel(ctx), failed)
		return nil, err
	}
	published, err := forkEnvironmentArtifactVolumeWithRetry(ctx, client, tempVolumeID, m.cfg.SandboxRequestTimeout, 200*time.Millisecond)
	if err != nil {
		failed := gatewaymanagedagents.NewWorkspaceBaseRecord(teamID, digest, gatewaymanagedagents.WorkspaceBaseStatusFailed, "", inputSnapshot, now)
		failed.FailureReason = err.Error()
		_ = m.repo.UpsertWorkspaceBase(context.WithoutCancel(ctx), failed)
		return nil, fmt.Errorf("publish workspace base volume: %w", err)
	}
	ready := gatewaymanagedagents.NewWorkspaceBaseRecord(teamID, digest, gatewaymanagedagents.WorkspaceBaseStatusReady, published.ID, inputSnapshot, now)
	if err := m.repo.UpsertWorkspaceBase(ctx, ready); err != nil {
		_, _ = client.DeleteVolumeWithOptions(context.WithoutCancel(ctx), published.ID, &sandbox0sdk.DeleteVolumeOptions{Force: true})
		return nil, err
	}
	return ready, nil
}

func (m *SDKRuntimeManager) workspaceBaseForSession(ctx context.Context, session *gatewaymanagedagents.SessionRecord) (string, *gatewaymanagedagents.WorkspaceBaseRecord, error) {
	digest, _, _, err := m.workspaceBaseInputForAgent(ctx, session.TeamID, agentSkillMapsFromMap(session.Agent), session.WorkingDirectory)
	if err != nil || digest == "" {
		return digest, nil, err
	}
	base, err := m.repo.GetWorkspaceBase(ctx, session.TeamID, digest)
	if err != nil {
		if errors.Is(err, gatewaymanagedagents.ErrWorkspaceBaseNotFound) {
			return digest, nil, nil
		}
		return digest, nil, err
	}
	if strings.TrimSpace(base.Status) != gatewaymanagedagents.WorkspaceBaseStatusReady || strings.TrimSpace(base.VolumeID) == "" {
		return digest, nil, nil
	}
	return digest, base, nil
}

func (m *SDKRuntimeManager) workspaceBaseInputForAgent(ctx context.Context, teamID string, skillEntries []map[string]any, workingDirectory string) (string, map[string]any, []resolvedWorkspaceBaseSkill, error) {
	if len(skillEntries) == 0 {
		return "", nil, nil, nil
	}
	skills := make([]resolvedWorkspaceBaseSkill, 0, len(skillEntries))
	for _, skill := range skillEntries {
		skillType := strings.TrimSpace(stringValue(skill["type"]))
		skillID := strings.TrimSpace(stringValue(skill["skill_id"]))
		version := strings.TrimSpace(stringValue(skill["version"]))
		if skillID == "" {
			return "", nil, nil, errors.New("agent skill is missing skill_id")
		}
		switch skillType {
		case "anthropic":
			return "", nil, nil, fmt.Errorf("anthropic pre-built skill %s is not supported", skillID)
		case "custom":
			if version == "" {
				return "", nil, nil, fmt.Errorf("custom skill %s is missing version", skillID)
			}
			stored, err := m.repo.GetStoredSkillVersion(ctx, teamID, skillID, version)
			if err != nil {
				return "", nil, nil, fmt.Errorf("resolve custom skill %s@%s: %w", skillID, version, err)
			}
			directory := skillDirectoryName(stored, skillID)
			if directory == "" {
				return "", nil, nil, fmt.Errorf("custom skill %s@%s is missing directory", skillID, version)
			}
			skills = append(skills, resolvedWorkspaceBaseSkill{
				SkillID:      skillID,
				Version:      version,
				VersionID:    stored.Snapshot.ID,
				Directory:    directory,
				BundlePath:   strings.TrimSpace(stored.Bundle.Path),
				BundleSHA256: strings.TrimSpace(stored.Bundle.SHA256),
			})
		default:
			return "", nil, nil, fmt.Errorf("unsupported agent skill type %q", skillType)
		}
	}
	sort.Slice(skills, func(i, j int) bool {
		if skills[i].SkillID == skills[j].SkillID {
			return skills[i].Version < skills[j].Version
		}
		return skills[i].SkillID < skills[j].SkillID
	})
	inputSnapshot := map[string]any{
		"schema":            1,
		"working_directory": strings.TrimSpace(workingDirectory),
		"skills":            skills,
	}
	payload, err := json.Marshal(inputSnapshot)
	if err != nil {
		return "", nil, nil, err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), inputSnapshot, skills, nil
}

func (m *SDKRuntimeManager) materializeWorkspaceBaseSkills(ctx context.Context, client *sandbox0sdk.Client, workspaceVolumeID, assetVolumeID string, skills []resolvedWorkspaceBaseSkill, workingDirectory string) error {
	for _, skill := range skills {
		bundleContent, err := client.ReadVolumeFile(ctx, assetVolumeID, skill.BundlePath)
		if err != nil {
			return fmt.Errorf("read skill bundle %s: %w", skill.BundlePath, err)
		}
		if err := m.writeSkillBundleToWorkspaceVolume(ctx, client, workspaceVolumeID, bundleContent, workingDirectory, skill.Directory); err != nil {
			return fmt.Errorf("materialize skill %s@%s: %w", skill.SkillID, skill.Version, err)
		}
	}
	return nil
}

func (m *SDKRuntimeManager) agentSkillNamesFromWorkspaceBase(ctx context.Context, teamID, workingDirectory string, agent map[string]any, runtimeWorkspaceBaseDigest string) ([]string, bool, error) {
	if strings.TrimSpace(runtimeWorkspaceBaseDigest) == "" {
		return nil, false, nil
	}
	digest, _, skills, err := m.workspaceBaseInputForAgent(ctx, teamID, agentSkillMapsFromMap(agent), workingDirectory)
	if err != nil {
		return nil, false, err
	}
	if digest == "" || digest != strings.TrimSpace(runtimeWorkspaceBaseDigest) {
		return nil, false, nil
	}
	preloadSet := make(map[string]struct{}, len(skills))
	for _, skill := range skills {
		preloadSet[skill.Directory] = struct{}{}
	}
	preloadNames := make([]string, 0, len(preloadSet))
	for name := range preloadSet {
		preloadNames = append(preloadNames, name)
	}
	sort.Strings(preloadNames)
	return preloadNames, true, nil
}

func (m *SDKRuntimeManager) writeSkillBundleToWorkspaceVolume(ctx context.Context, client *sandbox0sdk.Client, workspaceVolumeID string, bundleContent []byte, workingDirectory, directory string) error {
	targetRoot := workspaceMountedPathToVolumePath(m.cfg.WorkspaceMountPath, skillWorkspaceSkillsContainerPath(workingDirectory))
	if targetRoot == "" {
		return errors.New("skill workspace path is invalid")
	}
	gzipReader, err := gzip.NewReader(bytes.NewReader(bundleContent))
	if err != nil {
		return fmt.Errorf("open skill bundle gzip stream: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read skill bundle tar stream: %w", err)
		}
		relativePath, ok := safeSkillBundleMemberPath(header.Name, directory)
		if !ok {
			continue
		}
		targetPath := cleanMountPath(path.Join(targetRoot, relativePath))
		switch header.Typeflag {
		case tar.TypeDir:
			if _, err := client.MkdirVolumeFile(ctx, workspaceVolumeID, targetPath, true); err != nil {
				return fmt.Errorf("mkdir %s: %w", targetPath, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			parent := path.Dir(targetPath)
			if parent != "." && parent != "/" {
				if _, err := client.MkdirVolumeFile(ctx, workspaceVolumeID, parent, true); err != nil {
					return fmt.Errorf("mkdir %s: %w", parent, err)
				}
			}
			data, err := io.ReadAll(tarReader)
			if err != nil {
				return fmt.Errorf("read %s: %w", header.Name, err)
			}
			if _, err := client.WriteVolumeFile(ctx, workspaceVolumeID, targetPath, data); err != nil {
				return fmt.Errorf("write %s: %w", targetPath, err)
			}
		}
	}
	return nil
}

func safeSkillBundleMemberPath(name, directory string) (string, bool) {
	cleanName := strings.TrimPrefix(path.Clean("/"+strings.TrimSpace(name)), "/")
	cleanDirectory := strings.Trim(strings.TrimSpace(directory), "/")
	if cleanName == "." || cleanName == "" || cleanDirectory == "" {
		return "", false
	}
	prefix := cleanDirectory + "/"
	if cleanName != cleanDirectory && !strings.HasPrefix(cleanName, prefix) {
		return "", false
	}
	return cleanName, true
}

func agentSkillMapsFromAgent(agent gatewaymanagedagents.Agent) []map[string]any {
	out := make([]map[string]any, 0, len(agent.Skills))
	for _, skill := range agent.Skills {
		out = append(out, map[string]any{
			"type":     skill.Type,
			"skill_id": skill.SkillID,
			"version":  skill.Version,
		})
	}
	return out
}

func agentSkillMapsFromMap(agent map[string]any) []map[string]any {
	rawSkills := anySlice(agent["skills"])
	out := make([]map[string]any, 0, len(rawSkills))
	for _, raw := range rawSkills {
		out = append(out, mapValue(raw))
	}
	return out
}
