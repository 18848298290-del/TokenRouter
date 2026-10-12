package httpclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/imroc/req/v3"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/proxy"
	servertiming "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/ipmatch"
)

// Shared transport pool defaults.
const (
	defaultMaxIdleConns        = 100              // Maximum idle connections across hosts
	defaultMaxIdleConnsPerHost = 10               // Maximum idle connections per host
	defaultIdleConnTimeout     = 90 * time.Second // Idle connection lifetime; keep below the upstream load balancer's limit
	defaultDialTimeout         = 5 * time.Second  // TCP dial timeout for the default transport dialer
	defaultTLSHandshakeTimeout = 5 * time.Second  // TLS handshake timeout
	validatedHostTTL           = 30 * time.Second // Lifetime of cached DNS validation results
)

var (
	// sharedClients caches HTTP clients by construction options.
	sharedClients sync.Map

	// Tests can replace the DNS validator; production uses ValidateResolvedIP.
	validateResolvedIP = ValidateResolvedIP
)

// Options configures shared HTTP client construction.
type Options struct {
	ProxyURL              string        // HTTP, HTTPS, SOCKS5, or SOCKS5H proxy URL
	Timeout               time.Duration // Overall request timeout
	ResponseHeaderTimeout time.Duration // Timeout waiting for response headers
	InsecureSkipVerify    bool          // Rejected when true; TLS certificate verification is required
	ValidateResolvedIP    bool          // Check resolved addresses for nonpublic IPs before requests
	AllowPrivateHosts     bool          // Bypass resolved-IP validation when private hosts are allowed

	// Nonpositive idle limits use defaults.
	MaxIdleConns        int // Maximum total idle connections; default 100
	MaxIdleConnsPerHost int // Maximum idle connections per host; default 10
	MaxConnsPerHost     int // Maximum connections per host; zero means unlimited
}

type validatedTransport struct {
	base           http.RoundTripper
	validatedHosts sync.Map // map[string]time.Time containing validation expiry times
	now            func() time.Time
}

// GetClient returns a cached client for the supplied options.
// Callers share its transport and must not mutate the returned client.
// Invalid proxy configuration returns an error; callers must handle it before sending requests.
func GetClient(opts Options) (*http.Client, error) {
	key := buildClientKey(opts)
	if cached, ok := sharedClients.Load(key); ok {
		if client, ok := cached.(*http.Client); ok {
			return client, nil
		}
	}

	client, err := buildClient(opts)
	if err != nil {
		return nil, err
	}

	actual, _ := sharedClients.LoadOrStore(key, client)
	if c, ok := actual.(*http.Client); ok {
		return c, nil
	}
	return client, nil
}

func buildClient(opts Options) (*http.Client, error) {
	transport, err := buildTransport(opts)
	if err != nil {
		return nil, err
	}

	var rt http.RoundTripper = transport
	if opts.ValidateResolvedIP && !opts.AllowPrivateHosts {
		rt = newValidatedTransport(transport)
	}
	rt = servertiming.WrapRoundTripper(rt)
	return &http.Client{
		Transport: rt,
		Timeout:   opts.Timeout,
	}, nil
}

func buildTransport(opts Options) (*http.Transport, error) {
	// Use defaults for nonpositive idle connection limits.
	maxIdleConns := opts.MaxIdleConns
	if maxIdleConns <= 0 {
		maxIdleConns = defaultMaxIdleConns
	}
	maxIdleConnsPerHost := opts.MaxIdleConnsPerHost
	if maxIdleConnsPerHost <= 0 {
		maxIdleConnsPerHost = defaultMaxIdleConnsPerHost
	}

	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: defaultDialTimeout,
		}).DialContext,
		TLSHandshakeTimeout:   defaultTLSHandshakeTimeout,
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
		MaxConnsPerHost:       opts.MaxConnsPerHost, // Zero means unlimited
		IdleConnTimeout:       defaultIdleConnTimeout,
		ResponseHeaderTimeout: opts.ResponseHeaderTimeout,
	}

	if opts.InsecureSkipVerify {
		// Require certificate verification to authenticate the upstream server.
		return nil, fmt.Errorf("insecure_skip_verify is not allowed; install a trusted certificate instead")
	}

	_, parsed, err := proxy.Parse(opts.ProxyURL)
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		return transport, nil
	}

	if err := proxy.ConfigureTransportProxy(transport, parsed); err != nil {
		return nil, err
	}

	return transport, nil
}

func buildClientKey(opts Options) string {
	return fmt.Sprintf("%s|%s|%s|%t|%t|%t|%d|%d|%d",
		strings.TrimSpace(opts.ProxyURL),
		opts.Timeout.String(),
		opts.ResponseHeaderTimeout.String(),
		opts.InsecureSkipVerify,
		opts.ValidateResolvedIP,
		opts.AllowPrivateHosts,
		opts.MaxIdleConns,
		opts.MaxIdleConnsPerHost,
		opts.MaxConnsPerHost,
	)
}

func newValidatedTransport(base http.RoundTripper) *validatedTransport {
	return &validatedTransport{
		base: base,
		now:  time.Now,
	}
}

func (t *validatedTransport) isValidatedHost(host string, now time.Time) bool {
	if t == nil {
		return false
	}
	raw, ok := t.validatedHosts.Load(host)
	if !ok {
		return false
	}
	expireAt, ok := raw.(time.Time)
	if !ok {
		t.validatedHosts.Delete(host)
		return false
	}
	if now.Before(expireAt) {
		return true
	}
	t.validatedHosts.Delete(host)
	return false
}

func (t *validatedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req != nil && req.URL != nil {
		host := strings.ToLower(strings.TrimSpace(req.URL.Hostname()))
		if host != "" {
			now := time.Now()
			if t != nil && t.now != nil {
				now = t.now()
			}
			if !t.isValidatedHost(host, now) {
				if err := validateResolvedIP(host); err != nil {
					return nil, err
				}
				t.validatedHosts.Store(host, now.Add(validatedHostTTL))
			}
		}
	}
	if t == nil || t.base == nil {
		return nil, fmt.Errorf("validated transport base is nil")
	}
	return t.base.RoundTrip(req)
}

// DefaultBatchHTTPClient returns a shared client with dial, TLS, and response-header timeouts.
// Batch uploads and streaming downloads use phase timeouts with no overall request timeout.
func DefaultBatchHTTPClient() *http.Client {
	client, err := GetClient(Options{
		ResponseHeaderTimeout: 60 * time.Second,
	})
	if err != nil {
		return http.DefaultClient
	}
	return client
}

// CloseSharedIdleConnections closes idle connections in shared HTTP and req clients.
func CloseSharedIdleConnections() {
	sharedClients.Range(func(_, value any) bool {
		if client, ok := value.(*http.Client); ok {
			client.CloseIdleConnections()
		}
		return true
	})
	sharedReqClients.Range(func(_, value any) bool {
		if client, ok := value.(*req.Client); ok {
			client.GetClient().CloseIdleConnections()
		}
		return true
	})
}

// ValidateResolvedIP resolves a host and rejects lookup errors or any nonpublic address.
// The lookup has a five-second timeout; callers validate before sending requests.
// Validation does not pin the address used by a subsequent dial or proxy lookup.
func ValidateResolvedIP(host string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("dns resolution failed: %w", err)
	}

	for _, ip := range ips {
		if ipmatch.IsNonPublic(ip) {
			return fmt.Errorf("resolved ip %s is not allowed", ip.String())
		}
	}
	return nil
}
