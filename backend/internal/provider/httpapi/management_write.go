package httpapi

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/idempotency"
	idempotencyhttp "github.com/TokenFlux/TokenRouter/internal/idempotency/httpapi"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// CreateProviderRequest contains the HTTP fields for provider creation.
type CreateProviderRequest struct {
	Name               string         `json:"name" binding:"required"`
	Notes              *string        `json:"notes"`
	Platform           string         `json:"platform" binding:"required"`
	Type               string         `json:"type" binding:"required,oneof=oauth setup-token apikey upstream bedrock service_account cosy"`
	Credentials        map[string]any `json:"credentials" binding:"required"`
	Extra              map[string]any `json:"extra"`
	ProxyID            *int64         `json:"proxy_id"`
	Concurrency        int            `json:"concurrency"`
	Priority           int            `json:"priority"`
	RateMultiplier     *float64       `json:"rate_multiplier"`
	LoadFactor         *int           `json:"load_factor"`
	GroupIDs           []int64        `json:"group_ids"`
	ExpiresAt          *int64         `json:"expires_at"`
	AutoPauseOnExpired *bool          `json:"auto_pause_on_expired"`
}

// UpdateProviderRequest contains the HTTP fields for editing a provider.
// Pointer fields distinguish omission from an explicit zero value.
type UpdateProviderRequest struct {
	Name               string         `json:"name"`
	Notes              *string        `json:"notes"`
	Type               string         `json:"type" binding:"omitempty,oneof=oauth setup-token apikey upstream bedrock service_account cosy"`
	Credentials        map[string]any `json:"credentials"`
	Extra              map[string]any `json:"extra"`
	ProxyID            *int64         `json:"proxy_id"`
	Concurrency        *int           `json:"concurrency"`
	Priority           *int           `json:"priority"`
	RateMultiplier     *float64       `json:"rate_multiplier"`
	LoadFactor         *int           `json:"load_factor"`
	Status             string         `json:"status" binding:"omitempty,oneof=active inactive error"`
	GroupIDs           *[]int64       `json:"group_ids"`
	ExpiresAt          *int64         `json:"expires_at"`
	AutoPauseOnExpired *bool          `json:"auto_pause_on_expired"`
}

// Create creates a provider within the administrator idempotency scope.
// POST /api/v1/admin/providers
func (h *ManagementHandler) Create(c *gin.Context) {
	var req CreateProviderRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	providercore.DiscardDeprecatedProviderExtra(req.Extra)
	if req.RateMultiplier != nil && *req.RateMultiplier < 0 {
		response.BadRequest(c, "rate_multiplier must be >= 0")
		return
	}
	// Clamp base_rpm to the supported range of 0 through 10000.
	SanitizeExtraBaseRPM(req.Extra)
	if err := providercore.ValidateUpstreamRequestIDHeaderExtra(req.Extra); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// Capture the created provider so supported capability probes can run after successful creation.
	// An idempotent replay skips this closure, leaving createdProvider nil and skipping probe scheduling.
	var createdProvider *providercore.Record

	result, err := h.ExecuteAdminIdempotent(c, "admin.providers.create", req, h.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		provider, execErr := h.adminService.CreateProvider(ctx, &providercore.CreateProviderInput{
			Name:               req.Name,
			Notes:              req.Notes,
			Platform:           req.Platform,
			Type:               req.Type,
			Credentials:        req.Credentials,
			Extra:              req.Extra,
			ProxyID:            req.ProxyID,
			Concurrency:        req.Concurrency,
			Priority:           req.Priority,
			RateMultiplier:     req.RateMultiplier,
			LoadFactor:         req.LoadFactor,
			GroupIDs:           req.GroupIDs,
			ExpiresAt:          req.ExpiresAt,
			AutoPauseOnExpired: req.AutoPauseOnExpired,
		})
		if execErr != nil {
			return nil, execErr
		}
		createdProvider = provider
		// Apply privacy settings to new Antigravity OAuth providers.
		h.privacy.ForceAntigravityPrivacy(ctx, provider)
		// Apply privacy settings to new OpenAI OAuth providers.
		h.privacy.ForceOpenAIPrivacy(ctx, provider)
		return h.presenter.Present(ctx, provider), nil
	})
	if err != nil {
		if retryAfter := idempotency.RetryAfterSecondsFromError(err); retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		response.ErrorFrom(c, err)
		return
	}

	if result != nil && result.Replayed {
		c.Header("X-Idempotency-Replayed", "true")
	}
	if h.afterCreate != nil {
		h.afterCreate(createdProvider)
	}
	response.Success(c, result.Data)
}

