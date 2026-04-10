package managedagents

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"

	contract "github.com/sandbox0-ai/managed-agent/internal/apicontract/generated"
)

func convertJSON[Dst any](src any) (Dst, error) {
	var out Dst
	encoded, err := json.Marshal(src)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(encoded, &out); err != nil {
		return out, err
	}
	return out, nil
}

func contractListResponse[Dst any](items any, nextPage *string) (Dst, error) {
	return convertJSON[Dst](map[string]any{"data": items, "next_page": nextPage})
}

func contractSkillsListResponse[Dst any](items any, nextPage *string, hasMore bool) (Dst, error) {
	return convertJSON[Dst](map[string]any{"data": items, "next_page": nextPage, "has_more": hasMore})
}

func createSessionParamsFromContract(req contract.BetaManagedAgentsCreateSessionParams) (CreateSessionParams, error) {
	return convertJSON[CreateSessionParams](req)
}

func createAgentRequestFromContract(req contract.BetaManagedAgentsCreateAgentParams) (CreateAgentRequest, error) {
	return convertJSON[CreateAgentRequest](req)
}

func createEnvironmentRequestFromContract(req contract.BetaPublicEnvironmentCreateRequest) (CreateEnvironmentRequest, error) {
	return convertJSON[CreateEnvironmentRequest](req)
}

func createVaultRequestFromContract(req contract.BetaManagedAgentsCreateVaultRequest) (CreateVaultRequest, error) {
	return convertJSON[CreateVaultRequest](req)
}

func createCredentialRequestFromContract(req contract.BetaManagedAgentsCreateCredentialRequestBody) (CreateCredentialRequest, error) {
	return convertJSON[CreateCredentialRequest](req)
}

func addSessionResourceRequestFromContract(req contract.BetaManagedAgentsAddSessionResourceParams) (AddSessionResourceRequest, error) {
	return convertJSON[AddSessionResourceRequest](req)
}

func updateSessionResourceRequestFromContract(req contract.BetaManagedAgentsUpdateSessionResourceParams) (UpdateSessionResourceRequest, error) {
	return convertJSON[UpdateSessionResourceRequest](req)
}

func sendEventsParamsFromContract(req contract.BetaManagedAgentsSendSessionEventsParams) (SendEventsParams, error) {
	return convertJSON[SendEventsParams](req)
}

func listSessionsToContract(items []*Session, nextPage *string) (contract.BetaManagedAgentsListSessions, error) {
	return contractListResponse[contract.BetaManagedAgentsListSessions](items, nextPage)
}

func sessionToContract(item *Session) (contract.BetaManagedAgentsSession, error) {
	return convertJSON[contract.BetaManagedAgentsSession](item)
}

func deletedSessionToContract(item map[string]any) (contract.BetaManagedAgentsDeletedSession, error) {
	return convertJSON[contract.BetaManagedAgentsDeletedSession](item)
}

func listAgentsToContract(items []Agent, nextPage *string) (contract.BetaManagedAgentsListAgents, error) {
	return contractListResponse[contract.BetaManagedAgentsListAgents](items, nextPage)
}

func agentToContract(item *Agent) (contract.BetaManagedAgentsAgent, error) {
	return convertJSON[contract.BetaManagedAgentsAgent](item)
}

func listEnvironmentsToContract(items []Environment, nextPage *string) (contract.BetaEnvironmentListResponse, error) {
	return contractListResponse[contract.BetaEnvironmentListResponse](items, nextPage)
}

func environmentToContract(item *Environment) (contract.BetaEnvironment, error) {
	return convertJSON[contract.BetaEnvironment](item)
}

func deletedEnvironmentToContract(item map[string]any) (contract.BetaEnvironmentDeleteResponse, error) {
	return convertJSON[contract.BetaEnvironmentDeleteResponse](item)
}

func listVaultsToContract(items []Vault, nextPage *string) (contract.BetaManagedAgentsListVaultsResponse, error) {
	return contractListResponse[contract.BetaManagedAgentsListVaultsResponse](items, nextPage)
}

func vaultToContract(item *Vault) (contract.BetaManagedAgentsVault, error) {
	return convertJSON[contract.BetaManagedAgentsVault](item)
}

func deletedVaultToContract(item map[string]any) (contract.BetaManagedAgentsDeletedVault, error) {
	return convertJSON[contract.BetaManagedAgentsDeletedVault](item)
}

func listCredentialsToContract(items []Credential, nextPage *string) (contract.BetaManagedAgentsListCredentialsResponse, error) {
	return contractListResponse[contract.BetaManagedAgentsListCredentialsResponse](items, nextPage)
}

