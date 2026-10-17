package ops

import (
	"strings"
	"time"
)

// models.go defines error and system-log query models and applies display filters.
// An error query combines captured request facts with current user, provider,
// group, and API-key display metadata. Display status prefers a nonnull
// upstream value, while client-status guards and query-time SLA policy decide
// which observations are selected. Manual resolution is separate from those
// classifications, and user conversion reads this query view rather than the
// original wire response.
//
// Structured system-log fields remain available as decoded business and error
// context. Request identifiers correlate those logs with request records and
// usage, while complete usage performance is read from the usage row. Error-list
// selectors retain their distinct wildcard, model, status, and identity rules;
// repository queries apply those selectors before assembling display metadata.

// OpsSystemLog carries a persisted structured observation and optional request
// correlation metadata.
type OpsSystemLog struct {
	ID              int64     `json:"id"`                // persisted system-log row identity
	CreatedAt       time.Time `json:"created_at"`        // stored observation creation time
	Host            string    `json:"host"`              // recorded instance host label
	Level           string    `json:"level"`             // recorded logging severity label
	Component       string    `json:"component"`         // recorded logging component name
	Message         string    `json:"message"`           // captured display message text
	RequestID       string    `json:"request_id"`        // stored platform request identifier
	ClientRequestID string    `json:"client_request_id"` // stored external client identifier
	UserID          *int64    `json:"user_id"`           // optional recorded user identity
	APIKeyID        *int64    `json:"api_key_id"`        // optional recorded API-key identity
	ProviderID      *int64    `json:"provider_id"`       // optional recorded provider identity
	Platform        string    `json:"platform"`          // recorded execution platform snapshot
	Model           string    `json:"model"`             // recorded request model label
	// Extra contains decoded structured fields returned by PostgreSQL queries.
	// Consumers own the map result and its decoded nested values. These fields
	// provide log context; usage performance is obtained through usage queries.
	Extra map[string]any `json:"extra,omitempty"` // decoded structured event fields
}

// OpsErrorLog carries query display metadata for one saved error observation.
type OpsErrorLog struct {
	ID        int64     `json:"id"`         // persisted error observation identity
	CreatedAt time.Time `json:"created_at"` // stored error creation time

	// Phase uses the stored classification, with account_auth translated to
	// provider_auth by PostgreSQL list and detail readers. Other values are
	// returned as saved.
	Phase string `json:"phase"` // reported error execution phase
	Type  string `json:"type"`  // stored error-type classification label

	Owner  string `json:"error_owner"`  // client/provider/platform
	Source string `json:"error_source"` // client_request/upstream_http/gateway

	Severity string `json:"severity"` // recorded P0-P3 severity label

	// StatusCode is the query display value, preferring nonnull
	// upstream_status_code, then client status_code, then zero. It can differ from
	// the final client response status.
	StatusCode int    `json:"status_code"` // effective query display status
	Platform   string `json:"platform"`    // recorded execution platform snapshot
	Model      string `json:"model"`       // recorded execution model label

	// Resolved, ResolvedAt, ResolvedByUserID, and ResolvedByUserName describe
	// manual resolution separately from the error classification. PostgreSQL joins
	// the resolving user email for list rows; detail reads can leave that display
	// name empty.
	Resolved           bool       `json:"resolved"`              // stored manual resolution flag
	ResolvedAt         *time.Time `json:"resolved_at"`           // optional resolution recording time
	ResolvedByUserID   *int64     `json:"resolved_by_user_id"`   // optional resolving user identity
	ResolvedByUserName string     `json:"resolved_by_user_name"` // joined resolving user email
	// ResolvedStatusRaw is excluded from JSON and remains unpopulated by current
	// repository readers. No backend field consumer is present; normal query
	// results leave this internal value empty.
	ResolvedStatusRaw string `json:"-"` // unused internal status text

	ClientRequestID string `json:"client_request_id"` // stored external client identifier
	RequestID       string `json:"request_id"`        // stored platform request identifier
	Message         string `json:"message"`           // captured error description text

	UserID       *int64 `json:"user_id"`       // optional recorded user identity
	UserEmail    string `json:"user_email"`    // current joined user email
	APIKeyID     *int64 `json:"api_key_id"`    // optional recorded API-key identity
	ProviderID   *int64 `json:"provider_id"`   // optional recorded provider identity
	ProviderName string `json:"provider_name"` // current joined provider name
	GroupID      *int64 `json:"group_id"`      // optional recorded group identity
	GroupName    string `json:"group_name"`    // current joined group name

	ClientIP    *string `json:"client_ip"`    // optional recorded client address
	RequestPath string  `json:"request_path"` // captured incoming request path
	Stream      bool    `json:"stream"`       // observed streaming request flag

	InboundEndpoint  string `json:"inbound_endpoint"`  // captured incoming endpoint label
	UpstreamEndpoint string `json:"upstream_endpoint"` // captured upstream endpoint label
	RequestedModel   string `json:"requested_model"`   // captured caller model label
	UpstreamModel    string `json:"upstream_model"`    // captured upstream model label
	RequestType      *int16 `json:"request_type"`      // optional request protocol code
	UserAgent        string `json:"user_agent"`        // captured request user agent

	// APIKeyName and APIKeyDeleted come from the current api_keys join, including
	// soft-deleted rows. The retained name can remain visible after key
	// replacement, while a missing joined row supplies an empty name.
	APIKeyName string `json:"api_key_name,omitempty"` // current joined API-key name
	// APIKeyDeleted is true when the joined api_keys.deleted_at is nonnull. False
	// covers active keys and missing joined key rows, so it does not establish
	// current key existence.
	APIKeyDeleted bool `json:"api_key_deleted,omitempty"` // joined API-key soft-deletion flag
}

