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

	"github.com/jackc/pgx/v5/pgconn"
	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
	sandbox0sdk "github.com/sandbox0-ai/sdk-go"
	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
)

type resolvedAgentSkill struct {
	skillID       string
	version       string
	mountSlug     string
	contentDigest string
	stored        *gatewaymanagedagents.StoredSkillVersion
}

type skillBundleMount struct {
	volumeID  string
	mountPath string
}

func (m *SDKRuntimeManager) ensureSkillBundleMount(ctx context.Context, client *sandbox0sdk.Client, teamID, workingDirectory string, agent map[string]any) (*skillBundleMount, error) {
	skills, err := m.resolveCustomAgentSkills(ctx, teamID, agent)
	if err != nil {
		return nil, err
	}
	if len(skills) == 0 {
		return nil, nil
	}
	mountPath := skillBundleMountPath(workingDirectory)
	if mountPath == "" {
		return nil, errors.New("working directory is invalid for skill bundle mount")
	}
	cacheKey, err := skillBundleCacheKey(skills)
	if err != nil {
		return nil, err
	}
	bundle, err := m.repo.GetSkillSetBundle(ctx, teamID, cacheKey)
	if err == nil {
		if _, err := client.GetVolume(ctx, bundle.VolumeID); err == nil {
			return &skillBundleMount{
				volumeID:  bundle.VolumeID,
				mountPath: mountPath,
			}, nil
		} else if !isSandboxNotFound(err) {
			return nil, fmt.Errorf("get skill bundle volume: %w", err)
		}
		if err := m.repo.DeleteSkillSetBundle(ctx, teamID, cacheKey); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, gatewaymanagedagents.ErrSkillSetBundleNotFound) {
		return nil, err
	}

	volumeID, err := m.buildSkillSetBundle(ctx, client, teamID, cacheKey, skills)
	if err != nil {
		return nil, err
	}
	return &skillBundleMount{
		volumeID:  volumeID,
		mountPath: mountPath,
	}, nil
}

func (m *SDKRuntimeManager) resolveCustomAgentSkills(ctx context.Context, teamID string, agent map[string]any) ([]resolvedAgentSkill, error) {
	entries := anySlice(agent["skills"])
	if len(entries) == 0 {
		return nil, nil
	}
	resolved := make([]resolvedAgentSkill, 0, len(entries))
	bySkillID := make(map[string]resolvedAgentSkill, len(entries))
	byMountSlug := make(map[string]string, len(entries))
	for _, raw := range entries {
		skill := mapValue(raw)
		skillType := strings.TrimSpace(stringValue(skill["type"]))
		skillID := strings.TrimSpace(stringValue(skill["skill_id"]))
		version := strings.TrimSpace(stringValue(skill["version"]))
		if skillID == "" {
			return nil, errors.New("agent skill is missing skill_id")
		}
		switch skillType {
		case "anthropic":
			return nil, fmt.Errorf("anthropic pre-built skill %s is not supported", skillID)
		case "custom":
			if version == "" {
				return nil, fmt.Errorf("custom skill %s is missing version", skillID)
			}
			stored, err := m.repo.GetStoredSkillVersion(ctx, teamID, skillID, version)
			if err != nil {
				return nil, fmt.Errorf("resolve custom skill %s@%s: %w", skillID, version, err)
			}
			mountSlug := stableSkillMountSlug(stored)
			if mountSlug == "" {
				return nil, fmt.Errorf("custom skill %s@%s is missing mount slug", skillID, version)
			}
			contentDigest, err := storedSkillContentDigest(stored)
			if err != nil {
				return nil, fmt.Errorf("compute custom skill digest %s@%s: %w", skillID, version, err)
			}
			current := resolvedAgentSkill{
				skillID:       skillID,
				version:       version,
				mountSlug:     mountSlug,
				contentDigest: contentDigest,
				stored:        stored,
			}
			if existing, ok := bySkillID[skillID]; ok {
				if existing.version != current.version || existing.mountSlug != current.mountSlug || existing.contentDigest != current.contentDigest {
					return nil, fmt.Errorf("custom skill %s is pinned to multiple versions in one session", skillID)
				}
				continue
			}
			if existingSkillID, ok := byMountSlug[mountSlug]; ok && existingSkillID != skillID {
				return nil, fmt.Errorf("custom skills %s and %s share mount slug %s", existingSkillID, skillID, mountSlug)
			}
			bySkillID[skillID] = current
			byMountSlug[mountSlug] = skillID
			resolved = append(resolved, current)
		default:
			return nil, fmt.Errorf("unsupported agent skill type %q", skillType)
		}
	}
	sort.Slice(resolved, func(i, j int) bool {
		if resolved[i].mountSlug == resolved[j].mountSlug {
			if resolved[i].skillID == resolved[j].skillID {
				return resolved[i].version < resolved[j].version
			}
			return resolved[i].skillID < resolved[j].skillID
		}
		return resolved[i].mountSlug < resolved[j].mountSlug
	})
	return resolved, nil
}

