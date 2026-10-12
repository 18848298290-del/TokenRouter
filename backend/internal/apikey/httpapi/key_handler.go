package httpapi

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/apikey/httpapi/dto"
	idempotencyhttp "github.com/TokenFlux/TokenRouter/internal/idempotency/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// APIKeyHandler handles key use cases and delegates group presentation and capacity reads.
type APIKeyHandler[G any] struct {
	idempotencyhttp.Executor

	apiKeyService        *apikey.APIKeyService
	groupCapacityService GroupCapacityReader
	groupPresentation    func(context.Context, *routing.Group, *accessview.GroupCapacitySummary, bool) *G
	presentGroup         func(*routing.Group, *accessview.GroupCapacitySummary) *G
}
type GroupCapacityReader interface {
	GetGroupCapacityByIDs(context.Context, []int64) (map[int64]accessview.GroupCapacitySummary, error)
}

// CreateAPIKeyRequest represents the create API key request payload.
type CreateAPIKeyRequest struct {
	Name        string `json:"name" binding:"required"`
	Scope       string `json:"scope" binding:"omitempty,oneof=personal team"`
	GroupID     *int64 `json:"group_id"` // nullable
	IsComposite bool   `json:"is_composite"`
	// CompositeGroups contains the full set of group-prefix mappings.
	CompositeGroups         []apikey.APIKeyCompositeGroupInput `json:"composite_groups"`
	CustomKey               *string                            `json:"custom_key"`       // Optional custom credential
	IPWhitelist             []string                           `json:"ip_whitelist"`     // Allowed IP addresses and CIDRs
	IPBlacklist             []string                           `json:"ip_blacklist"`     // Denied IP addresses and CIDRs
	FastModePolicy          string                             `json:"fast_mode_policy"` // Empty follows the client request
	BillingMode             string                             `json:"billing_mode"`     // Empty selects funding automatically
	PreferredSubscriptionID *int64                             `json:"preferred_subscription_id"`
	ModelMapping            map[string]string                  `json:"model_mapping"`   // Complete model redirect rules for this key
	Quota                   *float64                           `json:"quota"`           // Quota limit in USD
	ExpiresInDays           *int                               `json:"expires_in_days"` // Validity in days

	// Key request limits; zero means unlimited.
	ConcurrencyLimit int `json:"concurrency_limit"`
	RPMLimit         int `json:"rpm_limit"`

	// Rate limit fields (0 = unlimited)
	RateLimit5h *float64 `json:"rate_limit_5h"`
	RateLimit1d *float64 `json:"rate_limit_1d"`
	RateLimit7d *float64 `json:"rate_limit_7d"`
	// Permits the configured fallback when the bound group is unavailable; nil uses the service default.
	FallbackWhenGroupUnavailable *bool `json:"fallback_when_group_unavailable"`
}

// UpdateAPIKeyRequest represents the update API key request payload.
type UpdateAPIKeyRequest struct {
	Name        string `json:"name"`
	GroupID     *int64 `json:"group_id"`
	IsComposite *bool  `json:"is_composite"`
	// CompositeGroups replaces all mappings when non-nil.
	CompositeGroups         *[]apikey.APIKeyCompositeGroupInput `json:"composite_groups"`
	Status                  string                              `json:"status" binding:"omitempty,oneof=active inactive"`
	IPWhitelist             *[]string                           `json:"ip_whitelist"`     // nil: unchanged; empty: clear the allowlist
	IPBlacklist             *[]string                           `json:"ip_blacklist"`     // nil: unchanged; empty: clear the denylist
	FastModePolicy          *string                             `json:"fast_mode_policy"` // nil leaves the policy unchanged
	BillingMode             *string                             `json:"billing_mode"`     // nil leaves the funding mode unchanged
	PreferredSubscriptionID *int64                              `json:"preferred_subscription_id"`
	ModelMapping            *map[string]string                  `json:"model_mapping"` // nil: unchanged; empty object: clear mappings
	Quota                   *float64                            `json:"quota"`         // Quota limit in USD; zero means unlimited
	ExpiresAt               *string                             `json:"expires_at"`    // Expiration timestamp in RFC3339 format
	ResetQuota              *bool                               `json:"reset_quota"`   // Reset consumed quota

	// Nil leaves each request limit unchanged; zero clears it.
	ConcurrencyLimit *int `json:"concurrency_limit"`
	RPMLimit         *int `json:"rpm_limit"`

	// Rate limit fields (nil = no change, 0 = unlimited)
	RateLimit5h         *float64 `json:"rate_limit_5h"`
	RateLimit1d         *float64 `json:"rate_limit_1d"`
	RateLimit7d         *float64 `json:"rate_limit_7d"`
	ResetRateLimitUsage *bool    `json:"reset_rate_limit_usage"` // Reset window usage
	// Nil leaves the fallback setting unchanged.
	FallbackWhenGroupUnavailable *bool `json:"fallback_when_group_unavailable"`
}

