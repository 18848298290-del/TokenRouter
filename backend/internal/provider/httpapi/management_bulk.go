package httpapi

import (
	"github.com/gin-gonic/gin"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Bulk editing selects provider IDs or a filter-defined population, then sends
// supplied fields to administration. Pointer presence counts as an update even
// for zero values, while name, status and map changes require nonempty values
// before the handler admits the request.

// BulkUpdateProvidersRequest selects provider targets and configuration values
// for strict bulk editing.
type BulkUpdateProvidersRequest struct {
	// ProviderIDs supplies explicit targets and takes precedence over Filters when
	// nonempty. An empty list requires a Filters object for this handler to accept
	// selection.
	// selected provider identifiers
	ProviderIDs []int64 `json:"provider_ids"`
	// Filters supplies list predicates when ProviderIDs is empty. A present object
	// with empty fields still counts as a selection and is interpreted by
	// administration.
	// provider selection filters
	Filters *BulkUpdateProviderFilters `json:"filters"`
	// provider display name
	Name string `json:"name"`
	// ProxyID, Concurrency, Priority, RateMultiplier, LoadFactor and Schedulable
	// use pointer presence for updates. Nil preserves each target's corresponding
	// value, while supplied zero or false reaches administrative validation.
	ProxyID *int64 `json:"proxy_id"` // optional egress proxy update
	// optional provider request capacity
	Concurrency *int `json:"concurrency"`
	Priority    *int `json:"priority"` // optional scheduler priority update
	// optional provider billing multiplier
	RateMultiplier *float64 `json:"rate_multiplier"`
	// optional scheduler capacity override
	LoadFactor *int `json:"load_factor"`
	// stored provider state
	Status string `json:"status" binding:"omitempty,oneof=active inactive error"`
	// optional administrator scheduling switch
	Schedulable *bool `json:"schedulable"`
	// GroupIDs distinguishes omission from a supplied collection. Nil retains each
	// target's associations, while an empty supplied slice requests clearing them
	// through administration.
	GroupIDs *[]int64 `json:"group_ids"` // associated routing groups
	// Credentials and Extra contain selected bulk configuration changes. Nonempty
	// maps count as supplied updates, and administration sanitizes and validates
	// them before applying per-provider write outcomes.
	// selected authorization field changes
	Credentials map[string]any `json:"credentials"`
	// selected provider configuration changes
	Extra map[string]any `json:"extra"`
}

// BulkUpdateProviderFilters describes the administrator list predicates used to
// resolve bulk targets.
type BulkUpdateProviderFilters struct {
	Platform    string `json:"platform"`     // upstream platform identifier
	Type        string `json:"type"`         // provider authentication kind
	Status      string `json:"status"`       // stored provider state
	Group       string `json:"group"`        // routing group filter
	Search      string `json:"search"`       // provider search text
	PrivacyMode string `json:"privacy_mode"` // recorded privacy filter
}

// BulkUpdate validates strict JSON, a target selection and at least one
// supplied update. Explicit IDs take precedence in administration; per-item
// failures remain in the successful batch response after core validation.
// POST /api/v1/admin/providers/bulk-update
func (h *ManagementHandler) BulkUpdate(c *gin.Context) {
	var req BulkUpdateProvidersRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	// Reject negative billing overrides before submitting per-provider edits.
	if req.RateMultiplier != nil && *req.RateMultiplier < 0 {
		response.BadRequest(c, "rate_multiplier must be >= 0")
		return
	}
	if len(req.ProviderIDs) == 0 && req.Filters == nil {
		response.BadRequest(c, "provider_ids or filters is required")
		return
	}
	// Normalize managed request-rate input before the core validates target
	// configuration, keeping its persisted base_rpm within the supported integer range.
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
	// Retired keys can satisfy the preceding nonempty-map check. Discard them
	// before administration so ignored historical values cannot become a configuration update.
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

// ToBulkUpdateProviderFilters copies HTTP filter strings into the core
// selection shape. A nil input remains nil so administration can distinguish
// absent filters from a present filter object with empty predicates.
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