func credentialToContract(item *Credential) (contract.BetaManagedAgentsCredential, error) {
	return convertJSON[contract.BetaManagedAgentsCredential](item)
}

func deletedCredentialToContract(item map[string]any) (contract.BetaManagedAgentsDeletedCredential, error) {
	return convertJSON[contract.BetaManagedAgentsDeletedCredential](item)
}

func fileMetadataToContract(item FileMetadata) (contract.BetaFileMetadataSchema, error) {
	return convertJSON[contract.BetaFileMetadataSchema](item)
}

func fileListToContract(item FileListResponse) (contract.BetaFileListResponse, error) {
	return convertJSON[contract.BetaFileListResponse](item)
}

func deletedFileToContract(item map[string]any) (contract.BetaFileDeleteResponse, error) {
	return convertJSON[contract.BetaFileDeleteResponse](item)
}

func listSessionResourcesToContract(items []map[string]any, nextPage *string) (contract.BetaManagedAgentsListSessionResources, error) {
	return contractListResponse[contract.BetaManagedAgentsListSessionResources](items, nextPage)
}

func sessionResourceToContract(item map[string]any) (contract.BetaManagedAgentsSessionResource, error) {
	return convertJSON[contract.BetaManagedAgentsSessionResource](item)
}

func addedSessionResourceToContract(item map[string]any) (contract.BetaManagedAgentsAddSessionResource, error) {
	return convertJSON[contract.BetaManagedAgentsAddSessionResource](item)
}

func updatedSessionResourceToContract(item map[string]any) (contract.BetaManagedAgentsUpdateSessionResource, error) {
	return convertJSON[contract.BetaManagedAgentsUpdateSessionResource](item)
}

func deletedSessionResourceToContract(item map[string]any) (contract.BetaManagedAgentsDeleteSessionResource, error) {
	return convertJSON[contract.BetaManagedAgentsDeleteSessionResource](item)
}

func skillToCreateContract(item *Skill) (contract.BetaCreateSkillResponse, error) {
	return convertJSON[contract.BetaCreateSkillResponse](item)
}

func skillToGetContract(item *Skill) (contract.BetaGetSkillResponse, error) {
	return convertJSON[contract.BetaGetSkillResponse](item)
}

func deletedSkillToContract(item map[string]any) (contract.BetaDeleteSkillResponse, error) {
	return convertJSON[contract.BetaDeleteSkillResponse](item)
}

func listSkillsToContract(items []Skill, nextPage *string, hasMore bool) (contract.BetaListSkillsResponse, error) {
	return contractSkillsListResponse[contract.BetaListSkillsResponse](items, nextPage, hasMore)
}

func skillVersionToCreateContract(item *SkillVersion) (contract.BetaCreateSkillVersionResponse, error) {
	return convertJSON[contract.BetaCreateSkillVersionResponse](item)
}

func skillVersionToGetContract(item *SkillVersion) (contract.BetaGetSkillVersionResponse, error) {
	return convertJSON[contract.BetaGetSkillVersionResponse](item)
}

func deletedSkillVersionToContract(item map[string]any) (contract.BetaDeleteSkillVersionResponse, error) {
	return convertJSON[contract.BetaDeleteSkillVersionResponse](item)
}

func listSkillVersionsToContract(items []SkillVersion, nextPage *string, hasMore bool) (contract.BetaListSkillVersionsResponse, error) {
	return contractSkillsListResponse[contract.BetaListSkillVersionsResponse](items, nextPage, hasMore)
}

func listSessionEventsToContract(events []map[string]any, nextPage *string) (contract.BetaManagedAgentsListSessionEvents, error) {
	contractEvents, err := convertJSON[[]contract.BetaManagedAgentsSessionEvent](events)
	if err != nil {
		return contract.BetaManagedAgentsListSessionEvents{}, err
	}
	return contractListResponse[contract.BetaManagedAgentsListSessionEvents](contractEvents, nextPage)
}

func sendSessionEventsToContract(events []map[string]any) (contract.BetaManagedAgentsSendSessionEvents, error) {
	return convertJSON[contract.BetaManagedAgentsSendSessionEvents](map[string]any{"data": events})
}

func streamSessionEventToContract(event map[string]any) (contract.BetaManagedAgentsStreamSessionEvents, error) {
	return convertJSON[contract.BetaManagedAgentsStreamSessionEvents](event)
}

func jsonValueToMap(value any) (map[string]any, error) {
	return convertJSON[map[string]any](value)
}

func int32QueryValue(value *int32) int {
	if value == nil {
		return 0
	}
	return int(*value)
}

func intQueryValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func boolQueryValue(value *bool) bool {
	return value != nil && *value
}

func stringQueryValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func listOrderQueryValue(value *contract.BetaManagedAgentsListOrder) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func timestampQueryValue(value *contract.BetaTimestamp) *time.Time {
	if value == nil {
		return nil
	}
	timestamp := time.Time(*value)
	return &timestamp
}

func updateSessionParamsFromJSON(body []byte) (UpdateSessionParams, error) {
	var req contract.BetaManagedAgentsUpdateSessionParams
	if err := decodeJSONBytes(body, &req); err != nil {
		return UpdateSessionParams{}, err
	}
	raw, err := decodeRawObject(body)
	if err != nil {
		return UpdateSessionParams{}, err
	}
	var out UpdateSessionParams
	if value, ok := raw["title"]; ok {
		out.Title.Set = true
		if isJSONNull(value) {
			out.Title.Value = nil
		} else {
			var title string
			if err := decodeJSONBytes(value, &title); err != nil {
				return UpdateSessionParams{}, err
			}
			out.Title.Value = &title
		}
	}
	if value, ok := raw["metadata"]; ok {
		out.Metadata.Set = true
		if isJSONNull(value) {
			out.Metadata.Clear = true
		} else {
			var metadata map[string]*string
			if err := decodeJSONBytes(value, &metadata); err != nil {
				return UpdateSessionParams{}, err
			}
			out.Metadata.Values = metadata
		}
	}
	if value, ok := raw["vault_ids"]; ok {
		out.VaultIDs.Set = true
		if isJSONNull(value) {
			return UpdateSessionParams{}, errors.New("vault_ids must be an array")
		}
		var vaultIDs []string
		if err := decodeJSONBytes(value, &vaultIDs); err != nil {
			return UpdateSessionParams{}, err
		}
		out.VaultIDs.Values = vaultIDs
	}
	return out, nil
}

func updateAgentRequestFromJSON(body []byte) (UpdateAgentRequest, error) {
	var req contract.BetaManagedAgentsUpdateAgentParams
	if err := decodeJSONBytes(body, &req); err != nil {
		return UpdateAgentRequest{}, err
	}
	raw, err := decodeRawObject(body)
	if err != nil {
		return UpdateAgentRequest{}, err
	}
	out := UpdateAgentRequest{Version: int(req.Version)}
	if value, ok := raw["name"]; ok {
		if isJSONNull(value) {
			return UpdateAgentRequest{}, errors.New("name must be a string")
		}
		out.Name.Set = true
		if err := decodeJSONBytes(value, &out.Name.Value); err != nil {
			return UpdateAgentRequest{}, err
		}
	}
	if value, ok := raw["description"]; ok {
		out.Description.Set = true
		if !isJSONNull(value) {
			var description string
			if err := decodeJSONBytes(value, &description); err != nil {
				return UpdateAgentRequest{}, err
			}
			out.Description.Value = &description
		}
	}
	if value, ok := raw["system"]; ok {
		out.System.Set = true
		if !isJSONNull(value) {
			var system string
			if err := decodeJSONBytes(value, &system); err != nil {
				return UpdateAgentRequest{}, err
			}
			out.System.Value = &system
		}
	}
	if value, ok := raw["model"]; ok {
		if isJSONNull(value) {
			return UpdateAgentRequest{}, errors.New("model must not be null")
		}
		var model any
		if err := json.Unmarshal(value, &model); err != nil {
			return UpdateAgentRequest{}, err
		}
		out.Model = OptionalValueField{Set: true, Value: model}
	}
	if value, ok := raw["tools"]; ok {
		values, err := decodeJSONArrayOrNull(value)
		if err != nil {
			return UpdateAgentRequest{}, err
		}
		out.Tools = OptionalJSONArrayField{Set: true, Values: values}
	}
	if value, ok := raw["mcp_servers"]; ok {
		values, err := decodeJSONArrayOrNull(value)
		if err != nil {
			return UpdateAgentRequest{}, err
		}
		out.MCPServers = OptionalJSONArrayField{Set: true, Values: values}
	}
	if value, ok := raw["skills"]; ok {
		values, err := decodeJSONArrayOrNull(value)
		if err != nil {
			return UpdateAgentRequest{}, err
		}
		out.Skills = OptionalJSONArrayField{Set: true, Values: values}
	}
	if value, ok := raw["metadata"]; ok {
		out.Metadata.Set = true
		if isJSONNull(value) {
			out.Metadata.Clear = true
		} else {
			var metadata map[string]*string
			if err := decodeJSONBytes(value, &metadata); err != nil {
				return UpdateAgentRequest{}, err
			}
			out.Metadata.Values = metadata
		}
	}
	return out, nil
}

