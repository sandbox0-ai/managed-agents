package managedagents

import (
	"errors"
	"path"
	"strings"
)

func parseSkillUpload(files []uploadedSkillFile, fallbackDirectory string) (*parsedSkillUpload, error) {
	if len(files) == 0 {
		return nil, errors.New("files are required")
	}
	topDirectory := ""
	skillMarkdown := ""
	normalizedFiles := make([]uploadedSkillFile, 0, len(files))
	for _, file := range files {
		normalizedPath := normalizeUploadedPath(file.Path)
		if normalizedPath == "" {
			return nil, errors.New("uploaded file path is required")
		}
		directory, relativePath := splitUploadedPath(normalizedPath)
		if directory != "" {
			if topDirectory == "" {
				topDirectory = directory
			} else if topDirectory != directory {
				return nil, errors.New("all skill files must be in the same top-level directory")
			}
		}
		if strings.EqualFold(relativePath, "SKILL.md") {
			skillMarkdown = string(file.Content)
		}
		normalizedFiles = append(normalizedFiles, uploadedSkillFile{
			Path:    normalizedPath,
			Content: append([]byte(nil), file.Content...),
		})
	}
	if topDirectory == "" {
		topDirectory = sanitizeSkillDirectory(fallbackDirectory)
	}
	if topDirectory == "" {
		topDirectory = "skill"
	}
	if strings.TrimSpace(skillMarkdown) == "" {
		return nil, errors.New("skill upload must include SKILL.md at the top-level directory root")
	}
	name, description := extractSkillMetadata(skillMarkdown)
	if name == "" {
		name = topDirectory
	}
	if description == "" {
		description = "Custom skill uploaded to sandbox0 managed agents"
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

func sanitizeSkillDirectory(value string) string {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	trimmed = strings.ReplaceAll(trimmed, " ", "-")
	trimmed = strings.ReplaceAll(trimmed, "_", "-")
	trimmed = strings.Trim(trimmed, "-")
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	for _, char := range trimmed {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
			builder.WriteRune(char)
		}
	}
	return strings.Trim(builder.String(), "-")
}

func extractSkillMetadata(markdown string) (string, string) {
	frontMatter := parseFrontMatter(markdown)
	name := strings.TrimSpace(frontMatter["name"])
	description := strings.TrimSpace(frontMatter["description"])
	if name == "" {
		name = firstMarkdownHeading(markdown)
	}
	if description == "" {
		description = firstMarkdownParagraph(markdown)
	}
	return name, description
}

func parseFrontMatter(markdown string) map[string]string {
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return map[string]string{}
	}
	values := map[string]string{}
	currentKey := ""
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
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
	return values
}

func firstMarkdownHeading(markdown string) string {
	for _, line := range strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			return strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		}
	}
	return ""
}

func firstMarkdownParagraph(markdown string) string {
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")
	skipFrontMatter := false
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		skipFrontMatter = true
	}
	paragraph := []string{}
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if skipFrontMatter {
			if index == 0 {
				continue
			}
			if trimmed == "---" {
				skipFrontMatter = false
			}
			continue
		}
		if trimmed == "" {
			if len(paragraph) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		paragraph = append(paragraph, trimmed)
	}
	return strings.TrimSpace(strings.Join(paragraph, " "))
}
