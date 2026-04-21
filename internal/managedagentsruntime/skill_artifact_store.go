package managedagentsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
	sandbox0sdk "github.com/sandbox0-ai/sdk-go"
	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
)

type VolumeSkillArtifactStore struct {
	repo           *gatewaymanagedagents.Repository
	baseURL        string
	timeout        time.Duration
	sandbox0APIKey string
}

func NewVolumeSkillArtifactStore(repo *gatewaymanagedagents.Repository, baseURL string, timeout time.Duration, sandbox0APIKey string) *VolumeSkillArtifactStore {
	return &VolumeSkillArtifactStore{
		repo:           repo,
		baseURL:        strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		timeout:        timeout,
		sandbox0APIKey: strings.TrimSpace(sandbox0APIKey),
	}
}

func (s *VolumeSkillArtifactStore) PutSkillVersion(ctx context.Context, credential gatewaymanagedagents.RequestCredential, req gatewaymanagedagents.SkillArtifactPutRequest) (gatewaymanagedagents.SkillArtifactObject, error) {
	client, err := s.client(credential, req.TeamID)
	if err != nil {
		return gatewaymanagedagents.SkillArtifactObject{}, err
	}
	content, err := io.ReadAll(req.Content)
	if err != nil {
		return gatewaymanagedagents.SkillArtifactObject{}, fmt.Errorf("read skill artifact content: %w", err)
	}
	volumeID, err := s.resolveArtifactStoreVolume(ctx, client, req.TeamID)
	if err != nil {
		return gatewaymanagedagents.SkillArtifactObject{}, err
	}
	artifactPath := managedSkillArtifactStorePath(req.TeamID, req.SkillID, req.Version, req.ContentDigest)
	if _, err := client.MkdirVolumeFile(ctx, volumeID, path.Dir(artifactPath), true); err != nil {
		return gatewaymanagedagents.SkillArtifactObject{}, fmt.Errorf("create skill artifact directory: %w", err)
	}
	if _, err := client.WriteVolumeFile(ctx, volumeID, artifactPath, content); err != nil {
		return gatewaymanagedagents.SkillArtifactObject{}, fmt.Errorf("write skill artifact content: %w", err)
	}
	sum := sha256.Sum256(content)
	return gatewaymanagedagents.SkillArtifactObject{
		VolumeID:  volumeID,
		Path:      artifactPath,
		SizeBytes: int64(len(content)),
		SHA256:    hex.EncodeToString(sum[:]),
	}, nil
}

func (s *VolumeSkillArtifactStore) DeleteSkillVersion(ctx context.Context, credential gatewaymanagedagents.RequestCredential, req gatewaymanagedagents.SkillArtifactDeleteRequest) error {
	if strings.TrimSpace(req.VolumeID) == "" || strings.TrimSpace(req.Path) == "" {
		return nil
	}
	client, err := s.client(credential, req.TeamID)
	if err != nil {
		return err
	}
	if _, err := client.DeleteVolumeFile(ctx, req.VolumeID, req.Path); err != nil {
		if isSandboxNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete skill artifact content: %w", err)
	}
	return nil
}

func (s *VolumeSkillArtifactStore) resolveArtifactStoreVolume(ctx context.Context, client *sandbox0sdk.Client, teamID string) (string, error) {
	if s.repo == nil {
		return "", fmt.Errorf("skill artifact store repository is required")
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return "", fmt.Errorf("skill artifact store team id is required")
	}
	volumeID, err := s.repo.GetSkillArtifactStoreVolume(ctx, teamID)
	switch {
	case err == nil:
		if _, err := client.GetVolume(ctx, volumeID); err == nil {
			return volumeID, nil
		} else if !isSandboxNotFound(err) {
			return "", fmt.Errorf("get skill artifact store volume: %w", err)
		}
		newVolume, err := client.CreateVolume(ctx, apispec.CreateSandboxVolumeRequest{})
		if err != nil {
			return "", fmt.Errorf("recreate skill artifact store volume: %w", err)
		}
		if err := s.repo.UpsertSkillArtifactStoreVolume(ctx, teamID, newVolume.ID, time.Now().UTC()); err != nil {
			_, _ = client.DeleteVolumeWithOptions(ctx, newVolume.ID, &sandbox0sdk.DeleteVolumeOptions{Force: true})
			return "", err
		}
		return newVolume.ID, nil
	case err != nil && !errors.Is(err, gatewaymanagedagents.ErrSkillArtifactStoreNotFound):
		return "", err
	}

	newVolume, err := client.CreateVolume(ctx, apispec.CreateSandboxVolumeRequest{})
	if err != nil {
		return "", fmt.Errorf("create skill artifact store volume: %w", err)
	}
	now := time.Now().UTC()
	if err := s.repo.CreateSkillArtifactStoreVolume(ctx, teamID, newVolume.ID, now); err != nil {
		_, _ = client.DeleteVolumeWithOptions(ctx, newVolume.ID, &sandbox0sdk.DeleteVolumeOptions{Force: true})
		return "", err
	}
	resolvedVolumeID, err := s.repo.GetSkillArtifactStoreVolume(ctx, teamID)
	if err != nil {
		_, _ = client.DeleteVolumeWithOptions(ctx, newVolume.ID, &sandbox0sdk.DeleteVolumeOptions{Force: true})
		return "", err
	}
	if resolvedVolumeID != newVolume.ID {
		_, _ = client.DeleteVolumeWithOptions(ctx, newVolume.ID, &sandbox0sdk.DeleteVolumeOptions{Force: true})
	}
	return resolvedVolumeID, nil
}

func (s *VolumeSkillArtifactStore) client(credential gatewaymanagedagents.RequestCredential, teamID string) (*sandbox0sdk.Client, error) {
	_ = credential
	_ = teamID
	if strings.TrimSpace(s.baseURL) == "" {
		return nil, fmt.Errorf("skill artifact store sandbox0 base URL is required")
	}
	if strings.TrimSpace(s.sandbox0APIKey) == "" {
		return nil, fmt.Errorf("skill artifact store sandbox0 api key is required")
	}
	opts := []sandbox0sdk.Option{
		sandbox0sdk.WithBaseURL(s.baseURL),
		sandbox0sdk.WithToken(s.sandbox0APIKey),
		sandbox0sdk.WithTimeout(s.timeout),
	}
	return sandbox0sdk.NewClient(opts...)
}

func managedSkillArtifactStorePath(teamID, skillID, version, contentDigest string) string {
	artifactName := strings.TrimSpace(contentDigest)
	if artifactName == "" {
		artifactName = "content"
	}
	return path.Join(
		"/managed-agent-skill-artifacts",
		sanitizeName(teamID),
		sanitizeName(skillID),
		sanitizeName(version),
		sanitizeName(artifactName)+".tar.gz",
	)
}