type ApiKeyLimitInput struct {
	field string
	value *float64
}

// APIKeyBillingSubscriptionOptionResponse exposes subscription options for funding selection.
type APIKeyBillingSubscriptionOptionResponse struct {
	ID               int64     `json:"id"`
	PlanID           int64     `json:"plan_id"`
	PlanName         string    `json:"plan_name"`
	ExpiresAt        time.Time `json:"expires_at"`
	GroupsRestricted bool      `json:"groups_restricted"`
	ApplicableGroups []int64   `json:"applicable_groups"`
}

func NewAPIKeyHandler[G any](keys *apikey.APIKeyService, present func(*routing.Group, *accessview.GroupCapacitySummary) *G) *APIKeyHandler[G] {
	return &APIKeyHandler[G]{apiKeyService: keys, presentGroup: present}
}

// SetGroupPresentation configures group rendering for authorized console queries.
func (h *APIKeyHandler[G]) SetGroupPresentation(present func(context.Context, *routing.Group, *accessview.GroupCapacitySummary, bool) *G) {
	h.groupPresentation = present
}

func (h *APIKeyHandler[G]) SetGroupCapacityService(c GroupCapacityReader) { h.groupCapacityService = c }

func (h *APIKeyHandler[G]) keyResponse(k *apikey.APIKey, language string) *dto.APIKey[G] {
	return dto.APIKeyFromKey(k, func(g *routing.Group) *G {
		if g != nil {
			copy := routing.CloneGroup(g)
			display, _ := routing.GroupDisplay(g, language)
			copy.DisplayName, copy.Description = display.DisplayName, display.Description
			return h.presentGroup(copy, nil)
		}
		return h.presentGroup(g, nil)
	})
}

// ValidateAPIKeyLimitFields applies service validation to supplied HTTP limit fields.
func ValidateAPIKeyLimitFields(limits ...ApiKeyLimitInput) error {
	for _, limit := range limits {
		if limit.value == nil {
			continue
		}
		if err := apikey.ValidateAPIKeyLimit(limit.field, *limit.value); err != nil {
			return err
		}
	}
	return nil
}

// ValidateAPIKeyCreateRequest validates limits and relative expiration.
func ValidateAPIKeyCreateRequest(req CreateAPIKeyRequest) error {
	if err := ValidateAPIKeyLimitFields(
		ApiKeyLimitInput{field: "quota", value: req.Quota},
		ApiKeyLimitInput{field: "rate_limit_5h", value: req.RateLimit5h},
		ApiKeyLimitInput{field: "rate_limit_1d", value: req.RateLimit1d},
		ApiKeyLimitInput{field: "rate_limit_7d", value: req.RateLimit7d},
	); err != nil {
		return err
	}
	if req.ExpiresInDays != nil {
		return apikey.ValidateAPIKeyExpiresInDays(*req.ExpiresInDays)
	}
	return nil
}

// ValidateAPIKeyUpdateRequest validates supplied update limits.
func ValidateAPIKeyUpdateRequest(req UpdateAPIKeyRequest) error {
	return ValidateAPIKeyLimitFields(
		ApiKeyLimitInput{field: "quota", value: req.Quota},
		ApiKeyLimitInput{field: "rate_limit_5h", value: req.RateLimit5h},
		ApiKeyLimitInput{field: "rate_limit_1d", value: req.RateLimit1d},
		ApiKeyLimitInput{field: "rate_limit_7d", value: req.RateLimit7d},
	)
}

// List handles listing user's API keys with pagination
// GET /api/v1/keys
func (h *APIKeyHandler[G]) List(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	page, pageSize := response.ParsePagination(c)
	params := pagination.PaginationParams{
		Page:      page,
		PageSize:  pageSize,
		SortBy:    c.DefaultQuery("sort_by", "created_at"),
		SortOrder: c.DefaultQuery("sort_order", "desc"),
	}

	// Parse filter parameters
	var filters apikey.APIKeyListFilters
	if search := strings.TrimSpace(c.Query("search")); search != "" {
		if len(search) > 100 {
			search = search[:100]
		}
		filters.Search = search
	}
	filters.Status = c.Query("status")
	filters.Scope = strings.ToLower(strings.TrimSpace(c.Query("scope")))
	if groupIDStr := c.Query("group_id"); groupIDStr != "" {
		gid, err := strconv.ParseInt(groupIDStr, 10, 64)
		if err == nil {
			filters.GroupID = &gid
		}
	}

	keys, result, err := h.apiKeyService.List(c.Request.Context(), subject.UserID, params, filters)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]dto.APIKey[G], 0, len(keys))
	for i := range keys {
		out = append(out, *h.keyResponse(&keys[i], locale.FromContext(c.Request.Context())))
	}
	response.Paginated(c, out, result.Total, page, pageSize)
}

