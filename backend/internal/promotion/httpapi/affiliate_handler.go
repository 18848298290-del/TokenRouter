package httpapi

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Administrator edits can change an invitation code and a rebate override
// through separate persistent calls. Code replacement happens before a rate
// edit; clearing settings removes the rate before generating a replacement
// code, so a later failure can leave the earlier write applied.
//
// Audit lists accept either RFC3339 instants or local calendar dates.
// Date-only end bounds use the next local midnight minus one nanosecond,
// retaining an entire requested day across daylight-saving changes. Invalid
// date text contributes no time predicate, and the injected calendar
// interprets dates when a requested user timezone cannot be loaded.

// AffiliateHandler serves affiliate administrator edits, recipient lookup,
// and paged audit records.
type AffiliateHandler struct {
	// affiliateService is retained for administrator edits and record queries.
	// Callers provide an initialized service and coordinate its lifetime with
	// the handler.
	// shared affiliate administrator operations
	affiliateService *promotion.AffiliateService
	// lookupUsers receives nonempty q text and the request context.
	// providePromotionAffiliateHTTP installs a callback returning at most twenty
	// matching account identities.
	// recipient selector search callback
	lookupUsers func(context.Context, string) ([]AffiliateUserSummary, error)
	// calendar interprets date-only filters when no valid user timezone is
	// supplied. Its injected location also determines daylight-saving
	// transitions for inclusive day endings.
	calendar timezone.Calendar // local date interpretation calendar
}

// UpdateAffiliateUserRequest carries independent code replacement and
// inviter-rate edits.
// PUT /api/v1/admin/affiliates/users/:user_id
type UpdateAffiliateUserRequest struct {
	// AffCode nil leaves the saved invitation code unchanged. A supplied value
	// is handled first and can persist even if the later rate edit fails.
	// optional replacement invitation code
	AffCode *string `json:"aff_code"`
	// AffRebateRatePercent requests a replacement when present, while
	// ClearRebateRate takes precedence and requests SQL NULL. Omission of both
	// retains the existing percentage override.
	// optional inviter percentage override
	AffRebateRatePercent *float64 `json:"aff_rebate_rate_percent"`
	// remove saved rate override
	ClearRebateRate bool `json:"clear_rebate_rate"`
}

// BatchSetRateRequest carries one rate replacement or removal for submitted
// user IDs.
// POST /api/v1/admin/affiliates/users/batch-rate
type BatchSetRateRequest struct {
	// UserIDs is required and cannot be empty at the handler. The service
	// filters nonpositive IDs, while the response still counts all submitted
	// entries.
	// submitted target account identifiers
	UserIDs []int64 `json:"user_ids" binding:"required"`
	// AffRebateRatePercent supplies the replacement percentage when Clear is
	// false and is then required. Clear takes precedence over a supplied value
	// and requests removal of every selected override.
	// optional replacement inviter percentage
	AffRebateRatePercent *float64 `json:"aff_rebate_rate_percent"`
	// remove selected rate overrides
	Clear bool `json:"clear"`
}

// AffiliateUserSummary identifies a recipient in the administrator lookup
// selector.
type AffiliateUserSummary struct {
	ID       int64  `json:"id"`       // candidate account identifier
	Email    string `json:"email"`    // candidate account email
	Username string `json:"username"` // candidate account display name
}

// NewAffiliateHandler retains affiliate operations, recipient lookup, and a
// calendar for administrator endpoints.
//
// Callers provide usable dependencies; each request passes its context to
// operations and lookup while calendar state remains shared.
func NewAffiliateHandler(affiliate *promotion.AffiliateService, lookup func(context.Context, string) ([]AffiliateUserSummary, error), calendar timezone.Calendar) *AffiliateHandler {
	return &AffiliateHandler{affiliateService: affiliate, lookupUsers: lookup, calendar: calendar}
}

