package httpapi

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Dedicated token/reset profile patches distinguish omitted fields, explicit
// null, and integer selection values. The custom nullable patch records
// presence so null can clear these IDs, while ordinary scalar pointers and rule
// slices still treat JSON null like omission.
//
// Router updates merge those presence decisions into the currently stored
// complete value before service normalization and persistence. Empty rule
// arrays clear the ordered rules, supplied false disables matching, and
// dedicated zero or -1 selections retain their built-in/random meaning.
// Validation errors become bad requests and missing reads become not found. The
// read and complete-value replacement are separate operations, so concurrent
// administrative changes can be overwritten by a later patch assembled from an
// earlier read.

// TLSFingerprintRouterHandler binds ordered router configuration operations to
// protected administrator HTTP requests.
type TLSFingerprintRouterHandler struct {
	// service is retained for repository reads, writes, and runtime refresh. The
	// application owns its startup and subscriber shutdown while the handler
	// serves protected administrator endpoints.
	service *egress.TLSFingerprintRouterService // retained router administration service
}

// CreateTLSFingerprintRouterRequest contains fields for a new TLS router.
//
// ChatGPTOAuthTokenUserAgent and ChatGPTOAuthTokenTLSFingerprintProfileID
// supply dedicated token settings. CodexInviteResetUserAgent and
// CodexInviteResetTLSFingerprintProfileID supply dedicated reset settings;
// Rules is the separate ordered request-matching collection.
type CreateTLSFingerprintRouterRequest struct {
	// requested router display name
	Name string `json:"name" binding:"required"`
	// optional router description text
	Description *string `json:"description"`
	// Enabled supplies matching activation when a boolean is present. Omission or
	// JSON null selects the handler's enabled default for a newly created router.
	// optional matching activation flag
	Enabled *bool `json:"enabled"`
	// requested OAuth token User-Agent
	ChatGPTOAuthTokenUserAgent string `json:"chatgpt_oauth_token_user_agent"`
	// nullable OAuth token profile selection
	ChatGPTOAuthTokenTLSFingerprintProfileID *int64 `json:"chatgpt_oauth_token_tls_fingerprint_profile_id"`
	// requested Codex reset User-Agent
	CodexInviteResetUserAgent string `json:"codex_invite_reset_user_agent"`
	// nullable Codex reset profile selection
	CodexInviteResetTLSFingerprintProfileID *int64 `json:"codex_invite_reset_tls_fingerprint_profile_id"`
	// Rules becomes a present empty slice when omitted or null during creation.
	// Each supplied rule is later normalized and validated by the router service.
	// ordered requested User-Agent rules
	Rules []egress.TLSFingerprintRouterRule `json:"rules"`
}

// UpdateTLSFingerprintRouterRequest contains optional fields for a router update.
//
// ChatGPTOAuthTokenTLSFingerprintProfileID and
// CodexInviteResetTLSFingerprintProfileID track JSON presence through
// nullableInt64Patch. Omission retains stored selection, explicit null clears
// it, and supplied integers replace it after service range validation.
type UpdateTLSFingerprintRouterRequest struct {
	// Name replaces the current router name when present. Omission or JSON null
	// retains it, while a supplied blank name is rejected after service trimming.
	// requested router display name
	Name *string `json:"name"`
	// Description replaces the stored string when present, including empty text.
	// Omission and JSON null retain the current optional description, so this
	// ordinary pointer field cannot request clearing.
	// optional router description text
	Description *string `json:"description"`
	// Enabled retains current matching activation when omitted or null. A supplied
	// false value remains present and disables the router through the complete
	// merged update.
	// optional matching activation flag
	Enabled *bool `json:"enabled"`
	// ChatGPTOAuthTokenUserAgent replaces token identity text when present,
	// including empty text. Omission and JSON null retain the stored value before
	// service normalization.
	// requested OAuth token User-Agent
	ChatGPTOAuthTokenUserAgent *string `json:"chatgpt_oauth_token_user_agent"`
	// nullable OAuth token profile selection
	ChatGPTOAuthTokenTLSFingerprintProfileID nullableInt64Patch `json:"chatgpt_oauth_token_tls_fingerprint_profile_id"`
	// CodexInviteResetUserAgent replaces reset identity text when present,
	// including empty text. Omission and JSON null retain the stored value before
	// service normalization.
	// requested Codex reset User-Agent
	CodexInviteResetUserAgent *string `json:"codex_invite_reset_user_agent"`
	// nullable Codex reset profile selection
	CodexInviteResetTLSFingerprintProfileID nullableInt64Patch `json:"codex_invite_reset_tls_fingerprint_profile_id"`
	// Rules retains stored order when decoded as nil, including omission or JSON
	// null. A present empty array replaces the collection and clears persisted
	// rules.
	// ordered requested User-Agent rules
	Rules []egress.TLSFingerprintRouterRule `json:"rules"`
}

