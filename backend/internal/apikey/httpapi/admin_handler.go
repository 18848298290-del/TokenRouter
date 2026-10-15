package httpapi

import (
	"context"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/apikey/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// admin_handler.go accepts administrator requests to change Key bindings and
// reset spending windows. The handler accepts the group pointer's omitted or
// zero value, maps the resulting grant details beside the returned Key, and
// relies on route middleware for administrator authorization.

// KeyAdministration submits administrative binding changes and optional
// spending-window resets together.
type KeyAdministration interface {
	// UpdateManagedFields applies selected binding and reset changes, returning
	// the Key and any exclusive access grant. Nil group leaves its binding, zero
	// unbinds, and a true reset requests all spending counters and starts to be
	// cleared.
	UpdateManagedFields(context.Context, int64, *int64, bool) (*apikey.AdminUpdateAPIKeyGroupIDResult, error)
}

// AdminAPIKeyHandler maps one administrative Key operation into its configured
// group response.
type AdminAPIKeyHandler[G any] struct {
	adminService KeyAdministration       // configured administrative Key use case
	group        func(*routing.Group) *G // configured group presentation mapper
}

// AdminUpdateAPIKeyGroupRequest selects group binding replacement and optional
// usage-window reset.
type AdminUpdateAPIKeyGroupRequest struct {
	// GroupID is omitted to retain the binding, zero to clear it, or
	// positive to select a target. Negative input is rejected by the administration use case.
	GroupID *int64 `json:"group_id"` // omitted, zero or selected group binding
	// ResetRateLimitUsage requests clearing all three spending counters and
	// starts when true. Nil and false leave reset unselected for this request.
	ResetRateLimitUsage *bool `json:"reset_rate_limit_usage"` // requested spending-window usage reset
}

// NewAdminAPIKeyHandler retains the administrative use case and group mapper
// for console responses. Callers supply usable dependencies and install
// administrator middleware on the route group; the mapper accepts absent group
// input.
func NewAdminAPIKeyHandler[G any](a KeyAdministration, g func(*routing.Group) *G) *AdminAPIKeyHandler[G] {
	return &AdminAPIKeyHandler[G]{a, g}
}

// UpdateGroup decodes the administrator's optional binding and reset choices
// and calls one Key administration operation. Parseable signed IDs reach the
// service; malformed IDs or JSON return bad requests. Service failures use the
// shared application error response, while success returns the mapped Key and
// any reported exclusive-group grant details.
func (h *AdminAPIKeyHandler[G]) UpdateGroup(c *gin.Context) {
	keyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid API key ID")
		return
	}

	var req AdminUpdateAPIKeyGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	reset := req.ResetRateLimitUsage != nil && *req.ResetRateLimitUsage
	result, err := h.adminService.UpdateManagedFields(c.Request.Context(), keyID, req.GroupID, reset)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	resp := struct {
		APIKey                 *dto.APIKey[G] `json:"api_key"`                      // returned credential configuration response
		AutoGrantedGroupAccess bool           `json:"auto_granted_group_access"`    // reported exclusive grant occurrence
		GrantedGroupID         *int64         `json:"granted_group_id,omitempty"`   // reported granted group identifier
		GrantedGroupName       string         `json:"granted_group_name,omitempty"` // reported granted group display name
	}{
		APIKey:                 dto.APIKeyFromKey(result.APIKey, h.group),
		AutoGrantedGroupAccess: result.AutoGrantedGroupAccess,
		GrantedGroupID:         result.GrantedGroupID,
		GrantedGroupName:       result.GrantedGroupName,
	}
	response.Success(c, resp)
}
