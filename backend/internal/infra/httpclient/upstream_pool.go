package httpclient

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"golang.org/x/net/http2"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/proxy"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	servertiming "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
)

const (
	defaultUpstreamDialTimeout         = 10 * time.Second
	defaultUpstreamDialKeepAlive       = 30 * time.Second
	defaultUpstreamTLSHandshakeTimeout = 10 * time.Second
	http2ReadIdleTimeout               = 15 * time.Second
	http2PingTimeout                   = 15 * time.Second
)

var (
	// ErrUpstreamClientLimitReached reports that the cache is full and every entry is in use.
	ErrUpstreamClientLimitReached = errors.New("upstream client cache limit reached")

	errResponseHeaderTimeout = &responseHeaderTimeoutError{}
)

// TransportProtocol selects HTTP negotiation and the caller-defined cache variant.
type TransportProtocol struct {
	CacheVariant string
	HTTP2        bool
	DisableHTTP2 bool
}

// UpstreamRequestOptions configures one outbound request and its connection pool.
type UpstreamRequestOptions struct {
	ProxyURL   string
	ProviderID int64
	Isolation  string
	MaxClients int
	IdleTTL    time.Duration
	Settings   UpstreamSettings
	Protocol   TransportProtocol
	TLSProfile *tlsfingerprint.Profile
	// CheckRedirect applies to this request; nil uses the net/http default.
	CheckRedirect func(*http.Request, []*http.Request) error
	// PrepareClient receives a client copy with CheckRedirect set. It may replace
	// callbacks or wrap the shared transport, but must not mutate that transport.
	PrepareClient func(*http.Client) *http.Client
	// ObserveResult runs after response headers or failure, before release or body processing.
	ObserveResult func(error)
}

// UpstreamPool caches ordinary and fingerprinted clients and tracks in-flight requests.
type UpstreamPool struct {
	mu      sync.RWMutex
	clients map[string]*upstreamClientEntry
}

// UpstreamSettings configures connection limits and timeouts.
type UpstreamSettings struct {
	MaxIdleConns          int           // Maximum idle connections across hosts.
	MaxIdleConnsPerHost   int           // Maximum idle connections per host.
	MaxConnsPerHost       int           // Maximum connections per host, including active connections.
	IdleConnTimeout       time.Duration // Maximum idle connection age.
	ResponseHeaderTimeout time.Duration // Maximum wait for response headers.
}

// upstreamClientEntry tracks a cached client and the metadata used for reuse and eviction.
type upstreamClientEntry struct {
	client       *http.Client
	proxyKey     string // Detects proxy changes under provider isolation.
	poolKey      string // Detects connection pool configuration changes.
	protocolMode string // Caller-defined protocol cache variant.
	lastUsed     int64  // Unix nanoseconds; used for LRU eviction.
	inFlight     int64  // Outstanding requests; positive counts prevent idle eviction.
}

type responseHeaderTimeoutError struct{}

// responseHeaderTimeoutRoundTripper bounds RoundTrip for an http2.Transport.
// The timer stops at response headers so it does not cancel a long-lived SSE body.
type responseHeaderTimeoutRoundTripper struct {
	base    http.RoundTripper
	timeout time.Duration
}

type cancelOnCloseReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

// trackedBody runs its release callback once when the body is closed.
type trackedBody struct {
	io.ReadCloser
	once    sync.Once
	onClose func()
}

type zstdResponseReader struct {
	io.ReadCloser
	warnOnce sync.Once
}

// decompressedBody closes the decoder and the underlying response body.
type decompressedBody struct {
	reader io.Reader
	closer io.Closer
}

// NewUpstreamPool creates a pool with its own client cache.
func NewUpstreamPool() *UpstreamPool {
	return &UpstreamPool{clients: make(map[string]*upstreamClientEntry)}
}

// Do acquires a client, sends the request, and decodes supported response encodings.
// The caller must close the response body to release the in-flight count.
// @project-doc docs/operations/upstream_transport_security.md#upstream_client_pool
func (s *UpstreamPool) Do(req *http.Request, opts UpstreamRequestOptions) (*http.Response, error) {
	entry, err := s.acquire(opts)
	if err != nil {
		return nil, err
	}
	release := func() {
		atomic.AddInt64(&entry.inFlight, -1)
		atomic.StoreInt64(&entry.lastUsed, time.Now().UnixNano())
	}
	// Copy callbacks per request so concurrent requests cannot overwrite each other's policies.
	requestClient := *entry.client
	requestClient.CheckRedirect = opts.CheckRedirect
	client := &requestClient
	if opts.PrepareClient != nil {
		client = opts.PrepareClient(client)
	}
	if req != nil {
		req = req.WithContext(servertiming.BeginHTTPTrace(req.Context()))
	}
	resp, err := servertiming.Do(client, req)
	if opts.ObserveResult != nil {
		opts.ObserveResult(err)
	}
	if err != nil {
		release()
		return nil, err
	}
	decompressResponseBody(resp)
	resp.Body = wrapTrackedBody(resp.Body, release)
	return resp, nil
}

