package httpapi

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	idempotencyhttp "github.com/TokenFlux/TokenRouter/internal/idempotency/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// RedeemAdministrator provides the redemption management operations used by HTTP handlers.
type RedeemAdministrator interface {
	ListRedeemCodes(ctx context.Context, page, pageSize int, codeType, status, search string, sortBy, sortOrder string) ([]billing.RedeemCode, int64, error)
	GetRedeemCode(ctx context.Context, id int64) (*billing.RedeemCode, error)
	GenerateRedeemCodes(ctx context.Context, input *billing.GenerateRedeemCodesInput) ([]billing.RedeemCode, error)
	UpdateRedeemCode(ctx context.Context, id int64, input *billing.UpdateRedeemCodeInput) (*billing.RedeemCode, error)
	DeleteRedeemCode(ctx context.Context, id int64) error
	BatchDeleteRedeemCodes(ctx context.Context, ids []int64) (int64, error)
	ExpireRedeemCode(ctx context.Context, id int64) (*billing.RedeemCode, error)
}

// AdminRedeemHandler handles admin redeem code management.
type AdminRedeemHandler struct {
	idempotencyhttp.Executor

	adminService  RedeemAdministrator
	redeemService *billing.RedeemService
}

// GenerateRedeemCodesRequest supplies redemption code generation parameters.
type GenerateRedeemCodesRequest struct {
	RequiresPayment bool    `json:"requires_payment"` // False allows users with no payment history.
	Code            string  `json:"code" binding:"omitempty,max=32"`
	Count           int     `json:"count" binding:"required,min=1,max=100"`
	Type            string  `json:"type" binding:"required,oneof=balance concurrency subscription invitation"`
	Value           float64 `json:"value"`
	MaxUses         *int    `json:"max_uses" binding:"omitempty,min=0"`
	ExpiresAt       *int64  `json:"expires_at" binding:"omitempty,min=0"`
	ExpiresInDays   *int    `json:"expires_in_days" binding:"omitempty,min=1,max=3650"`
	PlanID          *int64  `json:"plan_id"` // Required for subscription codes.
}

// UpdateRedeemCodeRequest supplies optional redemption code changes.
type UpdateRedeemCodeRequest struct {
	RequiresPayment *bool    `json:"requires_payment"` // Omission keeps the eligibility requirement unchanged.
	Value           *float64 `json:"value"`
	MaxUses         *int     `json:"max_uses" binding:"omitempty,min=0"`
	ExpiresAt       *int64   `json:"expires_at" binding:"omitempty,min=0"`
	PlanID          *int64   `json:"plan_id"` // Applies to subscription codes.
}

// CreateAndRedeemCodeRequest represents creating a fixed code and redeeming it for a target user.
// An omitted Type defaults to balance.
type CreateAndRedeemCodeRequest struct {
	Code          string  `json:"code" binding:"required,min=3,max=128"`
	Type          string  `json:"type" binding:"omitempty,oneof=balance concurrency subscription invitation"`
	Value         float64 `json:"value" binding:"required"`
	UserID        int64   `json:"user_id" binding:"required,gt=0"`
	PlanID        *int64  `json:"plan_id"` // Required for subscription codes.
	Notes         string  `json:"notes"`
	ExpiresAt     *int64  `json:"expires_at" binding:"omitempty,min=0"`
	ExpiresInDays *int    `json:"expires_in_days" binding:"omitempty,min=1,max=3650"`
}

// NewAdminRedeemHandler constructs the administrator redemption handler.
func NewAdminRedeemHandler(adminService RedeemAdministrator, redeemService *billing.RedeemService) *AdminRedeemHandler {
	return &AdminRedeemHandler{
		adminService:  adminService,
		redeemService: redeemService,
	}
}

