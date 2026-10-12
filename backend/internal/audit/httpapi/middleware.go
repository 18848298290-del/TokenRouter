package httpapi

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/audit"
	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
)

// Handlers and authentication middleware use these Gin context keys to supply audit metadata.
const (
	auditCtxKeyAction     = "audit_action"
	auditCtxKeyActorID    = "audit_actor_id"
	auditCtxKeyActorEmail = "audit_actor_email"
	auditCtxKeySkip       = "audit_skip"
	// ContextKeyAuthEmail holds the email supplied by authentication middleware.
	ContextKeyAuthEmail = "auth_email"
)

var (
	// auditSensitiveReads maps audited GET method/route pairs to action names.
	auditSensitiveReads = map[string]string{
		"GET /api/v1/admin/providers/data":            "admin.providers.export",
		"GET /api/v1/admin/proxies/data":              "admin.proxies.export",
		"GET /api/v1/admin/redeem-codes/export":       "admin.redeem_codes.export",
		"GET /api/v1/admin/backups/:id/download-url":  "admin.backups.download",
		"GET /api/v1/admin/backups/:id/download":      "admin.backups.download",
		"GET /api/v1/admin/settings/admin-api-key":    "admin.admin_api_key.read",
		"GET /api/v1/admin/users/:id/api-keys":        "admin.users.api_keys.read",
		"GET /api/v1/admin/groups/:id/api-keys":       "admin.groups.api_keys.read",
		"GET /api/v1/admin/backups/s3-config":         "admin.backups.s3_config.read",
		"GET /api/v1/admin/backups/storage-config":    "admin.backups.storage_config.read",
		"GET /api/v1/admin/data-management/s3/config": "admin.data_management.s3_config.read",
	}

	// auditActionOverrides supplies fixed action names before route-based derivation.
	auditActionOverrides = map[string]string{
		"POST /api/v1/auth/login":                                 audit.AuditActionLogin,
		"POST /api/v1/auth/login/2fa":                             audit.AuditActionLogin2FA,
		"POST /api/v1/auth/register":                              audit.AuditActionRegister,
		"POST /api/v1/auth/refresh":                               audit.AuditActionTokenRefresh,
		"POST /api/v1/user/totp/step-up":                          audit.AuditActionStepUpVerify,
		"POST /api/v1/admin/audit-logs/clear":                     audit.AuditActionAuditLogClear,
		"POST /api/v1/admin/providers/data":                       "admin.providers.import",
		"POST /api/v1/admin/providers/:id/upstream-usage/query":   "admin.providers.upstream_usage.query",
		"POST /api/v1/admin/providers/upstream-usage/query/batch": "admin.providers.upstream_usage.query_batch",
		"POST /api/v1/admin/backups":                              "admin.backups.create",
		"POST /api/v1/admin/backups/:id/restore":                  "admin.backups.restore",
		"DELETE /api/v1/admin/backups/:id":                        "admin.backups.delete",
		"PUT /api/v1/admin/backups/s3-config":                     "admin.backups.s3_config.update",
		"POST /api/v1/admin/settings/admin-api-key/regenerate":    "admin.admin_api_key.regenerate",
		"DELETE /api/v1/admin/settings/admin-api-key":             "admin.admin_api_key.delete",
		"POST /api/v1/subscriptions/:id/revoke":                   audit.AuditActionUserSubscriptionRevoke,
	}

	// auditBodyOmittedRoutes lists routes whose bodies contain credentials embedded in strings.
	// Key-based redaction cannot safely inspect these values, so the entire body is omitted.
	auditBodyOmittedRoutes = map[string]struct{}{
		"POST /api/v1/auth/passkey/login/finish":                     {},
		"POST /api/v1/user/passkeys/register/finish":                 {},
		"POST /api/v1/admin/providers/import/codex-session":          {},
		"PUT /api/v1/admin/providers/:id/ollama-cloud-usage/session": {},
	}
)

// AuditLogMiddleware distinguishes audit middleware for Wire injection.
type AuditLogMiddleware gin.HandlerFunc

// restoredBody combines the captured prefix with the unread original body.
// Handlers receive the complete body; Close delegates to the original body.
type restoredBody struct {
	io.Reader
	closer io.Closer
}

// SetAuditAction overrides the action derived for the current request.
func SetAuditAction(c *gin.Context, action string) {
	c.Set(auditCtxKeyAction, action)
}

// SetAuditActor supplies or overrides the actor, including during login before authentication.
func SetAuditActor(c *gin.Context, userID int64, email string) {
	if userID > 0 {
		c.Set(auditCtxKeyActorID, userID)
	}
	if email != "" {
		c.Set(auditCtxKeyActorEmail, email)
	}
}

// SkipAudit suppresses the audit record for the current request.
func SkipAudit(c *gin.Context) {
	c.Set(auditCtxKeySkip, true)
}

