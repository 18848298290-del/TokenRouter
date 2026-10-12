package httpapi

import (
	"github.com/gin-gonic/gin"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// BulkUpdateProvidersRequest represents the payload for bulk editing providers.
type BulkUpdateProvidersRequest struct {
	ProviderIDs    []int64                    `json:"provider_ids"`
	Filters        *BulkUpdateProviderFilters `json:"filters"`
	Name           string                     `json:"name"`
	ProxyID        *int64                     `json:"proxy_id"`
	Concurrency    *int                       `json:"concurrency"`
	Priority       *int                       `json:"priority"`
	RateMultiplier *float64                   `json:"rate_multiplier"`
	LoadFactor     *int                       `json:"load_factor"`
	Status         string                     `json:"status" binding:"omitempty,oneof=active inactive error"`
	Schedulable    *bool                      `json:"schedulable"`
	GroupIDs       *[]int64                   `json:"group_ids"`
	Credentials    map[string]any             `json:"credentials"`
	Extra          map[string]any             `json:"extra"`
}
type BulkUpdateProviderFilters struct {
	Platform    string `json:"platform"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Group       string `json:"group"`
	Search      string `json:"search"`
	PrivacyMode string `json:"privacy_mode"`
}

// BulkUpdate handles bulk updating providers with selected fields/credentials.
// POST /api/v1/admin/providers/bulk-update
func (h *ManagementHandler) BulkUpdate(c *gin.Context) {
	var req BulkUpdateProvidersRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if req.RateMultiplier != nil && *req.RateMultiplier < 0 {
		response.BadRequest(c, "rate_multiplier must be >= 0")
		return
	}
	if len(req.ProviderIDs) == 0 && req.Filters == nil {
		response.BadRequest(c, "provider_ids or filters is required")
		return
	}
	// Clamp base_rpm to the supported range of 0 through 10000.
	SanitizeExtraBaseRPM(req.Extra)
	if err := providercore.ValidateUpstreamRequestIDHeaderExtra(req.Extra); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	hasUpdates := req.Name != "" ||
		req.ProxyID != nil ||
		req.Concurrency != nil ||
		req.Priority != nil ||
		req.RateMultiplier != nil ||
		req.LoadFactor != nil ||
		req.Status != "" ||
		req.Schedulable != nil ||
		req.GroupIDs != nil ||
		len(req.Credentials) > 0 ||
		len(req.Extra) > 0

	if !hasUpdates {
		response.BadRequest(c, "No updates provided")
		return
	}
	providercore.DiscardDeprecatedProviderExtra(req.Extra)

	result, err := h.adminService.BulkUpdateProviders(c.Request.Context(), &providercore.BulkUpdateProvidersInput{
		ProviderIDs:    req.ProviderIDs,
		Filters:        ToBulkUpdateProviderFilters(req.Filters),
		Name:           req.Name,
		ProxyID:        req.ProxyID,
		Concurrency:    req.Concurrency,
		Priority:       req.Priority,
		RateMultiplier: req.RateMultiplier,
		LoadFactor:     req.LoadFactor,
		Status:         req.Status,
		Schedulable:    req.Schedulable,
		GroupIDs:       req.GroupIDs,
		Credentials:    req.Credentials,
		Extra:          req.Extra,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, result)
}

func ToBulkUpdateProviderFilters(filters *BulkUpdateProviderFilters) *providercore.BulkUpdateProviderFilters {
	if filters == nil {
		return nil
	}
	return &providercore.BulkUpdateProviderFilters{
		Platform:    filters.Platform,
		Type:        filters.Type,
		Status:      filters.Status,
		Group:       filters.Group,
		Search:      filters.Search,
		PrivacyMode: filters.PrivacyMode,
	}
}
