package httpapi

import (
	"github.com/gin-gonic/gin"
)

// routes_admin.go registers provider management, authorization, usage, and
// connection-test routes for administrators.
// Provider administration mounts management, credential exchange, usage and
// connection-test endpoints under the caller's administrator group. The group
// supplies authentication, rate limiting and auditing before registration, and
// the raw-credential export route adds its own supplied step-up middleware.

// ProviderRouteEndpoints holds constructed handler dependencies used when
// administrator routes are registered.
type ProviderRouteEndpoints struct {
	ProviderArchive     *ArchiveHandler          // raw credential backup transfers
	ProviderCRS         *CRSHandler              // external CRS source synchronization
	ProviderCodexImport *CodexImportHandler      // Codex credential session imports
	ProviderManagement  *ManagementHandler       // provider configuration and maintenance
	ProviderOAuthUsage  *OAuthUsageHandler       // OAuth usage and local statistics
	ProviderOllama      *OllamaUsageHandler      // managed Ollama usage and sessions
	ProviderTests       *TestHandler             // streaming provider connection tests
	CodexInviteReset    *CodexInviteResetHandler // Codex invitations and reset credits
	OAuth               *ClaudeOAuthHandler      // Claude scoped authorization sessions
	OpenAIOAuth         *OpenAIOAuthHandler      // OpenAI authorization and quota actions
	UpstreamUsage       *UpstreamUsageHandler    // manual API-key upstream usage
}

// RegisterAntigravityOAuthRoutes mounts Google session generation, code
// exchange and refresh-token validation under antigravity. The caller supplies
// an administrator group already protected by authentication, rate limiting and
// auditing before registration.
func RegisterAntigravityOAuthRoutes(admin *gin.RouterGroup, endpoint *AntigravityOAuthHandler) {
	antigravity := admin.Group("/antigravity")
	{
		antigravity.POST("/oauth/auth-url", endpoint.GenerateAuthURL)
		antigravity.POST("/oauth/exchange-code", endpoint.ExchangeCode)
		antigravity.POST("/oauth/refresh-token", endpoint.RefreshToken)
	}
}

// RegisterGeminiOAuthRoutes mounts Gemini session generation, exchange and
// capability display under gemini. The caller supplies the protected
// administrator group and a constructed authorization handler before route
// registration.
func RegisterGeminiOAuthRoutes(admin *gin.RouterGroup, endpoint *GeminiOAuthHandler) {
	gemini := admin.Group("/gemini")
	{
		gemini.POST("/oauth/auth-url", endpoint.GenerateAuthURL)
		gemini.POST("/oauth/exchange-code", endpoint.ExchangeCode)
		gemini.GET("/oauth/capabilities", endpoint.GetCapabilities)
	}
}

// RegisterGrokOAuthRoutes mounts Grok authorization, SSO imports,
// reconciliation, quota and runtime diagnostics under grok. The caller supplies
// a constructed handler and an authenticated, rate-limited, audited
// administrator group.
func RegisterGrokOAuthRoutes(admin *gin.RouterGroup, endpoint *GrokOAuthHandler) {
	grok := admin.Group("/grok")
	{
		grok.GET("/oauth/capabilities", endpoint.GetCapabilities)
		grok.POST("/oauth/auth-url", endpoint.GenerateAuthURL)
		grok.POST("/oauth/exchange-code", endpoint.ExchangeCode)
		grok.POST("/oauth/refresh-token", endpoint.RefreshToken)
		grok.POST("/oauth/sso-token", endpoint.ValidateSSOToken)
		grok.POST("/oauth/password", endpoint.AuthorizePassword)
		grok.POST("/oauth/create-from-oauth", endpoint.CreateProviderFromOAuth)
		grok.POST("/sso-to-oauth", endpoint.CreateProvidersFromSSO)
		grok.POST("/oauth/reconcile", endpoint.ReconcileOAuthProviders)
		grok.POST("/providers/:id/refresh", endpoint.RefreshProviderToken)
		grok.GET("/providers/:id/quota", endpoint.QueryQuota)
		grok.POST("/providers/:id/reset-quota", endpoint.ResetQuota)
		grok.GET("/runtime-sanity", endpoint.RuntimeSanity)
	}
}

