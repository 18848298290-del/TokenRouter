package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

type UserHandler struct{ affiliateService *promotion.AffiliateService }

func NewUserHandler(s *promotion.AffiliateService) *UserHandler {
	return &UserHandler{affiliateService: s}
}

// GetAffiliate returns the current user's affiliate details.
// GET /api/v1/user/aff
func (h *UserHandler) GetAffiliate(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		httpx.Unauthorized(c, "User not authenticated")
		return
	}

	detail, err := h.affiliateService.GetAffiliateDetail(c.Request.Context(), subject.UserID)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, detail)
}

// TransferAffiliateQuota moves the current user's available affiliate quota into balance.
// POST /api/v1/user/aff/transfer
func (h *UserHandler) TransferAffiliateQuota(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		httpx.Unauthorized(c, "User not authenticated")
		return
	}

	transferred, balance, err := h.affiliateService.TransferAffiliateQuota(c.Request.Context(), subject.UserID)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, gin.H{
		"transferred_quota": transferred,
		"balance":           balance,
	})
}
