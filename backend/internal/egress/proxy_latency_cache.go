package egress

import (
	"context"
	"time"
)

// Proxy connectivity observations and quality assessments share one cached
// result keyed by proxy ID. A later latency-only update can retain the earlier
// quality details, allowing administrator listings to show connection success
// and target quality from different checks with their respective timestamps.

// ProxyLatencyInfo combines the latest connectivity observation with optional
// retained target-quality assessment values.
//
// QualityStatus, QualityScore, QualityGrade, QualitySummary, QualityCheckedAt,
// and QualityCFRay form the retained assessment. ProxyAdmin copies the
// existing group when every incoming quality field is empty, while
// connectivity values and UpdatedAt describe the incoming probe.
type ProxyLatencyInfo struct {
	Success          bool      `json:"success"`                      // base connectivity success flag
	LatencyMs        *int64    `json:"latency_ms,omitempty"`         // milliseconds
	Message          string    `json:"message,omitempty"`            // latest probe result message
	IPAddress        string    `json:"ip_address,omitempty"`         // probed proxy exit address
	Country          string    `json:"country,omitempty"`            // probed exit country text
	CountryCode      string    `json:"country_code,omitempty"`       // probed exit country code
	Region           string    `json:"region,omitempty"`             // probed exit region text
	City             string    `json:"city,omitempty"`               // probed exit city text
	QualityStatus    string    `json:"quality_status,omitempty"`     // target quality overall status
	QualityScore     *int      `json:"quality_score,omitempty"`      // weighted quality score points
	QualityGrade     string    `json:"quality_grade,omitempty"`      // target quality grade letter
	QualitySummary   string    `json:"quality_summary,omitempty"`    // target quality result text
	QualityCheckedAt *int64    `json:"quality_checked_at,omitempty"` // Unix seconds
	QualityCFRay     string    `json:"quality_cf_ray,omitempty"`     // first captured quality ray
	UpdatedAt        time.Time `json:"updated_at"`                   // latest cache observation time
}

// ProxyLatencyCache stores and retrieves proxy connectivity observations
// augmented with target-quality assessment results.
type ProxyLatencyCache interface {
	// GetProxyLatencies returns available observations keyed by requested proxy
	// IDs. The Redis adapter omits missing or malformed entries and returns
	// command errors separately, so callers can display records with absent cached
	// observations.
	GetProxyLatencies(ctx context.Context, proxyIDs []int64) (map[int64]*ProxyLatencyInfo, error)
	// SetProxyLatency stores one proxy observation for administrative listing and
	// assessment merging. A nil observation is a no-op in the Redis adapter; other
	// values are serialized together under the proxy identity.
	SetProxyLatency(ctx context.Context, proxyID int64, info *ProxyLatencyInfo) error
}