// nullableInt64Patch distinguishes an omitted JSON field from null, which clears its value.
type nullableInt64Patch struct {
	// Set records that UnmarshalJSON was invoked for the field. Value is nil for
	// explicit null and points to a decoded integer after success; parse failure
	// can leave a prior Value while Set remains true.
	Set   bool   // decoded field presence flag
	Value *int64 // nullable decoded profile identity
}

// NewTLSFingerprintRouterHandler retains the router service for protected
// administrator CRUD endpoints. The application owns service startup and
// subscriber shutdown, and binds route authentication before any handler
// operation is served.
func NewTLSFingerprintRouterHandler(service *egress.TLSFingerprintRouterService) *TLSFingerprintRouterHandler {
	return &TLSFingerprintRouterHandler{service: service}
}

// UnmarshalJSON marks field presence and decodes null or a signed integer into
// Value. Null clears Value; integer success installs its pointer. Invalid input
// returns an error after Set became true and can leave an earlier Value, so
// callers reject the failed decode.
//
// Omitted fields never invoke this method and retain Set=false in a fresh
// request. That distinction lets router updates keep a stored dedicated profile
// for omission and clear it for null, while zero and -1 remain actual selection
// values rather than absence.
func (p *nullableInt64Patch) UnmarshalJSON(data []byte) error {
	// Record presence before decoding so explicit null remains distinguishable
	// from an omitted field. Binding errors reject the request before any failed value is applied.
	p.Set = true
	if strings.TrimSpace(string(data)) == "null" {
		p.Value = nil
		return nil
	}
	var value int64
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	p.Value = &value
	return nil
}

// List returns persisted router configuration through the common administrator
// success envelope. The service forwards repository errors to shared response
// handling, and repository mapping supplies a present Rules array when stored
// rules are absent.
func (h *TLSFingerprintRouterHandler) List(c *gin.Context) {
	routers, err := h.service.List(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, routers)
}

// GetByID parses a signed decimal path ID and returns the selected persisted
// router. Parse failure is bad request, an absent nil result is not found, and
// other repository errors use standard response handling.
func (h *TLSFingerprintRouterHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid router ID")
		return
	}
	router, err := h.service.GetByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if router == nil {
		response.NotFound(c, "Router not found")
		return
	}
	response.Success(c, router)
}

