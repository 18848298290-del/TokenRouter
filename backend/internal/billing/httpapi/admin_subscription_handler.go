package httpapi

import (
	"context"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	idempotencyhttp "github.com/TokenFlux/TokenRouter/internal/idempotency/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// AdminSubscriptionHandler handles administrator subscription operations.
type AdminSubscriptionHandler struct {
	idempotencyhttp.Executor

	subscriptionService *billing.SubscriptionService
}

// AssignSubscriptionRequest supplies a subscription grant for one user.
type AssignSubscriptionRequest struct {
	UserID       int64  `json:"user_id" binding:"required"`
	PlanID       int64  `json:"plan_id" binding:"required"`
	ValidityDays int    `json:"validity_days" binding:"omitempty,max=36500"` // max 100 years
	Notes        string `json:"notes"`
}

// BulkAssignSubscriptionRequest supplies a subscription grant for multiple users.
type BulkAssignSubscriptionRequest struct {
	UserIDs      []int64 `json:"user_ids" binding:"required,min=1"`
	PlanID       int64   `json:"plan_id" binding:"required"`
	ValidityDays int     `json:"validity_days" binding:"omitempty,max=36500"` // max 100 years
	Notes        string  `json:"notes"`
}

// AdjustSubscriptionRequest sets the target subscription validity period.
type AdjustSubscriptionRequest struct {
	Days int `json:"days" binding:"required,min=1,max=36500"` // Target days from now, or from the pending subscription's start.
}

// ResetSubscriptionQuotaRequest selects the usage windows to reset.
type ResetSubscriptionQuotaRequest struct {
	Daily   bool `json:"daily"`
	Weekly  bool `json:"weekly"`
	Monthly bool `json:"monthly"`
}

// toResponsePagination converts pagination results for HTTP responses.
func toResponsePagination(p *pagination.PaginationResult) *response.PaginationResult {
	if p == nil {
		return nil
	}
	return &response.PaginationResult{
		Total:    p.Total,
		Page:     p.Page,
		PageSize: p.PageSize,
		Pages:    p.Pages,
	}
}

// NewAdminSubscriptionHandler constructs the administrator subscription handler.
func NewAdminSubscriptionHandler(subscriptionService *billing.SubscriptionService) *AdminSubscriptionHandler {
	return &AdminSubscriptionHandler{
		subscriptionService: subscriptionService,
	}
}

// List returns a filtered page of subscriptions.
// GET /api/v1/admin/subscriptions
func (h *AdminSubscriptionHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)

	// Malformed optional IDs are ignored.
	var userID, planID *int64
	if userIDStr := c.Query("user_id"); userIDStr != "" {
		if id, err := strconv.ParseInt(userIDStr, 10, 64); err == nil {
			userID = &id
		}
	}
	if planIDStr := c.Query("plan_id"); planIDStr != "" {
		if id, err := strconv.ParseInt(planIDStr, 10, 64); err == nil {
			planID = &id
		}
	}
	status := c.Query("status")
	platform := c.Query("platform")

	sortBy := c.DefaultQuery("sort_by", "created_at")
	sortOrder := c.DefaultQuery("sort_order", "desc")

	subscriptions, pagination, err := h.subscriptionService.List(c.Request.Context(), page, pageSize, userID, planID, status, platform, sortBy, sortOrder)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]AdminUserSubscription, 0, len(subscriptions))
	for i := range subscriptions {
		out = append(out, *UserSubscriptionFromServiceAdmin(&subscriptions[i]))
	}
	response.PaginatedWithResult(c, out, toResponsePagination(pagination))
}

// GetByID returns a subscription by ID.
// GET /api/v1/admin/subscriptions/:id
func (h *AdminSubscriptionHandler) GetByID(c *gin.Context) {
	subscriptionID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid subscription ID")
		return
	}

	subscription, err := h.subscriptionService.GetByID(c.Request.Context(), subscriptionID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, UserSubscriptionFromServiceAdmin(subscription))
}

// GetProgress returns quota usage progress for a subscription.
// GET /api/v1/admin/subscriptions/:id/progress
func (h *AdminSubscriptionHandler) GetProgress(c *gin.Context) {
	subscriptionID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid subscription ID")
		return
	}

	progress, err := h.subscriptionService.GetSubscriptionProgress(c.Request.Context(), subscriptionID)
	if err != nil {
		response.NotFound(c, "Subscription not found")
		return
	}

	response.Success(c, progress)
}

