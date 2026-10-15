package openai

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	proxyurl "github.com/TokenFlux/TokenRouter/internal/infra/httpclient/proxy"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	openaiwsv2 "github.com/TokenFlux/TokenRouter/internal/upstream/openai/ws/relay"
)

// ws_client.go dials OpenAI WebSockets and caches HTTP/1.1 handshake
// transports by proxy and effective TLS profile.
// WebSocket Upgrade uses HTTP/1.1 even when a configured TLS fingerprint
// advertises HTTP/2. Proxy/TLS client selection removes HTTP/2 from the
// effective profile before forming the cache key, keeping the cached transport
// identity consistent with the handshake actually sent.
//
// The dialer reuses clients by normalized proxy and effective fingerprint,
// retires idle entries on cache misses, and evicts least recently used entries
// above capacity. Eviction closes idle HTTP connections while upgraded
// WebSocket ownership stays with the returned adapter. Failed handshakes retain
// a bounded response body and headers for recovery classification. The adapter
// raises the message limit for Codex events and exposes raw frames to relay;
// its idle-ping capability is false because coder/websocket needs an active
// reader to consume the pong.

// WebSocket constants bound complete message reads, handshake-client cache size
// and age, and idle HTTP transport resources reused by proxy/fingerprint Upgrade requests.
const (
	WSMessageReadLimitBytes int64 = 16 * 1024 * 1024

	openAIWSProxyTransportMaxIdleConns        = 128
	openAIWSProxyTransportMaxIdleConnsPerHost = 64
	openAIWSProxyTransportIdleConnTimeout     = 90 * time.Second
	openAIWSProxyClientCacheMaxEntries        = 256
	openAIWSProxyClientCacheIdleTTL           = 15 * time.Minute
)

// The frame assertion checks relay compatibility, and the closed sentinel gives
// pool and adapter callers one typed result for missing or closed connection operations.
var (
	_ openaiwsv2.FrameConn = (*coderOpenAIWSClientConn)(nil)

	// ErrWSConnClosed is the closed-connection sentinel shared with the pool.
	ErrWSConnClosed = errors.New("openai ws connection closed")
)

// WSTransportMetricsSnapshot reports cached-client reuse observations from a
// WebSocket dialer.
type WSTransportMetricsSnapshot struct {
	// ProxyClientCacheHits and ProxyClientCacheMisses count cached reuse and client
	// creation. TransportReuseRatio uses those observed counts, which the coder
	// snapshot reads independently while other callers can still update them.
	ProxyClientCacheHits   int64 `json:"proxy_client_cache_hits"`   // successful cached-client reuse count
	ProxyClientCacheMisses int64 `json:"proxy_client_cache_misses"` // successful handshake-client creation count
	// TransportReuseRatio uses observed hits divided by hits plus creations; the
	// coder snapshot returns zero when neither count has observed an event.
	TransportReuseRatio float64 `json:"transport_reuse_ratio"`
}

// WSClientConn provides contextual message operations and cleanup used by
// connection pooling.
type WSClientConn interface {
	// WriteJSON sends one encoded application value using the supplied operation
	// context. Pool callers keep application writes ordered and retain ownership
	// until the operation returns; the concrete transport determines how
	// cancellation affects later connection reuse.
	WriteJSON(ctx context.Context, value any) error
	// ReadMessage reads one application message payload using the supplied context.
	// Callers retain connection ownership until reading returns, then finish
	// session cleanup before returning a pooled lease. The transport determines
	// whether cancellation also closes its underlying socket.
	ReadMessage(ctx context.Context) ([]byte, error)
	// Ping tests peer liveness using the supplied context and implementation rules.
	// Pool callers consult idle-ping capability before attempting it without
	// an active reader. Reader-dependent transports need another read operation
	// to consume the pong before this liveness check can finish.
	Ping(ctx context.Context) error
	// Close releases connection resources when session ownership ends or setup fails.
	// The implementation owns handshake behavior and error reporting. Pool
	// callers end connection reuse afterward and retain any operation failure
	// separately from the result of resource cleanup.
	Close() error
}

