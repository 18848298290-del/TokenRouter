package httpapi

import (
	"strconv"

	"github.com/gin-gonic/gin"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// SetPrivacy handles setting privacy for a single OpenAI/Antigravity OAuth provider
// POST /api/v1/admin/providers/:id/set-privacy
func (h *ManagementHandler) SetPrivacy(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}
	provider, err := h.adminService.GetProvider(c.Request.Context(), providerID)
	if err != nil {
		response.NotFound(c, "Provider not found")
		return
	}
	if provider.Type != providercore.ProviderTypeOAuth {
		response.BadRequest(c, "Only OAuth providers support privacy setting")
		return
	}
	var mode string
	switch provider.Platform {
	case providercore.PlatformOpenAI:
		mode = h.privacy.ForceOpenAIPrivacy(c.Request.Context(), provider)
	case providercore.PlatformAntigravity:
		mode = h.privacy.ForceAntigravityPrivacy(c.Request.Context(), provider)
	default:
		response.BadRequest(c, "Only OpenAI and Antigravity OAuth providers support privacy setting")
		return
	}
	if mode == "" {
		response.BadRequest(c, "Cannot set privacy: missing access_token")
		return
	}
	// Reload the persisted privacy state for the response.
	updated, err := h.adminService.GetProvider(c.Request.Context(), providerID)
	if err != nil {
		// Privacy was set successfully; if reloading fails, update the response's in-memory copy.
		if provider.Extra == nil {
			provider.Extra = make(map[string]any)
		}
		provider.Extra["privacy_mode"] = mode
		response.Success(c, h.presenter.Present(c.Request.Context(), provider))
		return
	}
	response.Success(c, h.presenter.Present(c.Request.Context(), updated))
}
