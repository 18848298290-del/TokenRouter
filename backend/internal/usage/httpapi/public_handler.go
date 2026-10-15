package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	keycore "github.com/TokenFlux/TokenRouter/internal/apikey"
	billingcore "github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// public_handler.go builds public API-key usage responses from funding context,
// key limits, and optional usage statistics.
// API-key usage responses show the funding source selected during gateway
// authentication beside optional analysis summaries. A resolved subscription
// source stays in its subscription response branch even when the selected
// grant is unavailable, while wallet responses obtain the authenticated
// payer's latest balance when a reader is bound.
//
// Key lifetime quotas and spending windows choose the quota_limited shape
// independently of the funding source. Usage totals, daily rows and model
// statistics are best-effort additions, so their query failures leave the
// funding view available. Model-statistic dates default to an instant-based
// thirty-day range; daily rows use a separate calendar-day count and
// timezone. The public endpoint writes its response shape directly as JSON.

// PublicUsageContext supplies gateway-authenticated key and funding views for
// the public usage response.
type PublicUsageContext struct {
	// Key, Billing and Subscription are request readers bound by the
	// application. Usage and its funding helpers call their required callbacks
	// directly, so the registered public route needs nonnil bindings.
	// Key returns a nonnil authenticated API key whenever its boolean is true.
	// Public Usage reads that key's identifiers and limits after the
	// authentication check.
	Key          func(*gin.Context) (*keycore.APIKey, bool)                   // authenticated key view callback
	Billing      func(*gin.Context) (*billingcore.APIKeyBillingContext, bool) // resolved funding view callback
	Subscription func(*gin.Context) (*billingcore.UserSubscription, bool)     // legacy grant view callback
}
type (
	// PublicUserBalance carries the latest wallet amount selected for the public
	// funding response.
	PublicUserBalance struct {
		Balance float64 // wallet balance amount USD
	}
	// PublicBalanceReader supplies current wallet values for authenticated payer
	// display in usage.
	PublicBalanceReader interface {
		// GetByID returns a nonnil current wallet view on success for the requested
		// payer. Public funding responses propagate lookup failures and use the
		// key's loaded account balance when this reader is absent.
		GetByID(context.Context, int64) (*PublicUserBalance, error)
	}
)

// PublicBalanceQuery implements wallet lookup through a function bound by the
// application.
type PublicBalanceQuery func(context.Context, int64) (*PublicUserBalance, error)

// PublicWindowReader supplies current key spending-window counters for public
// quota display.
type PublicWindowReader interface {
	// GetRateLimitData returns the current key spending counters and window
	// anchors. Public quota responses treat lookup errors or nil data as absent
	// optional window metadata while retaining the base funding view.
	GetRateLimitData(context.Context, int64) (*billingcore.APIKeyRateLimitData, error)
}
type (
	// BalanceUnitReader supplies the display unit used for public funding and
	// quota amounts.
	BalanceUnitReader interface {
		// GetBalanceUnitName returns the configured display unit for wallet,
		// subscription and key-quota values. Public usage uses USD when this reader
		// is absent and forwards the returned unit to both response shapes.
		GetBalanceUnitName(context.Context) string
	}
	// PublicUsageHandler serves API-key funding information with best-effort
	// measured usage additions.
	PublicUsageHandler struct {
		usageService   *usage.UsageService // optional analysis usage query service
		apiKeyService  PublicWindowReader  // optional key spending window reader
		userService    PublicBalanceReader // optional current payer wallet reader
		settingService BalanceUnitReader   // optional funding display unit reader
		request        PublicUsageContext  // gateway key and funding callbacks
		calendar       timezone.Calendar   // public range and daily calendar
	}
)

// GetByID invokes the bound wallet query for the authenticated payer ID and
// returns its result. The function adapter must be nonnil before public
// funding lookup; the owning query supplies storage cancellation and error
// handling.
func (f PublicBalanceQuery) GetByID(ctx context.Context, id int64) (*PublicUserBalance, error) {
	return f(ctx, id)
}

// NewPublicUsageHandler binds public funding readers, gateway request
// callbacks and the analysis calendar.
func NewPublicUsageHandler(u *usage.UsageService, keys PublicWindowReader, users PublicBalanceReader, settings BalanceUnitReader, request PublicUsageContext, calendar timezone.Calendar) *PublicUsageHandler {
	return &PublicUsageHandler{usageService: u, apiKeyService: keys, userService: users, settingService: settings, request: request, calendar: calendar}
}

// errorResponse writes the public usage error shape with its supplied HTTP
// status.
func (h *PublicUsageHandler) errorResponse(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{"type": "error", "error": gin.H{"type": errType, "message": message}})
}

