package managedagents

import (
	"context"
	"io"
)

// FileStore keeps uploaded file bytes outside PostgreSQL while PostgreSQL remains
// the metadata source of truth.
type FileStore interface {
	PutFile(ctx context.Context, credential RequestCredential, req FileStorePutRequest) (FileStoreObject, error)
	ReadFile(ctx context.Context, credential RequestCredential, req FileStoreReadRequest) ([]byte, error)
	DeleteFile(ctx context.Context, credential RequestCredential, req FileStoreDeleteRequest) error
}

type FileStorePutRequest struct {
	TeamID   string
	FileID   string
	Filename string
	MimeType string
	Content  io.Reader
}

type FileStoreReadRequest struct {
	TeamID          string
	FileID          string
	VolumeID        string
	Path            string
	FallbackContent []byte
}

type FileStoreDeleteRequest struct {
	TeamID   string
	FileID   string
	VolumeID string
	Path     string
}

type FileStoreObject struct {
	VolumeID  string
	Path      string
	SizeBytes int64
	SHA256    string
}