// Duplicate creates an independent copy of an existing provider's configuration.
// POST /api/v1/admin/providers/:id/duplicate
func (h *ManagementHandler) Duplicate(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}
	actorScope := idempotencyhttp.AdminActorScope(c)

	result, err := h.ExecuteAdminIdempotent(
		c,
		"admin.providers.duplicate",
		struct {
			ProviderID int64 `json:"provider_id"`
		}{ProviderID: providerID},
		h.DefaultWriteIdempotencyTTL(),
		func(ctx context.Context) (any, error) {
			provider, execErr := h.adminService.DuplicateProvider(ctx, providerID, actorScope, c.GetHeader("Idempotency-Key"))
			if execErr != nil {
				return nil, execErr
			}
			return h.presenter.Present(ctx, provider), nil
		},
	)
	if err != nil {
		reason := infraerrors.Reason(err)
		if reason == infraerrors.Reason(idempotency.ErrIdempotencyInProgress) || reason == infraerrors.Reason(idempotency.ErrIdempotencyStoreUnavail) {
			recovered, recoverErr := h.adminService.RecoverDuplicateProvider(c.Request.Context(), providerID, actorScope, c.GetHeader("Idempotency-Key"))
			if recoverErr != nil {
				slog.Warn("provider_duplicate_recovery_failed", "provider_id", providerID, "actor_scope", actorScope, "reason", reason, "error", recoverErr)
			} else if recovered != nil {
				c.Header("X-Idempotency-Recovered", "true")
				response.Success(c, h.presenter.Present(c.Request.Context(), recovered))
				return
			}
		}
		response.ErrorFrom(c, err)
		return
	}

	if result != nil && result.Replayed {
		c.Header("X-Idempotency-Replayed", "true")
	}
	response.Success(c, result.Data)
}

// Update applies supplied fields and retains omitted values.
// PUT /api/v1/admin/providers/:id
func (h *ManagementHandler) Update(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}

	var req UpdateProviderRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if normalizedExtra, shouldReplaceExtra := providercore.NormalizeDeprecatedProviderExtraUpdate(req.Extra); shouldReplaceExtra {
		req.Extra = normalizedExtra
	} else {
		req.Extra = nil
	}
	if req.RateMultiplier != nil && *req.RateMultiplier < 0 {
		response.BadRequest(c, "rate_multiplier must be >= 0")
		return
	}
	// Clamp base_rpm to the supported range of 0 through 10000.
	SanitizeExtraBaseRPM(req.Extra)
	if err := providercore.ValidateUpstreamRequestIDHeaderExtra(req.Extra); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	provider, err := h.adminService.UpdateProvider(c.Request.Context(), providerID, &providercore.UpdateProviderInput{
		Name:               req.Name,
		Notes:              req.Notes,
		Type:               req.Type,
		Credentials:        req.Credentials,
		Extra:              req.Extra,
		ProxyID:            req.ProxyID,
		Concurrency:        req.Concurrency, // nil means omitted.
		Priority:           req.Priority,    // nil means omitted.
		RateMultiplier:     req.RateMultiplier,
		LoadFactor:         req.LoadFactor,
		Status:             req.Status,
		GroupIDs:           req.GroupIDs,
		ExpiresAt:          req.ExpiresAt,
		AutoPauseOnExpired: req.AutoPauseOnExpired,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, h.presenter.Present(c.Request.Context(), provider))
}

func SanitizeExtraBaseRPM(extra map[string]any) { providercore.SanitizeManagedBaseRPM(extra) }
