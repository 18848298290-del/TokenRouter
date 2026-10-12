package transport

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	proxyinfra "github.com/TokenFlux/TokenRouter/internal/infra/httpclient/proxy"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

const (
	// Defaults used when transport options are absent or outside their accepted ranges.
	// defaultMaxIdleConns limits idle connections across hosts.
	defaultMaxIdleConns = 240
	// defaultMaxIdleConnsPerHost limits idle connections to one host.
	defaultMaxIdleConnsPerHost = 120
	// defaultMaxConnsPerHost includes active connections; new requests wait at the limit.
	defaultMaxConnsPerHost = 240
	// defaultIdleConnTimeout bounds how long unused connections remain open.
	defaultIdleConnTimeout = 90 * time.Second
	// defaultResponseHeaderTimeout allows for upstream generation queues.
	defaultResponseHeaderTimeout = 300 * time.Second
	// defaultMaxUpstreamClients limits the cached upstream clients.
	defaultMaxUpstreamClients = 5000
	// defaultClientIdleTTLSeconds is the client eviction idle threshold in seconds.
	defaultClientIdleTTLSeconds = 900
	// Defaults for switching an incompatible OpenAI proxy to HTTP/1.1.
	defaultOpenAIHTTP2FallbackErrorThreshold = 2
	defaultOpenAIHTTP2FallbackWindow         = 60 * time.Second
	defaultOpenAIHTTP2FallbackTTL            = 10 * time.Minute

	// Hosts used by the Grok CLI access-denied retry path.
	grokCLIProxyHost    = "cli-chat-proxy.grok.com"
	grokOfficialAPIHost = "api.x.ai"

	upstreamProtocolModeDefault          = "default"
	upstreamProtocolModeOpenAIH1         = "openai_h1"
	upstreamProtocolModeOpenAIH2         = "openai_h2"
	upstreamProtocolModeOpenAIH1Fallback = "openai_h1_fallback"
)

type openAIHTTP2Settings struct {
	enabled                   bool
	allowProxyFallbackToHTTP1 bool
	fallbackErrorThreshold    int
	fallbackWindow            time.Duration
	fallbackTTL               time.Duration
}

// poolSettings contains connection pool settings supplied by the application.
type poolSettings = httpclient.UpstreamSettings

// upstreamPool lets tests substitute the transport used for upstream requests.
type upstreamPool interface {
	Do(*http.Request, httpclient.UpstreamRequestOptions) (*http.Response, error)
}

// Client applies provider transport policies and reuses an upstream connection pool.
type Client struct {
	source          func() *Options
	pool            upstreamPool
	transportPolicy egress.TransportPolicy
}

// httpClientForEgressPolicy clones the client when a request needs redirect restrictions.
func httpClientForEgressPolicy(s *Client, client *http.Client, policy egress.EgressPolicy) *http.Client {
	if client == nil {
		return nil
	}
	switch {
	case policy.DisableRedirects:
		clone := *client
		clone.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		return &clone
	case policy.PublicHostsOnly && s != nil:
		clone := *client
		clone.CheckRedirect = func(next *http.Request, via []*http.Request) error {
			// Apply the public-host check at every hop before the client
			// runs its existing redirect policy.
			next = next.WithContext(upstream.WithHTTPUpstreamPublicHostsOnly(next.Context()))
			if err := s.redirectChecker(next, via); err != nil {
				return err
			}
			if client.CheckRedirect != nil {
				return client.CheckRedirect(next, via)
			}
			return nil
		}
		return &clone
	default:
		return client
	}
}

func httpClientWithGrokAccessDeniedFallback(client *http.Client) *http.Client {
	return xai.ClientWithAccessDeniedFallback(client)
}

func applyGrokCLIProxyHeaders(req *http.Request) { xai.ApplyTransportCLIHeaders(req) }

func (s *Client) shouldValidateResolvedIP() bool {
	if s.options() == nil {
		return false
	}
	return s.options().ValidateResolvedIP
}

func (s *Client) validateRequestHost(req *http.Request) error {
	if !s.requestPolicy(req).RequiresHostValidation() {
		return nil
	}
	if req == nil || req.URL == nil {
		return errors.New("request url is nil")
	}
	host := strings.TrimSpace(req.URL.Hostname())
	if host == "" {
		return errors.New("request host is empty")
	}
	return httpclient.ValidateResolvedIP(host)
}

func (s *Client) redirectChecker(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return s.validateRequestHost(req)
}

