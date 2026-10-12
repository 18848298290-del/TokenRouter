package httpclient

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
)

// httpClientSink retains benchmark results so assignments cannot be optimized away.
var httpClientSink *http.Client

type poolRoundTripFunc func(*http.Request) (*http.Response, error)

type responseTestBody struct {
	io.Reader
}

type blockingHeaderRoundTripper struct{}

// TestUpstreamPoolActiveLimitAndCloseOnce checks in-flight protection and one release per body.
func TestUpstreamPoolActiveLimitAndCloseOnce(t *testing.T) {
	pool := NewUpstreamPool()
	one := poolTestOptions(1, nil)
	one.MaxClients = 1
	held, err := pool.Do(poolTestRequest(t), one)
	require.NoError(t, err)
	two := poolTestOptions(2, nil)
	two.MaxClients = 1
	_, err = pool.Do(poolTestRequest(t), two)
	require.ErrorIs(t, err, ErrUpstreamClientLimitReached)
	require.NoError(t, held.Body.Close())
	require.NoError(t, held.Body.Close())
	next, err := pool.Do(poolTestRequest(t), two)
	require.NoError(t, err)
	require.NoError(t, next.Body.Close())
}

// TestUpstreamPoolFailureReleasesEntry checks that observation precedes release on failure.
func TestUpstreamPoolFailureReleasesEntry(t *testing.T) {
	pool := NewUpstreamPool()
	opts := poolTestOptions(1, nil)
	opts.MaxClients = 1
	failure := errors.New("transport failed")
	observed := false
	opts.PrepareClient = func(c *http.Client) *http.Client {
		clone := *c
		clone.Transport = poolRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, failure
		})
		return &clone
	}
	// Probe with the same one-entry limit while the failed request still holds its slot.
	opts.ObserveResult = func(err error) {
		require.ErrorIs(t, err, failure)
		observed = true
		probe := poolTestOptions(2, nil)
		probe.MaxClients = 1
		_, acquireErr := pool.acquire(probe)
		require.ErrorIs(t, acquireErr, ErrUpstreamClientLimitReached)
	}
	_, err := pool.Do(poolTestRequest(t), opts)
	require.ErrorIs(t, err, failure)
	require.True(t, observed)
	next := poolTestOptions(2, nil)
	next.MaxClients = 1
	resp, err := pool.Do(poolTestRequest(t), next)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}

// TestUpstreamPoolIsolationAndConfigurationChange checks reuse across provider and proxy settings.
func TestUpstreamPoolIsolationAndConfigurationChange(t *testing.T) {
	for _, isolation := range []string{"proxy", "provider", "provider_proxy"} {
		t.Run(isolation, func(t *testing.T) {
			pool := NewUpstreamPool()
			var first, again, other *http.Client
			run := func(id int64, proxy string, capture **http.Client) {
				opts := poolTestOptions(id, capture)
				opts.Isolation = isolation
				opts.ProxyURL = proxy
				resp, err := pool.Do(poolTestRequest(t), opts)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
			}
			run(1, "http://proxy-a:8080", &first)
			run(1, "http://proxy-a:8080/", &again)
			require.Same(t, first.Transport, again.Transport)
			run(2, "http://proxy-a:8080", &other)
			if isolation == "proxy" {
				require.Same(t, first.Transport, other.Transport)
			} else {
				require.NotSame(t, first.Transport, other.Transport)
			}
			run(1, "http://proxy-b:8080", &other)
			require.NotSame(t, first.Transport, other.Transport)
		})
	}
	pool := NewUpstreamPool()
	var first, second *http.Client
	opts := poolTestOptions(1, &first)
	resp, err := pool.Do(poolTestRequest(t), opts)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	opts = poolTestOptions(1, &second)
	opts.Settings.ResponseHeaderTimeout = time.Second
	resp, err = pool.Do(poolTestRequest(t), opts)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.NotSame(t, first.Transport, second.Transport)
}