// NewAuditLogMiddleware records mutations and selected sensitive reads.
// It records POST, PUT, PATCH, DELETE, and allowlisted GET requests.
// Mount it after authentication for admin, user, and admin-payment groups so rejected authentication is excluded.
// The auth group has no preceding authentication and therefore includes failed login, registration, and refresh attempts.
func NewAuditLogMiddleware(auditService *audit.AuditLogService, redactor *audit.Redactor) AuditLogMiddleware {
	return AuditLogMiddleware(func(c *gin.Context) {
		routeKey := c.Request.Method + " " + c.FullPath()

		record := false
		action := ""
		switch c.Request.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			record = true
			if v, ok := auditActionOverrides[routeKey]; ok {
				action = v
			}
		case http.MethodGet:
			if v, ok := auditSensitiveReads[routeKey]; ok {
				record = true
				action = v
			}
		}
		if !record {
			c.Next()
			return
		}

		// Restore captured bytes so downstream JSON binding can read the complete body.
		// Read only the capture limit plus one byte and retain the unread stream,
		// avoiding a second full in-memory copy of large import bodies.
		var bodyRedacted string
		if _, omit := auditBodyOmittedRoutes[routeKey]; omit {
			bodyRedacted = "<credential-bearing body omitted>"
		} else if c.Request.Body != nil && c.Request.Method != http.MethodGet {
			orig := c.Request.Body
			raw, err := io.ReadAll(io.LimitReader(orig, audit.AuditRequestBodyCaptureLimit+1))
			c.Request.Body = &restoredBody{
				Reader: io.MultiReader(bytes.NewReader(raw), orig),
				closer: orig,
			}
			if err == nil {
				bodyRedacted = redactor.RedactBody(raw, c.GetHeader("Content-Type"))
			}
		}

		start := time.Now()
		c.Next()

		if c.GetBool(auditCtxKeySkip) {
			return
		}

		status := c.Writer.Status()
		// Successful refreshes are routine high-volume events; retain failures for investigation.
		if routeKey == "POST /api/v1/auth/refresh" && status < 400 {
			return
		}

		entry := &audit.AuditLog{
			CreatedAt:   time.Now().UTC(),
			Action:      action,
			Method:      c.Request.Method,
			Path:        c.FullPath(),
			ClientIP:    identityhttp.SecurityClientIP(c),
			UserAgent:   c.Request.UserAgent(),
			RequestBody: bodyRedacted,
			StatusCode:  status,
			LatencyMs:   time.Since(start).Milliseconds(),
		}
		if entry.Path == "" {
			entry.Path = c.Request.URL.Path
		}
		if entry.Action == "" {
			entry.Action = deriveAuditAction(c.Request.Method, entry.Path)
		}
		if v, ok := c.Get(auditCtxKeyAction); ok {
			if s, ok := v.(string); ok && s != "" {
				entry.Action = s
			}
		}
		if requestID, ok := c.Request.Context().Value(telemetry.RequestID).(string); ok {
			entry.RequestID = requestID
		}

		// Read authentication metadata first, then apply any actor overrides supplied by the handler.
		if subject, ok := authctx.GetAuthSubjectFromContext(c); ok && subject.UserID > 0 {
			uid := subject.UserID
			entry.ActorUserID = &uid
		}
		if role, ok := authctx.GetUserRoleFromContext(c); ok {
			entry.ActorRole = role
		}
		entry.ActorEmail = c.GetString(ContextKeyAuthEmail)
		entry.AuthMethod = c.GetString("auth_method")
		if entry.AuthMethod == "" && entry.ActorUserID != nil {
			entry.AuthMethod = audit.AuditAuthMethodJWT
		}
		if v, ok := c.Get(auditCtxKeyActorID); ok {
			if id, ok := v.(int64); ok && id > 0 {
				entry.ActorUserID = &id
			}
		}
		if v, ok := c.Get(auditCtxKeyActorEmail); ok {
			if s, ok := v.(string); ok && s != "" {
				entry.ActorEmail = s
			}
		}

		// Mask credentials before storing request-header metadata.
		entry.CredentialMasked = MaskedRequestCredential(c)

		extra := map[string]any{}
		if len(c.Params) > 0 {
			params := make(map[string]string, len(c.Params))
			for _, p := range c.Params {
				params[p.Key] = p.Value
			}
			extra["params"] = params
		}
		if q := audit.RedactAuditQuery(c.Request.URL.RawQuery); q != "" {
			extra["query"] = q
		}
		if len(extra) > 0 {
			entry.Extra = extra
		}

		auditService.Record(entry)
	})
}

func (b *restoredBody) Close() error { return b.closer.Close() }

// MaskedRequestCredential extracts and masks the API key or Authorization credential.
func MaskedRequestCredential(c *gin.Context) string {
	if apiKey := strings.TrimSpace(c.GetHeader("x-api-key")); apiKey != "" {
		return "x-api-key " + audit.MaskAuditCredential(apiKey)
	}
	authHeader := strings.TrimSpace(c.GetHeader("Authorization"))
	if authHeader == "" {
		return ""
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) == 2 {
		return parts[0] + " " + audit.MaskAuditCredential(strings.TrimSpace(parts[1]))
	}
	return audit.MaskAuditCredential(authHeader)
}

// deriveAuditAction combines the method and route template into an action name.
// For example, PUT /api/v1/admin/providers/:id becomes admin.providers.update.
func deriveAuditAction(method, fullPath string) string {
	path := strings.TrimPrefix(fullPath, "/api/v1/")
	path = strings.Trim(path, "/")
	segs := strings.Split(path, "/")
	parts := make([]string, 0, len(segs))
	for _, seg := range segs {
		if seg == "" || strings.HasPrefix(seg, ":") || strings.HasPrefix(seg, "*") {
			continue
		}
		parts = append(parts, strings.ReplaceAll(seg, "-", "_"))
	}
	verb := ""
	switch method {
	case "POST":
		verb = "create"
	case "PUT", "PATCH":
		verb = "update"
	case "DELETE":
		verb = "delete"
	case "GET":
		verb = "read"
	default:
		verb = strings.ToLower(method)
	}
	if len(parts) == 0 {
		return verb
	}
	return strings.Join(parts, ".") + "." + verb
}
