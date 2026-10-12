package egress

import (
	"context"
	"fmt"
	"strings"
)

// CRSProxySpec contains proxy connection details from a CRS import.
type CRSProxySpec struct {
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// CRSProxyStore lists active proxies and creates records without connectivity probes.
type CRSProxyStore interface {
	ListActive(context.Context) ([]Proxy, error)
	Create(context.Context, *Proxy) error
}

// MatchOrCreateCRSProxy normalizes SOCKS aliases and reuses or creates an active proxy.
func MatchOrCreateCRSProxy(ctx context.Context, store CRSProxyStore, enabled bool, cached *[]Proxy, src *CRSProxySpec, defaultName string) (*int64, error) {
	if !enabled || src == nil {
		return nil, nil
	}
	protocol := strings.ToLower(strings.TrimSpace(src.Protocol))
	switch protocol {
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

	// Reuse active proxies supplied by the caller or created earlier in this import.
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

	// Create the record directly so CRS imports do not start admin latency probes.
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

func CRSDefaultProxyName(base, protocol, host string, port int) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "crs"
	}
	return fmt.Sprintf("%s (%s://%s:%d)", base, protocol, host, port)
}