// getIsolationMode returns proxy, provider, or provider_proxy.
// Missing or invalid settings use provider_proxy.
func (s *Client) getIsolationMode() string {
	if s.options() == nil {
		return "provider_proxy"
	}
	mode := strings.ToLower(strings.TrimSpace(s.options().ConnectionPoolIsolation))
	if mode == "" {
		return "provider_proxy"
	}
	switch mode {
	case "proxy", "provider", "provider_proxy":
		return mode
	default:
		return "provider_proxy"
	}
}

// maxUpstreamClients returns the configured cache limit or the default for nonpositive values.
func (s *Client) maxUpstreamClients() int {
	if s.options() == nil {
		return defaultMaxUpstreamClients
	}
	if s.options().MaxUpstreamClients > 0 {
		return s.options().MaxUpstreamClients
	}
	return defaultMaxUpstreamClients
}

// clientIdleTTL returns the configured idle lifetime or the default for nonpositive values.
func (s *Client) clientIdleTTL() time.Duration {
	if s.options() == nil {
		return time.Duration(defaultClientIdleTTLSeconds) * time.Second
	}
	if s.options().ClientIdleTTLSeconds > 0 {
		return time.Duration(s.options().ClientIdleTTLSeconds) * time.Second
	}
	return time.Duration(defaultClientIdleTTLSeconds) * time.Second
}

// resolvePoolSettings sizes provider-isolated pools to a positive provider concurrency limit.
// Other pools use the configured global limits.
func (s *Client) resolvePoolSettings(isolation string, providerConcurrency int) poolSettings {
	settings := defaultPoolSettings(s.options())
	if (isolation == "provider" || isolation == "provider_proxy") && providerConcurrency > 0 {
		settings.MaxIdleConns = providerConcurrency
		settings.MaxIdleConnsPerHost = providerConcurrency
		settings.MaxConnsPerHost = providerConcurrency
	}
	return settings
}

func (s *Client) applyProfilePoolSettings(settings poolSettings, profile upstream.HTTPUpstreamProfile) poolSettings {
	switch profile {
	case upstream.HTTPUpstreamProfileOpenAI:
		settings.ResponseHeaderTimeout = 0
		if s != nil && s.options() != nil && s.options().OpenAIResponseHeaderTimeout > 0 {
			settings.ResponseHeaderTimeout = time.Duration(s.options().OpenAIResponseHeaderTimeout) * time.Second
		}
	case upstream.HTTPUpstreamProfileGrok:
		// Bound the Grok response-header wait separately from the stream after headers arrive.
		settings.ResponseHeaderTimeout = 120 * time.Second
		if s != nil && s.options() != nil {
			settings.ResponseHeaderTimeout = time.Duration(s.options().GrokResponseHeaderTimeout) * time.Second
		}
	}
	return settings
}

func (s *Client) resolveOpenAIHTTP2Settings() openAIHTTP2Settings {
	settings := openAIHTTP2Settings{
		enabled:                   false,
		allowProxyFallbackToHTTP1: true,
		fallbackErrorThreshold:    defaultOpenAIHTTP2FallbackErrorThreshold,
		fallbackWindow:            defaultOpenAIHTTP2FallbackWindow,
		fallbackTTL:               defaultOpenAIHTTP2FallbackTTL,
	}
	if s == nil || s.options() == nil {
		return settings
	}
	cfg := s.options().OpenAIHTTP2
	settings.enabled = cfg.Enabled
	settings.allowProxyFallbackToHTTP1 = cfg.AllowProxyFallbackToHTTP1
	if cfg.FallbackErrorThreshold > 0 {
		settings.fallbackErrorThreshold = cfg.FallbackErrorThreshold
	}
	if cfg.FallbackWindowSeconds > 0 {
		settings.fallbackWindow = time.Duration(cfg.FallbackWindowSeconds) * time.Second
	}
	if cfg.FallbackTTLSeconds > 0 {
		settings.fallbackTTL = time.Duration(cfg.FallbackTTLSeconds) * time.Second
	}
	return settings
}

func resolveTLSFingerprintTransportProfile(profile *tlsfingerprint.Profile, protocolMode string) *tlsfingerprint.Profile {
	if protocolMode != upstreamProtocolModeOpenAIH2 {
		return tlsfingerprint.HTTP1OnlyProfile(profile)
	}
	return profile
}