// ListUsers returns the paged accounts with custom invitation-code or
// percentage settings.
//
// It passes search and parsed pagination to the service and uses the shared
// error mapper for failed queries.
// GET /api/v1/admin/affiliates/users
func (h *AffiliateHandler) ListUsers(c *gin.Context) {
	page, pageSize := httpx.ParsePagination(c)
	search := c.Query("search")

	entries, total, err := h.affiliateService.AdminListCustomUsers(c.Request.Context(), promotion.AffiliateAdminFilter{
		Search:   search,
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	httpx.Paginated(c, entries, total, page, pageSize)
}

// UpdateUserSettings applies optional code and rate edits for a positive
// user_id.
//
// JSON omission retains each setting; ClearRebateRate takes precedence over a
// supplied rate. The code update runs first, then a rate removal or
// replacement is attempted. These calls have separate persistence ownership,
// so a rate failure can return an error after the code is already saved.
// Success reports the requested user ID.
func (h *AffiliateHandler) UpdateUserSettings(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		httpx.BadRequest(c, "Invalid user_id")
		return
	}

	var req UpdateAffiliateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	// The handler supplies no combined transaction for these calls, so repository
	// completion of the code write can precede the rate operation.
	if req.AffCode != nil {
		if err := h.affiliateService.AdminUpdateUserAffCode(c.Request.Context(), userID, *req.AffCode); err != nil {
			httpx.ErrorFrom(c, err)
			return
		}
	}

	if req.ClearRebateRate {
		if err := h.affiliateService.AdminSetUserRebateRate(c.Request.Context(), userID, nil); err != nil {
			httpx.ErrorFrom(c, err)
			return
		}
	} else if req.AffRebateRatePercent != nil {
		if err := h.affiliateService.AdminSetUserRebateRate(c.Request.Context(), userID, req.AffRebateRatePercent); err != nil {
			httpx.ErrorFrom(c, err)
			return
		}
	}

	httpx.Success(c, gin.H{"user_id": userID})
}

// ClearUserSettings removes the user's rebate override and requests a
// generated invitation code.
//
// It rejects nonpositive user_id before writing, then clears the rate before
// resetting the code. These calls are separate persistent operations, so
// code-reset failure can return an error after the override has already been
// removed. Successful completion reports the requested user ID; existing
// referral relationships continue to reference that user's identity.
// DELETE /api/v1/admin/affiliates/users/:user_id
func (h *AffiliateHandler) ClearUserSettings(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		httpx.BadRequest(c, "Invalid user_id")
		return
	}
	// Send nil to the rate repository before generating code. Each service call
	// handles its own transaction participation, leaving no handler-owned
	// rollback across both operations.
	if err := h.affiliateService.AdminSetUserRebateRate(c.Request.Context(), userID, nil); err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	if _, err := h.affiliateService.AdminResetUserAffCode(c.Request.Context(), userID); err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	httpx.Success(c, gin.H{"user_id": userID})
}

// BatchSetRate replaces or removes one inviter percentage for the submitted
// account list.
//
// Clear takes precedence; otherwise a rate is required. The affected response
// is the submitted list length, including entries the service discards.
func (h *AffiliateHandler) BatchSetRate(c *gin.Context) {
	var req BatchSetRateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if len(req.UserIDs) == 0 {
		httpx.BadRequest(c, "user_ids cannot be empty")
		return
	}
	if !req.Clear && req.AffRebateRatePercent == nil {
		httpx.BadRequest(c, "aff_rebate_rate_percent is required unless clear=true")
		return
	}
	rate := req.AffRebateRatePercent
	if req.Clear {
		rate = nil
	}
	if err := h.affiliateService.AdminBatchSetUserRebateRate(c.Request.Context(), req.UserIDs, rate); err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	httpx.Success(c, gin.H{"affected": len(req.UserIDs)})
}

// LookupUsers returns recipient selector results for a nonempty q query.
//
// An empty query returns a nonnil empty list without invoking lookup. Search
// errors use the shared HTTP error mapper.
// GET /api/v1/admin/affiliates/users/lookup?q=
func (h *AffiliateHandler) LookupUsers(c *gin.Context) {
	keyword := c.Query("q")
	if keyword == "" {
		httpx.Success(c, []AffiliateUserSummary{})
		return
	}
	result, err := h.lookupUsers(c.Request.Context(), keyword)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, result)
}

// GetUserOverview returns the affiliate overview for a positive parsed
// user_id.
//
// It reports invalid IDs as bad requests and delegates effective-rate
// selection and available-quota observation to the service.
// GET /api/v1/admin/affiliates/users/:user_id/overview
func (h *AffiliateHandler) GetUserOverview(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		httpx.BadRequest(c, "Invalid user_id")
		return
	}
	overview, err := h.affiliateService.AdminGetUserOverview(c.Request.Context(), userID)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	httpx.Success(c, overview)
}

// ListInviteRecords returns paged inviter relationships using search,
// sorting, and inclusive time filters.
//
// It uses the calendar parsers and reports the normalized filter page and
// size in the response.
// GET /api/v1/admin/affiliates/invites
func (h *AffiliateHandler) ListInviteRecords(c *gin.Context) {
	page, pageSize := httpx.ParsePagination(c)
	filter := parseAffiliateRecordFilter(c, page, pageSize, h.calendar)
	items, total, err := h.affiliateService.AdminListInviteRecords(c.Request.Context(), filter)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	httpx.Paginated(c, items, total, filter.Page, filter.PageSize)
}

