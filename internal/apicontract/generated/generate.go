package generated

//go:generate go run ../../../cmd/openapi-normalize -in ../../../../managed-agents-spec/specs/managed-agent-openapi.sdk-compatible.yaml -out ./managed-agent-openapi.codegen.yaml
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.6.0 -generate types -package generated -o ./types_gen.go ./managed-agent-openapi.codegen.yaml
