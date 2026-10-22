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

// admin_redeem_handler.go handles administrator redemption-code queries,
// mutations, exports, and create-and-redeem requests.
// Administrator redemption requests distinguish expiry omission, clearing and
// future timestamps, while generation accepts either absolute seconds or
// relative UTC days. List and CSV export share Unicode search normalization
// and service sorting. The HTTP adapters map administrator records and pass
// entitlement validation to billing, with the route owner supplying
// administrator authorization.
//
// Create-and-redeem can find a code from an earlier request or from a
// concurrent creator. Its recovery checks the code's latest UsedBy summary
// before attempting redemption again, and the creation and grant phases have
// separate persistence lifetimes. Command replay uses the bound executor; a
// nil redemption service rejects only the endpoints that require that service.

// RedeemAdministrator supplies credential administration and filtered rows for
// HTTP response mapping.
type RedeemAdministrator interface {
	// ListRedeemCodes returns filtered page rows and the matching total for
	// administrator listing or export. Callers supply normalized search text and
	// sorting; the total describes all matches rather than the returned page
	// length.
	ListRedeemCodes(ctx context.Context, page, pageSize int, codeType, status, search string, sortBy, sortOrder string) ([]billing.RedeemCode, int64, error)
	// GetRedeemCode returns the credential selected by its record ID for
	// administrator mapping. A missing record returns the billing not-found
	// error; hydration and effective status belong to the administrator's
	// repository.
	GetRedeemCode(ctx context.Context, id int64) (*billing.RedeemCode, error)
	// GenerateRedeemCodes validates supplied count, code text and entitlement
	// terms before creating credentials. The concrete administrator creates
	// sequentially, so a later create failure can leave earlier credentials
	// persisted even though this call returns an error.
	GenerateRedeemCodes(ctx context.Context, input *billing.GenerateRedeemCodesInput) ([]billing.RedeemCode, error)
	// UpdateRedeemCode applies optional editable terms and returns the refreshed
	// credential. Expiry presence distinguishes clearing from omission, used
	// grant values stay immutable, and the concrete administrator owns mutation
	// transaction scope and response rereading.
	UpdateRedeemCode(ctx context.Context, id int64, input *billing.UpdateRedeemCodeInput) (*billing.RedeemCode, error)
	// DeleteRedeemCode removes a credential eligible for administration deletion
	// and returns a read or delete error otherwise. The concrete administrator
	// reads before deleting, so callers do not receive a locked read/delete
	// guarantee.
	DeleteRedeemCode(ctx context.Context, id int64) error
	// BatchDeleteRedeemCodes attempts the supplied record sequence and returns
	// the number deleted. The concrete administrator skips unreadable, used or
	// failed records, so a successful reply can contain a partial count and
	// repeated IDs remain repeated attempts.
	BatchDeleteRedeemCodes(ctx context.Context, ids []int64) (int64, error)
	// ExpireRedeemCode marks the selected record's stored status expired and
	// returns its updated view. The concrete administrator performs a read then
	// update, propagating either error without a locked transaction around the
	// pair.
	ExpireRedeemCode(ctx context.Context, id int64) (*billing.RedeemCode, error)
}

// AdminRedeemHandler maps administrator credential commands, recovery results
// and CSV responses.
type AdminRedeemHandler struct {
	// Executor uses the app-bound coordinator for generation and
	// create-and-redeem replay. Bind before serving requests; unbound fixtures
	// invoke their callbacks directly with default retention periods.
	idempotencyhttp.Executor

	adminService RedeemAdministrator // credential administration and listing dependency
	// redeemService is required by CreateAndRedeem and BatchUpdate. Those
	// endpoints return an internal error when it is nil; other administration
	// endpoints use adminService.
	redeemService *billing.RedeemService // optional grant and batch-update service
}

