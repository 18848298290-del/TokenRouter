package httpapi

import (
	"context"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// CodexInviteResetCommands provides invitation status, sending, and reset consumption operations.
type CodexInviteResetCommands interface {
	GetStatus(context.Context, int64) (*provider.CodexInviteResetStatus, error)
	SendInvite(context.Context, int64, []string) (*provider.CodexInviteResetInviteResult, error)
	Consume(context.Context, int64, string) (*provider.CodexInviteResetConsumeResult, error)
}

// CodexInviteResetHandler handles administrator requests for Codex invitation resets.
type CodexInviteResetHandler struct {
	service CodexInviteResetCommands
}

type codexInviteResetInviteRequest struct {
	Emails []string `json:"emails" binding:"required"`
}

type codexInviteResetConsumeRequest struct {
	// CreditID is optional; when omitted, the upstream selects an available reset credit.
	CreditID string `json:"credit_id"`
}

// NewCodexInviteResetHandler creates a Codex invitation reset handler.
func NewCodexInviteResetHandler(service CodexInviteResetCommands) *CodexInviteResetHandler {
	return &CodexInviteResetHandler{service: service}
}

// GetStatus returns the provider's invitation eligibility and available resets.
func (h *CodexInviteResetHandler) GetStatus(c *gin.Context) {
	providerID, ok := parseCodexInviteResetProviderID(c)
	if !ok {
		return
	}
	result, err := h.service.GetStatus(c.Request.Context(), providerID)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, result)
}

// SendInvite sends Codex invitation emails.
func (h *CodexInviteResetHandler) SendInvite(c *gin.Context) {
	providerID, ok := parseCodexInviteResetProviderID(c)
	if !ok {
		return
	}
	var req codexInviteResetInviteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.service.SendInvite(c.Request.Context(), providerID, req.Emails)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, result)
}

// Consume uses one Codex reset credit.
func (h *CodexInviteResetHandler) Consume(c *gin.Context) {
	providerID, ok := parseCodexInviteResetProviderID(c)
	if !ok {
		return
	}
	var req codexInviteResetConsumeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.service.Consume(c.Request.Context(), providerID, req.CreditID)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, result)
}

func parseCodexInviteResetProviderID(c *gin.Context) (int64, bool) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return 0, false
	}
	return providerID, true
}