// WSIdlePingCapable reports whether pooling can ping an idle connection without
// reading frames.
type WSIdlePingCapable interface {
	// SupportsIdlePingWithoutReader reports whether idle ping can complete without
	// another frame reader. False directs the pool to its stale-idle retirement
	// path when an active reader is unavailable.
	SupportsIdlePingWithoutReader() bool
}

// WSClientDialer creates upstream connections with prepared handshake headers
// and transport identity.
type WSClientDialer interface {
	// Dial creates a connection using context, URL, headers, proxy, and TLS profile.
	// Callers own returned successful connections; failure status and headers provide handshake recovery evidence.
	Dial(ctx context.Context, wsURL string, headers http.Header, proxyURL string, profile *tlsfingerprint.Profile) (WSClientConn, int, http.Header, error)
}

// WSTransportMetricsDialer exposes optional cached-transport observations for
// pool diagnostics.
type WSTransportMetricsDialer interface {
	// SnapshotTransportMetrics returns cached-client hits, creations, and reuse
	// ratio for diagnostics. Concurrent traffic can progress during observation;
	// the concrete dialer determines whether all returned counters belong to one
	// synchronized snapshot.
	SnapshotTransportMetrics() WSTransportMetricsSnapshot
}

// CoderWSClientDialer owns synchronized proxy/TLS client caching for coder
// WebSocket handshakes.
type CoderWSClientDialer struct {
	// proxyMu protects proxyClients and each entry last-use timestamp. proxyHits
	// and proxyMisses use atomic counters and are read independently by snapshots;
	// callers initialize the cache before concurrent client lookup begins.
	proxyMu      sync.Mutex                           // proxy client state mutex
	proxyClients map[string]*openAIWSProxyClientEntry // shared proxy/TLS client cache
	proxyHits    atomic.Int64                         // cached-client reuse atomic counter
	proxyMisses  atomic.Int64                         // created-client miss atomic counter
}

// WSHandshakeError retains bounded handshake bytes and a cause while Error
// omits the body.
type WSHandshakeError struct {
	Body []byte // captured bounded handshake bytes
	Err  error  // retained handshake dial cause
}

// openAIWSProxyClientEntry holds one shared handshake client and its cache-use
// timestamp.
type openAIWSProxyClientEntry struct {
	client           *http.Client // shared handshake HTTP client
	lastUsedUnixNano int64        // Unix nanoseconds
}

// coderOpenAIWSClientConn adapts the coder connection for pool and raw relay
// operations.
type coderOpenAIWSClientConn struct {
	conn *coderws.Conn // owned coder WebSocket connection
}

// NewDefaultWSClientDialer creates the coder dialer with an initialized client
// cache. Callers share it through their connection resources and use
// request-local settings; direct zero-value dialers require cache
// initialization before cache insertion.
func NewDefaultWSClientDialer() WSClientDialer {
	return &CoderWSClientDialer{
		proxyClients: make(map[string]*openAIWSProxyClientEntry),
	}
}

// Error returns the retained handshake cause message or the fixed failure
// label. Nil wrappers and nil causes use that label; captured body bytes remain
// available for recovery checks rather than formatting.
func (e *WSHandshakeError) Error() string {
	if e == nil || e.Err == nil {
		return "openai ws handshake failed"
	}
	return e.Err.Error()
}