func isOpenAIHTTP2CompatibilityError(err error) bool {
	if err == nil {
		return false
	}
	if isUpstreamTimeoutError(err) {
		return false
	}
	msg := strings.ToLower(err.Error())
	if msg == "" {
		return false
	}
	markers := []string{
		"alpn",
		"no application protocol",
		"protocol error",
		"stream error",
		"goaway",
		"refused_stream",
		"frame too large",
	}
	for _, marker := range markers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func isUpstreamTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	if msg == "" {
		return false
	}
	timeoutMarkers := []string{
		"timeout awaiting response headers",
		"i/o timeout",
		"context deadline exceeded",
		"client.timeout exceeded while awaiting headers",
		"tls handshake timeout",
	}
	for _, marker := range timeoutMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func (s *Client) recordOpenAIHTTP2Failure(profile upstream.HTTPUpstreamProfile, protocolMode, proxyKey string, err error) {
	activated, until := s.transportPolicy.ObserveFailure(string(profile), protocolMode, proxyKey, isOpenAIHTTP2CompatibilityError(err), s.http2Options(), time.Now())
	if activated {
		slog.Warn("openai_http2_proxy_fallback_activated", "proxy", proxyKey, "fallback_until", until.Format(time.RFC3339))
	}
}

func (s *Client) recordOpenAIHTTP2Success(profile upstream.HTTPUpstreamProfile, protocolMode, proxyKey string) {
	s.transportPolicy.ObserveSuccess(string(profile), protocolMode, proxyKey)
}

// normalizeProxyURL returns a canonical cache key and parsed proxy URL.
// Empty input returns "direct" and nil; invalid nonempty input returns an error.
func normalizeProxyURL(raw string) (string, *url.URL, error) {
	return proxyinfra.NormalizePoolKey(raw)
}

// defaultPoolSettings applies valid options over the defaults.
// Zero MaxConnsPerHost removes the connection cap; zero ResponseHeaderTimeout
// disables the header timeout. Other numeric options require positive values.
func defaultPoolSettings(cfg *Options) poolSettings {
	maxIdleConns := defaultMaxIdleConns
	maxIdleConnsPerHost := defaultMaxIdleConnsPerHost
	maxConnsPerHost := defaultMaxConnsPerHost
	idleConnTimeout := defaultIdleConnTimeout
	responseHeaderTimeout := defaultResponseHeaderTimeout

	if cfg != nil {
		if cfg.MaxIdleConns > 0 {
			maxIdleConns = cfg.MaxIdleConns
		}
		if cfg.MaxIdleConnsPerHost > 0 {
			maxIdleConnsPerHost = cfg.MaxIdleConnsPerHost
		}
		if cfg.MaxConnsPerHost >= 0 {
			maxConnsPerHost = cfg.MaxConnsPerHost
		}
		if cfg.IdleConnTimeoutSeconds > 0 {
			idleConnTimeout = time.Duration(cfg.IdleConnTimeoutSeconds) * time.Second
		}
		if cfg.ResponseHeaderTimeout >= 0 {
			responseHeaderTimeout = time.Duration(cfg.ResponseHeaderTimeout) * time.Second
		}
	}

	return poolSettings{
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
		MaxConnsPerHost:       maxConnsPerHost,
		IdleConnTimeout:       idleConnTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
	}
}

// New creates a client with its own upstream pool and reads options through source.
func New(source func() *Options) *Client {
	return &Client{source: source, pool: httpclient.NewUpstreamPool()}
}

func (s *Client) options() *Options {
	if s == nil || s.source == nil {
		return nil
	}
	return s.source()
}

// transportOptions resolves pool, TLS, redirect, and HTTP/2 settings for one request.
func (s *Client) transportOptions(req *http.Request, proxyURL string, providerID int64, concurrency int, tlsProfile *tlsfingerprint.Profile) (httpclient.UpstreamRequestOptions, error) {
	proxyKey, parsedProxy, err := normalizeProxyURL(proxyURL)
	if err != nil {
		return httpclient.UpstreamRequestOptions{}, err
	}
	profile := upstream.HTTPUpstreamProfileDefault
	if req != nil {
		profile = upstream.HTTPUpstreamProfileFromContext(req.Context())
	}
	isolation := s.getIsolationMode()
	settings := s.applyProfilePoolSettings(s.resolvePoolSettings(isolation, concurrency), profile)

	proxyScheme := ""
	if parsedProxy != nil {
		proxyScheme = parsedProxy.Scheme
	}
	targetPolicy := s.requestPolicy(req)
	policy := s.transportPolicy.Plan(egress.TransportRequest{TLSProfile: egressprovider.FromTLSProfile(tlsProfile), ValidateResolvedIP: targetPolicy.ValidateResolvedIP, PublicHostsOnly: targetPolicy.PublicHostsOnly, DisableRedirects: targetPolicy.DisableRedirects, Profile: string(profile), ProxyURL: proxyURL, ProxyKey: proxyKey, ProxyScheme: proxyScheme, HTTP2: s.http2Options(), HasTLSProfile: tlsProfile != nil, TLSSupportsHTTP2: tlsfingerprint.SupportsHTTP2(tlsProfile), Now: time.Now()})
	mode := policy.TransportMode
	if tlsProfile != nil {
		tlsProfile = resolveTLSFingerprintTransportProfile(egressprovider.ToTLSProfile(policy.TLSProfile), mode)
	}

	opts := httpclient.UpstreamRequestOptions{
		ProxyURL:   policy.ProxyURL,
		ProviderID: providerID,
		Isolation:  isolation,
		MaxClients: s.maxUpstreamClients(),
		IdleTTL:    s.clientIdleTTL(),
		Settings:   settings,
		TLSProfile: tlsProfile,
		Protocol: httpclient.TransportProtocol{
			CacheVariant: policy.TransportMode,
			HTTP2:        policy.TransportMode == upstreamProtocolModeOpenAIH2,
			DisableHTTP2: policy.TransportMode == upstreamProtocolModeOpenAIH1 || policy.TransportMode == upstreamProtocolModeOpenAIH1Fallback,
		},
	}
	if policy.ValidateResolvedIP {
		opts.CheckRedirect = s.redirectChecker
	}
	opts.PrepareClient = func(client *http.Client) *http.Client {
		return httpClientWithGrokAccessDeniedFallback(httpClientForEgressPolicy(s, client, policy))
	}
	opts.ObserveResult = func(err error) {
		if err != nil {
			s.recordOpenAIHTTP2Failure(profile, mode, proxyKey, err)
		} else {
			s.recordOpenAIHTTP2Success(profile, mode, proxyKey)
		}
	}
	return opts, nil
}

// Do applies Grok headers, validates the destination, and sends through the selected pool.
func (s *Client) Do(req *http.Request, proxyURL string, providerID int64, concurrency int) (*http.Response, error) {
	applyGrokCLIProxyHeaders(req)
	if err := s.validateRequestHost(req); err != nil {
		return nil, err
	}
	opts, err := s.transportOptions(req, proxyURL, providerID, concurrency, nil)
	if err != nil {
		return nil, err
	}
	return s.pool.Do(req, opts)
}

// DoWithTLS sends using the selected TLS fingerprint. A nil profile or a plain
// HTTP URL uses Do, including its configured proxy.
func (s *Client) DoWithTLS(req *http.Request, proxyURL string, providerID int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	if profile == nil || (req != nil && req.URL != nil && strings.EqualFold(req.URL.Scheme, "http")) {
		return s.Do(req, proxyURL, providerID, concurrency)
	}
	applyGrokCLIProxyHeaders(req)
	if err := s.validateRequestHost(req); err != nil {
		return nil, err
	}
	opts, err := s.transportOptions(req, proxyURL, providerID, concurrency, profile)
	if err != nil {
		return nil, err
	}
	return s.pool.Do(req, opts)
}

// CloseIdleConnections closes idle pooled connections when the pool supports it.
func (s *Client) CloseIdleConnections() {
	if closer, ok := s.pool.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// http2Options converts the configured HTTP/2 policy to egress options.
func (s *Client) http2Options() egress.HTTP2Options {
	v := s.resolveOpenAIHTTP2Settings()
	return egress.HTTP2Options{Enabled: v.enabled, AllowProxyFallbackToHTTP1: v.allowProxyFallbackToHTTP1, FallbackErrorThreshold: v.fallbackErrorThreshold, FallbackWindow: v.fallbackWindow, FallbackTTL: v.fallbackTTL}
}

// requestPolicy combines transport settings with per-request egress restrictions.
func (s *Client) requestPolicy(req *http.Request) egress.EgressPolicy {
	input := egress.RequestPolicyInput{}
	if s != nil {
		input.ValidateResolvedIP = s.shouldValidateResolvedIP()
	}
	if req != nil {
		input.DisableRedirects = upstream.HTTPUpstreamRedirectsDisabled(req.Context())
		input.PublicHostsOnly = upstream.HTTPUpstreamPublicHostsOnly(req.Context())
	}
	return egress.RequestPolicy(input)
}
