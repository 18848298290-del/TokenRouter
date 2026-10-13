package httpapi

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
	middleware2 "github.com/TokenFlux/TokenRouter/internal/server/middleware"
)

// User subscription views derive their recipient from authentication rather
// than a supplied query ID. Lists enrich group labels and resolve plan display
// text, while progress pairs a listed view with a separate service read and
// omits individual progress failures. The summary counts active views and
// totals their monthly usage, so it is a window report rather than a
// lifetime-consumption measure.
//
// Self-revocation parses a positive target ID and delegates
// ownership/highest-quota validation and replacement-key rebinding to the
// service. Handler replies keep the service result's revoked/replacement IDs
// and rebound count together. Missing identity, malformed targets and service
// failures follow their existing HTTP response paths, with authorization
// middleware installed by the route owner.

// SubscriptionSummaryItem carries one active-view grant's window amounts and
// optional expiry text.
type SubscriptionSummaryItem struct {
	ID       int64  `json:"id"`        // listed grant record identifier
	PlanID   int64  `json:"plan_id"`   // grant source plan identifier
	PlanName string `json:"plan_name"` // localized current plan name
	Status   string `json:"status"`    // listed subscription status label
	// DailyUsedUSD, DailyLimitUSD, WeeklyUsedUSD, WeeklyLimitUSD, MonthlyUsedUSD
	// and MonthlyLimitUSD retain each period's listed amounts. Nil source limits
	// become zero, and zero values are omitted by summary JSON tags.
	DailyUsedUSD    float64 `json:"daily_used_usd,omitempty"`    // USD
	DailyLimitUSD   float64 `json:"daily_limit_usd,omitempty"`   // USD
	WeeklyUsedUSD   float64 `json:"weekly_used_usd,omitempty"`   // USD
	WeeklyLimitUSD  float64 `json:"weekly_limit_usd,omitempty"`  // USD
	MonthlyUsedUSD  float64 `json:"monthly_used_usd,omitempty"`  // USD
	MonthlyLimitUSD float64 `json:"monthly_limit_usd,omitempty"` // USD
	// ExpiresAt is supplied for a nonzero source expiry using the
	// seconds-and-offset timestamp layout. A zero source time leaves this
	// optional string absent in summary JSON.
	ExpiresAt *string `json:"expires_at,omitempty"` // optional offset-bearing expiry text
}

// SubscriptionProgressInfo pairs the listed grant view with separately fetched
// quota progress.
type SubscriptionProgressInfo struct {
	// Subscription and Progress identify the same grant but come from separate
	// reads. GetProgress omits the pair if progress lookup fails and localizes
	// only the listed subscription view here.
	Subscription *UserSubscription             `json:"subscription"` // localized listed subscription view
	Progress     *billing.SubscriptionProgress `json:"progress"`     // separately fetched quota progress
}

// RevokeSubscriptionResponse identifies the revoked grant, replacement and
// rebound subscription-funded Keys.
type RevokeSubscriptionResponse struct {
	RevokedSubscriptionID int64 `json:"revoked_subscription_id"` // revoked grant record identifier
	// ReplacementSubscriptionID and ReboundAPIKeyCount come from the completed
	// revocation operation. Without a selected replacement, the ID is nil and the
	// count stays zero; a replacement may still rebind zero Keys.
	ReplacementSubscriptionID *int64 `json:"replacement_subscription_id"` // optional replacement grant identifier
	ReboundAPIKeyCount        int    `json:"rebound_api_key_count"`       // reported subscription key rebind count
}

// SubscriptionHandler exposes current-user grant lists, window displays and
// quota-conditioned revocation.
type SubscriptionHandler struct {
	subscriptionService *billing.SubscriptionService // current-user subscription operation service
}

// NewSubscriptionHandler binds a caller-supplied subscription service without
// reading grants or starting work. The caller mounts initialized handlers with
// authentication middleware; each request derives the current recipient from
// that context.
func NewSubscriptionHandler(subscriptionService *billing.SubscriptionService) *SubscriptionHandler {
	return &SubscriptionHandler{
		subscriptionService: subscriptionService,
	}
}

// List returns the authenticated user's subscription views and enriches plan
// group labels before mapping. Missing identity yields unauthorized; service
// errors use shared mapping, and successful plan text is localized using the
// request context language.
// GET /api/v1/subscriptions
func (h *SubscriptionHandler) List(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}

	subscriptions, err := h.subscriptionService.ListUserSubscriptions(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.subscriptionService.EnrichSubscriptionPlanGroups(c.Request.Context(), subscriptions)

	out := make([]UserSubscription, 0, len(subscriptions))
	for i := range subscriptions {
		out = append(out, *UserSubscriptionFromService(&subscriptions[i], locale.FromContext(c.Request.Context())))
	}
	response.Success(c, out)
}

// GetActive returns the authenticated user's service-selected active grant
// views with enriched plan groups. Missing identity yields unauthorized;
// service errors use shared mapping, and the user DTO resolves
// request-language plan display text.
// GET /api/v1/subscriptions/active
func (h *SubscriptionHandler) GetActive(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}

	subscriptions, err := h.subscriptionService.ListActiveUserSubscriptions(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.subscriptionService.EnrichSubscriptionPlanGroups(c.Request.Context(), subscriptions)

	out := make([]UserSubscription, 0, len(subscriptions))
	for i := range subscriptions {
		out = append(out, *UserSubscriptionFromService(&subscriptions[i], locale.FromContext(c.Request.Context())))
	}
	response.Success(c, out)
}

