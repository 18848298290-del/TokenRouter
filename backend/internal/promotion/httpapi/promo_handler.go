package httpapi

import (
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// PromoHandler handles admin promo code management.
type PromoHandler struct {
	promoService *promotion.PromoService
}

// CreatePromoCodeRequest contains the fields for a registration promotion code.
type CreatePromoCodeRequest struct {
	Code        string  `json:"code"`                                  // Empty generates a random code
	BonusAmount float64 `json:"bonus_amount" binding:"required,min=0"` // Balance credited on use
	MaxUses     int     `json:"max_uses" binding:"min=0"`              // Maximum uses; zero means unlimited
	ExpiresAt   *int64  `json:"expires_at"`                            // Expiration as Unix seconds
	Notes       string  `json:"notes"`                                 // Admin notes
}

// UpdatePromoCodeRequest contains optional edits to a promotion code.
type UpdatePromoCodeRequest struct {
	Code        *string  `json:"code"`
	BonusAmount *float64 `json:"bonus_amount" binding:"omitempty,min=0"`
	MaxUses     *int     `json:"max_uses" binding:"omitempty,min=0"`
	Status      *string  `json:"status" binding:"omitempty,oneof=active disabled"`
	ExpiresAt   *int64   `json:"expires_at"`
	Notes       *string  `json:"notes"`
}

// NewPromoHandler creates a new admin promo handler.
func NewPromoHandler(promoService *promotion.PromoService) *PromoHandler {
	return &PromoHandler{
		promoService: promoService,
	}
}

// List returns a filtered page of promotion codes.
// GET /api/v1/admin/promo-codes
func (h *PromoHandler) List(c *gin.Context) {
	page, pageSize := httpx.ParsePagination(c)
	status := c.Query("status")
	search := strings.TrimSpace(c.Query("search"))
	if len(search) > 100 {
		search = search[:100]
	}

	params := pagination.PaginationParams{
		Page:      page,
		PageSize:  pageSize,
		SortBy:    c.DefaultQuery("sort_by", "created_at"),
		SortOrder: c.DefaultQuery("sort_order", "desc"),
	}

	codes, paginationResult, err := h.promoService.List(c.Request.Context(), params, status, search)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	out := make([]PromoCode, 0, len(codes))
	for i := range codes {
		out = append(out, *PromoCodeFromService(&codes[i]))
	}
	httpx.Paginated(c, out, paginationResult.Total, page, pageSize)
}

// GetByID returns a promotion code by ID.
// GET /api/v1/admin/promo-codes/:id
func (h *PromoHandler) GetByID(c *gin.Context) {
	codeID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		httpx.BadRequest(c, "Invalid promo code ID")
		return
	}

	code, err := h.promoService.GetByID(c.Request.Context(), codeID)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, PromoCodeFromService(code))
}

// Create creates a promotion code.
// POST /api/v1/admin/promo-codes
func (h *PromoHandler) Create(c *gin.Context) {
	var req CreatePromoCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	input := &promotion.CreatePromoCodeInput{
		Code:        req.Code,
		BonusAmount: req.BonusAmount,
		MaxUses:     req.MaxUses,
		Notes:       req.Notes,
	}

	if req.ExpiresAt != nil {
		t := time.Unix(*req.ExpiresAt, 0)
		input.ExpiresAt = &t
	}

	code, err := h.promoService.Create(c.Request.Context(), input)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, PromoCodeFromService(code))
}

// Update edits a promotion code.
// PUT /api/v1/admin/promo-codes/:id
func (h *PromoHandler) Update(c *gin.Context) {
	codeID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		httpx.BadRequest(c, "Invalid promo code ID")
		return
	}

	var req UpdatePromoCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	input := &promotion.UpdatePromoCodeInput{
		Code:        req.Code,
		BonusAmount: req.BonusAmount,
		MaxUses:     req.MaxUses,
		Status:      req.Status,
		Notes:       req.Notes,
	}

	if req.ExpiresAt != nil {
		if *req.ExpiresAt == 0 {
			// Zero clears the expiration time.
			input.ExpiresAt = &time.Time{}
		} else {
			t := time.Unix(*req.ExpiresAt, 0)
			input.ExpiresAt = &t
		}
	}

	code, err := h.promoService.Update(c.Request.Context(), codeID, input)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, PromoCodeFromService(code))
}

// Delete deletes a promotion code.
// DELETE /api/v1/admin/promo-codes/:id
func (h *PromoHandler) Delete(c *gin.Context) {
	codeID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		httpx.BadRequest(c, "Invalid promo code ID")
		return
	}

	err = h.promoService.Delete(c.Request.Context(), codeID)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, gin.H{"message": "Promo code deleted successfully"})
}

// GetUsages returns a page of usage records for a promotion code.
// GET /api/v1/admin/promo-codes/:id/usages
func (h *PromoHandler) GetUsages(c *gin.Context) {
	codeID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		httpx.BadRequest(c, "Invalid promo code ID")
		return
	}

	page, pageSize := httpx.ParsePagination(c)
	params := pagination.PaginationParams{
		Page:     page,
		PageSize: pageSize,
	}

	usages, paginationResult, err := h.promoService.ListUsages(c.Request.Context(), codeID, params)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	out := make([]PromoCodeUsage, 0, len(usages))
	for i := range usages {
		out = append(out, *PromoCodeUsageFromService(&usages[i]))
	}
	httpx.Paginated(c, out, paginationResult.Total, page, pageSize)
}
