package httpapi

import (
	"strconv"

	"github.com/gin-gonic/gin"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// management_privacy.go requests provider privacy updates and presents the
// stored or locally observed mode.
// Privacy requests produce an upstream mode observation for eligible OpenAI or
// Antigravity OAuth providers. The mode may be returned after a persistence
// error, and response rereading distinguishes the current stored view from a
// locally applied observation when that reread fails.
//
// A nonempty upstream mode allows response presentation even when the privacy
// callback could not persist it. A successful reread supplies the response
// record; when rereading fails, the handler adds the observed mode to the
// earlier record. That local response field does not confirm a durable write.

// SetPrivacy requests a fresh eligible OAuth privacy mode and rereads the
// provider for response presentation. A nonempty mode is an observation that
// may outlive a write error; failed rereads apply it locally.
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
	// Read current stored state after the upstream observation. A returned mode
	// can survive a write error, so the reread supplies authoritative response fields.
	updated, err := h.adminService.GetProvider(c.Request.Context(), providerID)
	if err != nil {
		// When rereading fails, retain a useful upstream observation in the borrowed
		// response record; this local assignment does not establish durable privacy storage.
		if provider.Extra == nil {
			provider.Extra = make(map[string]any)
		}
		provider.Extra["privacy_mode"] = mode
		response.Success(c, h.presenter.Present(c.Request.Context(), provider))
		return
	}
	response.Success(c, h.presenter.Present(c.Request.Context(), updated))
}
