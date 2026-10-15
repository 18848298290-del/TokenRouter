package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// user_handler.go handles affiliate detail and quota-transfer requests for the
// authenticated account.
// User affiliate operations derive their account ID from the authenticated
// subject, so clients select no recipient in the request payload. Detail
// access can persist matured quota, and transfer returns the claimed quota
// together with the resulting account balance.

// UserHandler serves authenticated users' affiliate detail and quota-transfer
// requests.
type UserHandler struct {
	// affiliateService is retained by the handler and invoked with each request
	// context. Callers provide an initialized service and coordinate its
	// lifetime.
	// account affiliate operations
	affiliateService *promotion.AffiliateService
}

// NewUserHandler retains s for authenticated affiliate detail and transfer
// endpoints.
//
// The service remains shared with its owner; route registration supplies the
// authentication middleware these endpoints expect.
func NewUserHandler(s *promotion.AffiliateService) *UserHandler {
	return &UserHandler{affiliateService: s}
}

// GetAffiliate returns the authenticated subject's affiliate detail through
// the service.
//
// Missing subjects receive unauthorized; detail retrieval may persist thawed
// quota before returning masked invitees and the current stored quota.
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

// TransferAffiliateQuota transfers all available affiliate quota for the
// authenticated subject.
//
// Missing subjects receive unauthorized. Success reports the transferred
// quota and returned balance; service and storage errors use the shared HTTP
// mapper.
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