// calculateSubscriptionRemaining returns the selected grant's display
// remainder through billing. Nil grants return zero, absent positive limits
// return negative one, and enabled windows contribute their smallest
// available remainder without status maintenance.
func (h *PublicUsageHandler) calculateSubscriptionRemaining(sub *billingcore.UserSubscription) float64 {
	return billingcore.SubscriptionRemainingForDisplay(sub)
}

// apiKeyUsageSubscriptionPayload formats the selected grant's usage, limits
// and lifecycle values. Nil input returns nil; optional quota pointers and
// the weekly-window anchor remain shared with the supplied grant during
// response encoding.
func apiKeyUsageSubscriptionPayload(subscription *billingcore.UserSubscription) gin.H {
	if subscription == nil {
		return nil
	}
	return gin.H{
		"id":                  subscription.ID,
		"plan_id":             subscription.PlanID,
		"daily_usage_usd":     subscription.DailyUsageUSD,
		"weekly_usage_usd":    subscription.WeeklyUsageUSD,
		"monthly_usage_usd":   subscription.MonthlyUsageUSD,
		"daily_limit_usd":     subscription.DailyLimitUSD,
		"weekly_limit_usd":    subscription.WeeklyLimitUSD,
		"monthly_limit_usd":   subscription.MonthlyLimitUSD,
		"weekly_window_start": subscription.WeeklyWindowStart,
		"expires_at":          subscription.ExpiresAt,
		"status":              subscription.Status,
	}
}

// buildAPIKeyUsageBilling returns funding metadata and the selected
// subscription or wallet amount. Callers provide a key, authenticated subject
// and bound request readers; the resolved billing context takes precedence
// over the legacy subscription context.
//
// A subscription source returns its selected grant when present and keeps a
// nil wallet result even when the grant is unavailable. Other source values
// follow the wallet path: a bound reader refreshes subject.UserID, otherwise
// the loaded key account supplies its balance or zero remains. Wallet
// availability is computed from that resulting balance, and reader failures
// reject funding construction.
func (h *PublicUsageHandler) buildAPIKeyUsageBilling(c *gin.Context, ctx context.Context, apiKey *keycore.APIKey, subject authctx.AuthSubject, balanceUnitName string) (gin.H, *billingcore.UserSubscription, *float64, error) {
	mode := keycore.APIKeyEffectiveBillingMode(apiKey)
	billing := gin.H{
		"mode":                      mode,
		"source":                    "balance",
		"preferred_subscription_id": nil,
		"available":                 true,
		"unit":                      balanceUnitName,
	}
	if apiKey != nil && apiKey.PreferredSubscriptionID != nil {
		billing["preferred_subscription_id"] = *apiKey.PreferredSubscriptionID
	}

	var subscription *billingcore.UserSubscription
	if contextBilling, ok := h.request.Billing(c); ok && contextBilling != nil {
		billing["mode"] = contextBilling.Mode
		billing["source"] = contextBilling.Source
		billing["available"] = contextBilling.Available
		subscription = contextBilling.Subscription
	} else if contextSubscription, ok := h.request.Subscription(c); ok {
		// The legacy view supplies funding when resolved billing metadata was absent.
		// Keep the selected subscription source available to both public response shapes.
		billing["source"] = "subscription"
		subscription = contextSubscription
	}

	if billing["source"] == "subscription" {
		if subscription != nil {
			billing["subscription_id"] = subscription.ID
			billing["plan_id"] = subscription.PlanID
			billing["remaining"] = h.calculateSubscriptionRemaining(subscription)
			if subscription.Plan != nil {
				billing["plan_name"] = subscription.Plan.Name
			}
		}
		return billing, subscription, nil, nil
	}

	var balance float64
	if h.userService != nil {
		latestUser, err := h.userService.GetByID(ctx, subject.UserID)
		if err != nil {
			return nil, nil, nil, err
		}
		balance = latestUser.Balance
	} else if apiKey != nil && apiKey.User != nil {
		// A missing current-balance reader leaves the loaded account as the available
		// wallet observation for this response; the selected funding source stays balance.
		balance = apiKey.User.Balance
	}
	billing["balance"] = balance
	billing["remaining"] = balance
	billing["available"] = balance > 0
	return billing, nil, &balance, nil
}