// RegisterOpenAIOAuthRoutes mounts OpenAI authorization, provider import and
// quota maintenance under openai. Authentication, rate limiting and auditing
// belong to the supplied administrator group, whose lifetime covers the
// constructed handler dependencies.
func RegisterOpenAIOAuthRoutes(admin *gin.RouterGroup, endpoint *OpenAIOAuthHandler) {
	openai := admin.Group("/openai")
	{
		openai.POST("/generate-auth-url", endpoint.GenerateAuthURL)
		openai.POST("/exchange-code", endpoint.ExchangeCode)
		openai.POST("/refresh-token", endpoint.RefreshToken)
		openai.POST("/providers/:id/refresh", endpoint.RefreshProviderToken)
		openai.POST("/create-from-oauth", endpoint.CreateProviderFromOAuth)
		openai.POST("/create-from-codex-pat", endpoint.CreateProviderFromCodexPAT)
		openai.GET("/providers/:id/quota", endpoint.QueryQuota)
		openai.POST("/providers/:id/quota/refresh", endpoint.RefreshQuota)
		openai.POST("/providers/:id/reset-quota", endpoint.ResetQuota)
	}
}

// RegisterQoderOAuthRoutes mounts Qoder device session generation, exchange and
// polling under qoder. The caller supplies a constructed device-authorization
// handler and an administrator group with authentication, rate limiting and
// auditing.
func RegisterQoderOAuthRoutes(admin *gin.RouterGroup, endpoint *QoderOAuthHandler) {
	qoder := admin.Group("/qoder")
	{
		qoder.POST("/oauth/auth-url", endpoint.GenerateAuthURL)
		qoder.POST("/oauth/exchange-code", endpoint.ExchangeCode)
		qoder.POST("/oauth/poll", endpoint.Poll)
	}
}

// RegisterScheduledTestRoutes mounts plan mutations and result history plus the
// provider-nested plan list. The caller supplies a constructed schedule handler
// and an authenticated, rate-limited, audited administrator group.
func RegisterScheduledTestRoutes(admin *gin.RouterGroup, endpoint *ScheduledTestHandler) {
	plans := admin.Group("/scheduled-test-plans")
	{
		plans.POST("", endpoint.Create)
		plans.PUT("/:id", endpoint.Update)
		plans.DELETE("/:id", endpoint.Delete)
		plans.GET("/:id/results", endpoint.ListResults)
	}
	// Nested under providers
	admin.GET("/providers/:id/scheduled-test-plans", endpoint.ListByProvider)
}

