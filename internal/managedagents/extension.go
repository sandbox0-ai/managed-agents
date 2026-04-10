package managedagents

import (
	stdhttp "net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/sandbox0-ai/managed-agent/internal/httpauth"
)

// InternalWebhookPath is the internal sandbox webhook ingestion path.
const InternalWebhookPath = "/internal/managed-agents/runtime/webhooks"

// MountRoutes attaches managed-agent routes to an existing gin router.
func MountRoutes(router gin.IRouter, authenticator httpauth.Authenticator, handler *Handler) {
	if router == nil || authenticator == nil || handler == nil {
		return
	}
	public := router.Group("")
	public.Use(NormalizeAPIKeyHeader())
	public.Use(RequireManagedAgentsBeta())
	public.Use(authenticator.Authenticate())
	{
		public.GET("/v1/files", handler.ListFiles)
		public.POST("/v1/files", handler.UploadFile)
		public.GET("/v1/files/:file_id", handler.GetFileMetadata)
		public.DELETE("/v1/files/:file_id", handler.DeleteFile)
		public.GET("/v1/files/:file_id/content", handler.DownloadFile)

		public.GET("/v1/skills", handler.ListSkills)
		public.POST("/v1/skills", handler.CreateSkill)
		public.GET("/v1/skills/:skill_id", handler.GetSkill)
		public.DELETE("/v1/skills/:skill_id", handler.DeleteSkill)
		public.GET("/v1/skills/:skill_id/versions", handler.ListSkillVersions)
		public.POST("/v1/skills/:skill_id/versions", handler.CreateSkillVersion)
		public.GET("/v1/skills/:skill_id/versions/:version", handler.GetSkillVersion)
		public.DELETE("/v1/skills/:skill_id/versions/:version", handler.DeleteSkillVersion)

		public.GET("/v1/environments", handler.ListEnvironments)
		public.POST("/v1/environments", handler.CreateEnvironment)
		public.GET("/v1/environments/:environment_id", handler.GetEnvironment)
		public.POST("/v1/environments/:environment_id", handler.UpdateEnvironment)
		public.DELETE("/v1/environments/:environment_id", handler.DeleteEnvironment)
		public.POST("/v1/environments/:environment_id/archive", handler.ArchiveEnvironment)

		public.GET("/v1/sessions", handler.ListSessions)
		public.POST("/v1/sessions", handler.CreateSession)
		public.GET("/v1/sessions/:session_id", handler.GetSession)
		public.POST("/v1/sessions/:session_id", handler.UpdateSession)
		public.DELETE("/v1/sessions/:session_id", handler.DeleteSession)
		public.GET("/v1/sessions/:session_id/events", handler.ListEvents)
		public.POST("/v1/sessions/:session_id/events", handler.SendEvents)
		public.GET("/v1/sessions/:session_id/events/stream", handler.StreamEvents)
		public.POST("/v1/sessions/:session_id/archive", handler.ArchiveSession)
		public.GET("/v1/sessions/:session_id/resources", handler.ListSessionResources)
		public.POST("/v1/sessions/:session_id/resources", handler.AddSessionResource)
		public.GET("/v1/sessions/:session_id/resources/:resource_id", handler.GetSessionResource)
		public.POST("/v1/sessions/:session_id/resources/:resource_id", handler.UpdateSessionResource)
		public.DELETE("/v1/sessions/:session_id/resources/:resource_id", handler.DeleteSessionResource)

		public.GET("/v1/agents", handler.ListAgents)
		public.POST("/v1/agents", handler.CreateAgent)
		public.GET("/v1/agents/:agent_id", handler.GetAgent)
		public.POST("/v1/agents/:agent_id", handler.UpdateAgent)
		public.POST("/v1/agents/:agent_id/archive", handler.ArchiveAgent)
		public.GET("/v1/agents/:agent_id/versions", handler.ListAgentVersions)

		public.GET("/v1/vaults", handler.ListVaults)
		public.POST("/v1/vaults", handler.CreateVault)
		public.GET("/v1/vaults/:vault_id", handler.GetVault)
		public.POST("/v1/vaults/:vault_id", handler.UpdateVault)
		public.DELETE("/v1/vaults/:vault_id", handler.DeleteVault)
		public.POST("/v1/vaults/:vault_id/archive", handler.ArchiveVault)
		public.GET("/v1/vaults/:vault_id/credentials", handler.ListCredentials)
		public.POST("/v1/vaults/:vault_id/credentials", handler.CreateCredential)
		public.GET("/v1/vaults/:vault_id/credentials/:credential_id", handler.GetCredential)
		public.POST("/v1/vaults/:vault_id/credentials/:credential_id", handler.UpdateCredential)
		public.DELETE("/v1/vaults/:vault_id/credentials/:credential_id", handler.DeleteCredential)
		public.POST("/v1/vaults/:vault_id/credentials/:credential_id/archive", handler.ArchiveCredential)
	}
	router.POST(InternalWebhookPath, handler.RuntimeSandboxWebhook)
}

// NewHTTPHandler mounts the managed-agent contract at the service root.
func NewHTTPHandler(authenticator httpauth.Authenticator, handler *Handler) stdhttp.Handler {
	router := gin.New()
	MountRoutes(router, authenticator, handler)
	return router
}

// MatchesPath reports whether the request path should be served by the managed-agent extension.
func MatchesPath(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == InternalWebhookPath || strings.HasPrefix(trimmed, InternalWebhookPath+"/") {
		return true
	}
	return strings.HasPrefix(trimmed, "/v1/") || trimmed == "/v1"
}

// InternalSandboxWebhookURL builds the sandbox webhook ingestion path.
func InternalSandboxWebhookURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + InternalWebhookPath
}