// Create binds router input, defaults Enabled to true, and supplies a present
// empty Rules slice when omitted. The service normalizes and validates
// configuration before persistence; ValidationError is a bad request and other
// failures use common responses.
func (h *TLSFingerprintRouterHandler) Create(c *gin.Context) {
	var req CreateTLSFingerprintRouterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	router := &egress.TLSFingerprintRouter{
		Name:                                     req.Name,
		Description:                              req.Description,
		Enabled:                                  true,
		ChatGPTOAuthTokenUserAgent:               req.ChatGPTOAuthTokenUserAgent,
		ChatGPTOAuthTokenTLSFingerprintProfileID: req.ChatGPTOAuthTokenTLSFingerprintProfileID,
		CodexInviteResetUserAgent:                req.CodexInviteResetUserAgent,
		CodexInviteResetTLSFingerprintProfileID:  req.CodexInviteResetTLSFingerprintProfileID,
		Rules:                                    req.Rules,
	}
	if req.Enabled != nil {
		router.Enabled = *req.Enabled
	}
	if router.Rules == nil {
		router.Rules = []egress.TLSFingerprintRouterRule{}
	}
	created, err := h.service.Create(c.Request.Context(), router)
	if err != nil {
		if _, ok := err.(*egress.ValidationError); ok {
			response.BadRequest(c, err.Error())
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, created)
}

// Update reads current router values and applies field-presence decisions
// before complete-value replacement. Invalid ID or JSON is bad request, and an
// absent router is not found. Dedicated profile patches use Set to distinguish
// omission from explicit null.
//
// Other nullable pointers and nil rule slices retain stored values, while
// present false, empty strings, and empty rule arrays remain updates. The
// service then normalizes and validates the merged value. Read and write are
// separate repository operations, so a patch can replace intervening edits that
// occurred after the initial read.
func (h *TLSFingerprintRouterHandler) Update(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid router ID")
		return
	}
	var req UpdateTLSFingerprintRouterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	existing, err := h.service.GetByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if existing == nil {
		response.NotFound(c, "Router not found")
		return
	}
	router := &egress.TLSFingerprintRouter{
		ID:                                       id,
		Name:                                     existing.Name,
		Description:                              existing.Description,
		Enabled:                                  existing.Enabled,
		ChatGPTOAuthTokenUserAgent:               existing.ChatGPTOAuthTokenUserAgent,
		ChatGPTOAuthTokenTLSFingerprintProfileID: existing.ChatGPTOAuthTokenTLSFingerprintProfileID,
		CodexInviteResetUserAgent:                existing.CodexInviteResetUserAgent,
		CodexInviteResetTLSFingerprintProfileID:  existing.CodexInviteResetTLSFingerprintProfileID,
		Rules:                                    existing.Rules,
	}
	if req.Name != nil {
		router.Name = *req.Name
	}
	if req.Description != nil {
		router.Description = req.Description
	}
	if req.Enabled != nil {
		router.Enabled = *req.Enabled
	}
	if req.ChatGPTOAuthTokenUserAgent != nil {
		router.ChatGPTOAuthTokenUserAgent = *req.ChatGPTOAuthTokenUserAgent
	}
	if req.ChatGPTOAuthTokenTLSFingerprintProfileID.Set {
		router.ChatGPTOAuthTokenTLSFingerprintProfileID = req.ChatGPTOAuthTokenTLSFingerprintProfileID.Value
	}
	if req.CodexInviteResetUserAgent != nil {
		router.CodexInviteResetUserAgent = *req.CodexInviteResetUserAgent
	}
	if req.CodexInviteResetTLSFingerprintProfileID.Set {
		router.CodexInviteResetTLSFingerprintProfileID = req.CodexInviteResetTLSFingerprintProfileID.Value
	}
	if req.Rules != nil {
		router.Rules = req.Rules
	}
	if router.Rules == nil {
		router.Rules = []egress.TLSFingerprintRouterRule{}
	}
	updated, err := h.service.Update(c.Request.Context(), router)
	if err != nil {
		if _, ok := err.(*egress.ValidationError); ok {
			response.BadRequest(c, err.Error())
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, updated)
}

// Delete parses a signed decimal router ID and delegates persistence removal
// plus service refresh. Parse failure returns bad request, delegated errors use
// common response handling, and success returns the fixed administrator
// confirmation payload.
func (h *TLSFingerprintRouterHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid router ID")
		return
	}
	if err := h.service.Delete(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "Router deleted successfully"})
}
