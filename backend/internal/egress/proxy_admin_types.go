package egress

import (
	"context"
	"time"
)

// Proxy quality combines a base exit-information probe with unauthenticated
// requests to selected model endpoints. Each target defines statuses considered
// reachable, so an expected authentication error can pass while a Cloudflare
// challenge receives its own classification before status-based grading.

// Proxy quality request defaults bound inspected response bodies and choose the
// browser-shaped User-Agent used by unauthenticated target probes.
const (
	ProxyQualityMaxBodyBytes    = int64(8 * 1024)
	ProxyQualityClientUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"
)

// ProxyQualityTargets defines the sequential unauthenticated endpoint probes
// and reachable statuses scored by CheckProxyQuality. Callers treat the shared
// slice and status maps as immutable while quality probes use them.
var ProxyQualityTargets = []ProxyQualityTarget{
	{
		Target: "openai",
		URL:    "https://api.openai.com/v1/models",
		Method: "GET",
		AllowedStatuses: map[int]struct{}{
			401: {},
		},
	},
	{
		Target: "anthropic",
		URL:    "https://api.anthropic.com/v1/messages",
		Method: "GET",
		AllowedStatuses: map[int]struct{}{
			401: {},
			405: {},
			404: {},
			400: {},
		},
	},
	{
		Target: "gemini",
		URL:    "https://generativelanguage.googleapis.com/$discovery/rest?version=v1beta",
		Method: "GET",
		AllowedStatuses: map[int]struct{}{
			200: {},
		},
	},
	{
		Target: "grok",
		URL:    "https://api.x.ai/v1/models",
		Method: "GET",
		AllowedStatuses: map[int]struct{}{
			401: {},
		},
	},
}

// CreateProxyInput supplies administrator connection and expiration values for
// the proxy write operation.
//
// ExpiresAt, FallbackMode, BackupProxyID, and ExpiryWarnDays supply the
// expiration policy. ProxyAdmin defaults an empty FallbackMode to none and
// requires BackupProxyID for proxy fallback.
type CreateProxyInput struct {
	Name           string     // administrator proxy display name
	Protocol       string     // configured proxy scheme text
	Host           string     // proxy connection hostname text
	Port           int        // proxy TCP port number
	Username       string     // proxy authentication username text
	Password       string     // proxy authentication password text
	ExpiresAt      *time.Time // optional expiration cutoff timestamp
	FallbackMode   string     // expiration provider reassignment mode
	BackupProxyID  *int64     // optional fallback proxy identity
	ExpiryWarnDays int        // days
}

// UpdateProxyInput supplies administrator connection and expiration values for
// the proxy write operation.
//
// ExpiresAt, FallbackMode, BackupProxyID, and ExpiryWarnDays supply the
// expiration policy. ProxyAdmin defaults an empty FallbackMode to none and
// requires BackupProxyID for proxy fallback. UpdateProxy replaces this group
// even when supplied values are zero or nil.
type UpdateProxyInput struct {
	Name     string // administrator proxy display name
	Protocol string // configured proxy scheme text
	Host     string // proxy connection hostname text
	Port     int    // proxy TCP port number
	// Username replaces stored authentication text when nonempty. An empty update
	// retains the current username, so this administrative patch cannot clear that
	// field.
	Username string // proxy authentication username text
	// Password replaces stored authentication text when nonempty. An empty update
	// retains the current password, so this administrative patch cannot clear that
	// field.
	Password       string     // proxy authentication password text
	Status         string     // persisted proxy status value
	ExpiresAt      *time.Time // optional expiration cutoff timestamp
	FallbackMode   string     // expiration provider reassignment mode
	BackupProxyID  *int64     // optional fallback proxy identity
	ExpiryWarnDays int        // days
}

// ProxyBatchDeleteResult records successful proxy deletions and per-item
// reasons for skipped IDs.
type ProxyBatchDeleteResult struct {
	DeletedIDs []int64                   `json:"deleted_ids"` // successful proxy deletion identities
	Skipped    []ProxyBatchDeleteSkipped `json:"skipped"`     // per-ID failed deletion outcomes
}

