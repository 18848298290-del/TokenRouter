package egress

import (
	"net"
	"net/url"
	"strconv"
	"time"
)

const (
	FallbackModeNone   = "none"
	FallbackModeProxy  = "proxy"
	FallbackModeDirect = "direct"

	// StatusActive and StatusExpired are persisted proxy status values.
	StatusActive  = "active"
	StatusExpired = "expired"
)

type Proxy struct {
	ID             int64
	Name           string
	Protocol       string
	Host           string
	Port           int
	Username       string
	Password       string
	Status         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ExpiresAt      *time.Time
	FallbackMode   string
	BackupProxyID  *int64
	ExpiryWarnDays int
}

type ProxyWithProviderCount struct {
	Proxy
	ProviderCount  int64
	LatencyMs      *int64
	LatencyStatus  string
	LatencyMessage string
	IPAddress      string
	Country        string
	CountryCode    string
	Region         string
	City           string
	QualityStatus  string
	QualityScore   *int
	QualityGrade   string
	QualitySummary string
	QualityChecked *int64
}

type ProxyProviderSummary struct {
	ID       int64
	Name     string
	Platform string
	Type     string
	Notes    *string
}

// ProxyConnectionIdentity contains a proxy's address, credentials, and status.
type ProxyConnectionIdentity struct {
	Protocol string
	Host     string
	Port     int
	Username string
	Password string
	Status   string
}

func (p *Proxy) IsActive() bool {
	return p.Status == StatusActive
}

// IsExpired reports whether expires_at has been reached, regardless of stored status.
func (p *Proxy) IsExpired(now time.Time) bool {
	return p.ExpiresAt != nil && !p.ExpiresAt.After(now)
}

// URL includes authentication only when both username and password are present.
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

// ProxyConnectionIdentityFromProxy extracts proxy connection settings.
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
