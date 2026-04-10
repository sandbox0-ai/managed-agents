package managedagents

import (
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	contract "github.com/sandbox0-ai/managed-agent/internal/apicontract/generated"
	"go.uber.org/zap"
)

func (h *Handler) CreateAgent(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req contract.BetaManagedAgentsCreateAgentParams
	if err := decodeContractJSONBody(c, "BetaManagedAgentsCreateAgentParams", &req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	params, err := createAgentRequestFromContract(req)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	agent, err := h.service.CreateAgent(c.Request.Context(), principal, params)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := agentToContract(agent)
	if err != nil {
		h.logger.Error("failed to encode create-agent response", zap.Error(err))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) ListAgents(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req contract.BetaListAgentsParams
	if err := c.ShouldBindQuery(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	agents, nextPage, err := h.service.ListAgents(c.Request.Context(), principal, AgentListOptions{
		Limit:           int32QueryValue(req.Limit),
		Page:            stringQueryValue(req.Page),
		Order:           "",
		IncludeArchived: boolQueryValue(req.IncludeArchived),
		CreatedAt: TimeFilter{
			GTE: timestampQueryValue(req.CreatedAtGte),
			LTE: timestampQueryValue(req.CreatedAtLte),
		},
	})
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := listAgentsToContract(agents, nextPage)
	if err != nil {
		h.logger.Error("failed to encode list-agents response", zap.Error(err))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) GetAgent(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req contract.BetaGetAgentParams
	if err := c.ShouldBindQuery(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	agent, err := h.service.GetAgent(c.Request.Context(), principal, c.Param("agent_id"), int32QueryValue(req.Version))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := agentToContract(agent)
	if err != nil {
		h.logger.Error("failed to encode get-agent response", zap.Error(err), zap.String("agent_id", c.Param("agent_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) UpdateAgent(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	body, err := readValidatedContractJSONBody(c, "BetaManagedAgentsUpdateAgentParams")
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	req, err := updateAgentRequestFromJSON(body)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	agent, err := h.service.UpdateAgent(c.Request.Context(), principal, c.Param("agent_id"), req)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := agentToContract(agent)
	if err != nil {
		h.logger.Error("failed to encode update-agent response", zap.Error(err), zap.String("agent_id", c.Param("agent_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) ArchiveAgent(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	agent, err := h.service.ArchiveAgent(c.Request.Context(), principal, c.Param("agent_id"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := agentToContract(agent)
	if err != nil {
		h.logger.Error("failed to encode archive-agent response", zap.Error(err), zap.String("agent_id", c.Param("agent_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) ListAgentVersions(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req contract.BetaListAgentVersionsParams
	if err := c.ShouldBindQuery(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	versions, nextPage, err := h.service.ListAgentVersions(c.Request.Context(), principal, c.Param("agent_id"), int32QueryValue(req.Limit), stringQueryValue(req.Page))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := listAgentsToContract(versions, nextPage)
	if err != nil {
		h.logger.Error("failed to encode list-agent-versions response", zap.Error(err), zap.String("agent_id", c.Param("agent_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) CreateEnvironment(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req contract.BetaPublicEnvironmentCreateRequest
	if err := decodeContractJSONBody(c, "BetaPublicEnvironmentCreateRequest", &req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	params, err := createEnvironmentRequestFromContract(req)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	environment, err := h.service.CreateEnvironment(c.Request.Context(), principal, params)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := environmentToContract(environment)
	if err != nil {
		h.logger.Error("failed to encode create-environment response", zap.Error(err))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) ListEnvironments(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req contract.BetaListEnvironmentsV1EnvironmentsGetParams
	if err := c.ShouldBindQuery(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	environments, nextPage, err := h.service.ListEnvironments(c.Request.Context(), principal, intQueryValue(req.Limit), stringQueryValue(req.Page), boolQueryValue(req.IncludeArchived))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := listEnvironmentsToContract(environments, nextPage)
	if err != nil {
		h.logger.Error("failed to encode list-environments response", zap.Error(err))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) GetEnvironment(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	environment, err := h.service.GetEnvironment(c.Request.Context(), principal, c.Param("environment_id"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := environmentToContract(environment)
	if err != nil {
		h.logger.Error("failed to encode get-environment response", zap.Error(err), zap.String("environment_id", c.Param("environment_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) UpdateEnvironment(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	body, err := readValidatedContractJSONBody(c, "BetaPublicEnvironmentUpdateRequest")
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	req, err := updateEnvironmentRequestFromJSON(body)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	environment, err := h.service.UpdateEnvironment(c.Request.Context(), principal, c.Param("environment_id"), req)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := environmentToContract(environment)
	if err != nil {
		h.logger.Error("failed to encode update-environment response", zap.Error(err), zap.String("environment_id", c.Param("environment_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) DeleteEnvironment(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	response, err := h.service.DeleteEnvironment(c.Request.Context(), principal, c.Param("environment_id"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	contractResponse, err := deletedEnvironmentToContract(response)
	if err != nil {
		h.logger.Error("failed to encode delete-environment response", zap.Error(err), zap.String("environment_id", c.Param("environment_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, contractResponse)
}

func (h *Handler) ArchiveEnvironment(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	environment, err := h.service.ArchiveEnvironment(c.Request.Context(), principal, c.Param("environment_id"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := environmentToContract(environment)
	if err != nil {
		h.logger.Error("failed to encode archive-environment response", zap.Error(err), zap.String("environment_id", c.Param("environment_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) CreateVault(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req contract.BetaManagedAgentsCreateVaultRequest
	if err := decodeContractJSONBody(c, "BetaManagedAgentsCreateVaultRequest", &req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	params, err := createVaultRequestFromContract(req)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	vault, err := h.service.CreateVault(c.Request.Context(), principal, params)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := vaultToContract(vault)
	if err != nil {
		h.logger.Error("failed to encode create-vault response", zap.Error(err))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) ListVaults(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req contract.BetaListVaultsParams
	if err := c.ShouldBindQuery(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	vaults, nextPage, err := h.service.ListVaults(c.Request.Context(), principal, int32QueryValue(req.Limit), stringQueryValue(req.Page), boolQueryValue(req.IncludeArchived))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := listVaultsToContract(vaults, nextPage)
	if err != nil {
		h.logger.Error("failed to encode list-vaults response", zap.Error(err))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) GetVault(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	vault, err := h.service.GetVault(c.Request.Context(), principal, c.Param("vault_id"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := vaultToContract(vault)
	if err != nil {
		h.logger.Error("failed to encode get-vault response", zap.Error(err), zap.String("vault_id", c.Param("vault_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) UpdateVault(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	body, err := readValidatedContractJSONBody(c, "BetaManagedAgentsUpdateVaultRequestBody")
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	req, err := updateVaultRequestFromJSON(body)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	vault, err := h.service.UpdateVault(c.Request.Context(), principal, c.Param("vault_id"), req)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := vaultToContract(vault)
	if err != nil {
		h.logger.Error("failed to encode update-vault response", zap.Error(err), zap.String("vault_id", c.Param("vault_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) DeleteVault(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	response, err := h.service.DeleteVault(c.Request.Context(), principal, c.Param("vault_id"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	contractResponse, err := deletedVaultToContract(response)
	if err != nil {
		h.logger.Error("failed to encode delete-vault response", zap.Error(err), zap.String("vault_id", c.Param("vault_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, contractResponse)
}

func (h *Handler) ArchiveVault(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	vault, err := h.service.ArchiveVault(c.Request.Context(), principal, c.Param("vault_id"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := vaultToContract(vault)
	if err != nil {
		h.logger.Error("failed to encode archive-vault response", zap.Error(err), zap.String("vault_id", c.Param("vault_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) CreateCredential(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req contract.BetaManagedAgentsCreateCredentialRequestBody
	if err := decodeContractJSONBody(c, "BetaManagedAgentsCreateCredentialRequestBody", &req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	params, err := createCredentialRequestFromContract(req)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	credential, err := h.service.CreateCredential(c.Request.Context(), principal, c.Param("vault_id"), params)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := credentialToContract(credential)
	if err != nil {
		h.logger.Error("failed to encode create-credential response", zap.Error(err), zap.String("vault_id", c.Param("vault_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) ListCredentials(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req contract.BetaListCredentialsParams
	if err := c.ShouldBindQuery(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	credentials, nextPage, err := h.service.ListCredentials(c.Request.Context(), principal, c.Param("vault_id"), int32QueryValue(req.Limit), stringQueryValue(req.Page), boolQueryValue(req.IncludeArchived))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := listCredentialsToContract(credentials, nextPage)
	if err != nil {
		h.logger.Error("failed to encode list-credentials response", zap.Error(err), zap.String("vault_id", c.Param("vault_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) GetCredential(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	credential, err := h.service.GetCredential(c.Request.Context(), principal, c.Param("vault_id"), c.Param("credential_id"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := credentialToContract(credential)
	if err != nil {
		h.logger.Error("failed to encode get-credential response", zap.Error(err), zap.String("credential_id", c.Param("credential_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) UpdateCredential(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	body, err := readValidatedContractJSONBody(c, "BetaManagedAgentsUpdateCredentialRequestBody")
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	req, err := updateCredentialRequestFromJSON(body)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	credential, err := h.service.UpdateCredential(c.Request.Context(), principal, c.Param("vault_id"), c.Param("credential_id"), req)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := credentialToContract(credential)
	if err != nil {
		h.logger.Error("failed to encode update-credential response", zap.Error(err), zap.String("credential_id", c.Param("credential_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) DeleteCredential(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	response, err := h.service.DeleteCredential(c.Request.Context(), principal, c.Param("vault_id"), c.Param("credential_id"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	contractResponse, err := deletedCredentialToContract(response)
	if err != nil {
		h.logger.Error("failed to encode delete-credential response", zap.Error(err), zap.String("credential_id", c.Param("credential_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, contractResponse)
}

func (h *Handler) ArchiveCredential(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	credential, err := h.service.ArchiveCredential(c.Request.Context(), principal, c.Param("vault_id"), c.Param("credential_id"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := credentialToContract(credential)
	if err != nil {
		h.logger.Error("failed to encode archive-credential response", zap.Error(err), zap.String("credential_id", c.Param("credential_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) UploadFile(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "file is required")
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	metadata, err := h.service.UploadFile(c.Request.Context(), principal, fileHeader.Filename, fileHeader.Header.Get("Content-Type"), content)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := fileMetadataToContract(metadata)
	if err != nil {
		h.logger.Error("failed to encode upload-file response", zap.Error(err))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) ListFiles(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req contract.BetaListFilesV1FilesGetParams
	if err := c.ShouldBindQuery(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	response, err := h.service.ListFiles(c.Request.Context(), principal, FileListOptions{
		Limit:    intQueryValue(req.Limit),
		ScopeID:  stringQueryValue(req.ScopeId),
		BeforeID: stringQueryValue(req.BeforeId),
		AfterID:  stringQueryValue(req.AfterId),
	})
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	contractResponse, err := fileListToContract(response)
	if err != nil {
		h.logger.Error("failed to encode list-files response", zap.Error(err))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, contractResponse)
}

func (h *Handler) GetFileMetadata(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	metadata, err := h.service.GetFileMetadata(c.Request.Context(), principal, c.Param("file_id"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := fileMetadataToContract(metadata)
	if err != nil {
		h.logger.Error("failed to encode get-file-metadata response", zap.Error(err), zap.String("file_id", c.Param("file_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) DownloadFile(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	file, err := h.service.GetFileContent(c.Request.Context(), principal, c.Param("file_id"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.Header("Content-Type", file.MimeType)
	c.Header("Content-Length", strconv.FormatInt(file.SizeBytes, 10))
	c.Header("Content-Disposition", "attachment; filename=\""+file.Filename+"\"")
	c.Data(http.StatusOK, file.MimeType, file.Content)
}

func (h *Handler) DeleteFile(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	response, err := h.service.DeleteFile(c.Request.Context(), principal, c.Param("file_id"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	contractResponse, err := deletedFileToContract(response)
	if err != nil {
		h.logger.Error("failed to encode delete-file response", zap.Error(err), zap.String("file_id", c.Param("file_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, contractResponse)
}

func (h *Handler) ArchiveSession(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	session, err := h.service.ArchiveSession(c.Request.Context(), principal, c.Param("session_id"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := sessionToContract(session)
	if err != nil {
		h.logger.Error("failed to encode archive-session response", zap.Error(err), zap.String("session_id", c.Param("session_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) ListSessionResources(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req contract.BetaListResourcesParams
	if err := c.ShouldBindQuery(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	resources, nextPage, err := h.service.ListSessionResources(c.Request.Context(), principal, c.Param("session_id"), int32QueryValue(req.Limit), stringQueryValue(req.Page))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := listSessionResourcesToContract(resources, nextPage)
	if err != nil {
		h.logger.Error("failed to encode list-session-resources response", zap.Error(err), zap.String("session_id", c.Param("session_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) AddSessionResource(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req contract.BetaManagedAgentsAddSessionResourceParams
	if err := decodeContractJSONBody(c, "BetaManagedAgentsAddSessionResourceParams", &req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	params, err := addSessionResourceRequestFromContract(req)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	resource, err := h.service.AddSessionResource(c.Request.Context(), principal, c.Param("session_id"), params)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := addedSessionResourceToContract(resource)
	if err != nil {
		h.logger.Error("failed to encode add-session-resource response", zap.Error(err), zap.String("session_id", c.Param("session_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) GetSessionResource(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	resource, err := h.service.GetSessionResource(c.Request.Context(), principal, c.Param("session_id"), c.Param("resource_id"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := sessionResourceToContract(resource)
	if err != nil {
		h.logger.Error("failed to encode get-session-resource response", zap.Error(err), zap.String("resource_id", c.Param("resource_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) UpdateSessionResource(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req contract.BetaManagedAgentsUpdateSessionResourceParams
	if err := decodeContractJSONBody(c, "BetaManagedAgentsUpdateSessionResourceParams", &req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	params, err := updateSessionResourceRequestFromContract(req)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	resource, err := h.service.UpdateSessionResource(c.Request.Context(), principal, c.Param("session_id"), c.Param("resource_id"), params)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	response, err := updatedSessionResourceToContract(resource)
	if err != nil {
		h.logger.Error("failed to encode update-session-resource response", zap.Error(err), zap.String("resource_id", c.Param("resource_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) DeleteSessionResource(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	response, err := h.service.DeleteSessionResource(c.Request.Context(), principal, c.Param("session_id"), c.Param("resource_id"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	contractResponse, err := deletedSessionResourceToContract(response)
	if err != nil {
		h.logger.Error("failed to encode delete-session-resource response", zap.Error(err), zap.String("resource_id", c.Param("resource_id")))
		writeError(c, http.StatusInternalServerError, "api_error", "failed to encode response")
		return
	}
	c.JSON(http.StatusOK, contractResponse)
}
