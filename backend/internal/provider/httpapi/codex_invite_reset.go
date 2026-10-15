package httpapi

import (
	"context"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// codex_invite_reset.go exposes Codex invitation status, invitation sending,
// and reset-credit consumption over HTTP.
// Codex invitation eligibility and reset-credit consumption have different
// upstream operations and outcomes. Administrator handlers parse the provider
// identifier first, bind operation-specific input and pass domain failures
// through the shared error mapping before returning the service result.

// CodexInviteResetCommands supplies invitation status, invitation sending and
// reset-credit consumption operations.
type CodexInviteResetCommands interface {
	// GetStatus prepares provider credentials and reads invitation eligibility and
	// reset-credit observations. Eligibility failure can disable invitation while
	// other credit queries continue; the result summarizes each available upstream
	// observation.
	GetStatus(context.Context, int64) (*provider.CodexInviteResetStatus, error)
	// SendInvite validates supplied addresses and requests Codex invitations for
	// the selected provider. Upstream preparation and send failures are returned
	// through domain errors, allowing the handler to retain their status mapping.
	SendInvite(context.Context, int64, []string) (*provider.CodexInviteResetInviteResult, error)
	// Consume uses a selected or upstream-chosen reset credit for the
	// provider. Empty credit selection delegates availability choice; the returned
	// result describes consumption so callers can distinguish it from a status
	// query.
	Consume(context.Context, int64, string) (*provider.CodexInviteResetConsumeResult, error)
}

// CodexInviteResetHandler binds administrator requests to provider invitation
// and reset-credit operations.
type CodexInviteResetHandler struct {
	service CodexInviteResetCommands // borrowed invitation reset use case
}

// codexInviteResetInviteRequest carries the required invitation email
// collection from the administrator.
type codexInviteResetInviteRequest struct {
	Emails []string `json:"emails" binding:"required"` // requested invitation email addresses
}

// codexInviteResetConsumeRequest optionally selects an available upstream
// reset-credit identifier.
type codexInviteResetConsumeRequest struct {
	// CreditID optionally selects one upstream reset credit. An empty identifier
	// delegates selection of an available credit to the upstream reset-consumption operation.
	CreditID string `json:"credit_id"` // optional upstream credit identifier
}

// NewCodexInviteResetHandler borrows invitation and reset-credit operations for
// administrator routes. The service owns upstream preparation and request
// lifetime; its dependencies remain available until mounted operations finish.
func NewCodexInviteResetHandler(service CodexInviteResetCommands) *CodexInviteResetHandler {
	return &CodexInviteResetHandler{service: service}
}

// GetStatus returns invitation eligibility and available reset-credit
// observations for a decimal provider identifier. Path parsing and use-case
// failures end the request before the status result is encoded.
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

// SendInvite binds a required email collection and requests invitations for the
// path-selected provider. The use case validates addresses and upstream
// eligibility, and this handler returns its result with the shared response
// envelope.
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

// Consume binds an optional reset-credit ID and delegates its consumption for
// the path-selected provider. An empty identifier lets upstream choose a
// credit; use-case failures are returned before success encoding.
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

// parseCodexInviteResetProviderID parses the decimal path identifier and
// reports whether binding can continue. Parse failures write 400 and return
// false; zero or negative parsed values reach service validation unchanged.
func parseCodexInviteResetProviderID(c *gin.Context) (int64, bool) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return 0, false
	}
	return providerID, true
}