// TestUpstreamPoolEvictsOldestIdle uses fixed timestamps to identify the LRU entry.
func TestUpstreamPoolEvictsOldestIdle(t *testing.T) {
	pool := NewUpstreamPool()
	a, err := pool.acquire(poolTestOptions(1, nil))
	require.NoError(t, err)
	b, err := pool.acquire(poolTestOptions(2, nil))
	require.NoError(t, err)
	atomic.StoreInt64(&a.inFlight, 0)
	atomic.StoreInt64(&b.inFlight, 0)
	atomic.StoreInt64(&a.lastUsed, time.Now().Add(-2*time.Minute).UnixNano())
	atomic.StoreInt64(&b.lastUsed, time.Now().Add(-time.Minute).UnixNano())
	_, err = pool.acquire(poolTestOptions(3, nil))
	require.NoError(t, err)
	require.Len(t, pool.clients, 2)
	for _, entry := range pool.clients {
		require.NotSame(t, a, entry)
	}
}

// TestUpstreamPoolIdleTTLDoesNotEvictActive checks that active requests survive idle expiration.
func TestUpstreamPoolIdleTTLDoesNotEvictActive(t *testing.T) {
	pool := NewUpstreamPool()
	opts := poolTestOptions(1, nil)
	opts.IdleTTL = time.Second
	a, err := pool.acquire(opts)
	require.NoError(t, err)
	atomic.StoreInt64(&a.lastUsed, time.Now().Add(-2*time.Minute).UnixNano())
	opts.ProviderID = 2
	_, err = pool.acquire(opts)
	require.NoError(t, err)
	found := false
	for _, entry := range pool.clients {
		if entry == a {
			found = true
		}
	}
	require.True(t, found)
}

// TestUpstreamPoolConcurrentRelease checks repeated body closes during concurrent reuse.
func TestUpstreamPoolConcurrentRelease(t *testing.T) {
	pool := NewUpstreamPool()
	var group sync.WaitGroup
	errs := make(chan error, 64)
	for range 16 {
		group.Go(func() {
			opts := poolTestOptions(1, nil)
			opts.MaxClients = 1
			resp, err := pool.Do(poolTestRequest(t), opts)
			if err != nil {
				errs <- err
				return
			}
			if err = resp.Body.Close(); err != nil {
				errs <- err
			}
			if err = resp.Body.Close(); err != nil {
				errs <- err
			}
		})
	}
	group.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	opts := poolTestOptions(2, nil)
	opts.MaxClients = 1
	resp, err := pool.Do(poolTestRequest(t), opts)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}

func TestDecompressResponseBodyZstdUsage(t *testing.T) {
	payload := []byte(`{"usage":{"input_tokens":123,"output_tokens":45,"cache_read_input_tokens":67}}`)
	compressed := compressZstd(t, payload)
	resp := newEncodedResponse("zstd", compressed)

	decompressResponseBody(resp)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, payload, body)
	require.Equal(t, int64(123), gjson.GetBytes(body, "usage.input_tokens").Int())
	require.Equal(t, int64(45), gjson.GetBytes(body, "usage.output_tokens").Int())
	require.Equal(t, int64(67), gjson.GetBytes(body, "usage.cache_read_input_tokens").Int())
	require.Empty(t, resp.Header.Get("Content-Encoding"))
	require.Empty(t, resp.Header.Get("Content-Length"))
	require.Equal(t, int64(-1), resp.ContentLength)
	require.NoError(t, resp.Body.Close())
}

func TestDecompressResponseBodyExistingEncodings(t *testing.T) {
	payload := []byte(`{"ok":true}`)
	tests := []struct {
		name     string
		encoding string
		compress func(*testing.T, []byte) []byte
	}{
		{name: "gzip", encoding: "gzip", compress: compressGzip},
		{name: "brotli", encoding: "br", compress: compressBrotli},
		{name: "deflate", encoding: "deflate", compress: compressDeflate},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := newEncodedResponse(tt.encoding, tt.compress(t, payload))

			decompressResponseBody(resp)

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, payload, body)
			require.Empty(t, resp.Header.Get("Content-Encoding"))
			require.Empty(t, resp.Header.Get("Content-Length"))
			require.Equal(t, int64(-1), resp.ContentLength)
			require.NoError(t, resp.Body.Close())
		})
	}
}

func TestDecompressResponseBodyWithoutEncodingLeavesBodyUntouched(t *testing.T) {
	originalBody := &responseTestBody{Reader: bytes.NewReader([]byte("plain"))}
	resp := &http.Response{
		Header:        make(http.Header),
		Body:          originalBody,
		ContentLength: 5,
	}

	decompressResponseBody(resp)

	require.Same(t, originalBody, resp.Body)
	require.Equal(t, int64(5), resp.ContentLength)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "plain", string(body))
	require.NoError(t, resp.Body.Close())
}