// ResolveRedeemCodeExpiresAt accepts either Unix seconds or days from now and returns UTC. Zero or omitted expiry
// returns nil.
func ResolveRedeemCodeExpiresAt(expiresAt *int64, expiresInDays *int) (*time.Time, error) {
	if expiresAt != nil && *expiresAt < 0 {
		return nil, apperror.BadRequest("REDEEM_CODE_EXPIRES_AT_INVALID", "expires_at must be greater than or equal to zero")
	}
	if expiresAt != nil && expiresInDays != nil {
		return nil, apperror.BadRequest("REDEEM_CODE_EXPIRY_CONFLICT", "expires_at and expires_in_days cannot both be set")
	}

	now := time.Now().UTC()
	if expiresInDays != nil {
		if *expiresInDays <= 0 || *expiresInDays > 3650 {
			return nil, apperror.BadRequest("REDEEM_CODE_EXPIRES_IN_DAYS_INVALID", "expires_in_days must be between 1 and 3650")
		}
		expires := now.AddDate(0, 0, *expiresInDays)
		return &expires, nil
	}
	if expiresAt == nil || *expiresAt == 0 {
		return nil, nil
	}

	expires := time.Unix(*expiresAt, 0).UTC()
	if !expires.After(now) {
		return nil, apperror.BadRequest("REDEEM_CODE_EXPIRES_AT_INVALID", "expires_at must be in the future")
	}
	return &expires, nil
}

// List handles listing all redeem codes with pagination
// GET /api/v1/admin/redeem-codes
func (h *AdminRedeemHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	codeType := c.Query("type")
	status := c.Query("status")
	search := normalizeRedeemSearch(c.Query("search"))
	sortBy := c.DefaultQuery("sort_by", "id")
	sortOrder := c.DefaultQuery("sort_order", "desc")

	codes, total, err := h.adminService.ListRedeemCodes(c.Request.Context(), page, pageSize, codeType, status, search, sortBy, sortOrder)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]AdminRedeemCode, 0, len(codes))
	for i := range codes {
		out = append(out, *RedeemCodeFromServiceAdmin(&codes[i]))
	}
	response.Paginated(c, out, total, page, pageSize)
}

// normalizeRedeemSearch trims surrounding whitespace and keeps at most 100 Unicode code points.
func normalizeRedeemSearch(search string) string {
	search = strings.TrimSpace(search)
	count := 0
	for index := range search {
		if count == 100 {
			return search[:index]
		}
		count++
	}
	return search
}

// GetByID handles getting a redeem code by ID
// GET /api/v1/admin/redeem-codes/:id
func (h *AdminRedeemHandler) GetByID(c *gin.Context) {
	codeID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid redeem code ID")
		return
	}

	code, err := h.adminService.GetRedeemCode(c.Request.Context(), codeID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, RedeemCodeFromServiceAdmin(code))
}

// Update applies optional changes to a redemption code.
// PUT /api/v1/admin/redeem-codes/:id
func (h *AdminRedeemHandler) Update(c *gin.Context) {
	codeID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid redeem code ID")
		return
	}

	var req UpdateRedeemCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	input := &billing.UpdateRedeemCodeInput{
		RequiresPayment: req.RequiresPayment,
		Value:           req.Value,
		MaxUses:         req.MaxUses,
		PlanID:          req.PlanID,
		ExpiresAtSet:    req.ExpiresAt != nil,
	}
	if req.ExpiresAt != nil && *req.ExpiresAt > 0 {
		// Zero clears expiry; omission leaves it unchanged.
		t := time.Unix(*req.ExpiresAt, 0)
		input.ExpiresAt = &t
	}

	code, err := h.adminService.UpdateRedeemCode(c.Request.Context(), codeID, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, RedeemCodeFromServiceAdmin(code))
}

// Generate handles generating new redeem codes
// POST /api/v1/admin/redeem-codes/generate
func (h *AdminRedeemHandler) Generate(c *gin.Context) {
	var req GenerateRedeemCodesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	expiresAt, err := ResolveRedeemCodeExpiresAt(req.ExpiresAt, req.ExpiresInDays)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	h.ExecuteAdminIdempotentJSON(c, "admin.redeem_codes.generate", req, h.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		codes, execErr := h.adminService.GenerateRedeemCodes(ctx, &billing.GenerateRedeemCodesInput{
			RequiresPayment: req.RequiresPayment,
			Code:            req.Code,
			Count:           req.Count,
			Type:            req.Type,
			Value:           req.Value,
			MaxUses:         req.MaxUses,
			ExpiresAt:       expiresAt,
			PlanID:          req.PlanID,
		})
		if execErr != nil {
			return nil, execErr
		}

		out := make([]AdminRedeemCode, 0, len(codes))
		for i := range codes {
			out = append(out, *RedeemCodeFromServiceAdmin(&codes[i]))
		}
		return out, nil
	})
}