// UsageUnrestricted writes the funding response for keys lacking their own
// lifetime or spending-window limits. It uses the resolved funding source,
// keeps unavailable grants in the subscription shape, and includes each
// nonnil supplied analysis addition.
func (h *PublicUsageHandler) UsageUnrestricted(c *gin.Context, ctx context.Context, apiKey *keycore.APIKey, subject authctx.AuthSubject, usageData gin.H, dailyUsage any, modelStats any, balanceUnitName string) {
	billing, subscription, balance, billingErr := h.buildAPIKeyUsageBilling(c, ctx, apiKey, subject, balanceUnitName)
	if billingErr != nil {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "Failed to get billing source")
		return
	}
	if billing["source"] == "subscription" {
		planName := ""
		if subscription != nil && subscription.Plan != nil {
			planName = subscription.Plan.Name
		} else if apiKey.Group != nil {
			// A missing selected plan can use the loaded key group label for display.
			// This choice leaves resolved subscription availability and funding source in place.
			planName = apiKey.Group.Name
		}
		resp := gin.H{
			"mode":     "unrestricted",
			"isValid":  billing["available"],
			"planName": planName,
			"unit":     balanceUnitName,
			"billing":  billing,
		}

		remaining := 0.0
		if subscription != nil {
			remaining = h.calculateSubscriptionRemaining(subscription)
		}
		resp["remaining"] = remaining
		if subscription != nil {
			resp["subscription"] = apiKeyUsageSubscriptionPayload(subscription)
		} else {
			resp["subscription"] = gin.H{
				"id":        billing["preferred_subscription_id"],
				"available": false,
			}
		}

		if usageData != nil {
			resp["usage"] = usageData
		}
		if dailyUsage != nil {
			resp["daily_usage"] = dailyUsage
		}
		if modelStats != nil {
			resp["model_stats"] = modelStats
		}
		c.JSON(http.StatusOK, resp)
		return
	}

	currentBalance := 0.0
	if balance != nil {
		currentBalance = *balance
	}

	resp := gin.H{
		"mode":      "unrestricted",
		"isValid":   billing["available"],
		"planName":  "钱包余额",
		"remaining": currentBalance,
		"unit":      balanceUnitName,
		"balance":   currentBalance,
		"billing":   billing,
	}
	if usageData != nil {
		resp["usage"] = usageData
	}
	if dailyUsage != nil {
		resp["daily_usage"] = dailyUsage
	}
	if modelStats != nil {
		resp["model_stats"] = modelStats
	}
	c.JSON(http.StatusOK, resp)
}