// GenerateRedeemCodesRequest supplies bounded creation count, optional code
// and credential grant terms.
type GenerateRedeemCodesRequest struct {
	// RequiresPayment requests the service's paid-order eligibility check. The
	// administrator rejects this flag for invitations, while false permits
	// recipients without qualifying payment history.
	RequiresPayment bool `json:"requires_payment"` // paid-order eligibility requirement
	// Code is validated at at most thirty-two Unicode code points before handler
	// trimming. A nonempty trimmed custom code requires Count equal to one in the
	// administrator service.
	Code string `json:"code" binding:"omitempty,max=32"` // optional fixed credential text
	// requested count, one through one hundred
	Count int `json:"count" binding:"required,min=1,max=100"`
	// requested credential grant category
	Type  string  `json:"type" binding:"required,oneof=balance concurrency subscription invitation"`
	Value float64 `json:"value"` // credential grant amount or increment
	// MaxUses omitted selects the administrator default, and zero requests
	// unlimited capacity. Invitations are restricted by the service to one use
	// regardless of this supplied capacity.
	// optional lifetime usage capacity
	MaxUses *int `json:"max_uses" binding:"omitempty,min=0"`
	// ExpiresAt and ExpiresInDays are mutually exclusive even when absolute
	// seconds are zero. ResolveRedeemCodeExpiresAt maps absent/zero absolute
	// expiry to nil and accepts one through 3,650 relative
	// days.
	ExpiresAt *int64 `json:"expires_at" binding:"omitempty,min=0"` // Unix seconds
	// relative UTC calendar days
	ExpiresInDays *int `json:"expires_in_days" binding:"omitempty,min=1,max=3650"`
	// PlanID identifies the grant plan for subscription credentials and is
	// validated by the administrator service. Other credential categories do not
	// require this plan pointer.
	PlanID *int64 `json:"plan_id"` // optional source plan identifier
}

// UpdateRedeemCodeRequest represents optional single-credential terms with
// distinct expiry omission and clearing.
type UpdateRedeemCodeRequest struct {
	// RequiresPayment omission keeps the stored requirement. A supplied value
	// reaches administrator validation, including the prohibition on payment
	// eligibility for invitation credentials.
	RequiresPayment *bool `json:"requires_payment"` // optional paid-order eligibility change
	// Value omission retains the current amount. Any supplied replacement is
	// rejected by administrator validation after a recorded use, including an
	// identical amount pointer.
	Value *float64 `json:"value"` // optional grant amount replacement
	// MaxUses omission retains stored capacity and zero requests unlimited use. A
	// positive replacement cannot fall below completed uses, while invitations
	// remain limited to one.
	// optional lifetime capacity replacement
	MaxUses *int `json:"max_uses" binding:"omitempty,min=0"`
	// ExpiresAt omission leaves expiry unchanged; a supplied zero clears it. A
	// positive timestamp reaches service future-expiry validation after the
	// handler constructs its time value.
	ExpiresAt *int64 `json:"expires_at" binding:"omitempty,min=0"` // Unix seconds
	// PlanID applies to subscription credentials through service validation. Any
	// supplied plan replacement is rejected after recorded use, including a
	// pointer containing the current plan ID.
	PlanID *int64 `json:"plan_id"` // optional source plan replacement
}

