package managedagents

import (
	"testing"
)

func TestCreateSessionParamsRejectsUnknownResourceFields(t *testing.T) {
	err := validateJSONBodyAgainstContractSchema("BetaManagedAgentsCreateSessionParams", []byte(`{
		"agent":"agent_123",
		"environment_id":"env_123",
		"resources":[{"type":"file","file_id":"file_123","extra":true}]
	}`))
	if err == nil {
		t.Fatal("expected unknown resource field error")
	}
}

func TestCreateSessionParamsParsesGitHubResourceCheckout(t *testing.T) {
	err := validateJSONBodyAgainstContractSchema("BetaManagedAgentsCreateSessionParams", []byte(`{
		"agent":"agent_123",
		"environment_id":"env_123",
		"resources":[{"type":"github_repository","url":"https://github.com/example/repo","authorization_token":"ghp_x","checkout":{"type":"branch","name":"main"}}]
	}`))
	if err != nil {
		t.Fatalf("validate request: %v", err)
	}
}

func TestSendEventsParamsRejectsUnknownEventFields(t *testing.T) {
	err := validateJSONBodyAgainstContractSchema("BetaManagedAgentsSendSessionEventsParams", []byte(`{"events":[{"type":"user.interrupt","id":"evt_123"}]}`))
	if err == nil {
		t.Fatal("expected unknown event field error")
	}
}

func TestSendEventsParamsRejectsUnknownImageSourceFields(t *testing.T) {
	err := validateJSONBodyAgainstContractSchema("BetaManagedAgentsSendSessionEventsParams", []byte(`{
		"events":[{"type":"user.message","content":[{"type":"image","source":{"type":"url","url":"https://example.com/a.png","extra":true}}]}]
	}`))
	if err == nil {
		t.Fatal("expected unknown image source field error")
	}
}

func TestUpdateSessionResourceRequestRejectsUnknownFields(t *testing.T) {
	err := validateJSONBodyAgainstContractSchema("BetaManagedAgentsUpdateSessionResourceParams", []byte(`{"authorization_token":"ghp_x","extra":true}`))
	if err == nil {
		t.Fatal("expected unknown session resource update field error")
	}
}

func TestAddSessionResourceRequestRejectsGitHubRepository(t *testing.T) {
	err := validateJSONBodyAgainstContractSchema("BetaManagedAgentsAddSessionResourceParams", []byte(`{"type":"github_repository","url":"https://github.com/example/repo","authorization_token":"ghp_x"}`))
	if err == nil {
		t.Fatal("expected github repository add-resource error")
	}
}