// usageQuotaLimited writes key quota and window data alongside resolved
// funding metadata. Its isValid field accepts active, quota-exhausted and
// expired stored key statuses for this display; optional window lookup
// failures leave window metadata absent.
func (h *PublicUsageHandler) usageQuotaLimited(c *gin.Context, ctx context.Context, apiKey *keycore.APIKey, subject authctx.AuthSubject, usageData gin.H, dailyUsage any, modelStats any, balanceUnitName string) {
	resp := gin.H{
		"mode":    "quota_limited",
		"isValid": apiKey.Status == keycore.StatusAPIKeyActive || apiKey.Status == keycore.StatusAPIKeyQuotaExhausted || apiKey.Status == keycore.StatusAPIKeyExpired,
		"status":  apiKey.Status,
	}
	billing, subscription, balance, billingErr := h.buildAPIKeyUsageBilling(c, ctx, apiKey, subject, balanceUnitName)
	if billingErr != nil {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "Failed to get billing source")
		return
	}
	resp["billing"] = billing
	if subscription != nil {
		resp["subscription"] = apiKeyUsageSubscriptionPayload(subscription)
	} else if billing["source"] == "subscription" {
		resp["subscription"] = gin.H{
			"id":        billing["preferred_subscription_id"],
			"available": false,
		}
	}
	if balance != nil {
		resp["balance"] = *balance
	}

	// Lifetime key quota.
	if apiKey.Quota > 0 {
		remaining := apiKey.GetQuotaRemaining()
		resp["quota"] = gin.H{
			"limit":     apiKey.Quota,
			"used":      apiKey.QuotaUsed,
			"remaining": remaining,
			"unit":      balanceUnitName,
		}
		resp["remaining"] = remaining
		resp["unit"] = balanceUnitName
	}

	// Spending-window metadata is optional for this display. Read current counters
	// when a reader is bound, leaving funding and lifetime-quota fields available on failure.
	if apiKey.HasRateLimits() && h.apiKeyService != nil {
		rateLimitData, err := h.apiKeyService.GetRateLimitData(ctx, apiKey.ID)
		// An unavailable window read leaves rate_limits absent while funding and
		// lifetime quota remain in the response, allowing the base display to complete.
		if err == nil && rateLimitData != nil {
			var rateLimits []gin.H
			if apiKey.RateLimit5h > 0 {
				used := rateLimitData.EffectiveUsage5h()
				entry := gin.H{
					"window": "5h",
					"limit":  apiKey.RateLimit5h,
					"used":   used,
					// Keep limit and measured usage visible after the remainder reaches zero.
					"remaining":    max(0, apiKey.RateLimit5h-used),
					"window_start": rateLimitData.Window5hStart,
				}
				if rateLimitData.Window5hStart != nil && !billingcore.IsWindowExpired(rateLimitData.Window5hStart, keycore.RateLimitWindow5h) {
					entry["reset_at"] = rateLimitData.Window5hStart.Add(keycore.RateLimitWindow5h)
				}
				rateLimits = append(rateLimits, entry)
			}
			if apiKey.RateLimit1d > 0 {
				used := rateLimitData.EffectiveUsage1d()
				entry := gin.H{
					"window": "1d",
					"limit":  apiKey.RateLimit1d,
					"used":   used,
					// Keep limit and measured usage visible after the remainder reaches zero.
					"remaining":    max(0, apiKey.RateLimit1d-used),
					"window_start": rateLimitData.Window1dStart,
				}
				if rateLimitData.Window1dStart != nil && !billingcore.IsWindowExpired(rateLimitData.Window1dStart, keycore.RateLimitWindow1d) {
					entry["reset_at"] = rateLimitData.Window1dStart.Add(keycore.RateLimitWindow1d)
				}
				rateLimits = append(rateLimits, entry)
			}
			if apiKey.RateLimit7d > 0 {
				used := rateLimitData.EffectiveUsage7d()
				entry := gin.H{
					"window": "7d",
					"limit":  apiKey.RateLimit7d,
					"used":   used,
					// Keep limit and measured usage visible after the remainder reaches zero.
					"remaining":    max(0, apiKey.RateLimit7d-used),
					"window_start": rateLimitData.Window7dStart,
				}
				if rateLimitData.Window7dStart != nil && !billingcore.IsWindowExpired(rateLimitData.Window7dStart, keycore.RateLimitWindow7d) {
					entry["reset_at"] = rateLimitData.Window7dStart.Add(keycore.RateLimitWindow7d)
				}
				rateLimits = append(rateLimits, entry)
			}
			if len(rateLimits) > 0 {
				resp["rate_limits"] = rateLimits
			}
		}
	}

	// Key expiration.
	if apiKey.ExpiresAt != nil {
		resp["expires_at"] = apiKey.ExpiresAt
		resp["days_until_expiry"] = apiKey.GetDaysUntilExpiry()
	}

	if usageData != nil {
		resp["usage"] = usageData
	}
	if dailyUsage != nil {
		resp["daily_usage"] = dailyUsage
	}
	if modelStats != nil {
		resp["model_stats"] = modelStats
	}

	c.JSON(http.StatusOK, resp)
}

// buildAPIKeyDailyUsage queries the key's independent calendar-day range
// selected by days and timezone. Missing usage service or query failure
// returns nil, allowing the public funding response to omit this optional
// addition.
func (h *PublicUsageHandler) buildAPIKeyDailyUsage(c *gin.Context, apiKeyID int64, days int) any {
	if h.usageService == nil {
		return nil
	}
	startTime, endTime := apiKeyDailyUsageRange(days, c.Query("timezone"), h.calendar)
	stats, err := h.usageService.GetAPIKeyDailyUsageByKey(c.Request.Context(), apiKeyID, startTime, endTime)
	// Analysis is an optional funding addition. Leave it absent when its query
	// cannot supply data so the authenticated funding view can still complete.
	if err != nil {
		return nil
	}
	return stats
}

