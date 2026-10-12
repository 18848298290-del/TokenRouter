package httpapi

import (
	"strconv"

	"github.com/gin-gonic/gin"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// ApplyOAuthCredentialsRequest carries OAuth credentials obtained by reauthorization.
type ApplyOAuthCredentialsRequest struct {
	Type        string         `json:"type" binding:"required,oneof=oauth setup-token"`
	Credentials map[string]any `json:"credentials" binding:"required"`
	Extra       map[string]any `json:"extra"`
}

// Refresh returns the refresh result and any missing-project warning.
// POST /api/v1/admin/providers/:id/refresh
func (h *ManagementHandler) Refresh(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}

	// Provider lookup failures return 404.
	provider, err := h.adminService.GetProvider(c.Request.Context(), providerID)
	if err != nil {
		response.NotFound(c, "Provider not found")
		return
	}

	updatedProvider, warning, err := h.managed.Refresh(c.Request.Context(), provider)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	if warning == "missing_project_id_temporary" {
		response.Success(c, gin.H{
			"message": "Token refreshed successfully, but project_id could not be retrieved (will retry automatically)",
			"warning": "missing_project_id_temporary",
		})
		return
	}

	response.Success(c, h.presenter.Present(c.Request.Context(), updatedProvider))
}

// ApplyOAuthCredentials saves reauthorized credentials and merges Extra by key.
// POST /api/v1/admin/providers/:id/apply-oauth-credentials
//
// This endpoint accepts type, credentials, and extra:
// - Credentials replace the authorization values.
// - Extra merges by JSONB key, retaining unrelated base_rpm, quota_*, and privacy_mode settings.
// - Error state is cleared and the token cache is invalidated so later requests use the new credentials.
func (h *ManagementHandler) ApplyOAuthCredentials(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}

	var req ApplyOAuthCredentialsRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	providercore.DiscardDeprecatedProviderExtra(req.Extra)

	ctx := c.Request.Context()
	existing, err := h.adminService.GetProvider(ctx, providerID)
	if err != nil {
		response.NotFound(c, "Provider not found")
		return
	}
	core := h.managed
	updated, err := core.Reauthorize(ctx, existing, providercore.ManagedReauthorizationInput{Type: req.Type, Credentials: req.Credentials, Extra: req.Extra})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, h.presenter.Present(ctx, updated))
}
