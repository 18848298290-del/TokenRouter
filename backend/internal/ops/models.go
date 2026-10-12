package ops

import (
	"encoding/json"
	"strings"
	"time"
)

type OpsSystemLog struct {
	ID              int64          `json:"id"`
	CreatedAt       time.Time      `json:"created_at"`
	Host            string         `json:"host"`
	Level           string         `json:"level"`
	Component       string         `json:"component"`
	Message         string         `json:"message"`
	RequestID       string         `json:"request_id"`
	ClientRequestID string         `json:"client_request_id"`
	UserID          *int64         `json:"user_id"`
	APIKeyID        *int64         `json:"api_key_id"`
	ProviderID      *int64         `json:"provider_id"`
	Platform        string         `json:"platform"`
	Model           string         `json:"model"`
	Extra           map[string]any `json:"extra,omitempty"`
}

// OpsRequestTiming contains request timing and connection metadata from http.access logs.
// Timing fields are milliseconds since ingress; absent numeric fields remain nil.
type OpsRequestTiming struct {
	RequestContentLength           *int64 `json:"request_content_length,omitempty"`
	ProviderSlotAcquiredMs         *int64 `json:"provider_slot_acquired_ms,omitempty"`
	UpstreamGetConnMs              *int64 `json:"upstream_get_conn_ms,omitempty"`
	UpstreamGotConnMs              *int64 `json:"upstream_got_conn_ms,omitempty"`
	UpstreamWroteRequestMs         *int64 `json:"upstream_wrote_request_ms,omitempty"`
	UpstreamFirstResponseByteMs    *int64 `json:"upstream_first_response_byte_ms,omitempty"`
	UpstreamFirstSSEDataMs         *int64 `json:"upstream_first_sse_data_ms,omitempty"`
	FirstVisibleOutputMs           *int64 `json:"first_visible_output_ms,omitempty"`
	FirstDownstreamFlushMs         *int64 `json:"first_downstream_flush_ms,omitempty"`
	UpstreamGetConnCount           *int64 `json:"upstream_get_conn_count,omitempty"`
	UpstreamGotConnCount           *int64 `json:"upstream_got_conn_count,omitempty"`
	UpstreamAttemptCount           *int64 `json:"upstream_attempt_count,omitempty"`
	UpstreamFirstResponseByteCount *int64 `json:"upstream_first_response_byte_count,omitempty"`
	UpstreamConnectionReused       bool   `json:"upstream_connection_reused,omitempty"`
	UpstreamWroteRequestError      bool   `json:"upstream_wrote_request_error,omitempty"`
}

type OpsErrorLog struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"created_at"`

	// Standardized classification
	// - phase: request|auth|provider_auth|routing|upstream|network|internal
	// - owner: client|provider|platform
	// - source: client_request|upstream_http|gateway
	Phase string `json:"phase"`
	Type  string `json:"type"`

	Owner  string `json:"error_owner"`
	Source string `json:"error_source"`

	Severity string `json:"severity"`

	StatusCode int    `json:"status_code"`
	Platform   string `json:"platform"`
	Model      string `json:"model"`

	Resolved           bool       `json:"resolved"`
	ResolvedAt         *time.Time `json:"resolved_at"`
	ResolvedByUserID   *int64     `json:"resolved_by_user_id"`
	ResolvedByUserName string     `json:"resolved_by_user_name"`
	ResolvedStatusRaw  string     `json:"-"`

	ClientRequestID string `json:"client_request_id"`
	RequestID       string `json:"request_id"`
	Message         string `json:"message"`

	UserID       *int64 `json:"user_id"`
	UserEmail    string `json:"user_email"`
	APIKeyID     *int64 `json:"api_key_id"`
	ProviderID   *int64 `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	GroupID      *int64 `json:"group_id"`
	GroupName    string `json:"group_name"`

	ClientIP    *string `json:"client_ip"`
	RequestPath string  `json:"request_path"`
	Stream      bool    `json:"stream"`

	InboundEndpoint  string `json:"inbound_endpoint"`
	UpstreamEndpoint string `json:"upstream_endpoint"`
	RequestedModel   string `json:"requested_model"`
	UpstreamModel    string `json:"upstream_model"`
	RequestType      *int16 `json:"request_type"`
	UserAgent        string `json:"user_agent"`

	// APIKeyName is joined from api_keys; soft deletion retains the name after replacing the key.
	APIKeyName    string `json:"api_key_name,omitempty"`
	APIKeyDeleted bool   `json:"api_key_deleted,omitempty"`
}

type OpsErrorLogDetail struct {
	OpsErrorLog

	ErrorBody string `json:"error_body"`

	// Upstream context (optional)
	UpstreamStatusCode   *int   `json:"upstream_status_code,omitempty"`
	UpstreamErrorMessage string `json:"upstream_error_message,omitempty"`
	UpstreamErrorDetail  string `json:"upstream_error_detail,omitempty"`
	UpstreamErrors       string `json:"upstream_errors,omitempty"` // JSON array (string) for display/parsing

	// Timings (optional)
	AuthLatencyMs      *int64 `json:"auth_latency_ms"`
	RoutingLatencyMs   *int64 `json:"routing_latency_ms"`
	UpstreamLatencyMs  *int64 `json:"upstream_latency_ms"`
	ResponseLatencyMs  *int64 `json:"response_latency_ms"`
	TimeToFirstTokenMs *int64 `json:"time_to_first_token_ms"`

	// Whether a business limit caused the error.
	IsBusinessLimited bool `json:"is_business_limited"`

	// Snapshot of the bound, undeleted API key prefix when the error occurred.
	APIKeyPrefix string `json:"api_key_prefix,omitempty"`
}