// acquire reuses compatible clients and increments their in-flight count.
func (s *UpstreamPool) acquire(opts UpstreamRequestOptions) (*upstreamClientEntry, error) {
	isolation := opts.Isolation
	proxyKey, parsedProxy, err := proxy.NormalizePoolKey(opts.ProxyURL)
	if err != nil {
		return nil, err
	}
	settings := opts.Settings
	protocolMode := opts.Protocol.CacheVariant
	cacheKey := buildCacheKey(isolation, proxyKey, opts.ProviderID, protocolMode)
	poolKey := buildPoolKey(settings, protocolMode)
	if opts.TLSProfile != nil {
		profileKey := tlsfingerprint.CacheKey(opts.TLSProfile)
		cacheKey = "tls:" + profileKey + ":" + cacheKey
		poolKey += ":tls:" + profileKey
	}
	now := time.Now()
	nowUnix := now.UnixNano()

	// Use a read lock for cache hits.
	s.mu.RLock()
	if entry, ok := s.clients[cacheKey]; ok && s.shouldReuseEntry(entry, isolation, proxyKey, poolKey) {
		atomic.StoreInt64(&entry.lastUsed, nowUnix)
		{
			atomic.AddInt64(&entry.inFlight, 1)
		}
		s.mu.RUnlock()
		return entry, nil
	}
	s.mu.RUnlock()

	// Recheck under the write lock before creating or replacing a client.
	s.mu.Lock()
	if entry, ok := s.clients[cacheKey]; ok {
		if s.shouldReuseEntry(entry, isolation, proxyKey, poolKey) {
			atomic.StoreInt64(&entry.lastUsed, nowUnix)
			{
				atomic.AddInt64(&entry.inFlight, 1)
			}
			s.mu.Unlock()
			return entry, nil
		}
		s.removeClientLocked(cacheKey, entry)
	}

	// At capacity, reject acquisition when no idle entry can be evicted.
	if opts.MaxClients > 0 {
		s.evictIdleLocked(now, opts.IdleTTL)
		if len(s.clients) >= opts.MaxClients {
			if !s.evictOldestIdleLocked() {
				s.mu.Unlock()
				return nil, ErrUpstreamClientLimitReached
			}
		}
	}

	var transport http.RoundTripper
	if opts.TLSProfile == nil {
		transport, err = buildUpstreamTransport(settings, parsedProxy, opts.Protocol)
	} else {
		transport, err = buildUpstreamTransportWithTLSFingerprint(settings, parsedProxy, opts.TLSProfile, opts.Protocol)
	}
	if err != nil {
		s.mu.Unlock()
		if opts.TLSProfile != nil {
			return nil, fmt.Errorf("build TLS fingerprint transport: %w", err)
		}
		return nil, fmt.Errorf("build transport: %w", err)
	}
	client := &http.Client{Transport: transport}
	entry := &upstreamClientEntry{
		client:       client,
		proxyKey:     proxyKey,
		poolKey:      poolKey,
		protocolMode: protocolMode,
	}
	atomic.StoreInt64(&entry.lastUsed, nowUnix)
	{
		atomic.StoreInt64(&entry.inFlight, 1)
	}
	s.clients[cacheKey] = entry

	// Evict expired idle entries before enforcing the entry limit.
	s.evictIdleLocked(now, opts.IdleTTL)
	s.evictOverLimitLocked(opts.MaxClients)
	s.mu.Unlock()
	return entry, nil
}

// shouldReuseEntry reports whether the proxy and pool configuration still match.
func (s *UpstreamPool) shouldReuseEntry(entry *upstreamClientEntry, isolation, proxyKey, poolKey string) bool {
	if entry == nil {
		return false
	}
	if isolation == "provider" && entry.proxyKey != proxyKey {
		return false
	}
	if entry.poolKey != poolKey {
		return false
	}
	return true
}

// removeClientLocked deletes the entry and closes idle connections.
// The caller must hold s.mu for writing.
func (s *UpstreamPool) removeClientLocked(key string, entry *upstreamClientEntry) {
	delete(s.clients, key)
	if entry != nil && entry.client != nil {
		// Active connections remain open.
		entry.client.CloseIdleConnections()
		if closer, ok := entry.client.Transport.(interface {
			CloseIdleConnections()
		}); ok {
			closer.CloseIdleConnections()
		}
	}
}

