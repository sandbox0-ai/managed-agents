package managedagentsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	gatewaymanagedagents "github.com/sandbox0-ai/managed-agent/internal/managedagents"
	sandbox0sdk "github.com/sandbox0-ai/sdk-go"
	apispec "github.com/sandbox0-ai/sdk-go/pkg/apispec"
)

type VolumeFileStore struct {
	baseURL        string
	timeout        time.Duration
	sandbox0APIKey string
}

func NewVolumeAssetStore(baseURL string, timeout time.Duration, sandbox0APIKey string) *VolumeFileStore {
	return &VolumeFileStore{
		baseURL:        strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		timeout:        timeout,
		sandbox0APIKey: strings.TrimSpace(sandbox0APIKey),
	}
}

func (s *VolumeFileStore) CreateStore(ctx context.Context, credential gatewaymanagedagents.RequestCredential, req gatewaymanagedagents.AssetStoreCreateStoreRequest) (gatewaymanagedagents.AssetStoreVolume, error) {
	client, err := s.client(credential, req.TeamID)
	if err != nil {
		return gatewaymanagedagents.AssetStoreVolume{}, err
	}
	volume, err := client.CreateVolume(ctx, apispec.CreateSandboxVolumeRequest{})
	if err != nil {
		return gatewaymanagedagents.AssetStoreVolume{}, fmt.Errorf("create asset-store volume: %w", err)
	}
	return gatewaymanagedagents.AssetStoreVolume{VolumeID: volume.ID}, nil
}

func (s *VolumeFileStore) DeleteStore(ctx context.Context, credential gatewaymanagedagents.RequestCredential, req gatewaymanagedagents.AssetStoreDeleteStoreRequest) error {
	if strings.TrimSpace(req.VolumeID) == "" {
		return nil
	}
	client, err := s.client(credential, req.TeamID)
	if err != nil {
		return err
	}
	if _, err := client.DeleteVolumeWithOptions(ctx, req.VolumeID, &sandbox0sdk.DeleteVolumeOptions{Force: true}); err != nil {
		if isSandboxNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete asset-store volume: %w", err)
	}
	return nil
}

func (s *VolumeFileStore) PutObject(ctx context.Context, credential gatewaymanagedagents.RequestCredential, req gatewaymanagedagents.AssetStorePutObjectRequest) (gatewaymanagedagents.AssetStoreObject, error) {
	client, err := s.client(credential, req.TeamID)
	if err != nil {
		return gatewaymanagedagents.AssetStoreObject{}, err
	}
	content, err := io.ReadAll(req.Content)
	if err != nil {
		return gatewaymanagedagents.AssetStoreObject{}, fmt.Errorf("read asset-store content: %w", err)
	}
	sum := sha256.Sum256(content)
	if strings.TrimSpace(req.VolumeID) == "" {
		return gatewaymanagedagents.AssetStoreObject{}, fmt.Errorf("asset-store volume id is required")
	}
	if strings.TrimSpace(req.Path) == "" {
		return gatewaymanagedagents.AssetStoreObject{}, fmt.Errorf("asset-store path is required")
	}
	if _, err := client.MkdirVolumeFile(ctx, req.VolumeID, path.Dir(req.Path), true); err != nil {
		return gatewaymanagedagents.AssetStoreObject{}, fmt.Errorf("create asset-store directory: %w", err)
	}
	if _, err := client.WriteVolumeFile(ctx, req.VolumeID, req.Path, content); err != nil {
		return gatewaymanagedagents.AssetStoreObject{}, fmt.Errorf("write asset-store content: %w", err)
	}
	return gatewaymanagedagents.AssetStoreObject{
		Path:      req.Path,
		SizeBytes: int64(len(content)),
		SHA256:    hex.EncodeToString(sum[:]),
	}, nil
}

func (s *VolumeFileStore) ReadObject(ctx context.Context, credential gatewaymanagedagents.RequestCredential, req gatewaymanagedagents.AssetStoreReadObjectRequest) ([]byte, error) {
	if strings.TrimSpace(req.VolumeID) == "" || strings.TrimSpace(req.Path) == "" {
		return nil, gatewaymanagedagents.ErrFileNotFound
	}
	client, err := s.client(credential, req.TeamID)
	if err != nil {
		return nil, err
	}
	content, err := client.ReadVolumeFile(ctx, req.VolumeID, req.Path)
	if err != nil {
		return nil, fmt.Errorf("read asset-store content: %w", err)
	}
	return content, nil
}

func (s *VolumeFileStore) DeleteObject(ctx context.Context, credential gatewaymanagedagents.RequestCredential, req gatewaymanagedagents.AssetStoreDeleteObjectRequest) error {
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
		return fmt.Errorf("delete asset-store object: %w", err)
	}
	return nil
}

func (s *VolumeFileStore) client(credential gatewaymanagedagents.RequestCredential, teamID string) (*sandbox0sdk.Client, error) {
	_ = credential
	_ = teamID
	if strings.TrimSpace(s.baseURL) == "" {
		return nil, fmt.Errorf("file-store sandbox0 base URL is required")
	}
	if strings.TrimSpace(s.sandbox0APIKey) == "" {
		return nil, fmt.Errorf("file-store sandbox0 api key is required")
	}
	opts := []sandbox0sdk.Option{
		sandbox0sdk.WithBaseURL(s.baseURL),
		sandbox0sdk.WithToken(s.sandbox0APIKey),
		sandbox0sdk.WithTimeout(s.timeout),
	}
	return sandbox0sdk.NewClient(opts...)
}