// CreateAndRedeem creates a fixed redeem code and redeems it for a target user in one step.
// POST /api/v1/admin/redeem-codes/create-and-redeem
func (h *AdminRedeemHandler) CreateAndRedeem(c *gin.Context) {
	if h.redeemService == nil {
		response.InternalError(c, "redeem service not configured")
		return
	}

	var req CreateAndRedeemCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	// Older callers, including Sub2ApiPay, omit type for balance top-ups.
	// Default it before validation to keep those requests valid.
	if req.Type == "" {
		req.Type = "balance"
	}

	if req.Type == "subscription" {
		if req.PlanID == nil || *req.PlanID <= 0 {
			response.BadRequest(c, "plan_id is required for subscription type")
			return
		}
	}
	expiresAt, err := ResolveRedeemCodeExpiresAt(req.ExpiresAt, req.ExpiresInDays)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	h.ExecuteAdminIdempotentJSON(c, "admin.redeem_codes.create_and_redeem", req, h.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		existing, err := h.redeemService.GetByCode(ctx, req.Code)
		if err == nil {
			return h.resolveCreateAndRedeemExisting(ctx, existing, req.UserID)
		}
		if !errors.Is(err, billing.ErrRedeemCodeNotFound) {
			return nil, err
		}

		createErr := h.redeemService.CreateCode(ctx, &billing.RedeemCode{
			Code:      req.Code,
			Type:      req.Type,
			Value:     req.Value,
			Status:    billing.StatusUnused,
			MaxUses:   1,
			Notes:     req.Notes,
			PlanID:    req.PlanID,
			ExpiresAt: expiresAt,
		})
		if createErr != nil {
			// A concurrent creator may have won the unique-code race. Re-read and check used_by for idempotent success.
			existingAfterCreateErr, getErr := h.redeemService.GetByCode(ctx, req.Code)
			if getErr == nil {
				return h.resolveCreateAndRedeemExisting(ctx, existingAfterCreateErr, req.UserID)
			}
			return nil, createErr
		}

		redeemed, redeemErr := h.redeemService.Redeem(ctx, req.UserID, req.Code)
		if redeemErr != nil {
			return nil, redeemErr
		}
		return gin.H{"redeem_code": RedeemCodeFromServiceAdmin(redeemed)}, nil
	})
}

func (h *AdminRedeemHandler) resolveCreateAndRedeemExisting(ctx context.Context, existing *billing.RedeemCode, userID int64) (any, error) {
	if existing == nil {
		return nil, apperror.Conflict("REDEEM_CODE_CONFLICT", "redeem code conflict")
	}

	if existing.UsedBy != nil && *existing.UsedBy == userID {
		return gin.H{"redeem_code": RedeemCodeFromServiceAdmin(existing)}, nil
	}

	// Resume redemption when an earlier request created the code but stopped before redeeming it.
	if existing.IsExpired() {
		return nil, billing.ErrRedeemCodeExpired
	}
	if existing.CanUse() {
		redeemed, err := h.redeemService.Redeem(ctx, userID, existing.Code)
		if err == nil {
			return gin.H{"redeem_code": RedeemCodeFromServiceAdmin(redeemed)}, nil
		}
		if !errors.Is(err, billing.ErrRedeemCodeUsed) &&
			!errors.Is(err, billing.ErrRedeemCodeMaxUsed) &&
			!errors.Is(err, billing.ErrRedeemCodeAlreadyUsed) {
			return nil, err
		}
		if latest, getErr := h.redeemService.GetByCode(ctx, existing.Code); getErr == nil && latest != nil {
			if latest.UsedBy != nil && *latest.UsedBy == userID {
				return gin.H{"redeem_code": RedeemCodeFromServiceAdmin(latest)}, nil
			}
		}
	}

	return nil, apperror.Conflict("REDEEM_CODE_CONFLICT", "redeem code already used by another user")
}

// Delete handles deleting a redeem code
// DELETE /api/v1/admin/redeem-codes/:id
func (h *AdminRedeemHandler) Delete(c *gin.Context) {
	codeID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid redeem code ID")
		return
	}

	err = h.adminService.DeleteRedeemCode(c.Request.Context(), codeID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Redeem code deleted successfully"})
}

