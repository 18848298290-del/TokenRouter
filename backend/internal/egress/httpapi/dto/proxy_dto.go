package dto

import (
	"time"
)

// Ordinary proxy responses exclude password JSON, while administrator responses
// expose a separate password field. Both retain connection and expiration
// metadata for display, and augmented administrator responses combine persisted
// provider counts with cached connectivity and target-quality observations.

// Proxy contains response connection and expiration values with its password
// excluded from JSON.
//
// ExpiresAt and BackupProxyID are retained pointers from the egress mapper.
// Response consumers leave those optional values immutable, while
// ProxyFromEgress copies the surrounding scalar metadata.
type Proxy struct {
	ID       int64  `json:"id"`       // persisted proxy database identity
	Name     string `json:"name"`     // administrator proxy display name
	Protocol string `json:"protocol"` // stored proxy scheme text
	Host     string `json:"host"`     // stored proxy hostname text
	Port     int    `json:"port"`     // proxy TCP port number
	Username string `json:"username"` // proxy authentication username text
	// Password is excluded from JSON by this response type. ProxyFromEgress leaves
	// it unset, and privileged responses add their separate administrator password
	// field.
	Password  string    `json:"-"`          // JSON-excluded proxy password text
	Status    string    `json:"status"`     // persisted proxy availability status
	CreatedAt time.Time `json:"created_at"` // persisted record creation time
	UpdatedAt time.Time `json:"updated_at"` // persisted record update time

	ExpiresAt      *time.Time `json:"expires_at"`       // optional expiration cutoff timestamp
	FallbackMode   string     `json:"fallback_mode"`    // expiration provider reassignment mode
	BackupProxyID  *int64     `json:"backup_proxy_id"`  // optional destination backup identity
	ExpiryWarnDays int        `json:"expiry_warn_days"` // days
}

// AdminProxy contains proxy details, including the password, for administrator responses.
// Use Proxy for endpoints available to ordinary users.
type AdminProxy struct {
	Proxy // ordinary proxy response metadata
	// Password exposes the stored credential to administrator responses and is
	// omitted when empty. Route authentication belongs to the caller using this
	// privileged response type.
	Password string `json:"password,omitempty"` // administrator-visible proxy password text
}

// AdminProxyWithProviderCount adds provider counts and probe results to an admin proxy.
//
// LatencyMs, QualityScore, and QualityChecked retain optional numeric pointers
// from egress observations. Mapping does not clone these values, and response
// consumers keep them immutable until serialization completes.
type AdminProxyWithProviderCount struct {
	AdminProxy            // credential-bearing administrator proxy response
	ProviderCount  int64  `json:"provider_count"`            // nondeleted bound provider count
	LatencyMs      *int64 `json:"latency_ms,omitempty"`      // milliseconds
	LatencyStatus  string `json:"latency_status,omitempty"`  // cached connectivity result status
	LatencyMessage string `json:"latency_message,omitempty"` // cached connectivity diagnostic text
	IPAddress      string `json:"ip_address,omitempty"`      // probed proxy exit address
	Country        string `json:"country,omitempty"`         // probed exit country text
	CountryCode    string `json:"country_code,omitempty"`    // probed exit country code
	Region         string `json:"region,omitempty"`          // probed exit region text
	City           string `json:"city,omitempty"`            // probed exit city text
	QualityStatus  string `json:"quality_status,omitempty"`  // cached target quality status
	QualityScore   *int   `json:"quality_score,omitempty"`   // weighted quality score points
	QualityGrade   string `json:"quality_grade,omitempty"`   // cached quality grade letter
	QualitySummary string `json:"quality_summary,omitempty"` // cached quality outcome summary
	QualityChecked *int64 `json:"quality_checked,omitempty"` // Unix seconds
}

// ProxyProviderSummary identifies a bound provider in administrator proxy
// association responses.
type ProxyProviderSummary struct {
	ID       int64  `json:"id"`       // bound provider database identity
	Name     string `json:"name"`     // bound provider display name
	Platform string `json:"platform"` // bound upstream platform name
	Type     string `json:"type"`     // bound provider credential kind
	// Notes retains the source optional string pointer through response mapping.
	// Consumers leave the pointed-to value unchanged while response serialization
	// is using it.
	Notes *string `json:"notes,omitempty"` // optional administrator provider notes
}