// Assign grants a subscription to a user.
// POST /api/v1/admin/subscriptions/assign
func (h *AdminSubscriptionHandler) Assign(c *gin.Context) {
	var req AssignSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	adminID := adminID(c)

	subscription, err := h.subscriptionService.AssignSubscription(c.Request.Context(), &billing.AssignSubscriptionInput{
		UserID:       req.UserID,
		PlanID:       req.PlanID,
		ValidityDays: req.ValidityDays,
		AssignedBy:   adminID,
		Notes:        req.Notes,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, UserSubscriptionFromServiceAdmin(subscription))
}

// BulkAssign grants subscriptions to multiple users.
// POST /api/v1/admin/subscriptions/bulk-assign
func (h *AdminSubscriptionHandler) BulkAssign(c *gin.Context) {
	var req BulkAssignSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	adminID := adminID(c)

	result, err := h.subscriptionService.BulkAssignSubscription(c.Request.Context(), &billing.BulkAssignSubscriptionInput{
		UserIDs:      req.UserIDs,
		PlanID:       req.PlanID,
		ValidityDays: req.ValidityDays,
		AssignedBy:   adminID,
		Notes:        req.Notes,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, BulkAssignResultFromService(result))
}

// Extend sets the subscription's target validity period.
// POST /api/v1/admin/subscriptions/:id/extend
func (h *AdminSubscriptionHandler) Extend(c *gin.Context) {
	subscriptionID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid subscription ID")
		return
	}

	var req AdjustSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	idempotencyPayload := struct {
		SubscriptionID int64                     `json:"subscription_id"`
		Body           AdjustSubscriptionRequest `json:"body"`
	}{
		SubscriptionID: subscriptionID,
		Body:           req,
	}
	h.ExecuteAdminIdempotentJSON(c, "admin.subscriptions.extend", idempotencyPayload, h.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		subscription, execErr := h.subscriptionService.SetSubscriptionValidityDays(ctx, subscriptionID, req.Days)
		if execErr != nil {
			return nil, execErr
		}
		return UserSubscriptionFromServiceAdmin(subscription), nil
	})
}

// ResetQuota resets the selected daily, weekly, or monthly usage windows.
// POST /api/v1/admin/subscriptions/:id/reset-quota
func (h *AdminSubscriptionHandler) ResetQuota(c *gin.Context) {
	subscriptionID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid subscription ID")
		return
	}
	var req ResetSubscriptionQuotaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if !req.Daily && !req.Weekly && !req.Monthly {
		response.BadRequest(c, "At least one of 'daily', 'weekly', or 'monthly' must be true")
		return
	}
	sub, err := h.subscriptionService.AdminResetQuota(c.Request.Context(), subscriptionID, req.Daily, req.Weekly, req.Monthly)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, UserSubscriptionFromServiceAdmin(sub))
}

// Revoke revokes a subscription.
// POST /api/v1/admin/subscriptions/:id/revoke
// DELETE /api/v1/admin/subscriptions/:id is also supported for compatibility.
func (h *AdminSubscriptionHandler) Revoke(c *gin.Context) {
	subscriptionID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid subscription ID")
		return
	}

	err = h.subscriptionService.RevokeSubscription(c.Request.Context(), subscriptionID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Subscription revoked successfully"})
}

// Restore restores a revoked subscription.
// POST /api/v1/admin/subscriptions/:id/restore
func (h *AdminSubscriptionHandler) Restore(c *gin.Context) {
	subscriptionID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid subscription ID")
		return
	}

	subscription, err := h.subscriptionService.RestoreSubscription(c.Request.Context(), subscriptionID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, UserSubscriptionFromServiceAdmin(subscription))
}

// ListByPlan handles listing subscriptions for a plan.
func (h *AdminSubscriptionHandler) ListByPlan(c *gin.Context) {
	planID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid plan ID")
		return
	}

	page, pageSize := response.ParsePagination(c)

	subscriptions, pagination, err := h.subscriptionService.ListPlanSubscriptions(c.Request.Context(), planID, page, pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]AdminUserSubscription, 0, len(subscriptions))
	for i := range subscriptions {
		out = append(out, *UserSubscriptionFromServiceAdmin(&subscriptions[i]))
	}
	response.PaginatedWithResult(c, out, toResponsePagination(pagination))
}

// ListByUser returns a user's subscriptions.
// GET /api/v1/admin/users/:id/subscriptions
func (h *AdminSubscriptionHandler) ListByUser(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}

	subscriptions, err := h.subscriptionService.ListUserSubscriptions(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]AdminUserSubscription, 0, len(subscriptions))
	for i := range subscriptions {
		out = append(out, *UserSubscriptionFromServiceAdmin(&subscriptions[i]))
	}
	response.Success(c, out)
}

// adminID returns the authenticated administrator's audit identity, or zero when absent.
func adminID(c *gin.Context) int64 {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		return 0
	}
	return subject.UserID
}
