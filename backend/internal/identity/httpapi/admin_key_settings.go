package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Administrator API Key status responses expose a masked credential, while
// regeneration returns the newly saved full value. The runtime settings
// service owns generation and deletion, and these HTTP handlers write the
// resulting panel envelope or the service error.

// AdminKeySettingsHandler handles stored management-credential status,
// generation, and deletion requests.
type AdminKeySettingsHandler struct {
	settingService *identity.RuntimeSettings // management credential settings
}

// NewAdminKeySettingsHandler retains the supplied identity runtime settings
// for credential operations.
func NewAdminKeySettingsHandler(settings *identity.RuntimeSettings) *AdminKeySettingsHandler {
	return &AdminKeySettingsHandler{settingService: settings}
}

// GetAdminAPIKey writes credential existence and its masked display value in
// the panel envelope. Settings-read errors are mapped through the shared HTTP
// helper; callers install administrator authentication on the route.
func (h *AdminKeySettingsHandler) GetAdminAPIKey(c *gin.Context) {
	maskedKey, exists, err := h.settingService.GetAdminAPIKeyStatus(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, gin.H{
		"exists":     exists,
		"masked_key": maskedKey,
	})
}

// RegenerateAdminAPIKey generates and saves a replacement administrator
// credential before returning its full value. The route requires administrator
// authentication; settings errors produce the shared error response.
func (h *AdminKeySettingsHandler) RegenerateAdminAPIKey(c *gin.Context) {
	key, err := h.settingService.GenerateAdminAPIKey(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, gin.H{
		"key": key, // The full value is exposed in the generation response.
	})
}

// DeleteAdminAPIKey removes the stored administrator credential and writes a
// success message. Settings errors use the shared response mapping, and
// callers install administrator authentication before exposing this handler.
func (h *AdminKeySettingsHandler) DeleteAdminAPIKey(c *gin.Context) {
	if err := h.settingService.DeleteAdminAPIKey(c.Request.Context()); err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, gin.H{"message": "Admin API key deleted"})
}
