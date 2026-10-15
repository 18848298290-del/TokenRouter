package httpapi

import (
	"strconv"

	"github.com/gin-gonic/gin"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// management_refresh.go handles manual credential refresh and reauthorization
// through managed provider maintenance.
// Manual credential refresh and reauthorization share ManagedRefreshService,
// which owns platform eligibility, persistence and recovery. The HTTP wrapper
// maps a temporary Antigravity project-discovery warning to a message response;
// otherwise it presents the record returned by the maintenance use case.

// ApplyOAuthCredentialsRequest carries required authorization values and
// optional Extra updates from reauthorization.
type ApplyOAuthCredentialsRequest struct {
	Type        string         `json:"type" binding:"required,oneof=oauth setup-token"` // replacement authorization kind
	Credentials map[string]any `json:"credentials" binding:"required"`                  // exchanged authorization values
	// Extra supplies a selected patch after the credential update. ManagedRefreshService
	// merges these values separately, retaining unrelated stored configuration when
	// the patch is absent.
	Extra map[string]any `json:"extra"` // additional reauthorization settings
}

// Refresh loads a provider before managed credential exchange and returns the
// resulting runtime view. Every lookup failure returns 404; the missing-project
// warning instead produces the dedicated message and warning response.
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

// ApplyOAuthCredentials strictly binds reauthorization values and delegates
// saving, Extra merging and recovery to ManagedRefreshService. Deprecated
// settings are removed before dispatch; lookup failure returns 404 and
// completed use-case results receive runtime presentation.
// POST /api/v1/admin/providers/:id/apply-oauth-credentials
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
	// Reauthorization accepts historical Extra payloads. Remove retired keys
	// before the selected patch reaches managed maintenance, keeping retained neighboring settings eligible.
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