func updateEnvironmentRequestFromJSON(body []byte) (UpdateEnvironmentRequest, error) {
	var req contract.BetaPublicEnvironmentUpdateRequest
	if err := decodeJSONBytes(body, &req); err != nil {
		return UpdateEnvironmentRequest{}, err
	}
	raw, err := decodeRawObject(body)
	if err != nil {
		return UpdateEnvironmentRequest{}, err
	}
	var out UpdateEnvironmentRequest
	if value, ok := raw["name"]; ok {
		if isJSONNull(value) {
			return UpdateEnvironmentRequest{}, errors.New("name must be a string")
		}
		var name string
		if err := decodeJSONBytes(value, &name); err != nil {
			return UpdateEnvironmentRequest{}, err
		}
		out.Name = &name
	}
	if value, ok := raw["description"]; ok {
		out.Description.Set = true
		if !isJSONNull(value) {
			var description string
			if err := decodeJSONBytes(value, &description); err != nil {
				return UpdateEnvironmentRequest{}, err
			}
			out.Description.Value = &description
		}
	}
	if value, ok := raw["config"]; ok {
		if isJSONNull(value) {
			return UpdateEnvironmentRequest{}, errors.New("config must be an object")
		}
		var config map[string]any
		if err := json.Unmarshal(value, &config); err != nil {
			return UpdateEnvironmentRequest{}, err
		}
		out.Config = config
	}
	if value, ok := raw["metadata"]; ok {
		out.Metadata.Set = true
		if isJSONNull(value) {
			out.Metadata.Clear = true
		} else {
			var metadata map[string]*string
			if err := decodeJSONBytes(value, &metadata); err != nil {
				return UpdateEnvironmentRequest{}, err
			}
			out.Metadata.Values = metadata
		}
	}
	return out, nil
}

func updateVaultRequestFromJSON(body []byte) (UpdateVaultRequest, error) {
	var req contract.BetaManagedAgentsUpdateVaultRequestBody
	if err := decodeJSONBytes(body, &req); err != nil {
		return UpdateVaultRequest{}, err
	}
	raw, err := decodeRawObject(body)
	if err != nil {
		return UpdateVaultRequest{}, err
	}
	var out UpdateVaultRequest
	if value, ok := raw["display_name"]; ok {
		out.DisplayName.Set = true
		if !isJSONNull(value) {
			var displayName string
			if err := decodeJSONBytes(value, &displayName); err != nil {
				return UpdateVaultRequest{}, err
			}
			out.DisplayName.Value = &displayName
		}
	}
	if value, ok := raw["metadata"]; ok {
		out.Metadata.Set = true
		if isJSONNull(value) {
			out.Metadata.Clear = true
		} else {
			var metadata map[string]*string
			if err := decodeJSONBytes(value, &metadata); err != nil {
				return UpdateVaultRequest{}, err
			}
			out.Metadata.Values = metadata
		}
	}
	return out, nil
}

func updateCredentialRequestFromJSON(body []byte) (UpdateCredentialRequest, error) {
	var req contract.BetaManagedAgentsUpdateCredentialRequestBody
	if err := decodeJSONBytes(body, &req); err != nil {
		return UpdateCredentialRequest{}, err
	}
	raw, err := decodeRawObject(body)
	if err != nil {
		return UpdateCredentialRequest{}, err
	}
	var out UpdateCredentialRequest
	if value, ok := raw["auth"]; ok {
		if isJSONNull(value) {
			return UpdateCredentialRequest{}, errors.New("auth must not be null")
		}
		var auth map[string]any
		if err := json.Unmarshal(value, &auth); err != nil {
			return UpdateCredentialRequest{}, err
		}
		out.Auth = auth
	}
	if value, ok := raw["display_name"]; ok {
		out.DisplayName.Set = true
		if !isJSONNull(value) {
			var displayName string
			if err := decodeJSONBytes(value, &displayName); err != nil {
				return UpdateCredentialRequest{}, err
			}
			out.DisplayName.Value = &displayName
		}
	}
	if value, ok := raw["metadata"]; ok {
		out.Metadata.Set = true
		if isJSONNull(value) {
			out.Metadata.Clear = true
		} else {
			var metadata map[string]*string
			if err := decodeJSONBytes(value, &metadata); err != nil {
				return UpdateCredentialRequest{}, err
			}
			out.Metadata.Values = metadata
		}
	}
	return out, nil
}

func decodeRawObject(body []byte) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func decodeJSONArrayOrNull(body []byte) ([]any, error) {
	if isJSONNull(body) {
		return []any{}, nil
	}
	var values []any
	if err := json.Unmarshal(body, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func isJSONNull(body []byte) bool {
	return bytes.Equal(bytes.TrimSpace(body), []byte("null"))
}
