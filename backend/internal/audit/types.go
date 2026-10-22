package audit

import (
	"context"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// types.go defines management audit records, query filters, and the repository
// operations used to store and inspect them.
// Management audit records retain the actor and the request outcome together so
// administrators can inspect sensitive operations after they finish. List queries
// omit captured bodies, while detail queries expose the already redacted body and
// the metadata supplied by the request handler.

const (
	// Authentication labels match the middleware's auth_method values and distinguish
	// session-based administrators from API-key callers in stored records and filters.
	AuditAuthMethodJWT         = "jwt"           // authenticated JWT session
	AuditAuthMethodAdminAPIKey = "admin_api_key" // administrator API key

	// Body limits bound the raw data inspected by the redactor and the encoded text
	// retained in the database. Middleware reads one extra byte to detect overflow.
	auditRequestBodyMaxBytes     = 16 * 1024  // encoded bytes before truncation marker
	AuditRequestBodyCaptureLimit = 256 * 1024 // raw bytes accepted for redaction

	// Fixed action names identify authentication events and sensitive operations whose
	// meaning needs to remain stable when HTTP routes or method-based names change.
	AuditActionLogin                  = "auth.login"                    // initial login attempt
	AuditActionLogin2FA               = "auth.login.2fa"                // login second-factor verification
	AuditActionRegister               = "auth.register"                 // user registration attempt
	AuditActionTokenRefresh           = "auth.token.refresh"            // session token refresh
	AuditActionSessionBindingMismatch = "auth.session_binding.mismatch" // rejected session binding
	AuditActionStepUpVerify           = "auth.step_up.verify"           // sensitive-operation second-factor verification
	AuditActionAuditLogClear          = "admin.audit_log.clear"         // audit history clearing
	AuditActionUserSubscriptionRevoke = "user.subscriptions.revoke"     // user subscription revocation
)

// ErrAuditLogNotFound identifies a missing audit record so HTTP callers can return
// a not-found response rather than treating the lookup as a storage failure.
var ErrAuditLogNotFound = infraerrors.NotFound("AUDIT_LOG_NOT_FOUND", "audit log not found")

// AuditLog records a management operation and its observed request outcome.
type AuditLog struct {
	ID int64 `json:"id"` // database record identifier
	// CreatedAt timestamps the event. Record and PostgreSQL insertion fill a zero
	// value with the current UTC time before the observation reaches durable storage.
	CreatedAt time.Time `json:"created_at"`
	// ActorUserID is absent for attempts with no identified user; storage also treats
	// nonpositive identifiers as absent while retaining the supplied email and role.
	ActorUserID      *int64 `json:"actor_user_id,omitempty"`
	ActorEmail       string `json:"actor_email"`       // observed actor email
	ActorRole        string `json:"actor_role"`        // observed authorization role
	AuthMethod       string `json:"auth_method"`       // request authentication method
	CredentialMasked string `json:"credential_masked"` // masked request credential
	Action           string `json:"action"`            // classified operation name
	Method           string `json:"method"`            // request HTTP method
	// Path identifies the requested operation. Audit middleware uses the matched
	// route template, or the request URL path when Gin has no matching template.
	Path      string `json:"path"`
	RequestID string `json:"request_id"` // platform request identifier
	ClientIP  string `json:"client_ip"`  // security-resolved client address
	UserAgent string `json:"user_agent"` // request user agent
	// RequestBody holds captured text or an omission marker supplied by audit
	// middleware. PostgreSQL List clears it; GetByID returns the stored text unchanged.
	RequestBody string `json:"request_body,omitempty"`
	StatusCode  int    `json:"status_code"` // final HTTP status
	LatencyMs   int64  `json:"latency_ms"`  // milliseconds
	// Extra holds event metadata, including middleware route parameters and redacted
	// query text. Record shares this map and its mutable values with the asynchronous
	// writer, so callers finish mutations before enqueueing.
	Extra map[string]any `json:"extra,omitempty"`
}

// AuditLogFilter selects audit records and requests one page of results.
type AuditLogFilter struct {
	RequestID string // request identifier or external alias
	// Page and PageSize are normalized by the PostgreSQL store to a positive page
	// and a bounded page size. Callers may leave both zero for the defaults.
	Page     int // one-based page number
	PageSize int // requested records per page

	// StartTime and EndTime bound created_at inclusively after conversion to UTC.
	// A nil endpoint leaves that side unrestricted; both predicates apply when supplied.
	StartTime *time.Time // inclusive earliest event time
	EndTime   *time.Time // inclusive latest event time
	// ActorUserID restricts results to one stored actor when supplied. Nil leaves
	// the actor unrestricted, including records produced before a user was identified.
	ActorUserID *int64
	ActorEmail  string // case-insensitive actor email substring
	AuthMethod  string // exact authentication method
	Action      string // case-insensitive action substring
	Method      string // uppercase HTTP method
	ClientIP    string // exact client address
	// Success selects all statuses when nil, statuses below 400 when true, and
	// statuses at least 400 when false, matching the HTTP list query's classification.
	Success *bool
	Query   string // path, action, email, or credential-prefix substring
}

// AuditLogList is a page of audit records and its pagination metadata.
type AuditLogList struct {
	Logs     []*AuditLog // records in this page
	Total    int         // total matching records
	Page     int         // normalized one-based page number
	PageSize int         // normalized records per page
}

// AuditLogRepository stores management audit records and supports administrative queries.
type AuditLogRepository interface {
	// BatchInsert persists nonnil records together and reports the batch's processed
	// row count. An error means callers cannot interpret a positive count as committed database rows.
	BatchInsert(ctx context.Context, logs []*AuditLog) (int64, error)
	// Insert synchronously persists one supplied management-operation audit record.
	Insert(ctx context.Context, log *AuditLog) error
	// List returns a normalized page and total using the supplied filters.
	// A nil filter selects defaults; list records omit captured bodies for separate detail lookup.
	List(ctx context.Context, filter *AuditLogFilter) (*AuditLogList, error)
	// GetByID returns a full audit record with its stored redacted body.
	// Missing identifiers return ErrAuditLogNotFound so callers can distinguish absence from query failures.
	GetByID(ctx context.Context, id int64) (*AuditLog, error)
	// Count returns the number of audit records currently stored.
	Count(ctx context.Context) (int64, error)
	// ClearWithTrace clears the current records and persists the supplied clearing
	// trace in one transaction, returning the deleted count. Callers authorize the
	// operation before invoking the repository and provide a nonnil trace.
	//
	// Counting, clearing, and trace insertion succeed or roll back together, so the
	// retained trace describes the history removed by this operation. A failed
	// insertion leaves the prior records available for later administrative inspection.
	ClearWithTrace(context.Context, *AuditLog) (int64, error)
	// TruncateAll clears the audit table for repository compatibility and tests.
	// Administrative callers use ClearWithTrace, which retains the authenticated
	// actor and deleted count together in its clearing transaction.
	TruncateAll(ctx context.Context) error
	// DeleteBefore removes up to batchSize records older than cutoff and returns
	// their count. A nonpositive batch size selects the repository default; callers repeat until empty.
	DeleteBefore(ctx context.Context, cutoff time.Time, batchSize int) (int64, error)
}
