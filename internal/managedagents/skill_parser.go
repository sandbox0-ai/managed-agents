package managedagents

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

const (
	maxSkillNameLength        = 64
	maxSkillDescriptionLength = 1024
)

var reservedSkillNames = map[string]struct{}{
	"anthropic": {},
	"claude":    {},
	"codex":     {},
	"openai":    {},
}

func parseSkillUpload(files []uploadedSkillFile, fallbackDirectory string) (*parsedSkillUpload, error) {
	if len(files) == 0 {
		return nil, errors.New("files are required")
	}
	fallbackDirectory = normalizeUploadedPath(fallbackDirectory)
	if fallbackDirectory == "" || hasParentDirectorySegment(fallbackDirectory) {
		fallbackDirectory = ""
	}
	topDirectory := ""
	skillMarkdown := ""
	normalizedFiles := make([]uploadedSkillFile, 0, len(files))
	for _, file := range files {
		if hasParentDirectorySegment(strings.ReplaceAll(file.Path, "\\", "/")) {
			return nil, errors.New("uploaded file path must not contain parent directory segments")
		}
		normalizedPath := normalizeUploadedPath(file.Path)
		if normalizedPath == "" {
			return nil, errors.New("uploaded file path is required")
		}
		if hasParentDirectorySegment(normalizedPath) {
			return nil, errors.New("uploaded file path must not contain parent directory segments")
		}
		directory, relativePath := splitUploadedPath(normalizedPath)
		if directory == "" && fallbackDirectory != "" {
			directory = fallbackDirectory
			normalizedPath = path.Join(fallbackDirectory, relativePath)
		}
		if directory != "" {
			if topDirectory == "" {
				topDirectory = directory
			} else if topDirectory != directory {
				return nil, errors.New("all skill files must be in the same top-level directory")
			}
		}
		if directory != "" && strings.EqualFold(relativePath, "SKILL.md") {
			skillMarkdown = string(file.Content)
		}
		normalizedFiles = append(normalizedFiles, uploadedSkillFile{
			Path:    normalizedPath,
			Content: append([]byte(nil), file.Content...),
		})
	}
	if topDirectory == "" {
		return nil, errors.New("all skill files must be in the same top-level directory")
	}
	if strings.TrimSpace(skillMarkdown) == "" {
		return nil, errors.New("skill upload must include SKILL.md at the top-level directory root")
	}
	name, description, err := extractSkillMetadata(skillMarkdown)
	if err != nil {
		return nil, err
	}
	return &parsedSkillUpload{
		Name:        name,
		Description: description,
		Directory:   topDirectory,
		Files:       normalizedFiles,
	}, nil
}

func normalizeUploadedPath(value string) string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	trimmed = strings.TrimPrefix(trimmed, "./")
	trimmed = path.Clean(trimmed)
	if trimmed == "." || trimmed == "/" {
		return ""
	}
	return strings.TrimPrefix(trimmed, "/")
}

func splitUploadedPath(value string) (string, string) {
	parts := strings.Split(value, "/")
	if len(parts) <= 1 {
		return "", value
	}
	return parts[0], strings.Join(parts[1:], "/")
}

func hasParentDirectorySegment(value string) bool {
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func extractSkillMetadata(markdown string) (string, string, error) {
	frontMatter, ok := parseFrontMatter(markdown)
	if !ok {
		return "", "", errors.New("SKILL.md must begin with YAML front matter")
	}
	name := strings.TrimSpace(frontMatter["name"])
	description := strings.TrimSpace(frontMatter["description"])
	if err := validateSkillName(name); err != nil {
		return "", "", err
	}
	if err := validateSkillDescription(description); err != nil {
		return "", "", err
	}
	return name, description, nil
}

func parseFrontMatter(markdown string) (map[string]string, bool) {
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return map[string]string{}, false
	}
	values := map[string]string{}
	currentKey := ""
	closed := false
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			closed = true
			break
		}
		if currentKey != "" && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			piece := strings.TrimSpace(line)
			if piece != "" {
				if values[currentKey] != "" {
					values[currentKey] += " "
				}
				values[currentKey] += piece
			}
			continue
		}
		currentKey = ""
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			continue
		}
		if value == "|-" || value == ">-" || value == "|" || value == ">" {
			values[key] = ""
			currentKey = key
			continue
		}
		values[key] = strings.Trim(value, "\"'")
	}
	return values, closed
}

func validateSkillName(name string) error {
	if name == "" {
		return errors.New("skill name is required")
	}
	if len(name) > maxSkillNameLength {
		return fmt.Errorf("skill name must be at most %d characters", maxSkillNameLength)
	}
	if containsXMLTag(name) {
		return errors.New("skill name must not contain XML tags")
	}
	if _, reserved := reservedSkillNames[name]; reserved {
		return errors.New("skill name is reserved")
	}
	containsAlphaNumeric := false
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			containsAlphaNumeric = true
			continue
		}
		if char == '-' {
			continue
		}
		return errors.New("skill name must contain only lowercase letters, numbers, and hyphens")
	}
	if !containsAlphaNumeric {
		return errors.New("skill name must contain a lowercase letter or number")
	}
	return nil
}

func validateSkillDescription(description string) error {
	if description == "" {
		return errors.New("skill description is required")
	}
	if len(description) > maxSkillDescriptionLength {
		return fmt.Errorf("skill description must be at most %d characters", maxSkillDescriptionLength)
	}
	if containsXMLTag(description) {
		return errors.New("skill description must not contain XML tags")
	}
	return nil
}

func containsXMLTag(value string) bool {
	remaining := value
	for {
		start := strings.Index(remaining, "<")
		if start < 0 {
			return false
		}
		end := strings.Index(remaining[start+1:], ">")
		if end < 0 {
			return false
		}
		inside := strings.TrimSpace(remaining[start+1 : start+1+end])
		if inside != "" {
			first := inside[0]
			if first == '/' || (first >= 'A' && first <= 'Z') || (first >= 'a' && first <= 'z') {
				return true
			}
		}
		remaining = remaining[start+1+end+1:]
	}
}
