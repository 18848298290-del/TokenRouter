package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// User redemption and history requests derive the recipient from the
// authentication subject placed in Gin context. Binding and service errors
// become HTTP responses, while successful credentials pass through the user
// DTO mapping, including visible administrator adjustment reasons when the
// record type permits them.

// RedeemHandler exposes current-user redemption and receipt-history requests
// through the billing service.
type RedeemHandler struct {
	redeemService *billing.RedeemService // current-user redemption and history service
}

// RedeemRequest supplies the credential text submitted by the authenticated
// recipient.
type RedeemRequest struct {
	Code string `json:"code" binding:"required"` // submitted exact credential text
}

// NewRedeemHandler binds a redemption service without handling a request or
// opening resources. The caller supplies a usable service and mounts methods
// with authentication middleware so the handler can derive the current
// recipient.
func NewRedeemHandler(redeemService *billing.RedeemService) *RedeemHandler {
	return &RedeemHandler{
		redeemService: redeemService,
	}
}

// Redeem binds required credential text and uses the authenticated subject's
// user ID for the service attempt. Missing identity yields unauthorized,
// malformed JSON yields bad request, and service errors use the shared error
// response; success maps the returned user receipt.
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

// GetHistory returns the authenticated user's paginated redemption/adjustment
// views using parsed page and size. Missing identity yields unauthorized;
// service errors use the shared response, and successful rows retain the user
// mapper's per-type reason visibility.
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