func TestDecompressResponseBodyInvalidZstdWarnsAndPreservesBody(t *testing.T) {
	previousLogger := slog.Default()
	var logOutput bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logOutput, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
	})

	payload := []byte("not a zstd response")
	resp := newEncodedResponse("zstd", payload)

	require.NotPanics(t, func() {
		decompressResponseBody(resp)
	})

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, payload, body)
	require.Equal(t, "zstd", resp.Header.Get("Content-Encoding"))
	require.Equal(t, int64(len(payload)), resp.ContentLength)
	require.Contains(t, logOutput.String(), "msg=zstd_decompress_failed")
	require.NoError(t, resp.Body.Close())
}

func TestDecompressResponseBodyEmptyZstdWarnsAndPreservesBody(t *testing.T) {
	previousLogger := slog.Default()
	var logOutput bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logOutput, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
	})

	resp := newEncodedResponse("zstd", nil)

	require.NotPanics(t, func() {
		decompressResponseBody(resp)
	})

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Empty(t, body)
	require.Equal(t, "zstd", resp.Header.Get("Content-Encoding"))
	require.Equal(t, int64(0), resp.ContentLength)
	require.Contains(t, logOutput.String(), "msg=zstd_decompress_failed")
	require.NoError(t, resp.Body.Close())
}

// TestBuildUpstreamTransportSetsDialTimeout checks dial and TLS handshake configuration.
// A nil DialContext would use a zero-value net.Dialer with no dial timeout.
// ResponseHeaderTimeout starts after the request is written, so it cannot bound connection setup.
func TestBuildUpstreamTransportSetsDialTimeout(t *testing.T) {
	settings := upstreamTestSettings()

	transport, err := buildUpstreamTransport(settings, nil, TransportProtocol{CacheVariant: "default"})
	require.NoError(t, err)
	require.NotNil(t, transport.DialContext, "DialContext 缺失会退化为无超时的零值 dialer")
	require.Equal(t, defaultUpstreamTLSHandshakeTimeout, transport.TLSHandshakeTimeout)
}

func TestNewUpstreamDialerHasBoundedTimeout(t *testing.T) {
	dialer := newUpstreamDialer()

	require.Greater(t, dialer.Timeout, time.Duration(0), "建连超时必须有上限")
	require.Equal(t, defaultUpstreamDialTimeout, dialer.Timeout)
	require.Equal(t, defaultUpstreamDialKeepAlive, dialer.KeepAlive)
}

// TestBuildUpstreamTransportKeepsDialTimeoutWithHTTPProxy checks the configured dialer is retained.
func TestBuildUpstreamTransportKeepsDialTimeoutWithHTTPProxy(t *testing.T) {
	proxyURL, err := url.Parse("http://127.0.0.1:1080")
	require.NoError(t, err)

	transport, err := buildUpstreamTransport(upstreamTestSettings(), proxyURL, TransportProtocol{CacheVariant: "default"})
	require.NoError(t, err)
	require.NotNil(t, transport.Proxy)
	require.NotNil(t, transport.DialContext)
}

// TestBuildUpstreamTransportKeepsDialContextWithSOCKS5Proxy checks SOCKS dialer installation.
func TestBuildUpstreamTransportKeepsDialContextWithSOCKS5Proxy(t *testing.T) {
	proxyURL, err := url.Parse("socks5h://127.0.0.1:1080")
	require.NoError(t, err)

	transport, err := buildUpstreamTransport(upstreamTestSettings(), proxyURL, TransportProtocol{CacheVariant: "default"})
	require.NoError(t, err)
	require.NotNil(t, transport.DialContext)
}

// TestUpstreamDialerRespectsContextCancellation checks that canceled requests fail to dial.
// Use a closed local listener address.
func TestUpstreamDialerRespectsContextCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	require.NoError(t, listener.Close())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	conn, err := newUpstreamDialer().DialContext(ctx, "tcp", addr)
	if conn != nil {
		_ = conn.Close()
	}
	require.Error(t, err, "已取消的 context 必须立即中止拨号")
}

