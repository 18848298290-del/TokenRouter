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

// admin_subscription_handler.go handles administrator subscription assignment,
// queries, and quota or validity maintenance.
// Administrator grant operations use the subscription service for issued
// limits, renewal chains and quota maintenance. The handlers bind HTTP inputs
// and assignment identity, map service results to administrator DTOs and leave
// authentication/auditing with route ownership. Optional list IDs tolerate
// malformed text, while path IDs fail parsing before service calls.
//
// The extend route sets a target duration through the command executor using
// path and body identity, so replay handling remains tied to that request.
// Other service maintenance rules retain their own transaction ownership.
// Progress failures map to the existing not-found response, while ordinary
// operations use shared error mapping; request validation should not be
// mistaken for a uniform positivity check on every ID.

// AdminSubscriptionHandler exposes administrator grant queries and
// subscription maintenance through the service.
type AdminSubscriptionHandler struct {
	// Executor uses the app-bound coordinator for Extend replay handling. Bind
	// before requests begin; an unbound fixture executes its callback directly
	// using the executor's default TTL.
	idempotencyhttp.Executor // administrator command replay execution

	subscriptionService *billing.SubscriptionService // grant query and maintenance service
}

// AssignSubscriptionRequest supplies one recipient and plan with optional
// validity and notes.
type AssignSubscriptionRequest struct {
	UserID int64 `json:"user_id" binding:"required"` // requested grant recipient identifier
	PlanID int64 `json:"plan_id" binding:"required"` // requested grant plan identifier
	// ValidityDays is a positive override when supplied. Nonpositive values reach
	// the service's plan/default selection, and JSON binding limits only the
	// maximum configured duration.
	ValidityDays int    `json:"validity_days" binding:"omitempty,max=36500"` // optional positive duration override
	Notes        string `json:"notes"`                                       // administrator grant reason text
}

// BulkAssignSubscriptionRequest supplies a recipient sequence with common
// plan, validity and notes.
type BulkAssignSubscriptionRequest struct {
	// UserIDs keeps supplied order and duplicates. The service processes each
	// entry independently, so repeated IDs can request additional queued grants
	// for the same recipient.
	UserIDs []int64 `json:"user_ids" binding:"required,min=1"` // requested grant recipient sequence
	PlanID  int64   `json:"plan_id" binding:"required"`        // common grant plan identifier
	// ValidityDays is a positive override when supplied. Nonpositive values reach
	// the service's plan/default selection, and JSON binding limits only the
	// maximum configured duration.
	ValidityDays int    `json:"validity_days" binding:"omitempty,max=36500"` // optional positive duration override
	Notes        string `json:"notes"`                                       // common administrator grant reason
}

// AdjustSubscriptionRequest supplies the target validity duration for the
// selected grant.
type AdjustSubscriptionRequest struct {
	// Days selects duration from current time or a pending subscription's start
	// according to the service anchor. Binding requires one through
	// thirty-six-thousand-five-hundred days.
	Days int `json:"days" binding:"required,min=1,max=36500"` // target validity duration days
}

// ResetSubscriptionQuotaRequest selects independent day, week and month usage
// resets.
type ResetSubscriptionQuotaRequest struct {
	// Daily, Weekly and Monthly select independently resettable usage windows.
	// ResetQuota requires at least one flag, then delegates the selected
	// combination to the subscription service in one operation.
	Daily   bool `json:"daily"`   // reset daily usage window
	Weekly  bool `json:"weekly"`  // reset weekly usage window
	Monthly bool `json:"monthly"` // reset monthly usage window
}

// toResponsePagination copies page totals and bounds into the HTTP response
// type. Nil metadata returns nil; callers use this companion with the mapped
// grant rows, and successful copies retain the supplied scalar values.
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

