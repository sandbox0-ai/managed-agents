package managedagents

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

var stableSkillBundleTime = time.Unix(0, 0).UTC()

func buildSkillBundle(parsed *parsedSkillUpload) ([]byte, storedSkillBundle, error) {
	if parsed == nil || len(parsed.Files) == 0 {
		return nil, storedSkillBundle{}, fmt.Errorf("skill bundle requires files")
	}
	buffer := &bytes.Buffer{}
	gzipWriter := gzip.NewWriter(buffer)
	tarWriter := tar.NewWriter(gzipWriter)

	files := append([]uploadedSkillFile(nil), parsed.Files...)
	sort.Slice(files, func(i, j int) bool {
		return strings.TrimSpace(files[i].Path) < strings.TrimSpace(files[j].Path)
	})

	for _, file := range files {
		relativePath, err := normalizedSkillBundlePath(file.Path)
		if err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			return nil, storedSkillBundle{}, err
		}
		header := &tar.Header{
			Name:     relativePath,
			Mode:     0o644,
			Size:     int64(len(file.Content)),
			ModTime:  stableSkillBundleTime,
			Typeflag: tar.TypeReg,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			return nil, storedSkillBundle{}, fmt.Errorf("write skill bundle header %s: %w", relativePath, err)
		}
		if _, err := tarWriter.Write(file.Content); err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			return nil, storedSkillBundle{}, fmt.Errorf("write skill bundle content %s: %w", relativePath, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		_ = gzipWriter.Close()
		return nil, storedSkillBundle{}, fmt.Errorf("close skill bundle tar writer: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, storedSkillBundle{}, fmt.Errorf("close skill bundle gzip writer: %w", err)
	}
	data := buffer.Bytes()
	sum := sha256.Sum256(data)
	return append([]byte(nil), data...), storedSkillBundle{
		SHA256:    hex.EncodeToString(sum[:]),
		SizeBytes: int64(len(data)),
	}, nil
}

func normalizedSkillBundlePath(value string) (string, error) {
	cleaned := path.Clean(strings.TrimSpace(strings.TrimPrefix(value, "/")))
	if cleaned == "." || cleaned == "" || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("skill bundle path %q is invalid", value)
	}
	return cleaned, nil
}

func teamSkillBundleAssetStorePath(skillID, version string) string {
	return path.Join(
		"/managed-agents-assets",
		"skills",
		strings.TrimSpace(skillID),
		strings.TrimSpace(version),
		"bundle.tar.gz",
	)
}
