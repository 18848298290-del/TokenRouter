package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// routes_settings.go registers administrator routes for provider cooldown and
// stream-timeout policies.
// Provider health policy endpoints share the administrator settings group with
// authentication and auditing installed by the caller. Each GET displays an
// effective policy and each PUT delegates validation and storage to its
// handler, keeping duration units aligned with that policy's response shape.

// ProviderSettingsEndpoints supplies paired read and update handlers for
// provider health policies.
type ProviderSettingsEndpoints interface {
	// GetStreamTimeoutSettings returns the effective timeout action, duration and
	// event threshold. The implementation owns default selection and returns its
	// read failures through the administrator HTTP response mapping.
	GetStreamTimeoutSettings(*gin.Context)
	// UpdateStreamTimeoutSettings binds the timeout policy and validates it before
	// storage. The implementation then rereads effective settings; readback
	// failure can return an error after the stored update completes.
	UpdateStreamTimeoutSettings(*gin.Context)
	// GetOverloadCooldownSettings returns effective overload avoidance enablement
	// and its minute duration. The implementation obtains defaults through runtime
	// settings and maps read failures before sending the policy response.
	GetOverloadCooldownSettings(*gin.Context)
	// UpdateOverloadCooldownSettings binds overload enablement and minute duration
	// and requests validation and persistence. The implementation rereads
	// effective settings for its response, with a later read failure leaving
	// storage completed.
	UpdateOverloadCooldownSettings(*gin.Context)
	// GetOpenAI403CooldownSettings returns effective forbidden-response avoidance
	// and escalation thresholds. The implementation selects defaults through
	// runtime settings and maps read failures before sending the administrator
	// policy response.
	GetOpenAI403CooldownSettings(*gin.Context)
	// UpdateOpenAI403CooldownSettings binds cooldown and optional threshold
	// controls before validation and persistence. Omitted threshold pointers
	// choose compatibility defaults, and the implementation rereads effective
	// settings after its completed write.
	UpdateOpenAI403CooldownSettings(*gin.Context)
	// GetRateLimit429CooldownSettings returns effective default 429 avoidance
	// enablement and its second duration. Runtime settings owns default selection,
	// and the implementation maps read failures before policy response encoding.
	GetRateLimit429CooldownSettings(*gin.Context)
	// UpdateRateLimit429CooldownSettings binds default 429 avoidance controls and
	// saves them through validation. The implementation then reads the effective
	// stored policy, so readback errors occur after the update can complete.
	UpdateRateLimit429CooldownSettings(*gin.Context)
	// GetOpenAIImagesOAuthUnavailableCooldownSettings returns the effective OAuth
	// image-unavailability cooldown in minutes. The implementation selects runtime
	// defaults and maps read failures before sending the dedicated duration
	// response shape.
	GetOpenAIImagesOAuthUnavailableCooldownSettings(*gin.Context)
	// UpdateOpenAIImagesOAuthUnavailableCooldownSettings binds a minute duration
	// and saves it through runtime validation. The implementation returns the
	// validated settings object directly, while rejected input and writes map to
	// administrator errors.
	UpdateOpenAIImagesOAuthUnavailableCooldownSettings(*gin.Context)
}

// RegisterProviderSettingsRoutes mounts provider health policy reads and
// updates on the supplied settings group. The caller installs authentication
// and auditing first and keeps the handler implementation available throughout
// mounted requests.
func RegisterProviderSettingsRoutes(adminSettings *gin.RouterGroup, endpoint ProviderSettingsEndpoints) {
	adminapi.Register(adminSettings, ManagementOperations["settings.stream_timeout.list"], endpoint.GetStreamTimeoutSettings)
	adminapi.Register(adminSettings, ManagementOperations["settings.stream_timeout.update"], endpoint.UpdateStreamTimeoutSettings)
	adminapi.Register(adminSettings, ManagementOperations["settings.overload_cooldown.list"], endpoint.GetOverloadCooldownSettings)
	adminapi.Register(adminSettings, ManagementOperations["settings.overload_cooldown.update"], endpoint.UpdateOverloadCooldownSettings)
	adminapi.Register(adminSettings, ManagementOperations["settings.openai_403_cooldown.list"], endpoint.GetOpenAI403CooldownSettings)
	adminapi.Register(adminSettings, ManagementOperations["settings.openai_403_cooldown.update"], endpoint.UpdateOpenAI403CooldownSettings)
	adminapi.Register(adminSettings, ManagementOperations["settings.rate_limit_429_cooldown.list"], endpoint.GetRateLimit429CooldownSettings)
	adminapi.Register(adminSettings, ManagementOperations["settings.rate_limit_429_cooldown.update"], endpoint.UpdateRateLimit429CooldownSettings)
	adminapi.Register(adminSettings, ManagementOperations["settings.openai_images_oauth_unavailable_cooldown.list"], endpoint.GetOpenAIImagesOAuthUnavailableCooldownSettings)
	adminapi.Register(adminSettings, ManagementOperations["settings.openai_images_oauth_unavailable_cooldown.update"], endpoint.UpdateOpenAIImagesOAuthUnavailableCooldownSettings)
}