// evictIdleLocked removes expired entries with no in-flight requests.
// The caller must hold s.mu for writing; a nonpositive TTL disables expiration.
func (s *UpstreamPool) evictIdleLocked(now time.Time, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	cutoff := now.Add(-ttl).UnixNano()
	for key, entry := range s.clients {
		if atomic.LoadInt64(&entry.inFlight) != 0 {
			continue
		}
		if atomic.LoadInt64(&entry.lastUsed) <= cutoff {
			s.removeClientLocked(key, entry)
		}
	}
}

// evictOldestIdleLocked removes the least recently used idle entry.
// The caller must hold s.mu for writing. It returns false when every entry is active.
func (s *UpstreamPool) evictOldestIdleLocked() bool {
	var (
		oldestKey   string
		oldestEntry *upstreamClientEntry
		oldestTime  int64
	)
	for key, entry := range s.clients {
		if atomic.LoadInt64(&entry.inFlight) != 0 {
			continue
		}
		lastUsed := atomic.LoadInt64(&entry.lastUsed)
		if oldestEntry == nil || lastUsed < oldestTime {
			oldestKey = key
			oldestEntry = entry
			oldestTime = lastUsed
		}
	}
	if oldestEntry == nil {
		return false
	}
	s.removeClientLocked(oldestKey, oldestEntry)
	return true
}

// evictOverLimitLocked removes idle entries in LRU order until the limit is met
// or no idle entries remain. The caller must hold s.mu for writing.
func (s *UpstreamPool) evictOverLimitLocked(maxClients int) bool {
	if maxClients <= 0 {
		return false
	}
	evicted := false
	for len(s.clients) > maxClients {
		if !s.evictOldestIdleLocked() {
			return evicted
		}
		evicted = true
	}
	return evicted
}

// buildPoolKey identifies connection settings and the protocol cache variant.
func buildPoolKey(settings UpstreamSettings, protocolMode string) string {
	base := fmt.Sprintf(
		"idle:%d|idle_host:%d|max:%d|idle_timeout:%s|header_timeout:%s",
		settings.MaxIdleConns,
		settings.MaxIdleConnsPerHost,
		settings.MaxConnsPerHost,
		settings.IdleConnTimeout,
		settings.ResponseHeaderTimeout,
	)
	if protocolMode == "" || protocolMode == "default" {
		return base
	}
	return base + "|proto:" + protocolMode
}

// buildCacheKey selects proxy, provider, or combined isolation and appends
// the protocol variant. Unknown isolation values use proxy isolation.
func buildCacheKey(isolation, proxyKey string, providerID int64, protocolMode string) string {
	var base string
	switch isolation {
	case "provider":
		base = fmt.Sprintf("provider:%d", providerID)
	case "provider_proxy":
		base = fmt.Sprintf("provider:%d|proxy:%s", providerID, proxyKey)
	default:
		base = fmt.Sprintf("proxy:%s", proxyKey)
	}
	if protocolMode != "" && protocolMode != "default" {
		base += "|proto:" + protocolMode
	}
	return base
}

// newUpstreamDialer bounds DNS resolution and TCP connection establishment.
// A nil Transport.DialContext uses a zero-value dialer with no dial timeout.
func newUpstreamDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   defaultUpstreamDialTimeout,
		KeepAlive: defaultUpstreamDialKeepAlive,
	}
}

// buildUpstreamTransport applies pool settings, HTTP negotiation, and proxy routing.
// A nil proxyURL selects a direct connection. Proxy configuration errors are returned.
func buildUpstreamTransport(settings UpstreamSettings, proxyURL *url.URL, protocol TransportProtocol) (*http.Transport, error) {
	transport := &http.Transport{
		DialContext:           newUpstreamDialer().DialContext,
		TLSHandshakeTimeout:   defaultUpstreamTLSHandshakeTimeout,
		MaxIdleConns:          settings.MaxIdleConns,
		MaxIdleConnsPerHost:   settings.MaxIdleConnsPerHost,
		MaxConnsPerHost:       settings.MaxConnsPerHost,
		IdleConnTimeout:       settings.IdleConnTimeout,
		ResponseHeaderTimeout: settings.ResponseHeaderTimeout,
	}
	switch {
	case protocol.HTTP2:
		transport.ForceAttemptHTTP2 = true
		// Idle PINGs detect connections silently dropped by a proxy or NAT.
		enableHTTP2KeepAlive(transport)
	case protocol.DisableHTTP2:
		transport.ForceAttemptHTTP2 = false
		transport.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	}
	if err := proxy.ConfigureTransportProxy(transport, proxyURL); err != nil {
		return nil, err
	}
	return transport, nil
}

