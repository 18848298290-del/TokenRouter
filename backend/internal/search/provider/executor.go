package provider

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/proxy"
	"github.com/TokenFlux/TokenRouter/internal/search/contract"
)

const (
	defaultMaxResults    = contract.DefaultMaxResults
	proxyDialTimeout     = 3 * time.Second
	proxyTLSTimeout      = 3 * time.Second
	searchDataTimeout    = 60 * time.Second
	searchRequestTimeout = searchDataTimeout + proxyDialTimeout
	maxCachedClients     = 100
)

type (
	SearchRequest  = contract.SearchRequest
	SearchResponse = contract.SearchResponse
	SearchResult   = contract.SearchResult
	Provider       = contract.Provider
	ProviderConfig = contract.ProviderConfig
)

type Executor struct {
	clientMu    sync.Mutex
	clientCache map[string]*http.Client
}

func NewExecutor() *Executor { return &Executor{clientCache: make(map[string]*http.Client)} }
func (m *Executor) CloseIdle() {
	m.clientMu.Lock()
	defer m.clientMu.Unlock()
	for _, c := range m.clientCache {
		c.CloseIdleConnections()
	}
}

func (m *Executor) Search(ctx context.Context, cfg ProviderConfig, req SearchRequest) (*SearchResponse, error) {
	proxyURL := cfg.ProxyURL
	if req.ProxyURL != "" {
		proxyURL = req.ProxyURL
	}
	client, err := m.getOrCreateHTTPClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("websearch: %w", err)
	}
	provider := m.buildProvider(cfg, client)
	return provider.Search(ctx, req)
}

func (m *Executor) getOrCreateHTTPClient(proxyURL string) (*http.Client, error) {
	m.clientMu.Lock()
	defer m.clientMu.Unlock()

	if c, ok := m.clientCache[proxyURL]; ok {
		return c, nil
	}
	if len(m.clientCache) >= maxCachedClients {
		m.clientCache = make(map[string]*http.Client)
	}
	c, err := newHTTPClient(proxyURL)
	if err != nil {
		return nil, err
	}
	m.clientCache[proxyURL] = c
	return c, nil
}

// newHTTPClient sets dial, TLS handshake, response-header, and whole-request deadlines.
// Configured proxies use proxy.ConfigureTransportProxy for
// HTTP, HTTPS, SOCKS5, and SOCKS5H support.
// Invalid proxy configuration returns an error before any request can use a direct connection.
func newHTTPClient(proxyURL string) (*http.Client, error) {
	transport := &http.Transport{
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext:           (&net.Dialer{Timeout: proxyDialTimeout}).DialContext,
		TLSHandshakeTimeout:   proxyTLSTimeout,
		ResponseHeaderTimeout: searchDataTimeout,
	}
	if proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy URL %q: %w", proxyURL, err)
		}
		if err := proxy.ConfigureTransportProxy(transport, parsed); err != nil {
			return nil, fmt.Errorf("configure proxy: %w", err)
		}
	}
	return &http.Client{Transport: transport, Timeout: searchRequestTimeout}, nil
}

func (m *Executor) buildProvider(cfg ProviderConfig, client *http.Client) Provider {
	switch cfg.Type {
	case braveProviderName:
		return NewBraveProvider(cfg.APIKey, client)
	case tavilyProviderName:
		return NewTavilyProvider(cfg.APIKey, client)
	default:
		slog.Warn("websearch: unknown provider type, falling back to brave",
			"type", cfg.Type)
		return NewBraveProvider(cfg.APIKey, client)
	}
}

// isProxyError classifies network errors and proxy-related messages as connectivity failures.
// This heuristic can also classify direct-connection failures.
func isProxyError(err error) bool {
	if err == nil {
		return false
	}
	// Typed network errors include timeouts, refused connections, and DNS failures.
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	if _, ok := errors.AsType[*net.OpError](err); ok {
		return true
	}
	// An invalid TLS record can indicate a failed connection through the proxy.
	if _, ok := errors.AsType[*tls.RecordHeaderError](err); ok {
		return true
	}
	// Recognize connectivity messages even when error types are unavailable.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "proxy") ||
		strings.Contains(msg, "socks") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "tls handshake") ||
		strings.Contains(msg, "certificate")
}
func (m *Executor) IsProxyError(err error) bool { return isProxyError(err) }