// GetProgress returns listed active grant views paired with individually
// fetched progress for the authenticated user. Missing identity yields
// unauthorized, and failure of the initial list uses the shared error
// response. Individual progress failures omit that item while other grants
// remain in the result.
//
// The list view and progress query are separate observations joined by
// subscription ID, so the pair need not represent one atomic stored snapshot.
// The subscription portion uses request-language mapping, while the service
// constructs progress from its own current read.
// GET /api/v1/subscriptions/progress
func (h *SubscriptionHandler) GetProgress(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}

	subscriptions, err := h.subscriptionService.ListActiveUserSubscriptions(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	result := make([]SubscriptionProgressInfo, 0, len(subscriptions))
	for i := range subscriptions {
		sub := &subscriptions[i]
		progress, err := h.subscriptionService.GetSubscriptionProgress(c.Request.Context(), sub.ID)
		if err != nil {
			// A failed progress read omits this pair while keeping other successfully
			// read grant/progress items, so one unavailable detail does not fail the
			// complete active list.
			continue
		}
		result = append(result, SubscriptionProgressInfo{
			Subscription: UserSubscriptionFromService(sub, locale.FromContext(c.Request.Context())),
			Progress:     progress,
		})
	}

	response.Success(c, result)
}

// GetSummary returns active-view count, per-grant quota data and the sum of
// monthly usage for the authenticated user. Missing identity or list failure
// returns before construction; nil limits map to zero and expiry text is
// omitted for a zero timestamp.
//
// The total uses monthly usage alone, not an addition of day/week/month
// observations that can cover the same charge. It is this active-view monthly
// sum rather than lifetime user consumption. Plan names use request-language
// localization, and each item retains the listed grant's amounts and status.
// GET /api/v1/subscriptions/summary
func (h *SubscriptionHandler) GetSummary(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}

	subscriptions, err := h.subscriptionService.ListActiveUserSubscriptions(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	var totalUsed float64
	items := make([]SubscriptionSummaryItem, 0, len(subscriptions))

	for _, sub := range subscriptions {
		item := SubscriptionSummaryItem{
			ID:              sub.ID,
			PlanID:          sub.PlanID,
			Status:          sub.Status,
			DailyLimitUSD:   valueOrZero(sub.DailyLimitUSD),
			WeeklyLimitUSD:  valueOrZero(sub.WeeklyLimitUSD),
			MonthlyLimitUSD: valueOrZero(sub.MonthlyLimitUSD),
			DailyUsedUSD:    sub.DailyUsageUSD,
			WeeklyUsedUSD:   sub.WeeklyUsageUSD,
			MonthlyUsedUSD:  sub.MonthlyUsageUSD,
		}

		if sub.Plan != nil {
			item.PlanName = billing.LocalizePlan(sub.Plan, locale.FromContext(c.Request.Context())).Name
		}

		if !sub.ExpiresAt.IsZero() {
			// Emit seconds precision while retaining the source time's zone offset.
			formatted := sub.ExpiresAt.Format("2006-01-02T15:04:05Z07:00")
			item.ExpiresAt = &formatted
		}

		// Monthly usage is this summary's reporting bucket; summing the daily and
		// weekly observations too would combine overlapping period amounts for the
		// same grant.
		totalUsed += sub.MonthlyUsageUSD

		items = append(items, item)
	}

	summary := struct {
		// ActiveCount, TotalUsedUSD and Subscriptions describe this one active-view
		// list. The count includes every listed grant, and the total adds only their
		// monthly usage amounts.
		ActiveCount   int                       `json:"active_count"`   // listed active grant count
		TotalUsedUSD  float64                   `json:"total_used_usd"` // USD
		Subscriptions []SubscriptionSummaryItem `json:"subscriptions"`  // constructed grant summary items
	}{
		ActiveCount:   len(subscriptions),
		TotalUsedUSD:  totalUsed,
		Subscriptions: items,
	}

	response.Success(c, summary)
}

// Revoke parses a positive subscription ID and asks the service to revoke the
// authenticated user's highest-quota-exhausted active grant. Missing identity
// or invalid ID stops the request; the service owns locked eligibility, chain
// shifts and key rebinding, and success returns its replacement/count result.
// POST /api/v1/subscriptions/:id/revoke
func (h *SubscriptionHandler) Revoke(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}

	subscriptionID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || subscriptionID <= 0 {
		response.BadRequest(c, "Invalid subscription ID")
		return
	}
	middleware2.SetAuditAction(c, "user.subscriptions.revoke")

	result, err := h.subscriptionService.RevokeOwnExhaustedSubscription(
		c.Request.Context(),
		subject.UserID,
		subscriptionID,
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, RevokeSubscriptionResponse{
		RevokedSubscriptionID:     result.RevokedSubscriptionID,
		ReplacementSubscriptionID: result.ReplacementSubscriptionID,
		ReboundAPIKeyCount:        result.ReboundAPIKeyCount,
	})
}

// valueOrZero returns a supplied quota amount unchanged and uses zero for an
// absent pointer. Summary JSON omits zero amounts, so this mapping merges
// missing and zero limits while retaining other supplied numeric values.
func valueOrZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}
