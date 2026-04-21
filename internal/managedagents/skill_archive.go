package managedagents

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

type encodedSkillArtifact struct {
	Archive       []byte
	ContentDigest string
	FileCount     int
}

func buildSkillArtifact(parsed *parsedSkillUpload) (*encodedSkillArtifact, error) {
	files, err := normalizedSkillArtifactFiles(parsed)
	if err != nil {
		return nil, err
	}
	contentDigest, err := skillArtifactContentDigest(files)
	if err != nil {
		return nil, err
	}
	archive, err := encodeSkillArchive(files)
	if err != nil {
		return nil, err
	}
	return &encodedSkillArtifact{
		Archive:       archive,
		ContentDigest: contentDigest,
		FileCount:     len(files),
	}, nil
}

func normalizedSkillArtifactFiles(parsed *parsedSkillUpload) ([]storedSkillFile, error) {
	if parsed == nil {
		return nil, fmt.Errorf("parsed skill upload is required")
	}
	root := strings.TrimSpace(parsed.Directory)
	if root == "" {
		return nil, fmt.Errorf("skill directory is required")
	}
	out := make([]storedSkillFile, 0, len(parsed.Files))
	for _, file := range parsed.Files {
		relative := skillArtifactRelativePath(root, file.Path)
		if relative == "" {
			return nil, fmt.Errorf("skill artifact path %q is invalid", file.Path)
		}
		out = append(out, storedSkillFile{
			Path:    relative,
			Content: append([]byte(nil), file.Content...),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Path < out[j].Path
	})
	return out, nil
}

func skillArtifactRelativePath(root, uploadedPath string) string {
	cleanRoot := path.Clean(strings.TrimSpace(strings.TrimPrefix(root, "/")))
	cleanPath := path.Clean(strings.TrimSpace(strings.TrimPrefix(uploadedPath, "/")))
	if cleanRoot == "." || cleanRoot == "" || cleanPath == "." || cleanPath == "" || strings.HasPrefix(cleanPath, "../") {
		return ""
	}
	if cleanPath == cleanRoot {
		return ""
	}
	prefix := cleanRoot + "/"
	if !strings.HasPrefix(cleanPath, prefix) {
		return ""
	}
	relative := strings.TrimPrefix(cleanPath, prefix)
	if relative == "." || relative == "" || strings.HasPrefix(relative, "../") {
		return ""
	}
	return relative
}

func skillArtifactContentDigest(files []storedSkillFile) (string, error) {
	type digestFile struct {
		Path      string `json:"path"`
		SHA256    string `json:"sha256"`
		SizeBytes int    `json:"size_bytes"`
	}
	payload := struct {
		Schema string       `json:"schema"`
		Files  []digestFile `json:"files"`
	}{
		Schema: "managed-agent-skill-version-v1",
		Files:  make([]digestFile, 0, len(files)),
	}
	for _, file := range files {
		sum := sha256.Sum256(file.Content)
		payload.Files = append(payload.Files, digestFile{
			Path:      file.Path,
			SHA256:    hex.EncodeToString(sum[:]),
			SizeBytes: len(file.Content),
		})
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal skill artifact digest payload: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func encodeSkillArchive(files []storedSkillFile) ([]byte, error) {
	var archive bytes.Buffer
	gzipWriter, err := gzip.NewWriterLevel(&archive, gzip.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("create skill archive gzip writer: %w", err)
	}
	gzipWriter.ModTime = time.Unix(0, 0)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, file := range files {
		name := path.Clean(strings.TrimSpace(file.Path))
		if name == "." || name == "" || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			return nil, fmt.Errorf("skill archive path %q is invalid", file.Path)
		}
		header := &tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(file.Content)),
			Typeflag: tar.TypeReg,
			ModTime:  time.Unix(0, 0),
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			return nil, fmt.Errorf("write skill archive header %s: %w", name, err)
		}
		if _, err := tarWriter.Write(file.Content); err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			return nil, fmt.Errorf("write skill archive body %s: %w", name, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		_ = gzipWriter.Close()
		return nil, fmt.Errorf("close skill archive tar writer: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, fmt.Errorf("close skill archive gzip writer: %w", err)
	}
	return archive.Bytes(), nil
}