// GetByID handles getting a single API key
// GET /api/v1/keys/:id
func (h *APIKeyHandler[G]) GetByID(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	keyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid key ID")
		return
	}

	key, err := h.apiKeyService.GetByID(c.Request.Context(), keyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// Return the same error for missing keys and keys owned by another user.
	if key.UserID != subject.UserID {
		response.ErrorFrom(c, apikey.ErrAPIKeyNotFound)
		return
	}

	response.Success(c, h.keyResponse(key, locale.FromContext(c.Request.Context())))
}

// Create handles creating a new API key
// POST /api/v1/keys
func (h *APIKeyHandler[G]) Create(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	var req CreateAPIKeyRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := ValidateAPIKeyCreateRequest(req); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	svcReq := apikey.CreateAPIKeyRequest{
		Name:                         req.Name,
		Scope:                        req.Scope,
		GroupID:                      req.GroupID,
		IsComposite:                  req.IsComposite,
		CompositeGroups:              req.CompositeGroups,
		CustomKey:                    req.CustomKey,
		IPWhitelist:                  req.IPWhitelist,
		IPBlacklist:                  req.IPBlacklist,
		FastModePolicy:               req.FastModePolicy,
		BillingMode:                  req.BillingMode,
		PreferredSubscriptionID:      req.PreferredSubscriptionID,
		ConcurrencyLimit:             req.ConcurrencyLimit,
		RPMLimit:                     req.RPMLimit,
		ModelMapping:                 req.ModelMapping,
		ExpiresInDays:                req.ExpiresInDays,
		FallbackWhenGroupUnavailable: req.FallbackWhenGroupUnavailable,
	}
	if req.Quota != nil {
		svcReq.Quota = *req.Quota
	}
	if req.RateLimit5h != nil {
		svcReq.RateLimit5h = *req.RateLimit5h
	}
	if req.RateLimit1d != nil {
		svcReq.RateLimit1d = *req.RateLimit1d
	}
	if req.RateLimit7d != nil {
		svcReq.RateLimit7d = *req.RateLimit7d
	}

	h.ExecuteUserIdempotentJSON(c, "user.api_keys.create", req, h.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		key, err := h.apiKeyService.Create(ctx, subject.UserID, svcReq)
		if err != nil {
			return nil, err
		}
		return h.keyResponse(key, locale.FromContext(c.Request.Context())), nil
	})
}

// Update handles updating an API key
// PUT /api/v1/keys/:id
func (h *APIKeyHandler[G]) Update(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	keyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid key ID")
		return
	}

	var req UpdateAPIKeyRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := ValidateAPIKeyUpdateRequest(req); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	svcReq := apikey.UpdateAPIKeyRequest{
		IsComposite:                  req.IsComposite,
		CompositeGroups:              req.CompositeGroups,
		IPWhitelist:                  req.IPWhitelist,
		IPBlacklist:                  req.IPBlacklist,
		FastModePolicy:               req.FastModePolicy,
		BillingMode:                  req.BillingMode,
		PreferredSubscriptionID:      req.PreferredSubscriptionID,
		ConcurrencyLimit:             req.ConcurrencyLimit,
		RPMLimit:                     req.RPMLimit,
		ModelMapping:                 req.ModelMapping,
		Quota:                        req.Quota,
		ResetQuota:                   req.ResetQuota,
		RateLimit5h:                  req.RateLimit5h,
		RateLimit1d:                  req.RateLimit1d,
		RateLimit7d:                  req.RateLimit7d,
		ResetRateLimitUsage:          req.ResetRateLimitUsage,
		FallbackWhenGroupUnavailable: req.FallbackWhenGroupUnavailable,
	}
	if req.Name != "" {
		svcReq.Name = &req.Name
	}
	svcReq.GroupID = req.GroupID
	if req.Status != "" {
		svcReq.Status = &req.Status
	}
	// Parse expires_at if provided
	if req.ExpiresAt != nil {
		if *req.ExpiresAt == "" {
			// Empty string means clear expiration
			svcReq.ExpiresAt = nil
			svcReq.ClearExpiration = true
		} else {
			t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
			if err != nil {
				response.BadRequest(c, "Invalid expires_at format: "+err.Error())
				return
			}
			svcReq.ExpiresAt = &t
		}
	}

	key, err := h.apiKeyService.Update(c.Request.Context(), keyID, subject.UserID, svcReq)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, h.keyResponse(key, locale.FromContext(c.Request.Context())))
}

