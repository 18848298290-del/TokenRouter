package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/requestlog"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// handler.go serves administrator request-detail lookups and local writer
// health responses.
// Request diagnostics can contain upstream identifiers, provider metadata, and
// error or audit references. The handler checks an authenticated administrator
// before reading details or local writer health, then bounds lookup text and
// truncates the displayed result collection with an explicit continuation flag.

// Handler exposes administrator request diagnostics and the local
// request-writer health counts.
type Handler struct {
	service *requestlog.Service // shared request persistence and buffer service
}

// NewHandler binds the shared request-recording service to administrator
// diagnostic HTTP handlers.
func NewHandler(service *requestlog.Service) *Handler {
	return &Handler{service: service}
}

// Find returns request diagnostics after validating an authenticated
// administrator and lookup text. A trimmed nonempty query request_id takes
// precedence over the trimmed path value; empty, oversized, or
// control-character values are rejected before storage.
//
// The service receives authenticated UserID and trusted admin=true, independent
// of query flags. At most one hundred returned details are displayed, and
// has_more reports truncation of the already collected result. This adapter
// retains source order and does not provide a cursor for the omitted entries.
// @project-doc docs/operations/request_lookup.md#request_query
func (h *Handler) Find(c *gin.Context) {
	subject, ok := requireAdmin(c)
	if !ok {
		return
	}
	id := strings.TrimSpace(c.Query("request_id"))
	if id == "" {
		id = strings.TrimSpace(c.Param("request_id"))
	}
	// Reject empty or oversized lookup text and line/NUL controls before querying
	// retained external aliases, which share the correlation-ID length limit.
	if id == "" || len(id) > 255 || strings.ContainsAny(id, "\r\n\x00") {
		httpx.BadRequest(c, "Invalid request_id")
		return
	}
	items, err := h.service.Find(c.Request.Context(), id, subject.UserID, true)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	more := len(items) > 100
	// Return the first hundred collected details and signal omitted entries;
	// source queries can each contribute their own limit before this display cap.
	if more {
		items = items[:100]
	}

	httpx.Success(c, gin.H{"items": items, "has_more": more})
}

// Health returns local pending-ID and cumulative failure observations after
// requiring an authenticated administrator. It uses the shared service's nearby
// samples, while authorization failures return before any health data is
// exposed.
func (h *Handler) Health(c *gin.Context) {
	if _, ok := requireAdmin(c); !ok {
		return
	}
	health, err := h.service.Health()
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	httpx.Success(c, health)
}

// requireAdmin returns the authenticated subject when it has a positive user ID
// and the exact admin role. Missing identity returns HTTP 401 and other roles
// return 403; query parameters do not confer either value.
func requireAdmin(c *gin.Context) (authctx.AuthSubject, bool) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		httpx.Unauthorized(c, "User not authenticated")
		return authctx.AuthSubject{}, false
	}
	role, ok := authctx.GetUserRoleFromContext(c)
	if !ok || role != "admin" {
		httpx.Forbidden(c, "Admin access required")
		return authctx.AuthSubject{}, false
	}
	return subject, true
}