// buildUsageData formats today's and retained-total key statistics when
// available. Missing service, query failure or nil statistics returns nil so
// the public funding response can retain its base information.
func (h *PublicUsageHandler) buildUsageData(ctx context.Context, apiKeyID int64) gin.H {
	if h.usageService == nil {
		return nil
	}
	dashStats, err := h.usageService.GetAPIKeyDashboardStats(ctx, apiKeyID)
	// Analysis is an optional funding addition. Leave it absent when its query
	// cannot supply data so the authenticated funding view can still complete.
	if err != nil || dashStats == nil {
		return nil
	}
	return gin.H{
		"today": gin.H{
			"requests":              dashStats.TodayRequests,
			"input_tokens":          dashStats.TodayInputTokens,
			"output_tokens":         dashStats.TodayOutputTokens,
			"cache_creation_tokens": dashStats.TodayCacheCreationTokens,
			"cache_read_tokens":     dashStats.TodayCacheReadTokens,
			"total_tokens":          dashStats.TodayTokens,
			"cost":                  dashStats.TodayCost,
			"actual_cost":           dashStats.TodayActualCost,
		},
		"total": gin.H{
			"requests":              dashStats.TotalRequests,
			"input_tokens":          dashStats.TotalInputTokens,
			"output_tokens":         dashStats.TotalOutputTokens,
			"cache_creation_tokens": dashStats.TotalCacheCreationTokens,
			"cache_read_tokens":     dashStats.TotalCacheReadTokens,
			"total_tokens":          dashStats.TotalTokens,
			"cost":                  dashStats.TotalCost,
			"actual_cost":           dashStats.TotalActualCost,
		},
		"average_duration_ms": dashStats.AverageDurationMs,
		"rpm":                 dashStats.Rpm,
		"tpm":                 dashStats.Tpm,
	}
}

// parseUsageDateRange returns model-statistic bounds defaulting to thirty
// calendar dates before the current instant through now. Valid supplied
// bounds replace each side independently; invalid values retain defaults, and
// date-only ends advance one calendar day.
func (h *PublicUsageHandler) parseUsageDateRange(c *gin.Context) (time.Time, time.Time) {
	now := h.calendar.Now()
	endTime := now
	startTime := now.AddDate(0, 0, -30)
	userTZ := c.Query("timezone")

	if s := c.Query("start_date"); s != "" {
		// Malformed start input keeps the instant-based thirty-date default, allowing
		// model statistics to remain an optional report under the ordinary public range.
		if t, _, err := h.calendar.ParseDateTimeInUserLocation(s, userTZ); err == nil {
			startTime = t
		}
	}
	if s := c.Query("end_date"); s != "" {
		// Malformed end input keeps the current-instant default. Valid supplied ends
		// still replace it independently of whether the requested start date parsed successfully.
		if t, dateOnly, err := h.calendar.ParseDateTimeInUserLocation(s, userTZ); err == nil {
			if dateOnly {
				// Calendar expansion includes the selected local date across DST changes,
				// while explicitly timed end values retain their supplied instant unchanged.
				t = t.AddDate(0, 0, 1)
			}
			endTime = t
		}
	}
	return startTime, endTime
}

// Usage writes CC Switch funding and optional usage data for a
// gateway-authenticated key and subject. Invalid days returns a public
// bad-request error; missing authentication returns a public unauthorized
// error.
//
// A positive lifetime quota or configured spending window chooses
// quota_limited, independently of wallet or subscription funding. Otherwise
// UsageUnrestricted formats the funding source. Usage summaries, daily rows
// and model grouping can fail independently and remain absent; funding lookup
// failure rejects the response. The optional model range and daily day-count
// range therefore describe separate analysis windows.
// @project-doc docs/operations/observability_and_data_lifecycle.md#usage_query_contracts
func (h *PublicUsageHandler) Usage(c *gin.Context) {
	apiKey, ok := h.request.Key(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	ctx := c.Request.Context()
	balanceUnitName := "USD"
	if h.settingService != nil {
		balanceUnitName = h.settingService.GetBalanceUnitName(ctx)
	}

	// The optional date range controls model_stats.
	startTime, endTime := h.parseUsageDateRange(c)
	days, ok := ParseAPIKeyDailyUsageDays(c.DefaultQuery("days", ""))
	if !ok {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Invalid days, allowed range is 1-90")
		return
	}

	// Summary and daily queries enrich funding independently. Each helper returns
	// nil on failure so unavailable analysis can be omitted from the public response shape.
	usageData := h.buildUsageData(ctx, apiKey.ID)
	dailyUsage := h.buildAPIKeyDailyUsage(c, apiKey.ID, days)

	// A failed or empty model query contributes no model_stats addition. The funding
	// formatter still runs so analysis availability does not decide the base source response.
	var modelStats any
	if h.usageService != nil {
		if stats, err := h.usageService.GetAPIKeyModelStats(ctx, apiKey.ID, startTime, endTime); err == nil && len(stats) > 0 {
			modelStats = stats
		}
	}

	// Keys with a lifetime quota or spending-window limits use quota_limited.
	isQuotaLimited := apiKey.Quota > 0 || apiKey.HasRateLimits()

	if isQuotaLimited {
		h.usageQuotaLimited(c, ctx, apiKey, subject, usageData, dailyUsage, modelStats, balanceUnitName)
		return
	}

	h.UsageUnrestricted(c, ctx, apiKey, subject, usageData, dailyUsage, modelStats, balanceUnitName)
}
