package ops

import (
	"time"
)

const (
	OpsRequestKindSuccess OpsRequestKind = "success"
	OpsRequestKindError   OpsRequestKind = "error"
)

type OpsRequestKind string

// OpsRequestDetail is a request-level view across success (usage_logs) and error (ops_error_logs).
// It powers "request drilldown" UIs without exposing full request bodies for successful requests.
type OpsRequestDetail struct {
	Kind      OpsRequestKind `json:"kind"`
	CreatedAt time.Time      `json:"created_at"`
	RequestID string         `json:"request_id"`

	Platform string `json:"platform,omitempty"`
	Model    string `json:"model,omitempty"`

	DurationMs *int `json:"duration_ms,omitempty"`
	StatusCode *int `json:"status_code,omitempty"`

	// When Kind == "error", ErrorID links to /admin/ops/errors/:id.
	ErrorID *int64 `json:"error_id,omitempty"`

	Phase    string `json:"phase,omitempty"`
	Severity string `json:"severity,omitempty"`
	Message  string `json:"message,omitempty"`

	UserID     *int64 `json:"user_id,omitempty"`
	APIKeyID   *int64 `json:"api_key_id,omitempty"`
	ProviderID *int64 `json:"provider_id,omitempty"`
	GroupID    *int64 `json:"group_id,omitempty"`

	Stream bool `json:"stream"`
}
type OpsRequestDetailFilter struct {
	StartTime *time.Time
	EndTime   *time.Time

	// kind: success|error|all
	Kind string
	// SLAOnly keeps SLA-counted error rows for summary-card drilldown.
	SLAOnly bool
	// IgnoredStatusCodes lists ignored client statuses; nil uses defaults, and an empty slice ignores none.
	IgnoredStatusCodes []int

	Platform string
	GroupID  *int64

	UserID     *int64
	APIKeyID   *int64
	ProviderID *int64

	Model     string
	RequestID string
	Query     string

	MinDurationMs *int
	MaxDurationMs *int

	// Sort: created_at_desc (default) or duration_desc.
	Sort string

	Page     int
	PageSize int
}

type OpsRequestDetailList struct {
	Items    []*OpsRequestDetail `json:"items"`
	Total    int64               `json:"total"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"page_size"`
}

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

	if startTime.After(endTime) {
		startTime, endTime = endTime, startTime
	}

	return page, pageSize, startTime, endTime
}