// OpsErrorLogDetail adds captured response and latency evidence to an error
// observation.
type OpsErrorLogDetail struct {
	OpsErrorLog // base error query display fields

	// ErrorBody returns previously captured response text. Reading this detail
	// does not apply another redactor; supported JSON sanitation and plain-text
	// truncation follow the earlier capture and preparation paths.
	ErrorBody string `json:"error_body"` // stored captured error body

	// optional stored upstream status
	UpstreamStatusCode *int `json:"upstream_status_code,omitempty"`
	// stored upstream error description
	UpstreamErrorMessage string `json:"upstream_error_message,omitempty"`
	// stored upstream diagnostic text
	UpstreamErrorDetail string `json:"upstream_error_detail,omitempty"`
	// UpstreamErrors contains saved attempt JSON text for display and
	// ParseOpsUpstreamErrors. PostgreSQL trims the text and converts serialized
	// JSON null to an empty string.
	UpstreamErrors string `json:"upstream_errors,omitempty"` // serialized attempt JSON array

	// AuthLatencyMs, RoutingLatencyMs, UpstreamLatencyMs, ResponseLatencyMs, and
	// These fields contain captured phase durations
	// in milliseconds. Nil means absent evidence; these phase measurements differ
	// from the access-log event offsets in OpsRequestTiming.
	AuthLatencyMs     *int64 `json:"auth_latency_ms"`     // milliseconds
	RoutingLatencyMs  *int64 `json:"routing_latency_ms"`  // milliseconds
	UpstreamLatencyMs *int64 `json:"upstream_latency_ms"` // milliseconds
	ResponseLatencyMs *int64 `json:"response_latency_ms"` // milliseconds

	// IsBusinessLimited is the stored capture decision. Query-time ignored-status
	// policy can exclude additional client-side failures, so false alone does not
	// establish SLA eligibility.
	IsBusinessLimited bool `json:"is_business_limited"` // stored local SLA-exclusion flag

	// APIKeyPrefix is captured before asynchronous persistence and can remain
	// after key deletion. Gateway producers select the first eight bytes of the
	// bound key, rather than eight Unicode characters.
	APIKeyPrefix string `json:"api_key_prefix,omitempty"` // captured API-key byte prefix
}