// TestEnableOpenAIHTTP2KeepAlive_EnablesPingHealthCheck checks idle PING timing.
// PINGs detect connections silently dropped by a proxy or NAT.
func TestEnableOpenAIHTTP2KeepAlive_EnablesPingHealthCheck(t *testing.T) {
	tr := &http.Transport{}

	enableHTTP2KeepAlive(tr)
	require.NotNil(t, tr.HTTP2)
	require.Equal(t, http2ReadIdleTimeout, tr.HTTP2.SendPingTimeout)
	require.Equal(t, http2PingTimeout, tr.HTTP2.PingTimeout)
	requireHTTP2Configured(t, tr, "http2 必须已挂到底层 http.Transport 上")
}

func TestBuildUpstreamTransport_OpenAIH2_EnablesPingHealthCheck(t *testing.T) {
	tr, err := buildUpstreamTransport(http2KeepAliveTestPoolSettings(), nil, TransportProtocol{CacheVariant: "openai_h2", HTTP2: true})
	require.NoError(t, err)
	require.True(t, tr.ForceAttemptHTTP2, "openai_h2 必须启用 HTTP/2")
	requireHTTP2Configured(t, tr, "openai_h2 必须显式配置 http2 以启用 ReadIdleTimeout")
}

// TestBuildUpstreamTransport_NonOpenAIH2_NotEagerlyConfigured checks lazy default HTTP/2 setup.
func TestBuildUpstreamTransport_NonOpenAIH2_NotEagerlyConfigured(t *testing.T) {
	tr, err := buildUpstreamTransport(http2KeepAliveTestPoolSettings(), nil, TransportProtocol{CacheVariant: "default"})
	require.NoError(t, err)
	require.Nil(t, tr.Protocols, "default 模式不应在构建期主动配置 http2 keepalive")
	require.Nil(t, tr.TLSNextProto["h2"], "default 模式不应在构建期主动配置 http2 keepalive")
}

func TestBuildUpstreamTransport_OpenAIH2_NegotiatesHTTP2(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	tr, err := buildUpstreamTransport(http2KeepAliveTestPoolSettings(), nil, TransportProtocol{CacheVariant: "openai_h2", HTTP2: true})
	require.NoError(t, err)
	defer tr.CloseIdleConnections()
	require.NotNil(t, tr.TLSClientConfig)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	tr.TLSClientConfig.RootCAs = roots

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := tr.RoundTrip(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, 2, resp.ProtoMajor, "openai_h2 必须协商到 HTTP/2")
}

func TestBuildUpstreamTransport_OpenAIH2_WithHTTPProxy_EnablesKeepAlive(t *testing.T) {
	proxyURL, err := url.Parse("http://127.0.0.1:8080")
	require.NoError(t, err)

	tr, err := buildUpstreamTransport(http2KeepAliveTestPoolSettings(), proxyURL, TransportProtocol{CacheVariant: "openai_h2", HTTP2: true})
	require.NoError(t, err)
	require.True(t, tr.ForceAttemptHTTP2)
	requireHTTP2Configured(t, tr, "经代理的 openai_h2 也必须启用 http2 keepalive")
	require.NotNil(t, tr.Proxy, "HTTP 代理仍须通过 Transport.Proxy 生效")
}

// BenchmarkHTTPUpstreamProxyClient measures construction versus cached-client assignment.
func BenchmarkHTTPUpstreamProxyClient(b *testing.B) {
	svc := NewUpstreamPool()
	settings := UpstreamSettings{
		MaxIdleConns:          240,
		MaxIdleConnsPerHost:   120,
		MaxConnsPerHost:       240,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 300 * time.Second,
	}

	proxyURL := "http://127.0.0.1:8080"
	b.ReportAllocs()

	b.Run("新建", func(b *testing.B) {
		parsedProxy, err := url.Parse(proxyURL)
		if err != nil {
			b.Fatalf("解析代理地址失败: %v", err)
		}
		for range b.N {
			// Each iteration includes allocation of a transport.
			transport, err := buildUpstreamTransport(settings, parsedProxy, TransportProtocol{CacheVariant: "default"})
			if err != nil {
				b.Fatalf("创建 Transport 失败: %v", err)
			}
			httpClientSink = &http.Client{
				Transport: transport,
			}
		}
	})

	b.Run("复用", func(b *testing.B) {
		// Populate the cache before timing repeated assignments.
		entry, err := svc.acquire(UpstreamRequestOptions{
			ProxyURL:   proxyURL,
			ProviderID: 1,
			Isolation:  "provider_proxy",
			MaxClients: 5000,
			IdleTTL:    15 * time.Minute,
			Settings:   settings,
		})
		if err != nil {
			b.Fatalf("getOrCreateClient: %v", err)
		}
		client := entry.client
		b.ResetTimer() // Exclude cache warmup.
		for range b.N {
			// This measures assignment of a cached pointer, not a pool lookup.
			httpClientSink = client
		}
	})
}

