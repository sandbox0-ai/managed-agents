package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

func main() {
	inputPath := flag.String("in", "", "input OpenAPI YAML path")
	outputPath := flag.String("out", "", "output normalized OpenAPI YAML path")
	flag.Parse()

	if strings.TrimSpace(*inputPath) == "" || strings.TrimSpace(*outputPath) == "" {
		_, _ = fmt.Fprintln(os.Stderr, "usage: openapi-normalize -in <spec.yaml> -out <normalized.yaml>")
		os.Exit(2)
	}

	input, err := os.ReadFile(*inputPath)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "read input spec: %v\n", err)
		os.Exit(1)
	}

	var spec map[string]any
	if err := yaml.Unmarshal(input, &spec); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "decode input spec: %v\n", err)
		os.Exit(1)
	}

	normalizeSpec(spec)

	output, err := yaml.Marshal(spec)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "encode normalized spec: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(*outputPath, output, 0o644); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "write normalized spec: %v\n", err)
		os.Exit(1)
	}
}

func normalizeSpec(spec map[string]any) {
	if _, ok := spec["openapi"].(string); ok {
		spec["openapi"] = "3.0.3"
	}
	if info, ok := spec["info"].(map[string]any); ok {
		if strings.TrimSpace(asString(info["version"])) == "" {
			info["version"] = "managed-agent-spec"
		}
	}
	if paths, ok := spec["paths"].(map[string]any); ok {
		spec["paths"] = normalizePaths(paths)
	}
	normalizeNode(spec)
}

func normalizePaths(paths map[string]any) map[string]any {
	out := make(map[string]any, len(paths))
	for key, value := range paths {
		normalized := key
		if idx := strings.Index(normalized, "?"); idx >= 0 {
			normalized = normalized[:idx]
		}
		if _, exists := out[normalized]; exists {
			panic(fmt.Sprintf("normalized path collision: %s", normalized))
		}
		out[normalized] = value
	}
	return out
}

func normalizeNode(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			typed[key] = normalizeNode(child)
		}
		normalizeConst(typed)
		normalizeExamples(typed)
		normalizeNullableUnion(typed, "anyOf")
		normalizeNullableUnion(typed, "oneOf")
		return typed
	case []any:
		for i, child := range typed {
			typed[i] = normalizeNode(child)
		}
		return typed
	default:
		return value
	}
}

func normalizeNullableUnion(node map[string]any, field string) {
	entries, ok := node[field].([]any)
	if !ok || len(entries) == 0 {
		return
	}

	nonNull := make([]any, 0, len(entries))
	hasNull := false
	for _, entry := range entries {
		schema, ok := entry.(map[string]any)
		if ok && isNullSchema(schema) {
			hasNull = true
			continue
		}
		nonNull = append(nonNull, entry)
	}

	if !hasNull {
		return
	}

	node["nullable"] = true
	if len(nonNull) == 1 {
		if schema, ok := nonNull[0].(map[string]any); ok {
			for key, value := range schema {
				if _, exists := node[key]; !exists || key == field {
					node[key] = value
				}
			}
			delete(node, field)
			return
		}
	}

	node[field] = nonNull
}

func normalizeConst(node map[string]any) {
	value, ok := node["const"]
	if !ok {
		return
	}
	if _, exists := node["enum"]; !exists {
		node["enum"] = []any{value}
	}
	delete(node, "const")
}

func normalizeExamples(node map[string]any) {
	value, ok := node["examples"]
	if !ok {
		return
	}
	if _, exists := node["example"]; !exists {
		if examples, ok := value.([]any); ok && len(examples) > 0 {
			node["example"] = examples[0]
		}
	}
	delete(node, "examples")
}

func isNullSchema(schema map[string]any) bool {
	typeName, ok := schema["type"].(string)
	return ok && typeName == "null"
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}
