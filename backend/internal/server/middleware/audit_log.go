package middleware

import (
	"github.com/gin-gonic/gin"

	audit "github.com/TokenFlux/TokenRouter/internal/audit/httpapi"
)

// audit_log.go exposes audit action and actor setters to HTTP route handlers.
// Management handlers can refine an audit record with the operation name and
// the actor identified during execution. The audit HTTP adapter reads those
// values after the handler returns, while this middleware package exposes the
// adapter's type and setters to route assembly.

// AuditLogMiddleware names the audit adapter's Gin middleware for route
// assembly.
type AuditLogMiddleware = audit.AuditLogMiddleware

// SetAuditAction stores the handler-selected operation for the audit adapter to
// read after execution. An empty name leaves the adapter's fixed or
// route-derived action in effect.
func SetAuditAction(c *gin.Context, s string) { audit.SetAuditAction(c, s) }

// SetAuditActor supplies actor overrides for the completed request's audit
// record. The adapter accepts positive IDs and nonempty emails independently,
// including actors identified during unauthenticated login handling.
func SetAuditActor(c *gin.Context, id int64, email string) { audit.SetAuditActor(c, id, email) }
