package managedagents

import (
	"context"
	"io"
)

// AssetStore keeps file-like asset bytes outside PostgreSQL while PostgreSQL
// remains the metadata source of truth.
type AssetStore interface {
	CreateStore(ctx context.Context, credential RequestCredential, req AssetStoreCreateStoreRequest) (AssetStoreVolume, error)
	DeleteStore(ctx context.Context, credential RequestCredential, req AssetStoreDeleteStoreRequest) error
	PutObject(ctx context.Context, credential RequestCredential, req AssetStorePutObjectRequest) (AssetStoreObject, error)
	ReadObject(ctx context.Context, credential RequestCredential, req AssetStoreReadObjectRequest) ([]byte, error)
	DeleteObject(ctx context.Context, credential RequestCredential, req AssetStoreDeleteObjectRequest) error
}

type AssetStoreCreateStoreRequest struct {
	TeamID   string
	RegionID string
}

type AssetStoreDeleteStoreRequest struct {
	TeamID   string
	RegionID string
	VolumeID string
}

type AssetStorePutObjectRequest struct {
	TeamID   string
	RegionID string
	VolumeID string
	Path     string
	Content  io.Reader
}

type AssetStoreReadObjectRequest struct {
	TeamID   string
	RegionID string
	VolumeID string
	Path     string
}

type AssetStoreDeleteObjectRequest struct {
	TeamID   string
	RegionID string
	VolumeID string
	Path     string
}

type AssetStoreVolume struct {
	VolumeID string
}

type AssetStoreObject struct {
	Path      string
	SizeBytes int64
	SHA256    string
}