// TestUpstreamPoolRedirectPolicyIsPerRequest checks policy replacement, clearing, and failure release.
func TestUpstreamPoolRedirectPolicyIsPerRequest(t *testing.T) {
	target, hits := redirectTestServer(t)
	pool := NewUpstreamPool()
	blocked := errors.New("redirect blocked for this request")
	var previous *http.Client
	for _, step := range []struct {
		name  string
		block bool
	}{
		{name: "default"},
		{name: "block_after_default", block: true},
		{name: "clear_after_block"},
		{name: "block_again", block: true},
	} {
		t.Run(step.name, func(t *testing.T) {
			opts := UpstreamRequestOptions{Isolation: "provider", ProviderID: 1, MaxClients: 1}
			if step.block {
				opts.CheckRedirect = func(*http.Request, []*http.Request) error {
					return blocked
				}
			}
			var current *http.Client
			opts.PrepareClient = func(client *http.Client) *http.Client {
				current = client
				return client
			}
			before := hits.Load()
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
			require.NoError(t, err)
			resp, err := pool.Do(req, opts)
			if resp != nil {
				require.NoError(t, resp.Body.Close())
			}
			if step.block {
				require.ErrorIs(t, err, blocked)
				require.Equal(t, before, hits.Load(), "被拒绝的重定向不能访问目标")
			} else {
				require.NoError(t, err)
				require.Equal(t, http.StatusNoContent, resp.StatusCode)
				require.Equal(t, before+1, hits.Load())
			}
			if previous != nil {
				require.NotSame(t, previous, current, "每个请求应有独立的客户端策略")
				require.Same(t, previous.Transport, current.Transport, "切换策略仍复用同一 transport")
			}
			previous = current
		})
	}

	// A redirect failure must release the slot so a different provider can use the one-entry pool.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	require.NoError(t, err)
	resp, err := pool.Do(req, UpstreamRequestOptions{Isolation: "provider", ProviderID: 2, MaxClients: 1})
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}

// TestUpstreamPoolPrepareClientOverridesRequestPolicy checks overrides stay local to one request.
func TestUpstreamPoolPrepareClientOverridesRequestPolicy(t *testing.T) {
	target, hits := redirectTestServer(t)
	pool := NewUpstreamPool()
	opts := UpstreamRequestOptions{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("base policy must be overridden")
		},
		PrepareClient: func(client *http.Client) *http.Client {
			require.NotNil(t, client.CheckRedirect, "基础回调应在适配前设置")
			client.CheckRedirect = func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			}
			return client
		},
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	require.NoError(t, err)
	resp, err := pool.Do(req, opts)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Zero(t, hits.Load())

	// The next request should use the default redirect policy.
	req, err = http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	require.NoError(t, err)
	resp, err = pool.Do(req, UpstreamRequestOptions{})
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Equal(t, int64(1), hits.Load())
}

// TestUpstreamPoolConcurrentRedirectPolicies checks policy isolation with a shared transport.
func TestUpstreamPoolConcurrentRedirectPolicies(t *testing.T) {
	target, hits := redirectTestServer(t)
	pool := NewUpstreamPool()
	blocked := errors.New("redirect blocked")
	const requests = 24
	var group sync.WaitGroup
	errs := make(chan error, requests)
	start := make(chan struct{})
	for i := range requests {
		group.Go(func() {
			<-start
			opts := UpstreamRequestOptions{}
			if i%2 == 0 {
				opts.CheckRedirect = func(*http.Request, []*http.Request) error {
					return blocked
				}
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
			if err != nil {
				errs <- err
				return
			}
			resp, err := pool.Do(req, opts)
			if resp != nil {
				if closeErr := resp.Body.Close(); closeErr != nil {
					errs <- closeErr
					return
				}
			}
			if i%2 == 0 {
				if !errors.Is(err, blocked) {
					errs <- fmt.Errorf("请求 %d 应阻断重定向，实际错误：%v", i, err)
				}
			} else if err != nil {
				errs <- err
			}
		})
	}
	close(start)
	group.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int64(requests/2), hits.Load())
}