// ProxyBatchDeleteSkipped identifies one proxy whose batch deletion failed or
// remained in use.
type ProxyBatchDeleteSkipped struct {
	ID     int64  `json:"id"`     // skipped proxy database identity
	Reason string `json:"reason"` // per-proxy deletion failure reason
}

// ProxyTestResult reports exit information and connectivity observations from
// one proxy probe.
type ProxyTestResult struct {
	Success     bool   `json:"success"`                // probe connectivity success flag
	Message     string `json:"message"`                // probe result diagnostic text
	LatencyMs   int64  `json:"latency_ms,omitempty"`   // milliseconds
	IPAddress   string `json:"ip_address,omitempty"`   // probed proxy exit address
	City        string `json:"city,omitempty"`         // probed exit city text
	Region      string `json:"region,omitempty"`       // probed exit region text
	Country     string `json:"country,omitempty"`      // probed exit country text
	CountryCode string `json:"country_code,omitempty"` // probed exit country code
}

// ProxyQualityCheckResult aggregates one proxy's base connectivity and selected
// target probe outcomes.
type ProxyQualityCheckResult struct {
	ProxyID        int64                   `json:"proxy_id"`                  // assessed proxy database identity
	Score          int                     `json:"score"`                     // weighted quality score points
	Grade          string                  `json:"grade"`                     // quality score display letter
	Summary        string                  `json:"summary"`                   // formatted outcome count text
	ExitIP         string                  `json:"exit_ip,omitempty"`         // base probe exit address
	Country        string                  `json:"country,omitempty"`         // base probe country text
	CountryCode    string                  `json:"country_code,omitempty"`    // base probe country code
	BaseLatencyMs  int64                   `json:"base_latency_ms,omitempty"` // milliseconds
	PassedCount    int                     `json:"passed_count"`              // passing probe item count
	WarnCount      int                     `json:"warn_count"`                // warning probe item count
	FailedCount    int                     `json:"failed_count"`              // failed probe item count
	ChallengeCount int                     `json:"challenge_count"`           // challenge probe item count
	CheckedAt      int64                   `json:"checked_at"`                // Unix seconds
	Items          []ProxyQualityCheckItem `json:"items"`                     // ordered individual probe outcomes
}

// ProxyQualityCheckItem describes one endpoint's observed response and assigned
// quality classification.
type ProxyQualityCheckItem struct {
	Target     string `json:"target"`                // configured probe target label
	Status     string `json:"status"`                // pass/warn/fail/challenge classification text
	HTTPStatus int    `json:"http_status,omitempty"` // observed target HTTP status
	LatencyMs  int64  `json:"latency_ms,omitempty"`  // milliseconds
	Message    string `json:"message,omitempty"`     // target result diagnostic text
	CFRay      string `json:"cf_ray,omitempty"`      // captured target Cloudflare ray
}

// ProxyExitInfo holds an observed proxy exit address and any returned location
// metadata.
type ProxyExitInfo struct {
	IP          string // observed proxy exit address
	City        string // observed exit city text
	Region      string // observed exit region text
	Country     string // observed exit country text
	CountryCode string // observed exit country code
}

// ProxyExitInfoProber tests connectivity and returns exit information and latency in milliseconds.
type ProxyExitInfoProber interface {
	// ProbeProxy requests exit information through the selected proxy and returns
	// the successful target's header latency in milliseconds. Probe failure
	// returns an error; location metadata depends on the selected response parser.
	ProbeProxy(ctx context.Context, proxyURL string) (*ProxyExitInfo, int64, error)
}

// ProxyQualityTarget supplies one unauthenticated endpoint request and its
// reachable response statuses.
type ProxyQualityTarget struct {
	Target string // quality result target label
	URL    string // fixed probe endpoint URL
	// Method supplies the request verb to RunProxyQualityTarget. Empty text uses
	// the GET default of http.NewRequestWithContext before the unauthenticated probe.
	Method string // endpoint probe HTTP method
	// AllowedStatuses classifies reachability after challenge detection.
	// RunProxyQualityTarget treats membership as a pass even for expected
	// authentication or endpoint-method errors.
	AllowedStatuses map[int]struct{} // reachable endpoint response statuses
}