// RegisterProviderRoutes mounts provider administration and invokes the
// supplied scheduler diagnostics registration. All endpoint pointers and
// registration callbacks are required by the routes that use them; the caller
// supplies the protected administrator group and raw-export step-up middleware.
func RegisterProviderRoutes(admin *gin.RouterGroup, endpoints ProviderRouteEndpoints, stepUp gin.HandlerFunc, registerDiagnostics func(*gin.RouterGroup)) {
	providers := admin.Group("/providers")
	{
		providers.GET("", endpoints.ProviderManagement.List)
		providers.GET("/ollama-cloud-usage/settings", endpoints.ProviderOllama.GetOllamaCloudUsageSettings)
		providers.PUT("/ollama-cloud-usage/settings", endpoints.ProviderOllama.UpdateOllamaCloudUsageSettings)
		providers.GET("/:id", endpoints.ProviderManagement.GetByID)
		providers.POST("", endpoints.ProviderManagement.Create)
		providers.POST("/:id/duplicate", endpoints.ProviderManagement.Duplicate)
		providers.POST("/import/codex-session", endpoints.ProviderCodexImport.ImportCodexSession)
		providers.POST("/sync/crs", endpoints.ProviderCRS.SyncFromCRS)
		providers.POST("/sync/crs/preview", endpoints.ProviderCRS.PreviewFromCRS)
		providers.PUT("/:id", endpoints.ProviderManagement.Update)
		registerDiagnostics(providers)
		providers.GET("/:id/ollama-cloud-usage", endpoints.ProviderOllama.GetOllamaCloudUsage)
		providers.PUT("/:id/ollama-cloud-usage/session", endpoints.ProviderOllama.SaveOllamaCloudUsageSession)
		providers.DELETE("/:id/ollama-cloud-usage/session", endpoints.ProviderOllama.DeleteOllamaCloudUsageSession)
		providers.PUT("/:id/ollama-cloud-usage/auto-refresh", endpoints.ProviderOllama.SetOllamaCloudUsageAutoRefresh)
		providers.POST("/:id/ollama-cloud-usage/refresh", endpoints.ProviderOllama.RefreshOllamaCloudUsage)
		providers.DELETE("/:id", endpoints.ProviderManagement.Delete)
		providers.POST("/:id/test", endpoints.ProviderTests.Test)
		providers.POST("/:id/recover-state", endpoints.ProviderManagement.RecoverState)
		providers.POST("/:id/refresh", endpoints.ProviderManagement.Refresh)
		providers.POST("/:id/apply-oauth-credentials", endpoints.ProviderManagement.ApplyOAuthCredentials)
		providers.POST("/:id/set-privacy", endpoints.ProviderManagement.SetPrivacy)
		providers.POST("/:id/refresh-tier", endpoints.ProviderManagement.RefreshTier)
		providers.GET("/:id/stats", endpoints.ProviderManagement.GetStats)
		providers.POST("/:id/clear-error", endpoints.ProviderManagement.ClearError)
		providers.POST("/:id/revert-proxy-fallback", endpoints.ProviderManagement.RevertProxyFallback)
		providers.GET("/:id/usage", endpoints.ProviderOAuthUsage.GetUsage)
		providers.GET("/:id/today-stats", endpoints.ProviderOAuthUsage.GetTodayStats)
		providers.POST("/usage/batch", endpoints.ProviderOAuthUsage.GetBatchUsage)
		providers.POST("/today-stats/batch", endpoints.ProviderOAuthUsage.GetBatchTodayStats)
		providers.POST("/:id/clear-rate-limit", endpoints.ProviderManagement.ClearRateLimit)
		providers.POST("/:id/reset-quota", endpoints.ProviderManagement.ResetQuota)
		providers.POST("/:id/upstream-usage/query", endpoints.UpstreamUsage.QueryUpstreamUsage)
		providers.POST("/upstream-usage/query/batch", endpoints.UpstreamUsage.QueryBatchUpstreamUsage)
		providers.GET("/:id/temp-unschedulable", endpoints.ProviderManagement.GetTempUnschedulable)
		providers.DELETE("/:id/temp-unschedulable", endpoints.ProviderManagement.ClearTempUnschedulable)
		providers.POST("/:id/schedulable", endpoints.ProviderManagement.SetSchedulable)
		providers.POST("/models/sync-upstream-preview", endpoints.ProviderManagement.SyncUpstreamModelsPreview)
		providers.GET("/:id/models", endpoints.ProviderManagement.GetAvailableModels)
		providers.POST("/:id/models/sync-upstream", endpoints.ProviderManagement.SyncUpstreamModels)
		providers.POST("/batch", endpoints.ProviderManagement.BatchCreate)
		// Raw provider and proxy secrets are part of the backup format. Add the
		// supplied step-up middleware here before the archive exporter can return those values.
		providers.GET("/data", stepUp, endpoints.ProviderArchive.ExportData)
		providers.POST("/data", endpoints.ProviderArchive.ImportData)
		providers.POST("/batch-update-credentials", endpoints.ProviderManagement.BatchUpdateCredentials)
		providers.POST("/batch-refresh-tier", endpoints.ProviderManagement.BatchRefreshTier)
		providers.POST("/bulk-update", endpoints.ProviderManagement.BulkUpdate)
		providers.POST("/batch-delete", endpoints.ProviderManagement.BatchDelete)
		providers.POST("/batch-clear-error", endpoints.ProviderManagement.BatchClearError)
		providers.POST("/batch-refresh", endpoints.ProviderManagement.BatchRefresh)
		providers.GET("/:id/codex/invite-reset/status", endpoints.CodexInviteReset.GetStatus)
		providers.POST("/:id/codex/invite-reset/invite", endpoints.CodexInviteReset.SendInvite)
		providers.POST("/:id/codex/invite-reset/consume", endpoints.CodexInviteReset.Consume)

		// Antigravity default model mapping.
		providers.GET("/antigravity/default-model-mapping", endpoints.ProviderManagement.GetAntigravityDefaultModelMapping)

		// Spark shadow providers.
		providers.POST("/:id/shadow", endpoints.OpenAIOAuth.CreateShadow)

		// Claude OAuth routes
		providers.POST("/generate-auth-url", endpoints.OAuth.GenerateAuthURL)
		providers.POST("/generate-setup-token-url", endpoints.OAuth.GenerateSetupTokenURL)
		providers.POST("/exchange-code", endpoints.OAuth.ExchangeCode)
		providers.POST("/exchange-setup-token-code", endpoints.OAuth.ExchangeSetupTokenCode)
		providers.POST("/cookie-auth", endpoints.OAuth.CookieAuth)
		providers.POST("/setup-token-cookie-auth", endpoints.OAuth.SetupTokenCookieAuth)
	}
}
