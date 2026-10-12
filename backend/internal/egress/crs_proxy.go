package egress

import (
	"context"
	"fmt"
	"strings"
)

// CRS imports can repeat the same proxy connection before later items finish
// importing. Matching compares normalized supported protocol text with the exact
// cached host, port, and credential values, using active records supplied by the
// caller and records created earlier in the same import. Unsupported or
// incomplete source values produce no proxy association.
//
// Creation goes directly through CRSProxyStore and publishes the resulting
// record into the caller-owned lookup slice after persistence succeeds. A failed
// write leaves that new record absent from the slice. The caller serializes
// matching and cache append operations; this helper supplies no shared lock.
// Direct creation also leaves administrative connectivity-probe scheduling to
// other paths.

// CRSProxySpec contains proxy connection details from a CRS import.
type CRSProxySpec struct {
	Protocol string `json:"protocol"` // source proxy scheme text
	Host     string `json:"host"`     // source proxy hostname text
	Port     int    `json:"port"`     // source proxy port number
	Username string `json:"username"` // source proxy authentication username
	Password string `json:"password"` // source proxy authentication password
}

// CRSProxyStore supplies active proxy lookup and record creation for
// import-time matching.
type CRSProxyStore interface {
	// ListActive returns persisted proxies whose status is active for import-time
	// matching. Expiration timestamps are not checked by this listing; the
	// importing caller retains the returned records in its serial lookup cache.
	ListActive(context.Context) ([]Proxy, error)
	// Create persists the supplied proxy and sets its stored identity. The caller
	// uses this direct repository operation when import matching needs a record
	// without scheduling administrative connectivity probes.
	Create(context.Context, *Proxy) error
}

// MatchOrCreateCRSProxy normalizes supported CRS proxy values and returns a
// reused or newly stored ID. Disabled imports, missing source values,
// incomplete addresses, or unsupported schemes return nil with no error. The
// caller supplies a nonnil cached slice pointer and serializes its use.
//
// SOCKS aliases become socks5, and matching compares normalized credentials
// with cached values. A new active record is created directly through store and
// then appended to the cache, so later items in the same import can reuse it.
// Store failure leaves that new item absent from the slice.
func MatchOrCreateCRSProxy(ctx context.Context, store CRSProxyStore, enabled bool, cached *[]Proxy, src *CRSProxySpec, defaultName string) (*int64, error) {
	if !enabled || src == nil {
		return nil, nil
	}
	protocol := strings.ToLower(strings.TrimSpace(src.Protocol))
	switch protocol {
	// CRS socks and socks5h spellings select the supported socks5 import
	// representation; host, port, and credentials still determine cached reuse.
	case "socks":
		protocol = "socks5"
	case "socks5h":
		protocol = "socks5"
	}
	host := strings.TrimSpace(src.Host)
	port := src.Port
	username := strings.TrimSpace(src.Username)
	password := strings.TrimSpace(src.Password)

	if protocol == "" || host == "" || port <= 0 {
		return nil, nil
	}
	if protocol != "http" && protocol != "https" && protocol != "socks5" {
		return nil, nil
	}

	// The caller seeds cached with active proxies. Additions from earlier items
	// remain visible here so repeated CRS connection values reuse their stored ID.
	for _, p := range *cached {
		if strings.EqualFold(p.Protocol, protocol) &&
			p.Host == host &&
			p.Port == port &&
			p.Username == username &&
			p.Password == password {
			id := p.ID
			return &id, nil
		}
	}

	// Repository creation sets the new ID without invoking ProxyAdmin, whose
	// create method can register asynchronous latency probing with the task owner.
	proxy := &Proxy{
		Name:     CRSDefaultProxyName(defaultName, protocol, host, port),
		Protocol: protocol,
		Host:     host,
		Port:     port,
		Username: username,
		Password: password,
		Status:   StatusActive,
	}
	if err := store.Create(ctx, proxy); err != nil {
		return nil, err
	}

	*cached = append(*cached, *proxy)
	id := proxy.ID
	return &id, nil
}

// CRSDefaultProxyName formats a proxy name from the trimmed import label and
// supplied connection address. An empty label becomes crs; protocol and host
// text are used as supplied, with decimal port formatting.
func CRSDefaultProxyName(base, protocol, host string, port int) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "crs"
	}
	return fmt.Sprintf("%s (%s://%s:%d)", base, protocol, host, port)
}