type OpsErrorLogFilter struct {
	StartTime *time.Time
	EndTime   *time.Time

	Platform   string
	GroupID    *int64
	ProviderID *int64

	StatusCodes      []int
	StatusCodesOther bool
	Phase            string // Recovered provider rows bypass status>=400 only with the explicit opt-in below.
	Owner            string
	Source           string
	Resolved         *bool
	Query            string
	UserQuery        string // Search by user email

	// Optional correlation keys for exact matching.
	RequestID       string
	ClientRequestID string

	// User-scoped filters (used by the user-facing error requests endpoint and
	// by admin drill-down from the usage page).
	UserID   *int64
	APIKeyID *int64

	// Model matches against requested_model first, then model.
	Model string
	// ModelFuzzy enables ILIKE matching for user queries.
	// The default false value gives admin queries exact matching.
	ModelFuzzy bool

	// ExcludeCountTokens drops count_tokens probe errors (is_count_tokens=true).
	ExcludeCountTokens bool

	// IncludeRecoveredUpstream lets provider health queries include recovered
	// upstream/provider_auth rows with a client status below 400.
	// The default false value filters by the final client status.
	IncludeRecoveredUpstream bool

	// ErrorPhasesAny and ErrorTypesAny add ANY conditions independently of Phase.
	// With IncludeRecoveredUpstream, a phase list containing only upstream/provider_auth also bypasses the status guard.
	// Other ANY conditions retain the status guard. These lists represent UI error categories.
	ErrorPhasesAny []string
	ErrorTypesAny  []string

	// View selects actionable errors (errors), excluded errors (excluded), or all errors (all).
	View string

	// IgnoredStatusCodes lists ignored client statuses; nil uses defaults, and an empty slice ignores none.
	IgnoredStatusCodes []int

	Page     int
	PageSize int

	// SortBy and SortOrder select server-side sorting, as on the usage list.
	// Allowed columns are created_at, model, and status_code; unknown columns use created_at. Order defaults to descending.
	SortBy    string
	SortOrder string
}

type OpsErrorLogList struct {
	Errors   []*OpsErrorLog `json:"errors"`
	Total    int            `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
}

// OpsRequestTimingFromExtra extracts timing and connection metadata from system log fields.
func OpsRequestTimingFromExtra(extra map[string]any) *OpsRequestTiming {
	if len(extra) == 0 {
		return nil
	}
	timing := &OpsRequestTiming{
		RequestContentLength:           timingInt64Ptr(extra["request_content_length"]),
		ProviderSlotAcquiredMs:         timingInt64Ptr(extra["provider_slot_acquired_ms"]),
		UpstreamGetConnMs:              timingInt64Ptr(extra["upstream_get_conn_ms"]),
		UpstreamGotConnMs:              timingInt64Ptr(extra["upstream_got_conn_ms"]),
		UpstreamWroteRequestMs:         timingInt64Ptr(extra["upstream_wrote_request_ms"]),
		UpstreamFirstResponseByteMs:    timingInt64Ptr(extra["upstream_first_response_byte_ms"]),
		UpstreamFirstSSEDataMs:         timingInt64Ptr(extra["upstream_first_sse_data_ms"]),
		FirstVisibleOutputMs:           timingInt64Ptr(extra["first_visible_output_ms"]),
		FirstDownstreamFlushMs:         timingInt64Ptr(extra["first_downstream_flush_ms"]),
		UpstreamGetConnCount:           timingInt64Ptr(extra["upstream_get_conn_count"]),
		UpstreamGotConnCount:           timingInt64Ptr(extra["upstream_got_conn_count"]),
		UpstreamAttemptCount:           timingInt64Ptr(extra["upstream_attempt_count"]),
		UpstreamFirstResponseByteCount: timingInt64Ptr(extra["upstream_first_response_byte_count"]),
	}
	if value, ok := extra["upstream_connection_reused"].(bool); ok {
		timing.UpstreamConnectionReused = value
	}
	if value, ok := extra["upstream_wrote_request_error"].(bool); ok {
		timing.UpstreamWroteRequestError = value
	}
	if timing.RequestContentLength == nil && timing.ProviderSlotAcquiredMs == nil && timing.UpstreamGetConnMs == nil &&
		timing.UpstreamGotConnMs == nil && timing.UpstreamWroteRequestMs == nil && timing.UpstreamFirstResponseByteMs == nil &&
		timing.UpstreamFirstSSEDataMs == nil && timing.FirstVisibleOutputMs == nil && timing.FirstDownstreamFlushMs == nil &&
		timing.UpstreamGetConnCount == nil && timing.UpstreamGotConnCount == nil && timing.UpstreamAttemptCount == nil &&
		timing.UpstreamFirstResponseByteCount == nil && !timing.UpstreamConnectionReused && !timing.UpstreamWroteRequestError {
		return nil
	}
	return timing
}

// timingInt64Ptr parses numeric JSON values, retaining zero as a valid measurement.
func timingInt64Ptr(value any) *int64 {
	var parsed int64
	switch v := value.(type) {
	case int:
		parsed = int64(v)
	case int64:
		parsed = v
	case float64:
		parsed = int64(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			parsed = n
		} else {
			return nil
		}
	case string:
		if n, err := json.Number(strings.TrimSpace(v)).Int64(); err == nil {
			parsed = n
		} else {
			return nil
		}
	default:
		return nil
	}
	if parsed < 0 {
		return nil
	}
	return &parsed
}

// SetSort normalizes sort parameters for admin and user error lists.
func (f *OpsErrorLogFilter) SetSort(sortBy, sortOrder string) {
	f.SortBy = strings.TrimSpace(sortBy)
	f.SortOrder = strings.TrimSpace(sortOrder)
}
