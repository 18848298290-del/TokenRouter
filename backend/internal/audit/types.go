package audit

import (
	"context"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	// Authentication labels stored in audit records.
	// AuditAuthMethodJWT and AuditAuthMethodAdminAPIKey match the middleware's auth_method values.
	AuditAuthMethodJWT         = "jwt"
	AuditAuthMethodAdminAPIKey = "admin_api_key"

	// auditRequestBodyMaxBytes limits encoded body bytes before the truncation marker is appended.
	auditRequestBodyMaxBytes = 16 * 1024
	// AuditRequestBodyCaptureLimit is the maximum raw body size accepted for redaction, in bytes.
	// The middleware reads one extra byte to detect oversized bodies, which become placeholders.
	AuditRequestBodyCaptureLimit = 256 * 1024

	// Fixed action names cover authentication and sensitive operations; other names derive from routes.
	AuditActionLogin                  = "auth.login"
	AuditActionLogin2FA               = "auth.login.2fa"
	AuditActionRegister               = "auth.register"
	AuditActionTokenRefresh           = "auth.token.refresh"
	AuditActionSessionBindingMismatch = "auth.session_binding.mismatch"
	AuditActionStepUpVerify           = "auth.step_up.verify"
	AuditActionAuditLogClear          = "admin.audit_log.clear"
	AuditActionUserSubscriptionRevoke = "user.subscriptions.revoke"
)

// ErrAuditLogNotFound indicates that the requested audit record does not exist.
var ErrAuditLogNotFound = infraerrors.NotFound("AUDIT_LOG_NOT_FOUND", "audit log not found")

// AuditLog records a management operation.
type AuditLog struct {
	ID               int64          `json:"id"`
	CreatedAt        time.Time      `json:"created_at"`
	ActorUserID      *int64         `json:"actor_user_id,omitempty"`
	ActorEmail       string         `json:"actor_email"`
	ActorRole        string         `json:"actor_role"`
	AuthMethod       string         `json:"auth_method"`
	CredentialMasked string         `json:"credential_masked"`
	Action           string         `json:"action"`
	Method           string         `json:"method"`
	Path             string         `json:"path"`
	RequestID        string         `json:"request_id"`
	ClientIP         string         `json:"client_ip"`
	UserAgent        string         `json:"user_agent"`
	RequestBody      string         `json:"request_body,omitempty"`
	StatusCode       int            `json:"status_code"`
	LatencyMs        int64          `json:"latency_ms"`
	Extra            map[string]any `json:"extra,omitempty"`
}

// AuditLogFilter selects and paginates audit records.
type AuditLogFilter struct {
	RequestID string
	Page      int
	PageSize  int

	StartTime   *time.Time
	EndTime     *time.Time
	ActorUserID *int64
	ActorEmail  string
	AuthMethod  string
	Action      string
	Method      string
	ClientIP    string
	// Success selects all statuses when nil, statuses below 400 when true, and statuses at least 400 when false.
	Success *bool
	// Query matches substrings of path, action, or actor_email.
	Query string
}

// AuditLogList is a page of audit records and its pagination metadata.
type AuditLogList struct {
	Logs     []*AuditLog
	Total    int
	Page     int
	PageSize int
}

// AuditLogRepository appends, queries, and clears audit records.
type AuditLogRepository interface {
	BatchInsert(ctx context.Context, logs []*AuditLog) (int64, error)
	// Insert synchronously persists one record.
	Insert(ctx context.Context, log *AuditLog) error
	List(ctx context.Context, filter *AuditLogFilter) (*AuditLogList, error)
	GetByID(ctx context.Context, id int64) (*AuditLog, error)
	Count(ctx context.Context) (int64, error)
	// ClearWithTrace clears records and persists the clearing trace in one transaction.
	ClearWithTrace(context.Context, *AuditLog) (int64, error)
	// TruncateAll supports repository compatibility; production clearing uses ClearWithTrace.
	TruncateAll(ctx context.Context) error
	// DeleteBefore deletes an expired batch and returns its row count; repeated or concurrent calls are safe.
	DeleteBefore(ctx context.Context, cutoff time.Time, batchSize int) (int64, error)
}