// Unwrap returns the retained handshake cause or nil for nil wrappers. Standard
// error inspection can therefore recognize the underlying transport failure
// while callers retain bounded handshake evidence separately.
func (e *WSHandshakeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Dial opens the trimmed upstream URL using copied headers and optional
// proxy/TLS client selection. Empty targets fail before dialing; failed
// handshakes return captured status, cloned headers, and a bounded-body
// wrapper, while callers own successful connections with the expanded read
// limit.
func (d *CoderWSClientDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
	profile *tlsfingerprint.Profile,
) (WSClientConn, int, http.Header, error) {
	targetURL := strings.TrimSpace(wsURL)
	if targetURL == "" {
		return nil, 0, nil, errors.New("ws url is empty")
	}

	opts := &coderws.DialOptions{
		HTTPHeader:      upstream.CloneHeader(headers),
		CompressionMode: coderws.CompressionContextTakeover,
	}
	if profile != nil || strings.TrimSpace(proxyURL) != "" {
		proxyClient, err := d.ProxyHTTPClient(proxyURL, profile)
		if err != nil {
			return nil, 0, nil, err
		}
		opts.HTTPClient = proxyClient
	}

	conn, resp, err := coderws.Dial(ctx, targetURL, opts)
	if err != nil {
		status := 0
		respHeaders := http.Header(nil)
		if resp != nil {
			status = resp.StatusCode
			respHeaders = upstream.CloneHeader(resp.Header)
		}
		var body []byte
		if resp != nil && resp.Body != nil {
			// Capture a bounded failure prefix and close the handshake response;
			// available bytes remain useful for task checks even when reading that prefix fails.
			body, _ = io.ReadAll(io.LimitReader(resp.Body, 8<<10))
			_ = resp.Body.Close()
		}
		return nil, status, respHeaders, &WSHandshakeError{Body: body, Err: err}
	}
	// Raise coder/websocket's 32 KiB default limit for large Codex events such as
	// rate_limits and deltas, which would otherwise fail locally with message-too-big errors.
	conn.SetReadLimit(WSMessageReadLimitBytes)
	respHeaders := http.Header(nil)
	if resp != nil {
		respHeaders = upstream.CloneHeader(resp.Header)
	}
	return &coderOpenAIWSClientConn{conn: conn}, 0, respHeaders, nil
}

// ProxyHTTPClient returns the shared handshake client for normalized proxy and
// effective TLS profile. Construct the dialer with an initialized cache; nil
// receivers, invalid proxies, and an empty proxy without a profile return
// errors. Callers leave returned client configuration unchanged.
//
// HTTP/2 is removed from the profile before computing its cache key because
// Upgrade needs HTTP/1.1. Under proxyMu a hit updates last use and reuse
// metrics; a miss first retires idle clients, constructs the selected
// transport, and enforces cache capacity before recording creation. Those
// operations publish one client per key while preventing incompatible
// fingerprint identities from sharing a cached transport.
func (d *CoderWSClientDialer) ProxyHTTPClient(proxy string, profile *tlsfingerprint.Profile) (*http.Client, error) {
	if d == nil {
		return nil, errors.New("openai ws dialer is nil")
	}
	normalizedProxy, parsedProxyURL, err := proxyurl.Parse(proxy)
	if err != nil {
		return nil, err
	}
	if normalizedProxy == "" && profile == nil {
		return nil, errors.New("proxy url is empty")
	}
	// Upgrade needs HTTP/1.1, so strip HTTP/2 before deriving the cache key;
	// the effective fingerprint identifies the transport that will actually perform this handshake.
	profile = tlsfingerprint.HTTP1OnlyProfile(profile)
	profileKey := tlsfingerprint.CacheKey(profile)
	cacheKey := normalizedProxy + "|tls:" + profileKey
	now := time.Now().UnixNano()

	d.proxyMu.Lock()
	defer d.proxyMu.Unlock()
	if entry, ok := d.proxyClients[cacheKey]; ok && entry != nil && entry.client != nil {
		entry.lastUsedUnixNano = now
		d.proxyHits.Add(1)
		return entry.client, nil
	}
	d.cleanupProxyClientsLocked(now)
	transport, err := buildOpenAIWSHTTPTransport(normalizedProxy, parsedProxyURL, profile)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Transport: transport}
	d.proxyClients[cacheKey] = &openAIWSProxyClientEntry{
		client:           client,
		lastUsedUnixNano: now,
	}
	d.ensureProxyClientCapacityLocked()
	d.proxyMisses.Add(1)
	return client, nil
}