// NewAdminSubscriptionHandler binds the supplied service and leaves its
// executor initially unbound. App construction binds the coordinator before
// serving; standalone fixtures use direct callback execution, and route
// ownership supplies administrator authorization.
func NewAdminSubscriptionHandler(subscriptionService *billing.SubscriptionService) *AdminSubscriptionHandler {
	return &AdminSubscriptionHandler{
		subscriptionService: subscriptionService,
	}
}

// List returns administrator grant views with pagination, optional IDs, status
// and sorting inputs. Malformed optional ID text is ignored; sorting defaults
// to created_at descending, platform reaches a concrete service that ignores
// it, and service failures use shared error mapping.
// GET /api/v1/admin/subscriptions
func (h *AdminSubscriptionHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)

	// An optional ID is used only after successful signed parsing; malformed
	// query text drops that filter so the remaining list criteria can still be
	// applied.
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

// GetByID parses a signed decimal subscription path ID and returns the
// administrator grant mapping. Malformed text yields bad request, while
// zero/negative values reach service interpretation and service failures use
// shared error mapping.
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

// GetProgress parses the signed target ID and returns service quota progress.
// Malformed text yields bad request; every progress-service error is converted
// to the existing not-found response instead of shared error classification.
// GET /api/v1/admin/subscriptions/:id/progress
func (h *AdminSubscriptionHandler) GetProgress(c *gin.Context) {
	subscriptionID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid subscription ID")
		return
	}

	progress, err := h.subscriptionService.GetSubscriptionProgress(c.Request.Context(), subscriptionID)
	if err != nil {
		// The progress endpoint maps every service failure to not-found here,
		// retaining its existing response shape rather than exposing that error's
		// shared classification.
		response.NotFound(c, "Subscription not found")
		return
	}

	response.Success(c, progress)
}

// Assign binds one recipient/plan grant and records the authentication subject
// ID as assigner metadata. Invalid JSON yields bad request; the route owner
// supplies authorization, while service errors use shared mapping and success
// returns the administrator DTO.
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

// BulkAssign binds the recipient sequence and common grant terms, then returns
// the service's per-user results. Invalid JSON yields bad request; individual
// service failures can remain in a successful batch response, and assigner
// metadata comes from the authentication subject.
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

// Extend binds a positive target-day count and invokes
// SetSubscriptionValidityDays through the administrator command executor. It
// sets duration from the service's current/pending anchor rather than adding
// that number of days; payload identity includes the path ID and bound body
// for replay handling.
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
		// SubscriptionID and Body define the command payload together, so identical
		// bodies for different grant targets remain distinct replay identities. The
		// command executor owns claim/replay handling.
		SubscriptionID int64                     `json:"subscription_id"` // requested target grant identifier
		Body           AdjustSubscriptionRequest `json:"body"`            // bound target duration request
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

// ResetQuota binds window selection and requires at least one selected flag
// before service mutation. Malformed ID/JSON yields bad request, service
// failures use shared mapping and the updated grant uses the administrator
// DTO.
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

// Revoke parses a signed target ID and delegates administrator revocation,
// returning the existing success message after service completion. The route
// supports both POST and DELETE spellings; malformed IDs and service failures
// use their existing bad-request/shared-error paths.
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

// Restore parses a signed target ID and delegates revoked-grant restoration
// before mapping the administrator result. Malformed text yields bad request,
// while eligibility/chain/persistence errors retain the service's shared HTTP
// error classification.
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

// ListByPlan parses a signed plan path ID and returns its administrator grant
// page. Invalid numeric text yields bad request; service failures use shared
// error mapping, and page metadata is converted to the HTTP response
// structure.
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

// ListByUser parses a signed user path ID and returns that user's
// administrator grant views. Invalid text yields bad request; service failures
// use shared error mapping, and the service supplies status/window
// normalization before DTO conversion.
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

// adminID returns the authentication subject's user ID or zero when absent. It
// supplies assignment metadata only; administrator authorization is installed
// by the route owner rather than checked by this helper.
func adminID(c *gin.Context) int64 {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		return 0
	}
	return subject.UserID
}
