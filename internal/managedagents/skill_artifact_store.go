package managedagents

import (
	"context"
	"io"
)

// SkillArtifactStore persists immutable uploaded skill-version artifacts outside PostgreSQL.
type SkillArtifactStore interface {
	PutSkillVersion(ctx context.Context, credential RequestCredential, req SkillArtifactPutRequest) (SkillArtifactObject, error)
	DeleteSkillVersion(ctx context.Context, credential RequestCredential, req SkillArtifactDeleteRequest) error
}

type SkillArtifactPutRequest struct {
	TeamID        string
	SkillID       string
	Version       string
	ContentDigest string
	Content       io.Reader
}

type SkillArtifactDeleteRequest struct {
	TeamID   string
	SkillID  string
	Version  string
	VolumeID string
	Path     string
}

type SkillArtifactObject struct {
	VolumeID  string
	Path      string
	SizeBytes int64
	SHA256    string
}
