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
	baseURL string
	timeout time.Duration
}

func NewVolumeFileStore(baseURL string, timeout time.Duration) *VolumeFileStore {
	return &VolumeFileStore{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		timeout: timeout,
	}
}

func (s *VolumeFileStore) PutFile(ctx context.Context, credential gatewaymanagedagents.RequestCredential, req gatewaymanagedagents.FileStorePutRequest) (gatewaymanagedagents.FileStoreObject, error) {
	client, err := s.client(credential)
	if err != nil {
		return gatewaymanagedagents.FileStoreObject{}, err
	}
	content, err := io.ReadAll(req.Content)
	if err != nil {
		return gatewaymanagedagents.FileStoreObject{}, fmt.Errorf("read upload content: %w", err)
	}
	sum := sha256.Sum256(content)
	volume, err := client.CreateVolume(ctx, apispec.CreateSandboxVolumeRequest{})
	if err != nil {
		return gatewaymanagedagents.FileStoreObject{}, fmt.Errorf("create file-store volume: %w", err)
	}
	filePath := managedFileStorePath(req.TeamID, req.FileID, req.Filename)
	if _, err := client.MkdirVolumeFile(ctx, volume.ID, path.Dir(filePath), true); err != nil {
		_, _ = client.DeleteVolumeWithOptions(ctx, volume.ID, &sandbox0sdk.DeleteVolumeOptions{Force: true})
		return gatewaymanagedagents.FileStoreObject{}, fmt.Errorf("create file-store directory: %w", err)
	}
	if _, err := client.WriteVolumeFile(ctx, volume.ID, filePath, content); err != nil {
		_, _ = client.DeleteVolumeWithOptions(ctx, volume.ID, &sandbox0sdk.DeleteVolumeOptions{Force: true})
		return gatewaymanagedagents.FileStoreObject{}, fmt.Errorf("write file-store content: %w", err)
	}
	return gatewaymanagedagents.FileStoreObject{
		VolumeID:  volume.ID,
		Path:      filePath,
		SizeBytes: int64(len(content)),
		SHA256:    hex.EncodeToString(sum[:]),
	}, nil
}

func (s *VolumeFileStore) ReadFile(ctx context.Context, credential gatewaymanagedagents.RequestCredential, req gatewaymanagedagents.FileStoreReadRequest) ([]byte, error) {
	if strings.TrimSpace(req.VolumeID) == "" || strings.TrimSpace(req.Path) == "" {
		if len(req.FallbackContent) == 0 {
			return nil, gatewaymanagedagents.ErrFileNotFound
		}
		return append([]byte(nil), req.FallbackContent...), nil
	}
	client, err := s.client(credential)
	if err != nil {
		return nil, err
	}
	content, err := client.ReadVolumeFile(ctx, req.VolumeID, req.Path)
	if err != nil {
		return nil, fmt.Errorf("read file-store content: %w", err)
	}
	return content, nil
}

func (s *VolumeFileStore) DeleteFile(ctx context.Context, credential gatewaymanagedagents.RequestCredential, req gatewaymanagedagents.FileStoreDeleteRequest) error {
	if strings.TrimSpace(req.VolumeID) == "" {
		return nil
	}
	client, err := s.client(credential)
	if err != nil {
		return err
	}
	if _, err := client.DeleteVolumeWithOptions(ctx, req.VolumeID, &sandbox0sdk.DeleteVolumeOptions{Force: true}); err != nil {
		return fmt.Errorf("delete file-store volume: %w", err)
	}
	return nil
}

func (s *VolumeFileStore) client(credential gatewaymanagedagents.RequestCredential) (*sandbox0sdk.Client, error) {
	if strings.TrimSpace(s.baseURL) == "" {
		return nil, fmt.Errorf("file-store sandbox0 base URL is required")
	}
	if strings.TrimSpace(credential.Token) == "" {
		return nil, fmt.Errorf("request credential is required")
	}
	return sandbox0sdk.NewClient(
		sandbox0sdk.WithBaseURL(s.baseURL),
		sandbox0sdk.WithToken(credential.Token),
		sandbox0sdk.WithTimeout(s.timeout),
	)
}

func managedFileStorePath(teamID, fileID, filename string) string {
	name := sanitizeName(filename)
	if name == "" {
		name = "content"
	}
	return path.Join("/managed-agent-files", sanitizeName(teamID), sanitizeName(fileID), name)
}