func (m *SDKRuntimeManager) buildSkillSetBundle(ctx context.Context, client *sandbox0sdk.Client, teamID, cacheKey string, skills []resolvedAgentSkill) (string, error) {
	tempVolume, err := client.CreateVolume(ctx, apispec.CreateSandboxVolumeRequest{})
	if err != nil {
		return "", fmt.Errorf("create skill bundle temp volume: %w", err)
	}
	tempVolumeID := tempVolume.ID
	defer func() {
		_, _ = client.DeleteVolumeWithOptions(context.WithoutCancel(ctx), tempVolumeID, &sandbox0sdk.DeleteVolumeOptions{Force: true})
	}()

	for _, skill := range skills {
		if err := materializeResolvedSkill(ctx, client, tempVolumeID, skill); err != nil {
			return "", err
		}
	}

	published, err := client.ForkVolume(ctx, tempVolumeID, &apispec.ForkVolumeRequest{
		AccessMode: apispec.NewOptVolumeAccessMode(apispec.VolumeAccessModeROX),
	})
	if err != nil {
		return "", fmt.Errorf("publish skill bundle volume: %w", err)
	}

	now := time.Now().UTC()
	bundle := &gatewaymanagedagents.SkillBundle{
		ID:       gatewaymanagedagents.NewID("skbdl"),
		TeamID:   strings.TrimSpace(teamID),
		CacheKey: strings.TrimSpace(cacheKey),
		VolumeID: published.ID,
		Snapshot: map[string]any{
			"schema":      "managed-agent-skill-set-bundle-v1",
			"skill_names": orderedSkillNames(skills),
			"skills":      skillBundleSnapshotEntries(skills),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := m.repo.CreateSkillSetBundle(ctx, bundle); err != nil {
		if isUniqueViolation(err) {
			_, _ = client.DeleteVolumeWithOptions(ctx, published.ID, &sandbox0sdk.DeleteVolumeOptions{Force: true})
			existing, getErr := m.repo.GetSkillSetBundle(ctx, teamID, cacheKey)
			if getErr != nil {
				return "", getErr
			}
			return existing.VolumeID, nil
		}
		_, _ = client.DeleteVolumeWithOptions(ctx, published.ID, &sandbox0sdk.DeleteVolumeOptions{Force: true})
		return "", err
	}
	return published.ID, nil
}

func materializeResolvedSkill(ctx context.Context, client *sandbox0sdk.Client, volumeID string, skill resolvedAgentSkill) error {
	if skill.stored == nil {
		return errors.New("resolved skill snapshot is required")
	}
	artifact := skill.stored.Artifact
	if strings.TrimSpace(artifact.VolumeID) == "" || strings.TrimSpace(artifact.Path) == "" || strings.TrimSpace(artifact.ContentDigest) == "" {
		return fmt.Errorf("custom skill %s@%s is missing artifact metadata", skill.skillID, skill.version)
	}
	content, err := client.ReadVolumeFile(ctx, artifact.VolumeID, artifact.Path)
	if err != nil {
		return fmt.Errorf("read skill artifact %s@%s: %w", skill.skillID, skill.version, err)
	}
	if err := extractSkillArchiveToVolume(ctx, client, volumeID, skill.mountSlug, content); err != nil {
		return fmt.Errorf("extract skill artifact %s@%s: %w", skill.skillID, skill.version, err)
	}
	return nil
}

func extractSkillArchiveToVolume(ctx context.Context, client *sandbox0sdk.Client, volumeID, mountSlug string, archive []byte) error {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("create skill archive reader: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read skill archive entry: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			return fmt.Errorf("skill archive entry %s has unsupported type", header.Name)
		}
		relativePath := normalizedBundleArchivePath(header.Name)
		if relativePath == "" {
			return fmt.Errorf("skill archive entry %q is invalid", header.Name)
		}
		targetPath := cleanMountPath(path.Join("/", mountSlug, relativePath))
		if targetPath == "" {
			return fmt.Errorf("skill archive entry %q has invalid target path", header.Name)
		}
		content, err := io.ReadAll(tarReader)
		if err != nil {
			return fmt.Errorf("read skill archive file %s: %w", header.Name, err)
		}
		if err := writeVolumeFileWithParents(ctx, client, volumeID, targetPath, content); err != nil {
			return err
		}
	}
}

func writeVolumeFileWithParents(ctx context.Context, client *sandbox0sdk.Client, volumeID, targetPath string, content []byte) error {
	parent := path.Dir(targetPath)
	if parent != "." && parent != "/" {
		if err := retryVolumeFileOperation(ctx, func() error {
			_, err := client.MkdirVolumeFile(ctx, volumeID, parent, true)
			return err
		}); err != nil {
			return fmt.Errorf("mkdir bundle path %s: %w", parent, err)
		}
	}
	if err := retryVolumeFileOperation(ctx, func() error {
		_, err := client.WriteVolumeFile(ctx, volumeID, targetPath, content)
		return err
	}); err != nil {
		return fmt.Errorf("write bundle file %s: %w", targetPath, err)
	}
	return nil
}

func skillBundleMountPath(workingDirectory string) string {
	return cleanMountPath(path.Join(strings.TrimSpace(workingDirectory), ".claude", "skills"))
}

func stableSkillMountSlug(stored *gatewaymanagedagents.StoredSkillVersion) string {
	if stored == nil {
		return ""
	}
	return strings.TrimSpace(stored.MountSlug)
}

func storedSkillContentDigest(stored *gatewaymanagedagents.StoredSkillVersion) (string, error) {
	if stored == nil {
		return "", errors.New("stored skill version is required")
	}
	if digest := strings.TrimSpace(stored.Artifact.ContentDigest); digest != "" {
		return digest, nil
	}
	return "", errors.New("custom skill is missing content digest")
}

func skillBundleCacheKey(skills []resolvedAgentSkill) (string, error) {
	type cacheEntry struct {
		SkillID       string `json:"skill_id"`
		Version       string `json:"version"`
		ContentDigest string `json:"content_digest"`
		MountSlug     string `json:"mount_slug"`
	}
	payload := struct {
		Schema string       `json:"schema"`
		Skills []cacheEntry `json:"skills"`
	}{
		Schema: "managed-agent-skill-set-bundle-v1",
		Skills: make([]cacheEntry, 0, len(skills)),
	}
	for _, skill := range skills {
		payload.Skills = append(payload.Skills, cacheEntry{
			SkillID:       skill.skillID,
			Version:       skill.version,
			ContentDigest: skill.contentDigest,
			MountSlug:     skill.mountSlug,
		})
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal skill bundle cache key: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func orderedSkillNames(skills []resolvedAgentSkill) []string {
	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		names = append(names, skill.mountSlug)
	}
	sort.Strings(names)
	return names
}

func skillBundleSnapshotEntries(skills []resolvedAgentSkill) []map[string]string {
	out := make([]map[string]string, 0, len(skills))
	for _, skill := range skills {
		out = append(out, map[string]string{
			"skill_id":       skill.skillID,
			"version":        skill.version,
			"mount_slug":     skill.mountSlug,
			"content_digest": skill.contentDigest,
		})
	}
	return out
}

func normalizedBundleArchivePath(value string) string {
	cleaned := path.Clean(strings.TrimSpace(strings.TrimPrefix(value, "/")))
	if cleaned == "." || cleaned == "" || strings.HasPrefix(cleaned, "../") {
		return ""
	}
	return cleaned
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
