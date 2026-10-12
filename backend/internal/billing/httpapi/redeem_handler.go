package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// RedeemHandler handles redeem code-related requests.
type RedeemHandler struct {
	redeemService *billing.RedeemService
}

// RedeemRequest represents the redeem code request payload.
type RedeemRequest struct {
	Code string `json:"code" binding:"required"`
}

// NewRedeemHandler creates a new RedeemHandler.
func NewRedeemHandler(redeemService *billing.RedeemService) *RedeemHandler {
	return &RedeemHandler{
		redeemService: redeemService,
	}
}

// Redeem handles redeeming a code
// POST /api/v1/redeem
func (h *RedeemHandler) Redeem(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	var req RedeemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	result, err := h.redeemService.Redeem(c.Request.Context(), subject.UserID, req.Code)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, RedeemCodeFromService(result))
}

// GetHistory returns a page of the current user's redemption history.
// GET /api/v1/redeem/history?page=1&page_size=20
func (h *RedeemHandler) GetHistory(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	page, pageSize := response.ParsePagination(c)
	codes, total, err := h.redeemService.GetUserHistory(c.Request.Context(), subject.UserID, page, pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]RedeemCode, 0, len(codes))
	for i := range codes {
		out = append(out, *RedeemCodeFromService(&codes[i]))
	}
	response.Paginated(c, out, total, page, pageSize)
}