// RotateCredential replaces the current user's key credential and returns the updated record.
func (h *APIKeyHandler[G]) RotateCredential(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	keyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || keyID <= 0 {
		response.BadRequest(c, "Invalid key ID")
		return
	}
	key, err := h.apiKeyService.RotateCredential(c.Request.Context(), keyID, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, h.keyResponse(key, locale.FromContext(c.Request.Context())))
}

// Delete handles deleting an API key
// DELETE /api/v1/keys/:id
func (h *APIKeyHandler[G]) Delete(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	keyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid key ID")
		return
	}

	err = h.apiKeyService.Delete(c.Request.Context(), keyID, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "API key deleted successfully"})
}

// GetAvailableGroups lists groups the user may bind to a key.
// GET /api/v1/groups/available
func (h *APIKeyHandler[G]) GetAvailableGroups(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	includeModels, err := strconv.ParseBool(c.DefaultQuery("include_models", "true"))
	if err != nil {
		response.BadRequest(c, "Invalid include_models")
		return
	}

	var subscriptionID *int64
	if rawSubscriptionID := strings.TrimSpace(c.Query("subscription_id")); rawSubscriptionID != "" {
		parsedID, err := strconv.ParseInt(rawSubscriptionID, 10, 64)
		if err != nil || parsedID <= 0 {
			response.BadRequest(c, "Invalid subscription ID")
			return
		}
		subscriptionID = &parsedID
	}

	groups, err := h.apiKeyService.GetAvailableGroupsForScopeWithSubscription(
		c.Request.Context(),
		subject.UserID,
		c.DefaultQuery("scope", "personal"),
		subscriptionID,
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]G, 0, len(groups))
	capacityMap := h.getAvailableGroupCapacityMap(c.Request.Context(), groups)
	for i := range groups {
		var capacity *accessview.GroupCapacitySummary
		if value, ok := capacityMap[groups[i].ID]; ok {
			capacity = &value
		}
		groupDTO := h.presentGroup(&groups[i], capacity)
		if h.groupPresentation != nil {
			groupDTO = h.groupPresentation(c.Request.Context(), &groups[i], capacity, includeModels)
		}
		out = append(out, *groupDTO)
	}
	response.Success(c, out)
}

// GetBillingOptions lists subscriptions available for funding in the requested scope.
// GET /api/v1/keys/billing-options?scope=personal|team
func (h *APIKeyHandler[G]) GetBillingOptions(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	options, err := h.apiKeyService.ListBillingSubscriptionsForScope(
		c.Request.Context(),
		subject.UserID,
		c.DefaultQuery("scope", "personal"),
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out := make([]APIKeyBillingSubscriptionOptionResponse, 0, len(options))
	for i := range options {
		option := options[i]
		out = append(out, APIKeyBillingSubscriptionOptionResponse{
			ID:               option.ID,
			PlanID:           option.PlanID,
			PlanName:         option.PlanName,
			ExpiresAt:        option.ExpiresAt,
			GroupsRestricted: option.GroupsRestricted,
			ApplicableGroups: append([]int64(nil), option.ApplicableGroups...),
		})
	}
	response.Success(c, out)
}

func (h *APIKeyHandler[G]) getAvailableGroupCapacityMap(ctx context.Context, groups []routing.Group) map[int64]accessview.GroupCapacitySummary {
	if h.groupCapacityService == nil || len(groups) == 0 {
		return nil
	}
	groupIDs := make([]int64, 0, len(groups))
	for i := range groups {
		groupIDs = append(groupIDs, groups[i].ID)
	}

	// Capacity is optional load information; a failed lookup must not prevent listing groups.
	capacityMap, err := h.groupCapacityService.GetGroupCapacityByIDs(ctx, groupIDs)
	if err != nil {
		return nil
	}
	return capacityMap
}

// GetUserGroupRates returns the current user's group-specific rate multipliers.
// GET /api/v1/groups/rates
func (h *APIKeyHandler[G]) GetUserGroupRates(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	rates, err := h.apiKeyService.GetUserGroupRatesForScope(c.Request.Context(), subject.UserID, c.DefaultQuery("scope", "personal"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, rates)
}
