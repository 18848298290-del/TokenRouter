package dto

import (
	"time"
)

type Proxy struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Protocol  string    `json:"protocol"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	Username  string    `json:"username"`
	Password  string    `json:"-"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	ExpiresAt      *time.Time `json:"expires_at"`
	FallbackMode   string     `json:"fallback_mode"`
	BackupProxyID  *int64     `json:"backup_proxy_id"`
	ExpiryWarnDays int        `json:"expiry_warn_days"`
}

// AdminProxy contains proxy details, including the password, for administrator responses.
// Use Proxy for endpoints available to ordinary users.
type AdminProxy struct {
	Proxy
	Password string `json:"password,omitempty"`
}

// AdminProxyWithProviderCount adds provider counts and probe results to an admin proxy.
type AdminProxyWithProviderCount struct {
	AdminProxy
	ProviderCount  int64  `json:"provider_count"`
	LatencyMs      *int64 `json:"latency_ms,omitempty"`
	LatencyStatus  string `json:"latency_status,omitempty"`
	LatencyMessage string `json:"latency_message,omitempty"`
	IPAddress      string `json:"ip_address,omitempty"`
	Country        string `json:"country,omitempty"`
	CountryCode    string `json:"country_code,omitempty"`
	Region         string `json:"region,omitempty"`
	City           string `json:"city,omitempty"`
	QualityStatus  string `json:"quality_status,omitempty"`
	QualityScore   *int   `json:"quality_score,omitempty"`
	QualityGrade   string `json:"quality_grade,omitempty"`
	QualitySummary string `json:"quality_summary,omitempty"`
	QualityChecked *int64 `json:"quality_checked,omitempty"`
}

type ProxyProviderSummary struct {
	ID       int64   `json:"id"`
	Name     string  `json:"name"`
	Platform string  `json:"platform"`
	Type     string  `json:"type"`
	Notes    *string `json:"notes,omitempty"`
}
