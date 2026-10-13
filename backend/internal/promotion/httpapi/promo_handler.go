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

// Administrator Promo edits represent an omitted deadline separately from an
// explicit zero Unix timestamp. Update converts zero into a Go zero-time
// sentinel so PromoService can remove the saved deadline, while nonzero
// seconds become a concrete expiration instant.
//
// Creation supplies an active code, generating text when it is empty, and
// request binding checks amount, count, and status fields before the service
// call. Listing truncates search by bytes and builds the storage sort request
// from query parameters. Response conversion keeps promotion controls
// separate from recorded grant amounts and optional recipient details on
// usage receipts.

// PromoHandler serves administrator registration-Promo code management and
// usage queries.
type PromoHandler struct {
	// promoService is retained for every administrator request. Callers provide
	// an initialized service and coordinate its dependencies' lifetime with the
	// mounted routes.
	// shared Promo administrator operations
	promoService *promotion.PromoService
}

// CreatePromoCodeRequest carries validated administrator fields for a new
// registration grant.
type CreatePromoCodeRequest struct {
	// Code is optional and can be empty. PromoService trims and uppercases it,
	// generating random text when the trimmed value is absent.
	Code string `json:"code"` // requested promotion text
	// BonusAmount is a required numeric grant checked by JSON binding. The
	// required tag rejects its zero value before PromoService.Create is invoked.
	// USD
	BonusAmount float64 `json:"bonus_amount" binding:"required,min=0"`
	// zero leaves usage unlimited
	MaxUses int `json:"max_uses" binding:"min=0"`
	// ExpiresAt supplies whole Unix seconds when present. Nil leaves creation
	// without a deadline; a supplied zero represents the Unix epoch in this
	// create endpoint.
	ExpiresAt *int64 `json:"expires_at"` // Unix seconds
	Notes     string `json:"notes"`      // administrator promotion note
}

// UpdatePromoCodeRequest carries optional administrator replacements for
// saved grant controls.
type UpdatePromoCodeRequest struct {
	// Code, BonusAmount, MaxUses, and Status use nil for omission and supplied
	// pointers for replacement. Notes follows the same rule, including an empty
	// string that clears saved text.
	// optional promotion text replacement
	Code *string `json:"code"`
	// optional USD grant replacement
	BonusAmount *float64 `json:"bonus_amount" binding:"omitempty,min=0"`
	// optional total-use ceiling replacement
	MaxUses *int `json:"max_uses" binding:"omitempty,min=0"`
	// optional code availability replacement
	Status *string `json:"status" binding:"omitempty,oneof=active disabled"`
	// ExpiresAt nil leaves the deadline unchanged. A supplied zero requests
	// removal; other values supply the replacement deadline in whole Unix
	// seconds.
	ExpiresAt *int64 `json:"expires_at"` // optional Unix-second deadline
	// optional administrator note replacement
	Notes *string `json:"notes"`
}

// NewPromoHandler retains promoService for administrator management and
// receipt-list endpoints.
//
// The service remains shared with its owner; route registration supplies the
// administrator middleware required by these operations.
func NewPromoHandler(promoService *promotion.PromoService) *PromoHandler {
	return &PromoHandler{
		promoService: promoService,
	}
}

// List returns a page of Promo controls using status, search, and sort
// queries.
//
// Search is trimmed and limited to one hundred bytes; the repository maps
// supported sort keys and directions.
// GET /api/v1/admin/promo-codes
func (h *PromoHandler) List(c *gin.Context) {
	page, pageSize := httpx.ParsePagination(c)
	status := c.Query("status")
	search := strings.TrimSpace(c.Query("search"))
	if len(search) > 100 {
		// The byte ceiling can leave a partial UTF-8 sequence.
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

// GetByID returns the administrator Promo response for the parsed path
// identifier.
//
// Malformed integer text is a bad request; numeric values are passed to
// storage, which reports an absent code.
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

// Create validates JSON fields and requests a new active registration Promo.
//
// An empty code requests generation; an optional deadline is converted from
// Unix seconds. Success returns the stored promotion response.
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
		// The API deadline is expressed in whole Unix seconds.
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

// Update applies supplied administrator Promo edits for the parsed path
// identifier.
//
// Omitted pointer fields retain their stored values. A supplied expiration of
// zero becomes a Go zero-time pointer, which PromoService interprets as a
// request to remove the deadline. Other supplied Unix seconds become a
// concrete time. This conversion keeps omission separate from clearing while
// amount, count, and status binding validates the submitted replacements
// before persistence.
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
			// Use Go zero time as the service's clear sentinel, keeping an explicit
			// API zero distinct from an omitted deadline pointer.
			input.ExpiresAt = &time.Time{}
		} else {
			// Nonzero API deadline values represent whole Unix seconds.
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

// Delete requests hard deletion of the Promo identified by the parsed path
// integer.
//
// Malformed text is a bad request. Storage errors use the shared mapper, and
// success returns the deletion message.
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

// GetUsages returns a page of grant receipts for the parsed Promo identifier.
//
// It converts recorded usage amounts and optional loaded users to responses;
// a missing code can produce an empty usage page.
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
