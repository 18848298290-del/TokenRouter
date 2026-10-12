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

// A request can override the proxy resolved for its search supplier, so HTTP
// clients are cached by the complete selected proxy URL. The empty key selects
// direct transport. Brave and Tavily share these clients within one executor,
// while each supplier adapter supplies its own authentication fields.
//
// Cache lookup, construction, and replacement run under one mutex. A full cache
// is replaced before the next client is constructed; discarded clients can
// still serve exchanges already holding them, and that reset performs no idle
// close. Retirement closes idle connections reachable through the current map,
// and searches finishing after retirement close them again.

// These defaults bound supplier exchanges and client-cache growth; SOCKS
// routing delegates connection dialing to the shared proxy helper.
const (
	defaultMaxResults    = contract.DefaultMaxResults           // default requested result count
	proxyDialTimeout     = 3 * time.Second                      // direct and HTTP-proxy dial timeout
	proxyTLSTimeout      = 3 * time.Second                      // supplier TLS handshake timeout
	searchDataTimeout    = 60 * time.Second                     // response-header wait after request writing
	searchRequestTimeout = searchDataTimeout + proxyDialTimeout // whole HTTP request timeout
	maxCachedClients     = 100                                  // retained client count threshold
)

type (
	// SearchRequest is the shared query, result-count, and proxy-override input.
	SearchRequest = contract.SearchRequest
	// SearchResponse is the shared supplier result and submitted query output.
	SearchResponse = contract.SearchResponse
	// SearchResult is the shared link, title, snippet, and optional age value.
	SearchResult = contract.SearchResult
	// Provider is the shared supplier interface implemented by HTTP search
	// adapters.
	Provider = contract.Provider
	// ProviderConfig is the shared supplier credential and resolved proxy
	// configuration.
	ProviderConfig = contract.ProviderConfig
)

// Executor shares supplier HTTP clients and connectivity classification across
// Manager calls.
type Executor struct {
	// clientMu protects clientCache lookup, creation, replacement, and idle
	// closing. Search releases the lock before HTTP execution so different calls
	// can exchange concurrently.
	clientMu    sync.Mutex              // client cache operation lock
	clientCache map[string]*http.Client // clients keyed by selected proxy URL
}

// NewExecutor constructs an empty HTTP client cache for supplier search calls.
func NewExecutor() *Executor { return &Executor{clientCache: make(map[string]*http.Client)} }

// CloseIdle asks clients in the current cache to close idle connections. It
// holds the cache lock during those calls; active exchanges finish through
// their already selected clients.
func (m *Executor) CloseIdle() {
	m.clientMu.Lock()
	defer m.clientMu.Unlock()
	for _, c := range m.clientCache {
		c.CloseIdleConnections()
	}
}

// Search executes one supplier with the request's selected proxy and normalizes
// its response. Nonempty req.ProxyURL overrides cfg.ProxyURL, invalid proxy
// setup returns an error, and cfg.Type chooses the HTTP adapter.
func (m *Executor) Search(ctx context.Context, cfg ProviderConfig, req SearchRequest) (*SearchResponse, error) {
	proxyURL := cfg.ProxyURL
	// Honor the request-level provider proxy across every supplier attempt;
	// Manager uses this shared choice to decide whether supplier failover is
	// useful.
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

// getOrCreateHTTPClient returns the shared client for a literal proxy URL or
// constructs and caches one. Callers use an Executor from NewExecutor, leave
// returned client configuration stable, and execute HTTP calls after this
// helper releases its mutex.
//
// The lock serializes lookup and construction, preventing simultaneous misses
// from publishing different clients for the same key. When the map reaches its
// limit, replacement drops all remembered clients before construction; setup
// failure leaves that replacement empty. Existing client holders retain their
// exchanges, and discarded idle connections are not closed by replacement.
func (m *Executor) getOrCreateHTTPClient(proxyURL string) (*http.Client, error) {
	m.clientMu.Lock()
	defer m.clientMu.Unlock()

	if c, ok := m.clientCache[proxyURL]; ok {
		return c, nil
	}
	// Drop remembered clients before admitting another proxy key; existing holders
	// retain their clients, and this map replacement performs no idle-connection
	// cleanup.
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

// newHTTPClient constructs a supplier client with a sixty-three-second
// whole-request timeout. Direct and HTTP proxy dialing use three seconds, TLS
// handshakes use three seconds, and response-header waits use sixty seconds.
// SOCKS configuration replaces dialing with the shared helper's ten-second
// forward dialer; invalid proxy setup returns before supplier execution.
func newHTTPClient(proxyURL string) (*http.Client, error) {
	// TLS peers below version 1.2 fail negotiation before supplier credentials are
	// transmitted.
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

// buildProvider constructs the adapter selected by cfg.Type using the supplied
// client. Unknown types log a warning and use Brave; Manager can still report
// the configured type for that attempt.
func (m *Executor) buildProvider(cfg ProviderConfig, client *http.Client) Provider {
	switch cfg.Type {
	case braveProviderName:
		return NewBraveProvider(cfg.APIKey, client)
	case tavilyProviderName:
		return NewTavilyProvider(cfg.APIKey, client)
	default:
		// Use Brave for an unrecognized runtime type so a directly constructed
		// Manager still has an executable adapter for its candidate.
		slog.Warn("websearch: unknown provider type, falling back to brave",
			"type", cfg.Type)
		return NewBraveProvider(cfg.APIKey, client)
	}
}

// isProxyError identifies connectivity failures through wrapped network types
// and lowercase message fragments. Nil returns false; the heuristic can also
// classify failures from direct connections, so Manager's proxy policy depends
// on request configuration.
func isProxyError(err error) bool {
	if err == nil {
		return false
	}
	// Accept wrapped network error types before message matching so transport and
	// DNS failures keep their classification through supplier error wrappers.
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	if _, ok := errors.AsType[*net.OpError](err); ok {
		return true
	}
	// Treat a TLS record-header failure as connectivity-related because
	// negotiation failed before a supplier API response could be decoded.
	if _, ok := errors.AsType[*tls.RecordHeaderError](err); ok {
		return true
	}
	// Inspect text when wrappers expose no recognized network type; these
	// fragments also match direct transport failures and are heuristic evidence.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "proxy") ||
		strings.Contains(msg, "socks") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "tls handshake") ||
		strings.Contains(msg, "certificate")
}

// IsProxyError applies the connectivity heuristic used by Manager after failed
// searches. Nil returns false, and classified direct-connection failures follow
// the same proxy-handling branches when proxy configuration is present.
func (m *Executor) IsProxyError(err error) bool { return isProxyError(err) }