func TestTLSFingerprintHTTPSProxyFallsBackWithoutBypassingProxy(t *testing.T) {
	proxyURL, err := url.Parse("https://user:pass@proxy.example:8443")
	require.NoError(t, err)
	roundTripper, err := buildUpstreamTransportWithTLSFingerprint(
		UpstreamSettings{},
		proxyURL,
		&tlsfingerprint.Profile{Name: "test"},
		TransportProtocol{CacheVariant: "default"},
	)
	require.NoError(t, err)
	transport, ok := roundTripper.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.Proxy)
	require.Nil(t, transport.DialTLSContext)
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "upstream.example"}}
	resolved, err := transport.Proxy(req)
	require.NoError(t, err)
	require.Equal(t, "https://user:pass@proxy.example:8443", resolved.String())
}

func TestResponseHeaderTimeoutRoundTripperTimesOut(t *testing.T) {
	transport := &responseHeaderTimeoutRoundTripper{
		base:    blockingHeaderRoundTripper{},
		timeout: 10 * time.Millisecond,
	}
	req, err := http.NewRequest(http.MethodGet, "https://example.com/v1/responses", nil)
	require.NoError(t, err)

	startedAt := time.Now()
	resp, err := transport.RoundTrip(req)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	require.Error(t, err)
	require.Contains(t, err.Error(), "timeout awaiting response headers")
	require.Less(t, time.Since(startedAt), time.Second)
}

// upstreamTestSettings supplies fixed connection limits and timeouts for pool tests.
func upstreamTestSettings() UpstreamSettings {
	return UpstreamSettings{
		MaxIdleConns:          240,
		MaxIdleConnsPerHost:   120,
		MaxConnsPerHost:       240,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 300 * time.Second,
	}
}

func (f poolRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// poolTestOptions supplies a fake response while exercising acquisition, observation, and release.
func poolTestOptions(id int64, captured **http.Client) UpstreamRequestOptions {
	return UpstreamRequestOptions{ProviderID: id, Isolation: "provider_proxy", MaxClients: 2, IdleTTL: 15 * time.Minute, Settings: upstreamTestSettings(), PrepareClient: func(client *http.Client) *http.Client {
		if captured != nil {
			*captured = client
		}
		clone := *client
		clone.Transport = poolRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("response")),
				Request:    req,
			}, nil
		})
		return &clone
	}}
}

func poolTestRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com/", nil)
	require.NoError(t, err)
	return req
}

func (b *responseTestBody) Close() error {
	return nil
}

func newEncodedResponse(encoding string, body []byte) *http.Response {
	header := make(http.Header)
	header.Set("Content-Encoding", encoding)
	header.Set("Content-Length", "123")
	return &http.Response{
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func compressZstd(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	require.NoError(t, err)
	_, err = zw.Write(payload)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func compressGzip(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write(payload)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func compressBrotli(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := brotli.NewWriter(&buf)
	_, err := zw.Write(payload)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func compressDeflate(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := flate.NewWriter(&buf, flate.DefaultCompression)
	require.NoError(t, err)
	_, err = zw.Write(payload)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func http2KeepAliveTestPoolSettings() UpstreamSettings {
	return UpstreamSettings{
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   5,
		MaxConnsPerHost:       10,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: time.Minute,
	}
}

// requireHTTP2Configured checks that both HTTP/1.1 and HTTP/2 are enabled.
func requireHTTP2Configured(t *testing.T, tr *http.Transport, msg string) {
	t.Helper()
	require.NotNil(t, tr.Protocols, msg)
	require.True(t, tr.Protocols.HTTP1(), msg)
	require.True(t, tr.Protocols.HTTP2(), msg)
}

// redirectTestServer redirects locally and counts requests reaching the target.
func redirectTestServer(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	hits := new(atomic.Int64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/target", http.StatusFound)
			return
		}
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return server.URL + "/start", hits
}

func (blockingHeaderRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Hold response headers until the request context is canceled.
	<-req.Context().Done()
	return nil, req.Context().Err()
}