// BatchDelete handles batch deleting redeem codes
// POST /api/v1/admin/redeem-codes/batch-delete
func (h *AdminRedeemHandler) BatchDelete(c *gin.Context) {
	var req struct {
		IDs []int64 `json:"ids" binding:"required,min=1"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	deleted, err := h.adminService.BatchDeleteRedeemCodes(c.Request.Context(), req.IDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{
		"deleted": deleted,
		"message": "Redeem codes deleted successfully",
	})
}

// BatchUpdate applies batch field changes through the redemption service.
// POST /api/v1/admin/redeem-codes/batch-update
func (h *AdminRedeemHandler) BatchUpdate(c *gin.Context) {
	if h.redeemService == nil {
		response.InternalError(c, "redeem service not configured")
		return
	}

	var req BatchUpdateRedeemCodesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	result, err := h.redeemService.BatchUpdate(c.Request.Context(), &billing.RedeemCodeBatchUpdateInput{
		IDs:    req.IDs,
		Fields: RedeemBatchUpdateFieldsFromDTO(req.Fields),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{
		"updated": result.Updated,
		"message": "Redeem codes updated successfully",
	})
}

func RedeemBatchUpdateFieldsFromDTO(in BatchUpdateRedeemCodeFields) billing.RedeemCodeBatchUpdateFields {
	out := billing.RedeemCodeBatchUpdateFields{
		Status:  in.Status,
		Notes:   in.Notes,
		Type:    in.Type,
		Value:   in.Value,
		MaxUses: in.MaxUses,
		PlanID:  in.PlanID,
	}
	if in.ExpiresAt.Set {
		out.ExpiresAt = billing.NullableTimeUpdate{Set: true, Value: in.ExpiresAt.Value}
	}
	return out
}

// Expire handles expiring a redeem code
// POST /api/v1/admin/redeem-codes/:id/expire
func (h *AdminRedeemHandler) Expire(c *gin.Context) {
	codeID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid redeem code ID")
		return
	}

	code, err := h.adminService.ExpireRedeemCode(c.Request.Context(), codeID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, RedeemCodeFromServiceAdmin(code))
}

// Export handles exporting redeem codes to CSV
// GET /api/v1/admin/redeem-codes/export
func (h *AdminRedeemHandler) Export(c *gin.Context) {
	codeType := c.Query("type")
	status := c.Query("status")
	search := normalizeRedeemSearch(c.Query("search"))
	sortBy := c.DefaultQuery("sort_by", "id")
	sortOrder := c.DefaultQuery("sort_order", "desc")

	// Export at most 10000 matching codes using the first page.
	codes, _, err := h.adminService.ListRedeemCodes(c.Request.Context(), 1, 10000, codeType, status, search, sortBy, sortOrder)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)

	if err := writer.Write([]string{
		"id",
		"code",
		"type",
		"value",
		"status",
		"max_uses",
		"used_count",
		"expires_at",
		"last_used_by",
		"last_used_by_email",
		"last_used_at",
		"created_at",
	}); err != nil {
		response.InternalError(c, "Failed to export redeem codes: "+err.Error())
		return
	}

	for _, code := range codes {
		usedBy := ""
		if code.UsedBy != nil {
			usedBy = fmt.Sprintf("%d", *code.UsedBy)
		}
		usedByEmail := ""
		if code.User != nil {
			usedByEmail = code.User.Email
		}
		usedAt := ""
		if code.UsedAt != nil {
			usedAt = code.UsedAt.Format("2006-01-02 15:04:05")
		}
		expiresAt := ""
		if code.ExpiresAt != nil {
			expiresAt = code.ExpiresAt.Format("2006-01-02 15:04:05")
		}
		if err := writer.Write([]string{
			fmt.Sprintf("%d", code.ID),
			code.Code,
			code.Type,
			fmt.Sprintf("%.2f", code.Value),
			code.Status,
			fmt.Sprintf("%d", code.MaxUses),
			fmt.Sprintf("%d", code.UsedCount),
			expiresAt,
			usedBy,
			usedByEmail,
			usedAt,
			code.CreatedAt.Format("2006-01-02 15:04:05"),
		}); err != nil {
			response.InternalError(c, "Failed to export redeem codes: "+err.Error())
			return
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		response.InternalError(c, "Failed to export redeem codes: "+err.Error())
		return
	}

	c.Header("Content-Type", "text/csv")
	c.Header("Content-Disposition", "attachment; filename=redeem_codes.csv")
	c.Data(200, "text/csv", buf.Bytes())
}