// enableHTTP2KeepAlive configures HTTP/2 negotiation and idle PINGs.
func enableHTTP2KeepAlive(transport *http.Transport) {
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = new(tls.Config)
	}
	if transport.Protocols == nil {
		transport.Protocols = new(http.Protocols)
		transport.Protocols.SetHTTP1(true)
	}
	transport.Protocols.SetHTTP2(true)
	if transport.HTTP2 == nil {
		transport.HTTP2 = new(http.HTTP2Config)
	}
	transport.HTTP2.SendPingTimeout = http2ReadIdleTimeout
	transport.HTTP2.PingTimeout = http2PingTimeout
}

// buildUpstreamTransportWithTLSFingerprint uses the selected ClientHello profile
// for direct, HTTP CONNECT, and SOCKS connections. HTTPS proxies use the ordinary
// transport. HTTP/2 requires both protocol.HTTP2 and h2 support in the profile.
func buildUpstreamTransportWithTLSFingerprint(settings UpstreamSettings, proxyURL *url.URL, profile *tlsfingerprint.Profile, protocol TransportProtocol) (http.RoundTripper, error) {
	useHTTP2 := protocol.HTTP2 && tlsfingerprint.SupportsHTTP2(profile)
	transport := &http.Transport{
		MaxIdleConns:          settings.MaxIdleConns,
		MaxIdleConnsPerHost:   settings.MaxIdleConnsPerHost,
		MaxConnsPerHost:       settings.MaxConnsPerHost,
		IdleConnTimeout:       settings.IdleConnTimeout,
		ResponseHeaderTimeout: settings.ResponseHeaderTimeout,
		// Use the HTTP/2 round tripper only when the profile advertises h2.
		ForceAttemptHTTP2: useHTTP2,
	}

	var dialTLSContext func(ctx context.Context, network, addr string) (net.Conn, error)
	if proxyURL == nil {
		slog.Debug("tls_fingerprint_transport_direct")
		dialer := tlsfingerprint.NewDialer(profile, nil)
		dialTLSContext = dialer.DialTLSContext
	} else {
		scheme := strings.ToLower(proxyURL.Scheme)
		switch scheme {
		case "socks5", "socks5h":
			slog.Debug("tls_fingerprint_transport_socks5", "proxy", proxyURL.Host)
			socks5Dialer := tlsfingerprint.NewSOCKS5ProxyDialer(profile, proxyURL)
			dialTLSContext = socks5Dialer.DialTLSContext
		case "https":
			// Route HTTPS proxies through the ordinary transport.
			return buildUpstreamTransport(settings, proxyURL, TransportProtocol{CacheVariant: "default"})
		case "http":
			slog.Debug("tls_fingerprint_transport_http_connect", "proxy", proxyURL.Host)
			httpDialer := tlsfingerprint.NewHTTPProxyDialer(profile, proxyURL)
			dialTLSContext = httpDialer.DialTLSContext
		default:
			// Let the ordinary proxy configuration validate other schemes.
			slog.Debug("tls_fingerprint_transport_unknown_scheme_fallback", "scheme", scheme)
			if err := proxy.ConfigureTransportProxy(transport, proxyURL); err != nil {
				return nil, err
			}
		}
	}
	transport.DialTLSContext = dialTLSContext

	if useHTTP2 && dialTLSContext != nil {
		//nolint:staticcheck // uTLS has its own ConnectionState type; x/net accepts the raw TLS connection.
		h2Transport := &http2.Transport{
			IdleConnTimeout: settings.IdleConnTimeout,
			ReadIdleTimeout: http2ReadIdleTimeout,
			PingTimeout:     http2PingTimeout,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				conn, err := dialTLSContext(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				negotiatedProtocol := tlsfingerprint.NegotiatedProtocol(conn)
				if negotiatedProtocol != "h2" {
					_ = conn.Close()
					return nil, fmt.Errorf("http2: unexpected ALPN protocol %q; want %q", negotiatedProtocol, "h2")
				}
				return conn, nil
			},
		}
		var roundTripper http.RoundTripper = h2Transport
		if settings.ResponseHeaderTimeout > 0 {
			roundTripper = &responseHeaderTimeoutRoundTripper{
				base:    h2Transport,
				timeout: settings.ResponseHeaderTimeout,
			}
		}
		return roundTripper, nil
	}

	return transport, nil
}

func (e *responseHeaderTimeoutError) Error() string {
	return "net/http: timeout awaiting response headers"
}

