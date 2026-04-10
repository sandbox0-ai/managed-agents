package managedagents

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func (h *Handler) CreateAgent(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req CreateAgentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	agent, err := h.service.CreateAgent(c.Request.Context(), principal, req)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, agent)
}

func (h *Handler) ListAgents(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	createdAtGTE, err := parseTimestampQuery(c.Query("created_at[gte]"))
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	createdAtLTE, err := parseTimestampQuery(c.Query("created_at[lte]"))
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	agents, nextPage, err := h.service.ListAgents(c.Request.Context(), principal, AgentListOptions{
		Limit:           limit,
		Page:            c.Query("page"),
		Order:           c.Query("order"),
		IncludeArchived: strings.EqualFold(strings.TrimSpace(c.Query("include_archived")), "true"),
		CreatedAt: TimeFilter{
			GTE: createdAtGTE,
			LTE: createdAtLTE,
		},
	})
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": agents, "next_page": nextPage})
}

func (h *Handler) GetAgent(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	version, _ := strconv.Atoi(strings.TrimSpace(c.Query("version")))
	agent, err := h.service.GetAgent(c.Request.Context(), principal, c.Param("agent_id"), version)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, agent)
}

func (h *Handler) UpdateAgent(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req UpdateAgentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	agent, err := h.service.UpdateAgent(c.Request.Context(), principal, c.Param("agent_id"), req)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, agent)
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
	c.JSON(http.StatusOK, agent)
}

func (h *Handler) ListAgentVersions(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	versions, nextPage, err := h.service.ListAgentVersions(c.Request.Context(), principal, c.Param("agent_id"), limit, c.Query("page"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": versions, "next_page": nextPage})
}

func (h *Handler) CreateEnvironment(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req CreateEnvironmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	environment, err := h.service.CreateEnvironment(c.Request.Context(), principal, req)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, environment)
}

func (h *Handler) ListEnvironments(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	includeArchived := strings.EqualFold(strings.TrimSpace(c.Query("include_archived")), "true")
	environments, nextPage, err := h.service.ListEnvironments(c.Request.Context(), principal, limit, c.Query("page"), includeArchived)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": environments, "next_page": nextPage})
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
	c.JSON(http.StatusOK, environment)
}

func (h *Handler) UpdateEnvironment(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req UpdateEnvironmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	environment, err := h.service.UpdateEnvironment(c.Request.Context(), principal, c.Param("environment_id"), req)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, environment)
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
	c.JSON(http.StatusOK, response)
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
	c.JSON(http.StatusOK, environment)
}

func (h *Handler) CreateVault(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req CreateVaultRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	vault, err := h.service.CreateVault(c.Request.Context(), principal, req)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, vault)
}

func (h *Handler) ListVaults(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	includeArchived := strings.EqualFold(strings.TrimSpace(c.Query("include_archived")), "true")
	vaults, nextPage, err := h.service.ListVaults(c.Request.Context(), principal, limit, c.Query("page"), includeArchived)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": vaults, "next_page": nextPage})
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
	c.JSON(http.StatusOK, vault)
}

func (h *Handler) UpdateVault(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req UpdateVaultRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	vault, err := h.service.UpdateVault(c.Request.Context(), principal, c.Param("vault_id"), req)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, vault)
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
	c.JSON(http.StatusOK, response)
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
	c.JSON(http.StatusOK, vault)
}

func (h *Handler) CreateCredential(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req CreateCredentialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	credential, err := h.service.CreateCredential(c.Request.Context(), principal, c.Param("vault_id"), req)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, credential)
}

func (h *Handler) ListCredentials(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	includeArchived := strings.EqualFold(strings.TrimSpace(c.Query("include_archived")), "true")
	credentials, nextPage, err := h.service.ListCredentials(c.Request.Context(), principal, c.Param("vault_id"), limit, c.Query("page"), includeArchived)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": credentials, "next_page": nextPage})
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
	c.JSON(http.StatusOK, credential)
}

func (h *Handler) UpdateCredential(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req UpdateCredentialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	credential, err := h.service.UpdateCredential(c.Request.Context(), principal, c.Param("vault_id"), c.Param("credential_id"), req)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, credential)
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
	c.JSON(http.StatusOK, response)
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
	c.JSON(http.StatusOK, credential)
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
	c.JSON(http.StatusOK, metadata)
}

func (h *Handler) ListFiles(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	response, err := h.service.ListFiles(c.Request.Context(), principal, FileListOptions{
		Limit:    limit,
		ScopeID:  c.Query("scope_id"),
		BeforeID: c.Query("before_id"),
		AfterID:  c.Query("after_id"),
	})
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, response)
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
	c.JSON(http.StatusOK, metadata)
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
	c.JSON(http.StatusOK, response)
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
	c.JSON(http.StatusOK, session)
}

func (h *Handler) ListSessionResources(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	resources, nextPage, err := h.service.ListSessionResources(c.Request.Context(), principal, c.Param("session_id"), limit, c.Query("page"))
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": resources, "next_page": nextPage})
}

func (h *Handler) AddSessionResource(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req AddSessionResourceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	resource, err := h.service.AddSessionResource(c.Request.Context(), principal, c.Param("session_id"), req)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, resource)
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
	c.JSON(http.StatusOK, resource)
}

func (h *Handler) UpdateSessionResource(c *gin.Context) {
	principal, _, ok := h.requirePrincipal(c)
	if !ok {
		return
	}
	var req UpdateSessionResourceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}
	resource, err := h.service.UpdateSessionResource(c.Request.Context(), principal, c.Param("session_id"), c.Param("resource_id"), req)
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, resource)
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
	c.JSON(http.StatusOK, response)
}