// buildOpenAIWSHTTPTransport creates an HTTP/1-only Upgrade transport for the
// supplied parsed proxy and profile. A nil profile uses ordinary proxy
// configuration; fingerprint paths choose direct, SOCKS, or HTTP-proxy dialers,
// and unsupported schemes return an error.
func buildOpenAIWSHTTPTransport(normalizedProxy string, parsedProxyURL *url.URL, profile *tlsfingerprint.Profile) (*http.Transport, error) {
	transport := &http.Transport{
		MaxIdleConns:        openAIWSProxyTransportMaxIdleConns,
		MaxIdleConnsPerHost: openAIWSProxyTransportMaxIdleConnsPerHost,
		IdleConnTimeout:     openAIWSProxyTransportIdleConnTimeout,
		// Bound TLS establishment before the HTTP Upgrade headers can arrive.
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   false,
		// Disable automatic HTTP/2 negotiation for the HTTP/1.1 Upgrade handshake.
		TLSNextProto: make(map[string]func(string, *tls.Conn) http.RoundTripper),
	}
	profile = tlsfingerprint.HTTP1OnlyProfile(profile)
	if profile == nil {
		if parsedProxyURL != nil {
			transport.Proxy = http.ProxyURL(parsedProxyURL)
		}
		return transport, nil
	}

	// Use the TLS fingerprint dialer for the wss handshake, including proxy connections.
	switch {
	case parsedProxyURL == nil:
		dialer := tlsfingerprint.NewDialer(profile, nil)
		transport.DialTLSContext = dialer.DialTLSContext
	case parsedProxyURL.Scheme == "socks5" || parsedProxyURL.Scheme == "socks5h":
		dialer := tlsfingerprint.NewSOCKS5ProxyDialer(profile, parsedProxyURL)
		transport.DialTLSContext = dialer.DialTLSContext
	case parsedProxyURL.Scheme == "http" || parsedProxyURL.Scheme == "https":
		dialer := tlsfingerprint.NewHTTPProxyDialer(profile, parsedProxyURL)
		transport.DialTLSContext = dialer.DialTLSContext
	default:
		return nil, fmt.Errorf("unsupported proxy URL for OpenAI WS TLS fingerprint: %s", normalizedProxy)
	}
	return transport, nil
}

// cleanupProxyClientsLocked removes missing or idle-expired cache entries using
// a Unix-nanosecond clock. Callers hold proxyMu; evicted clients close their
// idle HTTP transport connections, while active upgraded WebSockets retain
// their separate connection ownership.
func (d *CoderWSClientDialer) cleanupProxyClientsLocked(nowUnixNano int64) {
	if d == nil || len(d.proxyClients) == 0 {
		return
	}
	idleTTL := openAIWSProxyClientCacheIdleTTL
	if idleTTL <= 0 {
		return
	}
	now := time.Unix(0, nowUnixNano)
	for key, entry := range d.proxyClients {
		if entry == nil || entry.client == nil {
			delete(d.proxyClients, key)
			continue
		}
		lastUsed := time.Unix(0, entry.lastUsedUnixNano)
		if now.Sub(lastUsed) > idleTTL {
			closeOpenAIWSProxyClient(entry.client)
			delete(d.proxyClients, key)
		}
	}
}

// ensureProxyClientCapacityLocked evicts cached clients until the fixed
// capacity is satisfied. Callers hold proxyMu and leave active WebSocket
// ownership with the returned adapters. Nil receivers and disabled capacity
// settings do no work.
//
// Each iteration selects the smallest last-use timestamp, treating a nil entry
// as zero. Equal timestamps follow map iteration and need no stable winner
// because both are equally old cache candidates. Idle transport connections
// close before the cache reference is removed, while repeated selection brings
// the map within its entry limit without closing upgraded sockets owned by
// sessions.
func (d *CoderWSClientDialer) ensureProxyClientCapacityLocked() {
	if d == nil {
		return
	}
	maxEntries := openAIWSProxyClientCacheMaxEntries
	if maxEntries <= 0 {
		return
	}
	for len(d.proxyClients) > maxEntries {
		var oldestKey string
		var oldestLastUsed int64
		hasOldest := false
		for key, entry := range d.proxyClients {
			lastUsed := int64(0)
			if entry != nil {
				lastUsed = entry.lastUsedUnixNano
			}
			if !hasOldest || lastUsed < oldestLastUsed {
				hasOldest = true
				oldestKey = key
				oldestLastUsed = lastUsed
			}
		}
		if !hasOldest {
			return
		}
		if entry := d.proxyClients[oldestKey]; entry != nil {
			closeOpenAIWSProxyClient(entry.client)
		}
		delete(d.proxyClients, oldestKey)
	}
}

// closeOpenAIWSProxyClient closes idle connections on a nonnil standard HTTP
// transport. Nil clients, absent transports, and other transport
// implementations are ignored; upgraded WebSocket connections remain owned by
// their connection adapters.
func closeOpenAIWSProxyClient(client *http.Client) {
	if client == nil || client.Transport == nil {
		return
	}
	if transport, ok := client.Transport.(*http.Transport); ok && transport != nil {
		transport.CloseIdleConnections()
	}
}