// ListRebateRecords returns paged source-order accruals using search,
// sorting, and inclusive time filters.
//
// It uses the calendar parsers and keeps price, charged money, and granted
// quota in their service fields.
// GET /api/v1/admin/affiliates/rebates
func (h *AffiliateHandler) ListRebateRecords(c *gin.Context) {
	page, pageSize := httpx.ParsePagination(c)
	filter := parseAffiliateRecordFilter(c, page, pageSize, h.calendar)
	items, total, err := h.affiliateService.AdminListRebateRecords(c.Request.Context(), filter)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	httpx.Paginated(c, items, total, filter.Page, filter.PageSize)
}

// ListTransferRecords returns paged quota-transfer receipts using search,
// sorting, and inclusive time filters.
//
// It uses the calendar parsers; old ledger receipts can expose missing
// post-transfer snapshots.
// GET /api/v1/admin/affiliates/transfers
func (h *AffiliateHandler) ListTransferRecords(c *gin.Context) {
	page, pageSize := httpx.ParsePagination(c)
	filter := parseAffiliateRecordFilter(c, page, pageSize, h.calendar)
	items, total, err := h.affiliateService.AdminListTransferRecords(c.Request.Context(), filter)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	httpx.Paginated(c, items, total, filter.Page, filter.PageSize)
}

// parseAffiliateRecordFilter builds record criteria from HTTP queries and
// supplied pagination.
//
// Sizes above one hundred are capped; only exact asc selects ascending order.
// Unsupported date text leaves the corresponding bound unset.
func parseAffiliateRecordFilter(c *gin.Context, page, pageSize int, calendar timezone.Calendar) promotion.AffiliateRecordFilter {
	filter := promotion.AffiliateRecordFilter{
		Search:   c.Query("search"),
		Page:     page,
		PageSize: pageSize,
		SortBy:   c.Query("sort_by"),
		SortDesc: c.Query("sort_order") != "asc",
	}
	if filter.PageSize > 100 {
		// The service applies this same audit-row ceiling.
		filter.PageSize = 100
	}
	userTZ := c.Query("timezone")
	if t := parseAffiliateRecordStartTime(c.Query("start_at"), userTZ, calendar); t != nil {
		filter.StartAt = t
	}
	if t := parseAffiliateRecordEndTime(c.Query("end_at"), userTZ, calendar); t != nil {
		filter.EndAt = t
	}
	return filter
}

// parseAffiliateRecordStartTime returns a trimmed RFC3339 instant or a date's
// midnight in the selected calendar location.
//
// Empty or unparseable text returns nil; a valid timestamp's offset takes
// precedence over userTZ.
func parseAffiliateRecordStartTime(raw string, userTZ string, calendar timezone.Calendar) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return &parsed
	}
	// Date-only input has no timestamp offset, so let the user location or
	// injected calendar establish its midnight instant.
	if parsed, err := calendar.ParseInUserLocation("2006-01-02", raw, userTZ); err == nil {
		return &parsed
	}
	// Leave the lower bound unset when both layouts fail, keeping the
	// administrator's remaining search and pagination filters available to the
	// record query.
	return nil
}

// parseAffiliateRecordEndTime returns the requested inclusive upper time
// bound.
//
// An RFC3339 value keeps its instant and offset, while date-only input uses
// the selected calendar location. Adding one calendar date before subtracting
// a nanosecond reaches that local day's final instant even when a
// daylight-saving transition changes its elapsed duration. Empty or
// unparseable input returns nil, so the record query receives no upper-time
// predicate.
func parseAffiliateRecordEndTime(raw string, userTZ string, calendar timezone.Calendar) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return &parsed
	}
	// A failed instant parse leaves date-only text to the selected calendar;
	// a valid supplied timestamp would already have returned with its offset.
	if parsed, err := calendar.ParseInUserLocation("2006-01-02", raw, userTZ); err == nil {
		// Advance to the next local date before subtracting a nanosecond; a fixed
		// daily duration would miss daylight-saving day-length changes.
		end := parsed.AddDate(0, 0, 1).Add(-time.Nanosecond)
		return &end
	}
	// Leave the upper bound unset when both layouts fail, keeping the
	// administrator's remaining search and pagination filters available to the
	// record query.
	return nil
}