// CreateAndRedeemCodeRequest names a fixed credential, target recipient and
// proposed creation terms.
type CreateAndRedeemCodeRequest struct {
	// Code is bound at three through one-hundred-twenty-eight code points before
	// trimming. New-code creation separately enforces the service's
	// thirty-two-code-point limit; existing-record resolution precedes creation.
	// fixed credential lookup text
	Code string `json:"code" binding:"required,min=3,max=128"`
	// Type omission becomes balance for callers that send top-ups without a
	// category. A supplied subscription type requires a positive PlanID before
	// existing-code recovery is attempted.
	// optional credential grant category
	Type   string  `json:"type" binding:"omitempty,oneof=balance concurrency subscription invitation"`
	Value  float64 `json:"value" binding:"required"`        // requested nonzero grant amount
	UserID int64   `json:"user_id" binding:"required,gt=0"` // positive intended grant recipient
	// PlanID is required and positive for subscription requests at this handler.
	// Existing-code recovery uses the stored code's grant terms, which may differ
	// from the proposed creation terms.
	PlanID *int64 `json:"plan_id"` // optional subscription source plan
	Notes  string `json:"notes"`   // proposed credential history reason
	// ExpiresAt and ExpiresInDays are mutually exclusive even when absolute
	// seconds are zero. ResolveRedeemCodeExpiresAt maps absent/zero absolute
	// expiry to nil and accepts one through 3,650 relative
	// days.
	ExpiresAt *int64 `json:"expires_at" binding:"omitempty,min=0"` // Unix seconds
	// relative UTC calendar days
	ExpiresInDays *int `json:"expires_in_days" binding:"omitempty,min=1,max=3650"`
}

// NewAdminRedeemHandler binds a usable administrator and an optional
// redemption service with an unbound executor. The route owner supplies
// authorization, app construction binds command replay, and missing redemption
// service is handled by its two dependent endpoints.
func NewAdminRedeemHandler(adminService RedeemAdministrator, redeemService *billing.RedeemService) *AdminRedeemHandler {
	return &AdminRedeemHandler{
		adminService:  adminService,
		redeemService: redeemService,
	}
}

// ResolveRedeemCodeExpiresAt returns an optional UTC expiry from one
// absolute-seconds or relative-day input. Missing or zero absolute seconds
// return nil, and supplying both pointers is a conflict even when the absolute
// value is zero. Negative seconds and relative days outside one through
// 3,650 are rejected.
//
// Relative days use calendar addition from current UTC time; absolute seconds
// have whole-second precision and must be strictly future at this call. Each
// invocation uses its own wall-clock observation, so callers resolving
// relative expiry retain the returned value through their subsequent create or
// grant operation.
func ResolveRedeemCodeExpiresAt(expiresAt *int64, expiresInDays *int) (*time.Time, error) {
	if expiresAt != nil && *expiresAt < 0 {
		return nil, apperror.BadRequest("REDEEM_CODE_EXPIRES_AT_INVALID", "expires_at must be greater than or equal to zero")
	}
	if expiresAt != nil && expiresInDays != nil {
		return nil, apperror.BadRequest("REDEEM_CODE_EXPIRY_CONFLICT", "expires_at and expires_in_days cannot both be set")
	}

	now := time.Now().UTC()
	if expiresInDays != nil {
		// Match the JSON binding range for direct helper callers too.
		if *expiresInDays <= 0 || *expiresInDays > 3650 {
			return nil, apperror.BadRequest("REDEEM_CODE_EXPIRES_IN_DAYS_INVALID", "expires_in_days must be between 1 and 3650")
		}
		expires := now.AddDate(0, 0, *expiresInDays)
		return &expires, nil
	}
	if expiresAt == nil || *expiresAt == 0 {
		return nil, nil
	}

	// Absolute input represents whole seconds since the epoch and is returned in
	// UTC.
	expires := time.Unix(*expiresAt, 0).UTC()
	if !expires.After(now) {
		return nil, apperror.BadRequest("REDEEM_CODE_EXPIRES_AT_INVALID", "expires_at must be in the future")
	}
	return &expires, nil
}

// List returns a filtered administrator credential page with normalized
// Unicode search and service mapping. Sorting defaults to ID descending;
// pagination comes from shared parsing, and service failures use the shared
// error response.
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

