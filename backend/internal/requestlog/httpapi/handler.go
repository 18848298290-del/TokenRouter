package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/requestlog"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Handler exposes request diagnostics to administrators.
type Handler struct {
	service *requestlog.Service
}

func NewHandler(service *requestlog.Service) *Handler {
	return &Handler{service: service}
}

// Find returns administrator-visible diagnostics for a request ID.
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
	if more {
		items = items[:100]
	}

	httpx.Success(c, gin.H{"items": items, "has_more": more})
}

// Health returns pending-write and failure counts.
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

// requireAdmin checks authenticated identity and role; query parameters cannot grant access.
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
