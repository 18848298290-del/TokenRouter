package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// AdminKeySettingsHandler manages administrator API credentials.
type AdminKeySettingsHandler struct{ settingService *identity.RuntimeSettings }

// NewAdminKeySettingsHandler uses the supplied identity settings instance.
func NewAdminKeySettingsHandler(settings *identity.RuntimeSettings) *AdminKeySettingsHandler {
	return &AdminKeySettingsHandler{settingService: settings}
}

// GetAdminAPIKey returns whether a key exists and its masked value.
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

// RegenerateAdminAPIKey generates a key and returns its full value.
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

// DeleteAdminAPIKey removes the configured administrator key.
func (h *AdminKeySettingsHandler) DeleteAdminAPIKey(c *gin.Context) {
	if err := h.settingService.DeleteAdminAPIKey(c.Request.Context()); err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, gin.H{"message": "Admin API key deleted"})
}