// normalizeRedeemSearch trims outer whitespace and keeps the first one hundred
// Unicode code points for both listing and export. Go string iteration yields
// each character's starting byte index, so truncation ends before the next
// whole character rather than at a guessed byte limit. Empty or all-whitespace
// input returns an empty string. Shorter inputs retain their trimmed text, and
// the same helper gives both endpoints identical search terms for their
// service queries.
func normalizeRedeemSearch(search string) string {
	search = strings.TrimSpace(search)
	count := 0
	for index := range search {
		// The shared list/export search limit ends at a character's starting byte
		// index, keeping valid Unicode text intact.
		if count == 100 {
			return search[:index]
		}
		count++
	}
	return search
}

// GetByID parses a signed decimal record ID and maps the administrator's
// credential response. Malformed text returns bad request; zero and negative
// values reach the service, and read failures use shared error mapping.
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

// Update applies optional single-code terms while retaining the difference
// between omitted expiry and an explicit clear. Invalid signed ID text or JSON
// stops before the administrator call; other validation and mutation errors
// use shared HTTP mapping. A missing expiry pointer leaves ExpiresAtSet false,
// while zero sets the flag with a nil time and positive seconds supply a time
// value.
//
// The administrator owns used-value, plan, capacity and future-expiry checks
// plus transaction handling. Request pointers pass through for synchronous
// consumption, and success maps the administrator's refreshed credential
// rather than constructing a response from requested fields.
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
		// Convert positive Unix seconds after recording presence separately; an
		// explicit zero therefore reaches the service as a clear rather than
		// omission.
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

// Generate binds bounded count and category, trims custom code text and
// resolves optional expiry before command execution. The executor hashes the
// normalized request with its supplied expiry inputs; the resolved time is
// retained for this callback, and administrator validation/creation errors use
// the command response helper.
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

// CreateAndRedeem creates a fixed single-use credential when missing and
// redeems it for the requested recipient. Missing redemption service or
// invalid request/expiry stops first; omitted type becomes balance, and
// subscription input requires a positive plan ID before command replay or code
// lookup.
//
// Existing credentials use their stored terms and the latest UsedBy summary
// for recovery. Creation failure triggers a reread that can resolve a
// concurrent creator's record; if that reread fails, the original create error
// returns. Credential creation and redemption are separate service operations,
// so failed redemption can leave a newly created code for a later request.
// Bound command replay determines whether the callback runs again.
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
		// Resolve an existing record before creation so a repeated fixed-code
		// request can continue from persisted creation or a completed recipient
		// summary.
		existing, err := h.redeemService.GetByCode(ctx, req.Code)
		if err == nil {
			return h.resolveCreateAndRedeemExisting(ctx, existing, req.UserID)
		}
		if !errors.Is(err, billing.ErrRedeemCodeNotFound) {
			return nil, err
		}

		// Persist the credential before redemption starts. If grant processing later
		// fails, this created record remains available to the existing-code recovery
		// path.
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
			// Reread after any create error to resolve a record another creator may
			// have supplied; if rereading fails, keep the original creation error.
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

// resolveCreateAndRedeemExisting returns a credential already attributed to
// this recipient or attempts a still-usable credential's redemption. A nil
// record conflicts; a matching latest UsedBy succeeds before expiry or status
// checks, using stored grant terms. This summary comparison does not establish
// all historical recipient usage for a multi-use credential.
//
// Otherwise expiry rejects, and CanUse controls whether redemption can resume.
// Used, exhausted-capacity and already-used service errors trigger a
// latest-record read; success requires that latest UsedBy match this
// recipient. Other service errors propagate, while an unsuccessful reread or
// unmatched summary yields the existing conflict response.
func (h *AdminRedeemHandler) resolveCreateAndRedeemExisting(ctx context.Context, existing *billing.RedeemCode, userID int64) (any, error) {
	if existing == nil {
		return nil, apperror.Conflict("REDEEM_CODE_CONFLICT", "redeem code conflict")
	}

	if existing.UsedBy != nil && *existing.UsedBy == userID {
		return gin.H{"redeem_code": RedeemCodeFromServiceAdmin(existing)}, nil
	}

	// A nonmatching recipient summary can still describe an incomplete creation.
	// Expiry and CanUse decide whether attempting its grant can continue that
	// request.
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
		// Usage-related failures can follow another completed attempt. Inspect the
		// latest recipient summary; a failed reread stays a conflict instead of
		// replacing it with a lookup error.
		if latest, getErr := h.redeemService.GetByCode(ctx, existing.Code); getErr == nil && latest != nil {
			if latest.UsedBy != nil && *latest.UsedBy == userID {
				return gin.H{"redeem_code": RedeemCodeFromServiceAdmin(latest)}, nil
			}
		}
	}

	return nil, apperror.Conflict("REDEEM_CODE_CONFLICT", "redeem code already used by another user")
}

