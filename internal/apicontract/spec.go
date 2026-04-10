package apicontract

import _ "embed"

// ManagedAgentOpenAPI contains the normalized managed-agent OpenAPI contract
// used for generated models and runtime request validation.
//
//go:embed generated/managed-agent-openapi.codegen.yaml
var ManagedAgentOpenAPI []byte