// SnapshotTransportMetrics reads hit and created-client counters independently
// and derives their observed reuse ratio. Nil dialers return zeros, no
// observations give ratio zero, and concurrent traffic can progress between the
// two atomic reads.
func (d *CoderWSClientDialer) SnapshotTransportMetrics() WSTransportMetricsSnapshot {
	if d == nil {
		return WSTransportMetricsSnapshot{}
	}
	hits := d.proxyHits.Load()
	misses := d.proxyMisses.Load()
	total := hits + misses
	reuseRatio := 0.0
	if total > 0 {
		reuseRatio = float64(hits) / float64(total)
	}
	return WSTransportMetricsSnapshot{
		ProxyClientCacheHits:   hits,
		ProxyClientCacheMisses: misses,
		TransportReuseRatio:    reuseRatio,
	}
}

// WriteJSON encodes one JSON message through the coder connection under ctx.
// Nil ctx selects Background, unavailable adapters return ErrWSConnClosed, and
// the pool serializes application writes through its connection write lock.
func (c *coderOpenAIWSClientConn) WriteJSON(ctx context.Context, value any) error {
	if c == nil || c.conn == nil {
		return ErrWSConnClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return wsjson.Write(ctx, c.conn, value)
}

// ReadMessage returns one text or binary message payload under ctx. Nil ctx
// selects Background, unavailable adapters or unsupported message types return
// ErrWSConnClosed, and callers serialize reads through the owning pooled
// connection.
func (c *coderOpenAIWSClientConn) ReadMessage(ctx context.Context) ([]byte, error) {
	if c == nil || c.conn == nil {
		return nil, ErrWSConnClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}

	msgType, payload, err := c.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	switch msgType {
	case coderws.MessageText, coderws.MessageBinary:
		return payload, nil
	default:
		return nil, ErrWSConnClosed
	}
}

// ReadFrame returns one raw message type and payload under ctx for relay. Nil
// ctx selects Background; unavailable adapters return the closed sentinel, and
// read errors retain their cause while using a text type placeholder.
func (c *coderOpenAIWSClientConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if c == nil || c.conn == nil {
		return coderws.MessageText, nil, ErrWSConnClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	msgType, payload, err := c.conn.Read(ctx)
	if err != nil {
		return coderws.MessageText, nil, err
	}
	return msgType, payload, nil
}

// WriteFrame sends one raw payload and message type through the coder
// connection under ctx. Nil ctx selects Background and unavailable adapters
// return ErrWSConnClosed; relay owns the timing and order of those writes.
func (c *coderOpenAIWSClientConn) WriteFrame(ctx context.Context, msgType coderws.MessageType, payload []byte) error {
	if c == nil || c.conn == nil {
		return ErrWSConnClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return c.conn.Write(ctx, msgType, payload)
}

// Ping delegates liveness checking to coder/websocket under ctx and needs an
// active reader for pong processing. Nil ctx selects Background and unavailable
// adapters return ErrWSConnClosed; the pool consults idle capability before
// using it.
func (c *coderOpenAIWSClientConn) Ping(ctx context.Context) error {
	if c == nil || c.conn == nil {
		return ErrWSConnClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return c.conn.Ping(ctx)
}

// SupportsIdlePingWithoutReader returns false because coder Ping waits for a
// pong processed by Read. The pool has no idle reader, so it retires stale idle
// connections rather than using a ping that cannot complete.
func (*coderOpenAIWSClientConn) SupportsIdlePingWithoutReader() bool {
	return false
}

// Close attempts normal coder closure and then immediate transport cleanup,
// returning nil even when either operation fails. Nil adapters are ignored, and
// repeated cleanup remains successful for connection owners ending their
// session.
func (c *coderOpenAIWSClientConn) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	// Pool cleanup can revisit closure after another session path ended the
	// socket. Suppress close errors while forcing transport cleanup so ownership still ends successfully.
	_ = c.conn.Close(coderws.StatusNormalClosure, "")
	_ = c.conn.CloseNow()
	return nil
}