// OpsErrorLogFilter selects saved error observations with query-time category
// and display rules.
type OpsErrorLogFilter struct {
	// StartTime and EndTime apply independent timestamp comparisons when nonnil
	// and nonzero. PostgreSQL converts them to UTC and uses inclusive
	// start/exclusive end; absent endpoints impose no corresponding time
	// condition.
	StartTime *time.Time // optional inclusive observation start
	EndTime   *time.Time // optional exclusive observation end

	Platform string // exact recorded platform selector
	// GroupID and ProviderID filter saved row identities when positive. Nil or
	// nonpositive values omit their individual condition; provider/group names are
	// read from current joins.
	GroupID    *int64 // optional positive group selection
	ProviderID *int64 // optional positive provider selection

	// StatusCodes selects effective upstream-first display statuses when nonempty
	// and takes precedence over StatusCodesOther. The latter excludes the fixed
	// common-status set only when no explicit list is supplied.
	StatusCodes      []int // requested effective status values
	StatusCodesOther bool  // exclude common effective statuses
	// Phase is trimmed and lowercased for equality matching. provider_auth also
	// includes legacy account_auth rows; this selection participates in
	// recovered-row eligibility when its explicit opt-in is set.
	Phase  string // requested stored phase selector
	Owner  string // requested error owner selector
	Source string // requested error source selector
	// Resolved compares the saved resolution bool when supplied, including false.
	// Nil retains both resolved and unresolved rows independently of the category
	// View.
	Resolved *bool // optional saved resolution selector
	// Query performs case-insensitive substring matching across stored request
	// IDs, client IDs, and error message text. Caller percent and underscore
	// wildcard characters remain active.
	Query string // request-ID/message wildcard search
	// UserQuery searches the currently joined users.email using a
	// surrounding-wildcard ILIKE pattern. Caller wildcard characters remain
	// active, and a matching user row is required.
	UserQuery string // joined user-email wildcard search

	// RequestID invokes request_lookup_ids and matches candidate platform
	// identifiers. Client-ID candidates are also matched when the supplied text is
	// not an exact local request_records identity.
	RequestID string // request correlation lookup identifier
	// ClientRequestID matches trimmed nonempty text against the stored
	// client_request_id exactly. It adds its own AND condition when RequestID is
	// also supplied.
	ClientRequestID string // exact external client identifier

	// UserID and APIKeyID filter saved identities when positive, adding separate
	// AND conditions. The user-facing service supplies its authenticated owner
	// UserID while retaining a requested APIKeyID.
	UserID   *int64 // optional positive user selection
	APIKeyID *int64 // optional positive API-key selection

	// Model matches COALESCE(requested_model, model, empty text) after trimming
	// input, so a stored nonnull empty requested_model wins. ModelFuzzy uses an
	// escaped ILIKE substring; false uses equality. Display sorting separately
	// handles blank requested names.
	Model      string // requested-model SQL selector
	ModelFuzzy bool   // escaped model substring selector

	ExcludeCountTokens bool // exclude captured token-count probes

	// IncludeRecoveredUpstream removes the client-status guard only for a specific
	// upstream/provider_auth phase, or an otherwise empty phase with an ANY list
	// containing only those phases. Other scopes retain the status-at-least-400 or
	// cyber_policy guard.
	IncludeRecoveredUpstream bool // qualified recovered-provider row opt-in

	// ErrorPhasesAny and ErrorTypesAny add separate AND/ANY conditions alongside
	// Phase. A literal provider_auth phase-list entry adds account_auth
	// compatibility; these lists do not by themselves remove the default
	// client-status guard.
	ErrorPhasesAny []string // additional phase ANY selector
	ErrorTypesAny  []string // additional type ANY selector

	// View uses errors for empty or unknown input, applying the SLA-countable
	// predicate. excluded selects business exclusions and all omits that
	// predicate; independent status, identity, and probe conditions still apply.
	View string // errors/excluded/all

	// IgnoredStatusCodes uses repository defaults when nil and no exclusions when
	// empty. Service reads replace nil with runtime policy; upstream context can
	// keep an otherwise ignored client status countable.
	IgnoredStatusCodes []int // query-time client exclusion policy

	// Page and PageSize default to one and twenty in ListErrorLogs when
	// nonpositive, with page size capped at five hundred. The repository returns
	// normalized pagination separately from these caller values.
	Page     int // one-based requested observation page
	PageSize int // requested observations per page

	// SortBy and SortOrder are trimmed and lowercased by storage. Supported
	// columns are created_at, model, and upstream-first status_code; unknown
	// columns use created_at, and only asc selects ascending order. Row ID breaks
	// ties in that same direction.
	SortBy    string // requested display sort column
	SortOrder string // requested display sort direction
}

// OpsErrorLogList carries normalized pagination and returned error query
// display rows.
type OpsErrorLogList struct {
	Errors   []*OpsErrorLog `json:"errors"`    // returned error display observations
	Total    int            `json:"total"`     // matching stored observation count
	Page     int            `json:"page"`      // one-based returned observation page
	PageSize int            `json:"page_size"` // returned observations per page
}

// SetSort trims and stores requested sort text in a nonnil filter, retaining
// its case and other supplied content. Column allowlisting, case folding,
// direction defaults, and tie ordering are applied later by the PostgreSQL
// reader.
func (f *OpsErrorLogFilter) SetSort(sortBy, sortOrder string) {
	f.SortBy = strings.TrimSpace(sortBy)
	f.SortOrder = strings.TrimSpace(sortOrder)
}
