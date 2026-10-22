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

// management_write.go handles provider creation, duplication, and
// configuration updates through administrator HTTP requests.
// Provider creation can finish before response idempotency storage succeeds.
// Creation and duplication use the shared executor, while duplication also
// passes its administrator operation identity to durable storage so the handler
// can recover a completed copy after an uncertain executor outcome. Replay
// responses bypass the creation closure and therefore have no newly captured
// provider.
//
// Creation normalizes deprecated settings before computing its fingerprint and
// applies platform privacy observations after the administrative create.
// Follow-up callbacks receive the captured result after the executor returns.
// Ordinary editing instead forwards selected fields to administration, where
// pointer omission, sensitive credentials and explicit clearing values have
// different meanings; its response is built from the updated record.

// CreateProviderRequest carries provider configuration and authorization values
// for strict creation binding.
type CreateProviderRequest struct {
	Name string `json:"name" binding:"required"` // provider display name
	// Notes optionally supplies administrator description text. Creation
	// normalizes the supplied text; nil leaves this description absent in the
	// newly constructed provider.
	// optional administrator description text
	Notes *string `json:"notes"`
	// upstream platform identifier
	Platform string `json:"platform" binding:"required"`
	// provider authentication kind
	Type string `json:"type" binding:"required,oneof=oauth setup-token apikey upstream bedrock service_account cosy"`
	// Credentials is the required authorization object passed to creation.
	// Administration validates platform-specific values and may normalize this
	// borrowed map before constructing the stored provider.
	Credentials map[string]any `json:"credentials" binding:"required" jsonschema:"API Key providers use api_key and base_url. Optional upstream_protocols contains IDs from protocol_capabilities.list, such as openai_responses or openai_chat_completions. Complete OAuth authorization in provider management."` // platform authorization creation values
	// Extra optionally supplies provider configuration. Deprecated keys are
	// discarded in place before idempotency fingerprinting, and creation validates
	// its platform-specific configuration before storage.
	Extra map[string]any `json:"extra"` // provider creation configuration values
	// ProxyID optionally selects the provider's egress proxy for administrative
	// creation. The selected pointer is forwarded to validation and construction;
	// nil supplies no explicit proxy binding.
	ProxyID *int64 `json:"proxy_id"` // optional creation proxy identifier
	// Concurrency is forwarded to administrative creation as supplied.
	// NormalizeProviderConcurrency applies the platform and authentication rules
	// when constructing the provider's effective request capacity.
	Concurrency int `json:"concurrency"` // supplied provider request capacity
	Priority    int `json:"priority"`    // scheduler priority value
	// RateMultiplier supplies a nonnegative billing multiplier. Nil delegates
	// creation defaults or preserves the stored update value, and an explicit zero
	// remains a supplied value.
	RateMultiplier *float64 `json:"rate_multiplier"` // provider billing multiplier
	// LoadFactor supplies an optional scheduler capacity override during creation.
	// Nonpositive values leave the override absent; administrative validation
	// rejects positive values above 10000.
	LoadFactor *int `json:"load_factor"` // optional scheduler capacity override
	// GroupIDs supplies creation group associations for administrator validation
	// and binding. An omitted or empty selection creates a provider with no
	// automatically selected default group.
	GroupIDs []int64 `json:"group_ids"` // requested routing group associations
	// ExpiresAt supplies optional provider expiry in Unix seconds. Administrative
	// creation installs a timestamp for positive values, while omission and
	// nonpositive input leave expiry absent.
	ExpiresAt *int64 `json:"expires_at"` // Unix seconds
	// AutoPauseOnExpired distinguishes a supplied false from omission. Creation
	// defaults omission to true, while updates preserve the stored policy when
	// this pointer is nil.
	AutoPauseOnExpired *bool `json:"auto_pause_on_expired"` // pause on expiry
}

