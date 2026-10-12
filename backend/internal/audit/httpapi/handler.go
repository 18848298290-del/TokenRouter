package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/audit"
	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Administrators inspect audit summaries through paginated filters and fetch
// redacted bodies through individual detail records. Clearing history verifies a
// fresh TOTP code and submits a synchronous trace, allowing the middleware to skip
// its usual asynchronous record after that transaction succeeds.

// AuditLogHandler exposes administrator audit-log queries and clearing.
// Clearing all records requires a fresh TOTP verification.
type AuditLogHandler struct {
	auditService *audit.AuditLogService // audit queries and transactional clearing
	totpService  TOTPVerifier           // fresh operation authorization verifier
}

// auditLogClearRequest supplies the fresh second factor for audit history clearing.
type auditLogClearRequest struct {
	TotpCode string `json:"totp_code" binding:"required"` // submitted one-time verification code
}

// TOTPVerifier validates the TOTP code supplied for this operation.
type TOTPVerifier interface {
	// VerifyCode checks the supplied user's fresh TOTP code before history clearing.
	// Verification errors, including missing setup and exhausted attempts, prevent
	// the handler from asking the repository to clear records.
	VerifyCode(context.Context, int64, string) error
}

// NewAuditLogHandler binds the audit service and TOTP verifier to administrator
// endpoints. The handler retains these dependencies for each request; app installs
// administrator authentication before registering its routes.
func NewAuditLogHandler(auditService *audit.AuditLogService, totpService TOTPVerifier) *AuditLogHandler {
	return &AuditLogHandler{
		auditService: auditService,
		totpService:  totpService,
	}
}

// List parses administrator audit filters and returns a paginated summary response.
// Actor identifiers must be positive and timestamps use RFC3339. A supplied
// success value selects successful statuses only when it equals "true"; other
// nonempty values select failure statuses, and page size is capped at two hundred.
// GET /api/v1/admin/audit-logs
func (h *AuditLogHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	if pageSize > 200 {
		// Bound response size before forwarding filters to the audit store.
		pageSize = 200
	}

	filter := &audit.AuditLogFilter{
		RequestID:  strings.TrimSpace(c.Query("request_id")),
		Page:       page,
		PageSize:   pageSize,
		ActorEmail: strings.TrimSpace(c.Query("actor_email")),
		AuthMethod: strings.TrimSpace(c.Query("auth_method")),
		Action:     strings.TrimSpace(c.Query("action")),
		Method:     strings.TrimSpace(c.Query("method")),
		ClientIP:   strings.TrimSpace(c.Query("client_ip")),
		Query:      strings.TrimSpace(c.Query("q")),
	}

	if v := strings.TrimSpace(c.Query("actor_user_id")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "Invalid actor_user_id")
			return
		}
		filter.ActorUserID = &id
	}
	if v := strings.TrimSpace(c.Query("start_time")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			response.BadRequest(c, "Invalid start_time, expect RFC3339")
			return
		}
		filter.StartTime = &t
	}
	if v := strings.TrimSpace(c.Query("end_time")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			response.BadRequest(c, "Invalid end_time, expect RFC3339")
			return
		}
		filter.EndTime = &t
	}
	if v := strings.TrimSpace(c.Query("success")); v != "" {
		success := v == "true"
		filter.Success = &success
	}

	result, err := h.auditService.List(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, result.Logs, int64(result.Total), result.Page, result.PageSize)
}

// Get returns an audit detail record including its stored redacted request body.
// The route identifier must be a positive decimal integer; repository absence
// and read errors are translated through the shared HTTP response helpers.
// GET /api/v1/admin/audit-logs/:id
func (h *AuditLogHandler) Get(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid audit log id")
		return
	}
	item, err := h.auditService.GetByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// Clear deletes all audit records and persists a trace of the operation.
// POST /api/v1/admin/audit-logs/clear
//
// Authorization and trace requirements:
//  1. Verify a fresh TOTP code for each clear, regardless of the step-up window.
//  2. Reject administrators who have not enabled TOTP.
//  3. Reject admin API keys.
//  4. Persist the actor, IP, user agent, and deleted count in the clearing transaction.
func (h *AuditLogHandler) Clear(c *gin.Context) {
	if c.GetString("auth_method") == audit.AuditAuthMethodAdminAPIKey {
		response.ErrorWithDetails(c, http.StatusForbidden,
			"Admin API key cannot clear audit logs; a two-factor verified admin session is required",
			"STEP_UP_ADMIN_API_KEY_FORBIDDEN", nil)
		return
	}

	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Unauthorized")
		return
	}

	var req auditLogClearRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorWithDetails(c, http.StatusBadRequest,
			"TOTP code is required to clear audit logs", "TOTP_CODE_REQUIRED", nil)
		return
	}

	// Verify this operation's code even after administrator middleware succeeds;
	// an existing step-up session cannot substitute for fresh authorization to erase history.
	if err := h.totpService.VerifyCode(c.Request.Context(), subject.UserID, strings.TrimSpace(req.TotpCode)); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	uid := subject.UserID
	role, _ := authctx.GetUserRoleFromContext(c)
	trace := &audit.AuditLog{
		ActorUserID:      &uid,
		ActorEmail:       c.GetString(ContextKeyAuthEmail),
		ActorRole:        role,
		AuthMethod:       c.GetString("auth_method"),
		CredentialMasked: MaskedRequestCredential(c),
		Method:           http.MethodPost,
		Path:             c.FullPath(),
		ClientIP:         identityhttp.SecurityClientIP(c),
		UserAgent:        c.Request.UserAgent(),
		StatusCode:       http.StatusOK,
	}
	if requestID := c.Writer.Header().Get("X-Request-ID"); requestID != "" {
		trace.RequestID = requestID
	}

	deleted, err := h.auditService.ClearAll(c.Request.Context(), trace)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// The clearing transaction already persisted its trace. Suppress middleware
	// recording for this request so one clear produces one retained administrative event.
	SkipAudit(c)
	response.Success(c, gin.H{"deleted": deleted})
}
