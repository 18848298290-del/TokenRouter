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

// AffiliateHandler manages custom invitation codes and rebate rates,
// including user lookup, paginated records, and batch rate updates.
type AffiliateHandler struct {
	affiliateService *promotion.AffiliateService
	lookupUsers      func(context.Context, string) ([]AffiliateUserSummary, error)
	calendar         timezone.Calendar
}

// UpdateAffiliateUserRequest updates a user's affiliate settings.
// PUT /api/v1/admin/affiliates/users/:user_id
//
// Both settings are optional and are applied independently.
type UpdateAffiliateUserRequest struct {
	AffCode              *string  `json:"aff_code"`
	AffRebateRatePercent *float64 `json:"aff_rebate_rate_percent"`
	// ClearRebateRate writes NULL to remove the override, distinguishing a clear from an omitted field.
	ClearRebateRate bool `json:"clear_rebate_rate"`
}

// BatchSetRateRequest sets or clears one rate for multiple users.
//
// When clear is true, aff_rebate_rate_percent is ignored and overrides are removed.
// Otherwise aff_rebate_rate_percent is required and applies to every supplied user ID.
// The clear flag distinguishes removal from an omitted or null JSON rate.
//
// POST /api/v1/admin/affiliates/users/batch-rate
type BatchSetRateRequest struct {
	UserIDs              []int64  `json:"user_ids" binding:"required"`
	AffRebateRatePercent *float64 `json:"aff_rebate_rate_percent"`
	Clear                bool     `json:"clear"`
}

// AffiliateUserSummary contains the user fields needed by the lookup selector.
type AffiliateUserSummary struct {
	ID       int64  `json:"id"`
	Email    string `json:"email"`
	Username string `json:"username"`
}

// NewAffiliateHandler builds the admin affiliate handler.
func NewAffiliateHandler(affiliate *promotion.AffiliateService, lookup func(context.Context, string) ([]AffiliateUserSummary, error), calendar timezone.Calendar) *AffiliateHandler {
	return &AffiliateHandler{affiliateService: affiliate, lookupUsers: lookup, calendar: calendar}
}

// ListUsers returns a page of users with custom affiliate settings.
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

// ClearUserSettings clears the user's rebate override and generates
// a replacement invitation code.
//
// The writes run sequentially; a failure may leave the rate cleared before the code is reset.
// DELETE /api/v1/admin/affiliates/users/:user_id
func (h *AffiliateHandler) ClearUserSettings(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		httpx.BadRequest(c, "Invalid user_id")
		return
	}
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

// LookupUsers searches by email or username for the custom-user dialog.
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

// GetUserOverview returns a user's affiliate summary.
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

// ListInviteRecords returns inviter and invitee relationships across the site.
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

// ListRebateRecords returns rebate records with their source-order details.
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

// ListTransferRecords returns records of affiliate quota transfers into balance.
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

func parseAffiliateRecordFilter(c *gin.Context, page, pageSize int, calendar timezone.Calendar) promotion.AffiliateRecordFilter {
	filter := promotion.AffiliateRecordFilter{
		Search:   c.Query("search"),
		Page:     page,
		PageSize: pageSize,
		SortBy:   c.Query("sort_by"),
		SortDesc: c.Query("sort_order") != "asc",
	}
	if filter.PageSize > 100 {
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

func parseAffiliateRecordStartTime(raw string, userTZ string, calendar timezone.Calendar) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return &parsed
	}
	if parsed, err := calendar.ParseInUserLocation("2006-01-02", raw, userTZ); err == nil {
		return &parsed
	}
	return nil
}

func parseAffiliateRecordEndTime(raw string, userTZ string, calendar timezone.Calendar) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return &parsed
	}
	if parsed, err := calendar.ParseInUserLocation("2006-01-02", raw, userTZ); err == nil {
		end := parsed.AddDate(0, 0, 1).Add(-time.Nanosecond)
		return &end
	}
	return nil
}