// UpdateProviderRequest carries selected provider edits, with pointer fields
// distinguishing supplied zero values from omission.
type UpdateProviderRequest struct {
	Name string `json:"name"` // provider display name
	// Notes preserves the stored description when nil. A supplied empty or
	// whitespace-only string is normalized by administration when replacing the
	// description.
	// optional administrator description edit
	Notes *string `json:"notes"`
	// provider authentication kind
	Type string `json:"type" binding:"omitempty,oneof=oauth setup-token apikey upstream bedrock service_account cosy"`
	// Credentials follows administrative merge rules for redacted edits. Omitted
	// sensitive keys inherit stored values, and an empty map skips ordinary
	// credential replacement; shadows have their own allowed-field rules.
	Credentials map[string]any `json:"credentials"` // redacted authorization configuration edit
	// Extra is interpreted as selected provider configuration. Omission leaves
	// settings unchanged; explicit empty input can clear configuration, while a
	// deprecated-only object becomes nil before administration.
	Extra map[string]any `json:"extra"` // selected provider configuration edit
	// ProxyID selects an egress proxy. Nil preserves update selection; a supplied
	// zero is passed to administration for its proxy clearing rules.
	ProxyID *int64 `json:"proxy_id"` // selected egress proxy
	// Concurrency and Priority use pointer presence to distinguish omission from
	// supplied zero values. Nil preserves the corresponding stored value;
	// administration normalizes concurrency according to provider rules.
	Concurrency *int `json:"concurrency"` // optional provider request capacity
	Priority    *int `json:"priority"`    // scheduler priority value
	// RateMultiplier supplies a nonnegative billing multiplier. Nil delegates
	// creation defaults or preserves the stored update value, and an explicit zero
	// remains a supplied value.
	RateMultiplier *float64 `json:"rate_multiplier"` // provider billing multiplier
	// LoadFactor supplies a scheduler capacity override. Nonpositive updates clear
	// the override; positive values are limited to 10000 by administration.
	LoadFactor *int   `json:"load_factor"`                                            // scheduler capacity override
	Status     string `json:"status" binding:"omitempty,oneof=active inactive error"` // stored provider state
	// GroupIDs selects provider group associations. Update omission preserves
	// them, while a supplied empty collection requests clearing through the
	// administrator use case.
	GroupIDs *[]int64 `json:"group_ids"` // associated routing groups
	// ExpiresAt supplies provider expiry in Unix seconds. Nil leaves update expiry
	// unchanged; a supplied nonpositive update clears the stored expiry.
	ExpiresAt *int64 `json:"expires_at"` // Unix seconds
	// AutoPauseOnExpired distinguishes a supplied false from omission. Creation
	// defaults omission to true, while updates preserve the stored policy when
	// this pointer is nil.
	AutoPauseOnExpired *bool `json:"auto_pause_on_expired"` // pause on expiry
}

// Create validates the strict creation body and returns an idempotent
// management response. Deprecated Extra keys are removed before fingerprinting,
// and privacy observations run inside the creation closure after storage
// succeeds. Request cancellation and persistence failures are returned through
// the shared executor.
//
// The closure captures its created record for AfterCreate. A replay skips that
// closure but still reaches the callback with nil; application callbacks need
// to handle that value. Follow-up work therefore depends on actual creation,
// while the response may come from stored idempotency data.
// POST /api/v1/admin/providers
func (h *ManagementHandler) Create(c *gin.Context) {
	var req CreateProviderRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	// Retired Extra settings are ignored by creation. Remove them before the
	// idempotency fingerprint so their supplied values cannot distinguish the same admitted configuration.
	providercore.DiscardDeprecatedProviderExtra(req.Extra)
	if req.RateMultiplier != nil && *req.RateMultiplier < 0 {
		response.BadRequest(c, "rate_multiplier must be >= 0")
		return
	}
	// Normalize the managed request-rate setting before fingerprinting so the
	// idempotency payload records the same supported value passed to administration.
	SanitizeExtraBaseRPM(req.Extra)
	if err := providercore.ValidateUpstreamRequestIDHeaderExtra(req.Extra); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// The closure captures a record after durable creation for the configured
	// follow-up callback. A response replay skips this closure and supplies nil later.
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
		// Privacy observation follows durable creation. Its platform eligibility
		// checks permit both calls, and an empty observation leaves creation successful.
		h.privacy.ForceAntigravityPrivacy(ctx, provider)
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

// Duplicate creates a source provider copy within the administrator idempotency
// scope. The source ID supplies the payload fingerprint, while actor scope and
// operation key are also passed to the durable duplication operation.
// Successful replay returns the stored response.
//
// An in-progress or unavailable idempotency store can follow a completed
// provider creation. Those two failures trigger recovery by the same source,
// actor and key, and a found record returns a recovered response. A failed or
// empty recovery retains the original executor error so uncertainty cannot
// become another immediate creation.
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
			ProviderID int64 `json:"provider_id"` // source duplicate provider
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
				// A durable duplicate supplies a reviewable result after response-cache
				// uncertainty; return it so this path does not execute creation again.
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

// Update strictly binds supplied configuration fields and returns the updated
// runtime view. Deprecated-only Extra becomes an omitted update; administration
// interprets pointer omission, sensitive credential retention and explicit
// clearing values.
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
	// A payload containing only retired keys means no Extra update. Keeping
	// nil here lets administration retain the currently stored configuration.
	if normalizedExtra, shouldReplaceExtra := providercore.NormalizeDeprecatedProviderExtraUpdate(req.Extra); shouldReplaceExtra {
		req.Extra = normalizedExtra
	} else {
		req.Extra = nil
	}
	if req.RateMultiplier != nil && *req.RateMultiplier < 0 {
		response.BadRequest(c, "rate_multiplier must be >= 0")
		return
	}
	// Normalize the supplied managed request-rate setting before administration,
	// whose configuration writes consume an integer value within the supported range.
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

// SanitizeExtraBaseRPM mutates the supplied Extra map through provider
// sanitation. Missing or nil maps are unchanged; supported numeric
// representations are converted to an integer and clamped between zero and
// 10000.
func SanitizeExtraBaseRPM(extra map[string]any) { providercore.SanitizeManagedBaseRPM(extra) }
