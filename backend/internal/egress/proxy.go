package egress

import (
	"net"
	"net/url"
	"strconv"
	"time"
)

// proxy.go defines proxy connection records and constructs their authenticated
// URLs and expiration checks.
// Proxy records retain network address, authentication, and expiration policy
// for outgoing requests and administration. A URL is built with escaped
// credentials only when both authentication fields are populated, while
// expiration compares its optional timestamp separately from the stored status
// used for active listings.

// Proxy fallback modes govern expiration-time provider reassignment, while
// status values govern persisted availability and administrative listing
// filters.
const (
	FallbackModeNone   = "none"   // retain the provider binding
	FallbackModeProxy  = "proxy"  // follow configured backup proxies
	FallbackModeDirect = "direct" // clear the provider binding

	// StatusActive selects active repository listings, and StatusExpired marks
	// stored expiration so backup resolution rejects a candidate with that value.
	StatusActive  = "active"
	StatusExpired = "expired"
)

// Proxy holds persisted connection values and provider fallback policy for an
// outbound proxy.
//
// ExpiresAt, FallbackMode, BackupProxyID, and ExpiryWarnDays describe
// expiration maintenance. IsExpired checks the timestamp independently of
// Status; the maintenance caller then selects configured provider
// reassignment.
type Proxy struct {
	ID       int64  // persisted proxy record identity
	Name     string // administrator proxy display name
	Protocol string // configured proxy scheme text
	Host     string // proxy connection hostname text
	Port     int    // proxy TCP port number
	// Username and Password produce URL authentication when both are nonempty. A
	// missing value in either field causes URL to omit authentication entirely.
	Username  string    // proxy authentication username text
	Password  string    // proxy authentication password text
	Status    string    // persisted proxy status value
	CreatedAt time.Time // persisted record creation time
	UpdatedAt time.Time // persisted record update time
	// ExpiresAt is absent for proxies with no time-based expiration. IsExpired
	// treats equality with the supplied clock as expired, independently of the
	// persisted Status value.
	ExpiresAt    *time.Time // optional expiration cutoff timestamp
	FallbackMode string     // expiration provider reassignment mode
	// BackupProxyID is followed when expiration uses proxy fallback.
	// ResolveProxyFallbackTarget reads the pointed-to identity from the caller's
	// snapshot and guards repeated IDs against cycles.
	BackupProxyID  *int64 // optional fallback proxy identity
	ExpiryWarnDays int    // days
}

// ProxyWithProviderCount adds bound-provider counts and cached probe
// observations for administrator listings.
//
// LatencyMs, LatencyStatus, and LatencyMessage describe the last cached
// connectivity result. QualityStatus, QualityScore, QualityGrade,
// QualitySummary, and QualityChecked can retain an earlier quality check when
// a later latency-only probe updates the cache.
type ProxyWithProviderCount struct {
	Proxy                 // persisted proxy connection values
	ProviderCount  int64  // nondeleted bound provider count
	LatencyMs      *int64 // milliseconds
	LatencyStatus  string // cached connectivity result status
	LatencyMessage string // cached connectivity diagnostic text
	IPAddress      string // last probed exit address
	Country        string // probed exit country text
	CountryCode    string // probed exit country code
	Region         string // probed exit region text
	City           string // probed exit city text
	QualityStatus  string // cached quality overall status
	QualityScore   *int   // weighted quality score points
	QualityGrade   string // cached quality grade letter
	QualitySummary string // cached quality result text
	QualityChecked *int64 // Unix seconds
}

// ProxyProviderSummary identifies a nondeleted provider associated with a proxy
// in administrator queries.
type ProxyProviderSummary struct {
	ID       int64   // bound provider database identity
	Name     string  // bound provider display name
	Platform string  // bound upstream platform name
	Type     string  // bound provider credential kind
	Notes    *string // optional administrator provider notes
}

// ProxyConnectionIdentity contains a proxy's address, credentials, and status.
type ProxyConnectionIdentity struct {
	// Protocol, Host, Port, Username, Password, and Status define the connection
	// identity compared during proxy updates. Changes to other Proxy metadata do
	// not trigger that comparison's usage-snapshot invalidation.
	Protocol string // configured proxy scheme text
	Host     string // proxy connection hostname text
	Port     int    // proxy TCP port number
	Username string // proxy authentication username text
	Password string // proxy authentication password text
	Status   string // persisted proxy status value
}

// IsActive reports whether the persisted status is the active proxy value.
func (p *Proxy) IsActive() bool {
	return p.Status == StatusActive
}

// IsExpired reports whether the optional expiration timestamp has been reached
// at now. A nil timestamp returns false, and equality counts as expired
// independently of the stored proxy status.
func (p *Proxy) IsExpired(now time.Time) bool {
	return p.ExpiresAt != nil && !p.ExpiresAt.After(now)
}

// URL formats the scheme and joined host/port with escaped username and
// password when both are present. Missing either credential omits
// authentication; callers keep the resulting credential-bearing URL out of
// public diagnostics.
func (p *Proxy) URL() string {
	u := &url.URL{
		Scheme: p.Protocol,
		Host:   net.JoinHostPort(p.Host, strconv.Itoa(p.Port)),
	}
	if p.Username != "" && p.Password != "" {
		u.User = url.UserPassword(p.Username, p.Password)
	}
	return u.String()
}

// ProxyConnectionIdentityFromProxy extracts connection and status values for
// repository update comparison.
func ProxyConnectionIdentityFromProxy(proxyIn *Proxy) ProxyConnectionIdentity {
	return ProxyConnectionIdentity{
		Protocol: proxyIn.Protocol,
		Host:     proxyIn.Host,
		Port:     proxyIn.Port,
		Username: proxyIn.Username,
		Password: proxyIn.Password,
		Status:   proxyIn.Status,
	}
}