// Delete parses a signed record ID and delegates eligible credential deletion
// to the administrator. Malformed text returns bad request; service failures
// use shared error mapping, and completion returns the existing success
// message.
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

// BatchDelete requires a nonempty record ID sequence and returns the
// administrator's deleted count. Invalid JSON returns bad request; the
// concrete administrator permits partial independent deletion, so a successful
// reply can report fewer deletes than supplied IDs.
// POST /api/v1/admin/redeem-codes/batch-delete
func (h *AdminRedeemHandler) BatchDelete(c *gin.Context) {
	var req RedeemCodeBatchDeleteRequest
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

// BatchUpdate requires its redemption service, binds IDs and optional fields,
// and delegates validated changes through that service. Missing configuration
// or invalid JSON stops first; service errors use shared mapping, and success
// reports the updated count.
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

// RedeemBatchUpdateFieldsFromDTO carries optional field pointers and the
// expiry presence/value pair into billing input. An omitted expiry stays
// unset, an explicit null carries Set true with a nil time, and a parsed
// timestamp retains its pointer. Keeping presence distinct lets the service
// separate an unchanged expiry from a requested clear.
//
// Other pointers are shared for synchronous validation and mutation; callers
// keep them unchanged while the service consumes the returned fields. This
// conversion performs no entitlement validation, so the service decides which
// batch fields are supported.
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

// Expire parses a signed record ID and asks the administrator to mark its
// stored status expired. Invalid numeric text returns bad request; service
// failures use shared mapping, and success maps the resulting credential view.
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

// Export returns the first ten thousand matching credentials as CSV using the
// same search and sort inputs as List. Sorting defaults to ID descending;
// finite values use two decimal places and timestamps omit offsets. Service or CSV
// write/flush failures return before attachment output, and optional
// recipient/time fields use empty cells.
// GET /api/v1/admin/redeem-codes/export
func (h *AdminRedeemHandler) Export(c *gin.Context) {
	codeType := c.Query("type")
	status := c.Query("status")
	search := normalizeRedeemSearch(c.Query("search"))
	sortBy := c.DefaultQuery("sort_by", "id")
	sortOrder := c.DefaultQuery("sort_order", "desc")

	// Limit the download to one page of ten thousand matches; any additional
	// matching credentials remain outside this CSV response.
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
		// CSV timestamps use second precision and omit zone offsets, exposing each
		// stored time's wall-clock text to spreadsheet readers.
		usedAt := ""
		if code.UsedAt != nil {
			usedAt = code.UsedAt.Format("2006-01-02 15:04:05")
		}
		expiresAt := ""
		if code.ExpiresAt != nil {
			expiresAt = code.ExpiresAt.Format("2006-01-02 15:04:05")
		}
		// Round every credential value to two decimal places in this CSV
		// representation.
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

	// Flush before setting download headers so buffered CSV errors become a JSON
	// failure instead of a partially published attachment.
	writer.Flush()
	if err := writer.Error(); err != nil {
		response.InternalError(c, "Failed to export redeem codes: "+err.Error())
		return
	}

	c.Header("Content-Type", "text/csv")
	c.Header("Content-Disposition", "attachment; filename=redeem_codes.csv")
	c.Data(200, "text/csv", buf.Bytes())
}
