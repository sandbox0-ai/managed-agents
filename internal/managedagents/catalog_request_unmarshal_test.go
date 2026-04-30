package managedagents

import (
	"testing"

	contract "github.com/sandbox0-ai/managed-agent/internal/apicontract/generated"
)

func TestCreateAgentRequestRejectsUnknownToolFields(t *testing.T) {
	err := validateJSONBodyAgainstContractSchema("BetaManagedAgentsCreateAgentParams", []byte(`{
		"name":"agent",
		"model":"claude-sonnet-4-6",
		"tools":[{"type":"custom","name":"lookup","description":"desc","input_schema":{"type":"object"},"extra":true}]
	}`))
	if err == nil {
		t.Fatal("expected unknown tool field error")
	}
}

func TestCreateAgentRequestRejectsInvalidSkillDiscriminator(t *testing.T) {
	err := validateJSONBodyAgainstContractSchema("BetaManagedAgentsCreateAgentParams", []byte(`{
		"name":"agent",
		"model":"claude-sonnet-4-6",
		"skills":[{"type":"unknown","skill_id":"x"}]
	}`))
	if err == nil {
		t.Fatal("expected invalid skill type error")
	}
}

func TestCreateAgentRequestAcceptsStdioMCPServer(t *testing.T) {
	err := validateJSONBodyAgainstContractSchema("BetaManagedAgentsCreateAgentParams", []byte(`{
		"name":"agent",
		"model":"claude-sonnet-4-6",
		"mcp_servers":[{
			"type":"stdio",
			"name":"local_docs",
			"command":"node",
			"args":["./server.js","--stdio"],
			"env":{"API_BASE_URL":"https://api.example.com"}
		}],
		"tools":[{"type":"mcp_toolset","mcp_server_name":"local_docs"}]
	}`))
	if err != nil {
		t.Fatalf("validate schema: %v", err)
	}
}

func TestCreateCredentialRequestRejectsImmutableServerURLOnUpdateShape(t *testing.T) {
	err := validateJSONBodyAgainstContractSchema("BetaManagedAgentsUpdateCredentialRequestBody", []byte(`{
		"auth":{"type":"static_bearer","mcp_server_url":"https://example.com/sse"}
	}`))
	if err == nil {
		t.Fatal("expected auth update shape error")
	}
}

func TestCreateCredentialRequestParsesTypedMcpOAuthAuth(t *testing.T) {
	body := []byte(`{
		"auth":{
			"type":"mcp_oauth",
			"mcp_server_url":"https://example.com/sse",
			"access_token":"access",
			"refresh":{
				"refresh_token":"refresh",
				"token_endpoint":"https://example.com/token",
				"client_id":"client",
				"token_endpoint_auth":{"type":"none"}
			}
		}
	}`)
	if err := validateJSONBodyAgainstContractSchema("BetaManagedAgentsCreateCredentialRequestBody", body); err != nil {
		t.Fatalf("validate schema: %v", err)
	}
	var req contract.BetaManagedAgentsCreateCredentialRequestBody
	err := decodeJSONBytes(body, &req)
	if err != nil {
		t.Fatalf("decode contract: %v", err)
	}
	params, err := createCredentialRequestFromContract(req)
	if err != nil {
		t.Fatalf("convert request: %v", err)
	}
	if got := stringValue(params.Auth["type"]); got != "mcp_oauth" {
		t.Fatalf("auth.type = %q, want mcp_oauth", got)
	}
}

func TestCreateAgentRequestRejectsUnknownModelFields(t *testing.T) {
	err := validateJSONBodyAgainstContractSchema("BetaManagedAgentsCreateAgentParams", []byte(`{
		"name":"agent",
		"model":{"id":"claude-sonnet-4-6","speed":"standard","extra":true}
	}`))
	if err == nil {
		t.Fatal("expected unknown model field error")
	}
}

func TestCreateAgentRequestRejectsInvalidPermissionPolicyType(t *testing.T) {
	err := validateJSONBodyAgainstContractSchema("BetaManagedAgentsCreateAgentParams", []byte(`{
		"name":"agent",
		"model":"claude-sonnet-4-6",
		"tools":[{"type":"agent_toolset_20260401","default_config":{"permission_policy":{"type":"sometimes"}}}]
	}`))
	if err == nil {
		t.Fatal("expected invalid permission policy error")
	}
}

func TestCreateCredentialRequestRejectsUnknownTokenEndpointAuthFields(t *testing.T) {
	err := validateJSONBodyAgainstContractSchema("BetaManagedAgentsCreateCredentialRequestBody", []byte(`{
		"auth":{
			"type":"mcp_oauth",
			"mcp_server_url":"https://example.com/sse",
			"access_token":"access",
			"refresh":{
				"refresh_token":"refresh",
				"token_endpoint":"https://example.com/token",
				"client_id":"client",
				"token_endpoint_auth":{"type":"client_secret_post","client_secret":"secret","extra":true}
			}
		}
	}`))
	if err == nil {
		t.Fatal("expected unknown token endpoint auth field error")
	}
}

func TestCreateAgentRequiresModel(t *testing.T) {
	service := NewService(&Repository{}, nil, nil)
	_, err := service.CreateAgent(t.Context(), Principal{TeamID: "team_123"}, CreateAgentRequest{Name: "agent"})
	if err == nil || err.Error() != "model is required" {
		t.Fatalf("CreateAgent error = %v, want model is required", err)
	}
}

func TestCreateAgentRejectsModelObjectWithoutID(t *testing.T) {
	service := NewService(&Repository{}, nil, nil)
	_, err := service.CreateAgent(t.Context(), Principal{TeamID: "team_123"}, CreateAgentRequest{
		Name:  "agent",
		Model: map[string]any{"speed": "standard"},
	})
	if err == nil || err.Error() != "model.id is required" {
		t.Fatalf("CreateAgent error = %v, want model.id is required", err)
	}
}

func TestCreateEnvironmentRequestRejectsUnknownNetworkingFields(t *testing.T) {
	err := validateJSONBodyAgainstContractSchema("BetaPublicEnvironmentCreateRequest", []byte(`{
		"name":"env",
		"config":{"type":"cloud","networking":{"type":"limited","extra":true}}
	}`))
	if err == nil {
		t.Fatal("expected unknown networking field error")
	}
}

func TestUpdateEnvironmentRequestRejectsInvalidNetworkingType(t *testing.T) {
	err := validateJSONBodyAgainstContractSchema("BetaPublicEnvironmentUpdateRequest", []byte(`{
		"config":{"type":"cloud","networking":{"type":"custom"}}
	}`))
	if err == nil {
		t.Fatal("expected invalid networking type error")
	}
}
