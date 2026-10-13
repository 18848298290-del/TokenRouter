package ops

import (
	"time"
)

// Administrator drilldown joins usage rows and client-error rows as separate
// observations, retaining a kind label and error identity so the UI can open
// the appropriate detail. The success label identifies a usage row; the query
// does not deduplicate the two sources into one final HTTP outcome.
//
// Time normalization gives an omitted filter a one-hour window and permits
// independently supplied endpoints. A missing start follows a supplied end,
// while reversed times are returned in order. Pagination is normalized
// alongside the time range, and source, identity, SLA, and duration filters are
// subsequently applied by the repository to the combined observation rows.

// Request-kind labels identify the usage and error branches joined by request
// drilldown queries, allowing the response to retain each source row identity.
const (
	OpsRequestKindSuccess OpsRequestKind = "success" // usage observation branch label
	OpsRequestKindError   OpsRequestKind = "error"   // client error branch label
)

// OpsRequestKind names the observation source branch shown by request drilldown
// queries.
type OpsRequestKind string

// OpsRequestDetail carries one usage or client-error observation in an
// administrator drilldown.
type OpsRequestDetail struct {
	// Kind distinguishes the usage branch labeled success from the client-error
	// branch labeled error. StatusCode and ErrorID are populated by error rows,
	// while both branches carry their own stored request and dimension metadata.
	Kind      OpsRequestKind `json:"kind"`       // source observation branch label
	CreatedAt time.Time      `json:"created_at"` // stored observation creation time
	RequestID string         `json:"request_id"` // selected request correlation identifier

	Platform string `json:"platform,omitempty"` // stored execution platform snapshot
	Model    string `json:"model,omitempty"`    // stored observation model dimension

	DurationMs *int `json:"duration_ms,omitempty"` // milliseconds
	StatusCode *int `json:"status_code,omitempty"` // optional final client status

	ErrorID *int64 `json:"error_id,omitempty"` // optional stored error identity

	Phase    string `json:"phase,omitempty"`    // stored error phase classification
	Severity string `json:"severity,omitempty"` // stored error severity classification
	Message  string `json:"message,omitempty"`  // stored error display message

	UserID     *int64 `json:"user_id,omitempty"`     // optional observed user identity
	APIKeyID   *int64 `json:"api_key_id,omitempty"`  // optional observed API-key identity
	ProviderID *int64 `json:"provider_id,omitempty"` // optional execution provider identity
	GroupID    *int64 `json:"group_id,omitempty"`    // optional recorded group identity

	Stream bool `json:"stream"` // observed streaming request flag
}

// OpsRequestDetailFilter selects observation rows and ordering for
// administrator request drilldown.
type OpsRequestDetailFilter struct {
	// StartTime and EndTime may be omitted independently. Normalize returns a
	// recent one-hour window by default, derives a missing start from a supplied
	// end, and swaps reversed endpoints before the query uses them.
	StartTime *time.Time // optional inclusive range start
	EndTime   *time.Time // optional exclusive range end

	// Kind selects success or error after trimming and lowercasing. Empty or all
	// keeps both observation sources; any other value fails repository validation.
	Kind string
	// SLAOnly applies the exclusion predicate to the error branch while usage rows
	// remain eligible. IgnoredStatusCodes uses defaults when nil and no status
	// exclusions when empty; retained upstream context can keep an ignored client
	// status countable.
	SLAOnly            bool  // restrict error SLA category
	IgnoredStatusCodes []int // configured client-status exclusion policy

	Platform string // recorded execution platform filter
	// GroupID, UserID, APIKeyID, and ProviderID constrain their recorded row
	// identities when positive. Nil or nonpositive pointers omit the corresponding
	// condition in ListRequestDetails, independently of the other identity filters.
	GroupID *int64 // optional recorded group identity

	UserID     *int64 // optional recorded user identity
	APIKeyID   *int64 // optional recorded API-key identity
	ProviderID *int64 // optional execution provider identity

	Model string // exact saved model filter
	// RequestID selects the request_lookup_ids expansion of trimmed nonempty
	// correlation text. This repository resolves stored platform identifiers and
	// their external aliases before matching combined rows.
	RequestID string
	// Query performs a case-insensitive SQL LIKE search across request ID, model,
	// and message. Storage adds surrounding wildcards and retains any
	// caller-supplied wildcard characters.
	Query string

	// MinDurationMs and MaxDurationMs impose inclusive lower and upper limits on
	// nonnull recorded duration. Nil omits that comparison; rows with missing
	// duration fail any supplied duration comparison.
	MinDurationMs *int // milliseconds
	MaxDurationMs *int // milliseconds

	// Sort defaults to created_at_desc when empty after trimming and lowercasing.
	// duration_desc places missing durations last and then orders by creation
	// time; other values fail validation after the count query.
	Sort string

	// Page and PageSize become the combined query OFFSET and LIMIT after
	// Normalize applies its one-based page and bounded page-size defaults. Their
	// source filter values remain unchanged.
	Page     int // one-based requested observation page
	PageSize int // requested observations per page
}

// OpsRequestDetailList carries normalized pagination and matching request
// observation entries.
type OpsRequestDetailList struct {
	Items    []*OpsRequestDetail `json:"items"`     // returned request observation entries
	Total    int64               `json:"total"`     // matching observation row count
	Page     int                 `json:"page"`      // one-based returned observation page
	PageSize int                 `json:"page_size"` // returned observations per page
}

// Normalize returns page parameters and ordered times for a request drilldown
// filter without changing its fields. A nil filter selects page one, fifty
// rows, and the hour ending at one current-time sample. Positive pagination
// replaces those defaults, with page size capped at one hundred.
//
// A supplied end replaces that sample, and an omitted start then becomes one
// hour before the supplied end. A supplied start with no end keeps the
// current-time end. Supplied time pointers are used even when their values are
// zero; reversed endpoints are swapped before the result is returned.
func (f *OpsRequestDetailFilter) Normalize() (page, pageSize int, startTime, endTime time.Time) {
	page = 1
	pageSize = 50
	endTime = time.Now()
	startTime = endTime.Add(-1 * time.Hour)

	if f == nil {
		return page, pageSize, startTime, endTime
	}

	if f.Page > 0 {
		page = f.Page
	}
	if f.PageSize > 0 {
		pageSize = f.PageSize
	}
	// The normalized page size becomes the SQL LIMIT over combined usage and error
	// observations, bounding each returned page.
	if pageSize > 100 {
		pageSize = 100
	}

	if f.EndTime != nil {
		endTime = *f.EndTime
	}
	if f.StartTime != nil {
		startTime = *f.StartTime
	} else if f.EndTime != nil {
		startTime = endTime.Add(-1 * time.Hour)
	}

	// Caller-supplied endpoints can arrive reversed. Return their ordered interval
	// so storage can apply the same inclusive-start, exclusive-end comparisons.
	if startTime.After(endTime) {
		startTime, endTime = endTime, startTime
	}

	return page, pageSize, startTime, endTime
}