func (e *responseHeaderTimeoutError) Timeout() bool {
	return true
}

func (e *responseHeaderTimeoutError) Temporary() bool {
	return true
}

// RoundTrip cancels on timeout and stops the timer when response headers arrive.
func (r *responseHeaderTimeoutRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if r == nil || r.base == nil {
		return nil, errors.New("response header timeout round tripper missing base")
	}
	if r.timeout <= 0 || req == nil {
		return r.base.RoundTrip(req)
	}
	ctx, cancel := context.WithCancel(req.Context())
	timeoutCh := make(chan struct{})
	timedOut := atomic.Bool{}
	timer := time.AfterFunc(r.timeout, func() {
		timedOut.Store(true)
		close(timeoutCh)
		cancel()
	})
	resp, err := r.base.RoundTrip(req.WithContext(ctx))
	if !timer.Stop() {
		<-timeoutCh
	}
	if timedOut.Load() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		cancel()
		return nil, errResponseHeaderTimeout
	}
	if err != nil {
		cancel()
		return nil, err
	}
	if resp == nil || resp.Body == nil {
		cancel()
		return resp, nil
	}
	resp.Body = &cancelOnCloseReadCloser{
		ReadCloser: resp.Body,
		cancel:     cancel,
	}
	return resp, nil
}

// CloseIdleConnections closes idle connections in the underlying transport.
func (r *responseHeaderTimeoutRoundTripper) CloseIdleConnections() {
	if r == nil || r.base == nil {
		return
	}
	if closer, ok := r.base.(interface {
		CloseIdleConnections()
	}); ok {
		closer.CloseIdleConnections()
	}
}

// Close closes the body and cancels its request context.
func (r *cancelOnCloseReadCloser) Close() error {
	err := r.ReadCloser.Close()
	if r.cancel != nil {
		r.cancel()
	}
	return err
}

// Close closes the body and invokes the release callback at most once.
func (b *trackedBody) Close() error {
	err := b.ReadCloser.Close()
	if b.onClose != nil {
		b.once.Do(b.onClose)
	}
	return err
}

// wrapTrackedBody attaches an at-most-once release callback to body.Close.
// A nil body is returned unchanged.
func wrapTrackedBody(body io.ReadCloser, onClose func()) io.ReadCloser {
	if body == nil {
		return body
	}
	return &trackedBody{ReadCloser: body, onClose: onClose}
}

// decompressResponseBody decodes supported Content-Encoding values.
// The transport does not decode responses when the request supplies Accept-Encoding.
// After installing a decoder, this function removes encoding and length headers.
func decompressResponseBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	ce := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	if ce == "" {
		return
	}

	originalBody := resp.Body
	var reader io.Reader
	switch ce {
	case "gzip":
		gr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return // Leave the encoded body unchanged if decoder initialization fails.
		}
		reader = gr
	case "br":
		reader = brotli.NewReader(resp.Body)
	case "deflate":
		reader = flate.NewReader(resp.Body)
	case "zstd":
		bufferedBody := bufio.NewReader(resp.Body)
		resp.Body = &decompressedBody{reader: bufferedBody, closer: originalBody}

		headerBytes, _ := bufferedBody.Peek(zstd.HeaderMaxSize)
		var header zstd.Header
		if err := header.Decode(headerBytes); err != nil {
			slog.Warn("zstd_decompress_failed", "error", err)
			return
		}

		zr, err := zstd.NewReader(bufferedBody)
		if err != nil {
			slog.Warn("zstd_decompress_failed", "error", err)
			return
		}
		reader = &zstdResponseReader{ReadCloser: zr.IOReadCloser()}
	default:
		return
	}

	resp.Body = &decompressedBody{reader: reader, closer: originalBody}
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Content-Length") // Decoded length is unknown.
	resp.ContentLength = -1
}

func (r *zstdResponseReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		r.warnOnce.Do(func() {
			slog.Warn("zstd_decompress_failed", "error", err)
		})
	}
	return n, err
}

func (d *decompressedBody) Read(p []byte) (int, error) {
	return d.reader.Read(p)
}

func (d *decompressedBody) Close() error {
	// Close decoder resources before the underlying body.
	if rc, ok := d.reader.(io.Closer); ok {
		_ = rc.Close()
	}
	return d.closer.Close()
}

// CloseIdleConnections releases idle connections while retaining cached clients.
// Callers must still close active response bodies.
func (s *UpstreamPool) CloseIdleConnections() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, entry := range s.clients {
		if entry != nil && entry.client != nil {
			entry.client.CloseIdleConnections()
		}
	}
}
