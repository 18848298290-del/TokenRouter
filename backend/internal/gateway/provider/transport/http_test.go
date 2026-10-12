package transport

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"golang.org/x/net/http2"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// HTTPUpstreamSuite tests upstream transport settings with a fresh Options value per test.

type HTTPUpstreamSuite struct {
	suite.Suite
	cfg *Options
}

// roundTripFunc handles test requests with a supplied function.
type roundTripFunc func(*http.Request) (*http.Response, error)

// testUpstreamPool applies client hooks and reports request results.
type testUpstreamPool struct {
	transport http.RoundTripper
}

// upstreamClientView captures the client, proxy key, and TLS settings selected for a request.
type upstreamClientView struct {
	tlsProfile   *tlsfingerprint.Profile
	client       *http.Client
	proxyKey     string
	protocolMode string
}

func TestHTTPUpstreamDoCanDisableRedirectsPerRequest(t *testing.T) {
	var redirectedCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectedCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	upstream := New(nil)
	req, err := http.NewRequestWithContext(
		upstreamcore.WithHTTPUpstreamRedirectsDisabled(t.Context()),
		http.MethodGet,
		redirector.URL,
		nil,
	)
	require.NoError(t, err)

	resp, err := upstream.Do(req, "", 1, 1)
	require.NoError(t, err)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
	require.Zero(t, redirectedCalls.Load())
}

func TestHTTPUpstreamDoWithTLSPlainHTTPUsesConfiguredHTTPProxy(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(upstream.Close)
	var proxyCalls atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(proxy.Close)

	req, err := http.NewRequest(http.MethodGet, upstream.URL, nil)
	require.NoError(t, err)
	client := New(nil)
	resp, err := client.DoWithTLS(req, proxy.URL, 41, 1, &tlsfingerprint.Profile{Name: "unused-for-http"})
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, int64(1), proxyCalls.Load())
	require.Zero(t, upstreamCalls.Load(), "plain HTTP must not bypass the configured proxy")
}

func TestHTTPUpstreamDoWithTLSPlainHTTPUsesConfiguredSOCKSProxy(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	proxyURL, proxyCalls := startTestSOCKS5Proxy(t)

	req, err := http.NewRequest(http.MethodGet, upstream.URL, nil)
	require.NoError(t, err)
	client := New(nil)
	resp, err := client.DoWithTLS(req, proxyURL, 42, 1, &tlsfingerprint.Profile{Name: "unused-for-http"})
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, int64(1), proxyCalls.Load())
	require.Equal(t, int64(1), upstreamCalls.Load())
}

func startTestSOCKS5Proxy(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = listener.Close()
	})
	calls := &atomic.Int64{}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			calls.Add(1)
			go serveTestSOCKS5Conn(conn)
		}
	}()
	return "socks5h://" + listener.Addr().String(), calls
}

func serveTestSOCKS5Conn(client net.Conn) {
	defer func() {
		_ = client.Close()
	}()
	header := make([]byte, 2)
	if _, err := io.ReadFull(client, header); err != nil || header[0] != 5 {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(client, methods); err != nil {
		return
	}
	if _, err := client.Write([]byte{5, 0}); err != nil {
		return
	}
	request := make([]byte, 4)
	if _, err := io.ReadFull(client, request); err != nil || request[0] != 5 || request[1] != 1 {
		return
	}
	var host string
	switch request[3] {
	case 1:
		address := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(client, address); err != nil {
			return
		}
		host = net.IP(address).String()
	case 3:
		length := make([]byte, 1)
		if _, err := io.ReadFull(client, length); err != nil {
			return
		}
		address := make([]byte, int(length[0]))
		if _, err := io.ReadFull(client, address); err != nil {
			return
		}
		host = string(address)
	case 4:
		address := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(client, address); err != nil {
			return
		}
		host = net.IP(address).String()
	default:
		return
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(client, portBytes); err != nil {
		return
	}
	target, err := net.Dial("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", binary.BigEndian.Uint16(portBytes))))
	if err != nil {
		_, _ = client.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer func() {
		_ = target.Close()
	}()
	if _, err := client.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	go func() {
		_, _ = io.Copy(target, client)
		_ = target.Close()
	}()
	_, _ = io.Copy(client, target)
}

func TestHTTPUpstreamDoAppliesGrokCLIIdentityBeforeOAuthRoundTrip(t *testing.T) {
	t.Setenv("XAI_GROK_CLI_VERSION", "")

	for _, endpoint := range []string{"responses", "chat/completions"} {
		t.Run(endpoint, func(t *testing.T) {
			upstream := New(nil)
			svc := upstream

			const providerID int64 = 4084

			var capturedHeaders http.Header
			svc.pool = testUpstreamPool{transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				capturedHeaders = req.Header.Clone()
				statusCode := http.StatusOK
				if req.Header.Get("X-XAI-Token-Auth") != "xai-grok-cli" {
					statusCode = http.StatusForbidden
				}
				return &http.Response{
					StatusCode: statusCode,
					Header:     make(http.Header),
					Body:       http.NoBody,
					Request:    req,
				}, nil
			})}

			req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/"+endpoint, nil)
			require.NoError(t, err)
			req.Header.Set("User-Agent", "tokenrouter-grok/1.0")

			resp, err := svc.Do(req, "", providerID, 1)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.NoError(t, resp.Body.Close())

			require.Equal(t, xai.CLIClientVersion, capturedHeaders.Get("x-grok-client-version"))
			require.Equal(t, "xai-grok-cli", capturedHeaders.Get("X-XAI-Token-Auth"))
			require.Equal(t, xai.CLIUserAgent(xai.CLIClientVersion), capturedHeaders.Get("User-Agent"))
		})
	}
}

func TestHTTPUpstreamDoFallsBackToOfficialGrokAPIOnCLIAccessDenied(t *testing.T) {
	upstream := New(nil)
	svc := upstream

	const providerID int64 = 4421

	payload := []byte(`{"model":"grok-4.5","input":"hello"}`)
	var calls int
	var fallbackBody []byte
	var fallbackHeaders http.Header
	svc.pool = testUpstreamPool{transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		if calls == 1 {
			require.Equal(t, grokCLIProxyHost, req.URL.Hostname())
			require.Equal(t, "xai-grok-cli", req.Header.Get("X-XAI-Token-Auth"))
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":"Access denied"}`)),
				Request:    req,
			}, nil
		}

		fallbackBody = body
		fallbackHeaders = req.Header.Clone()
		require.Equal(t, grokOfficialAPIHost, req.URL.Hostname())
		require.Equal(t, "/v1/responses", req.URL.Path)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"id":"response-ok"}`)),
			Request:    req,
		}, nil
	})}

	req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer oauth-token")

	resp, err := svc.Do(req, "", providerID, 1)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	responseBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.JSONEq(t, `{"id":"response-ok"}`, string(responseBody))
	require.Equal(t, 2, calls)
	require.Equal(t, payload, fallbackBody)
	require.Equal(t, "Bearer oauth-token", fallbackHeaders.Get("Authorization"))
	require.Empty(t, fallbackHeaders.Get("X-XAI-Token-Auth"))
	require.Empty(t, fallbackHeaders.Get("x-grok-client-version"))
	require.Empty(t, fallbackHeaders.Get("User-Agent"))
}

func TestGrokAccessDeniedFallbackRecognizesChatEndpointPermissionDenied(t *testing.T) {
	var hosts []string
	transport := &xai.AccessDeniedFallbackTransport{
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			hosts = append(hosts, req.URL.Hostname())
			if req.URL.Hostname() == grokCLIProxyHost {
				return &http.Response{
					StatusCode: http.StatusForbidden,
					Header:     make(http.Header),
					Body: io.NopCloser(strings.NewReader(
						`{"code":"permission_denied","error":"Access to the chat endpoint is denied. Please ensure you're using the correct credentials. If you believe this is a mistake, please contact support."}`,
					)),
					Request: req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"id":"response-ok"}`)),
				Request:    req,
			}, nil
		}),
	}

	req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", strings.NewReader(`{"model":"grok-4.5"}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer oauth-token")
	req.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, []string{grokCLIProxyHost, grokOfficialAPIHost}, hosts)
}

func TestIsGrokCLICompatibilityAccessDenied(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "legacy compatibility wording", body: `{"error":"Access denied"}`, want: true},
		{
			name: "observed chat endpoint permission denial",
			body: `{"code":"permission_denied","error":"Access to the chat endpoint is denied. Please ensure you're using the correct credentials. If you believe this is a mistake, please contact support."}`,
			want: true,
		},
		{
			name: "entitlement denial using the same broad terms",
			body: `{"code":"permission_denied","error":"Access to the chat endpoint is denied because a subscription is required"}`,
			want: false,
		},
		{
			name: "different permission denied endpoint",
			body: `{"code":"permission_denied","error":"Access to the billing endpoint is denied."}`,
			want: false,
		},
		{
			name: "wrong structured error code",
			body: `{"code":"subscription_required","error":"Access to the chat endpoint is denied. Please ensure you're using the correct credentials. If you believe this is a mistake, please contact support."}`,
			want: false,
		},
		{name: "malformed response", body: `permission_denied: chat endpoint denied`, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, xai.IsCLICompatibilityAccessDenied([]byte(tt.body)))
		})
	}
}

func TestIsGrokCLIAccessDeniedFallbackCandidateRequiresAuthenticatedReplayableCLI403(t *testing.T) {
	newRequest := func() *http.Request {
		req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", strings.NewReader(`{"model":"grok-4.5"}`))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer oauth-token")
		req.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
		return req
	}
	newResponse := func() *http.Response {
		return &http.Response{StatusCode: http.StatusForbidden}
	}

	t.Run("valid candidate", func(t *testing.T) {
		require.True(t, xai.IsCLIAccessDeniedFallbackCandidate(newRequest(), newResponse()))
	})
	t.Run("non CLI host", func(t *testing.T) {
		req := newRequest()
		req.URL.Host = "api.x.ai"
		require.False(t, xai.IsCLIAccessDeniedFallbackCandidate(req, newResponse()))
	})
	t.Run("missing CLI identity", func(t *testing.T) {
		req := newRequest()
		req.Header.Del("X-XAI-Token-Auth")
		require.False(t, xai.IsCLIAccessDeniedFallbackCandidate(req, newResponse()))
	})
	t.Run("missing bearer authentication", func(t *testing.T) {
		req := newRequest()
		req.Header.Del("Authorization")
		require.False(t, xai.IsCLIAccessDeniedFallbackCandidate(req, newResponse()))
	})
	t.Run("non forbidden response", func(t *testing.T) {
		resp := newResponse()
		resp.StatusCode = http.StatusUnauthorized
		require.False(t, xai.IsCLIAccessDeniedFallbackCandidate(newRequest(), resp))
	})
	t.Run("non replayable request", func(t *testing.T) {
		req := newRequest()
		req.GetBody = nil
		require.False(t, xai.IsCLIAccessDeniedFallbackCandidate(req, newResponse()))
	})
}

func TestHTTPUpstreamDoDoesNotFallbackForGrokEntitlementDenial(t *testing.T) {
	transport := &xai.AccessDeniedFallbackTransport{
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":"subscription required"}`)),
				Request:    req,
			}, nil
		}),
	}
	req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", strings.NewReader(`{"model":"grok-4.5"}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer oauth-token")
	req.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.JSONEq(t, `{"error":"subscription required"}`, string(body))
}

func TestApplyGrokCLIProxyHeaders(t *testing.T) {
	t.Run("uses pinned stable version for the CLI proxy", func(t *testing.T) {
		t.Setenv("XAI_GROK_CLI_VERSION", "")
		req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
		require.NoError(t, err)
		req.Header.Set("User-Agent", "tokenrouter-grok/1.0")

		applyGrokCLIProxyHeaders(req)

		require.Equal(t, xai.CLIClientVersion, req.Header.Get("x-grok-client-version"))
		require.Equal(t, "xai-grok-cli", req.Header.Get("X-XAI-Token-Auth"))
		require.Equal(t, xai.CLIUserAgent(xai.CLIClientVersion), req.Header.Get("User-Agent"))
	})

	t.Run("accepts a valid operator override", func(t *testing.T) {
		t.Setenv("XAI_GROK_CLI_VERSION", "0.2.121-alpha.1")
		req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/chat/completions", nil)
		require.NoError(t, err)

		applyGrokCLIProxyHeaders(req)

		require.Equal(t, "0.2.121-alpha.1", req.Header.Get("x-grok-client-version"))
		require.Equal(t, xai.CLIUserAgent("0.2.121-alpha.1"), req.Header.Get("User-Agent"))
	})

	t.Run("rejects an unsafe override", func(t *testing.T) {
		t.Setenv("XAI_GROK_CLI_VERSION", "0.2.121\r\nX-Injected: true")
		req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
		require.NoError(t, err)

		applyGrokCLIProxyHeaders(req)

		require.Equal(t, xai.CLIClientVersion, req.Header.Get("x-grok-client-version"))
		require.Empty(t, req.Header.Get("X-Injected"))
	})

	t.Run("rejects an override below the supported minimum", func(t *testing.T) {
		t.Setenv("XAI_GROK_CLI_VERSION", "0.2.119")
		req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
		require.NoError(t, err)

		applyGrokCLIProxyHeaders(req)

		require.Equal(t, xai.CLIClientVersion, req.Header.Get("x-grok-client-version"))
		require.Equal(t, xai.CLIUserAgent(xai.CLIClientVersion), req.Header.Get("User-Agent"))
	})

	t.Run("rejects a prerelease override at the minimum version", func(t *testing.T) {
		t.Setenv("XAI_GROK_CLI_VERSION", "0.2.120-beta.1")
		req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
		require.NoError(t, err)

		applyGrokCLIProxyHeaders(req)

		require.Equal(t, xai.CLIClientVersion, req.Header.Get("x-grok-client-version"))
		require.Equal(t, xai.CLIUserAgent(xai.CLIClientVersion), req.Header.Get("User-Agent"))
	})

	// These inputs exercise the accepted version syntax, including rejection of build metadata.
	for _, version := range []string{
		"0.2.0121",
		"0.2.121-alpha..1",
		"0.3",
		"1",
		"0.2.121+build.1",
	} {
		t.Run("rejects invalid semver "+version, func(t *testing.T) {
			t.Setenv("XAI_GROK_CLI_VERSION", version)
			req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
			require.NoError(t, err)

			applyGrokCLIProxyHeaders(req)

			require.Equal(t, xai.CLIClientVersion, req.Header.Get("x-grok-client-version"))
			require.Equal(t, xai.CLIUserAgent(xai.CLIClientVersion), req.Header.Get("User-Agent"))
		})
	}

	t.Run("leaves direct xAI API requests unchanged", func(t *testing.T) {
		t.Setenv("XAI_GROK_CLI_VERSION", "0.2.95")
		req, err := http.NewRequest(http.MethodPost, "https://api.x.ai/v1/responses", nil)
		require.NoError(t, err)
		req.Header.Set("User-Agent", "tokenrouter-grok/1.0")

		applyGrokCLIProxyHeaders(req)

		require.Empty(t, req.Header.Get("x-grok-client-version"))
		require.Empty(t, req.Header.Get("X-XAI-Token-Auth"))
		require.Equal(t, "tokenrouter-grok/1.0", req.Header.Get("User-Agent"))
	})
}

// SetupTest creates options that each test can override.
func (s *HTTPUpstreamSuite) SetupTest() {
	s.cfg = &Options{}
}

// newService returns a client that reads the suite options.
func (s *HTTPUpstreamSuite) newService() *Client {
	up := New(func() *Options { return s.cfg })
	svc := up
	return svc
}

// TestDefaultResponseHeaderTimeout checks that a zero option disables the header timeout.
func (s *HTTPUpstreamSuite) TestDefaultResponseHeaderTimeout() {
	svc := s.newService()
	entry := mustGetOrCreateClient(s.T(), svc, "", 0, 0)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), time.Duration(0), transport.ResponseHeaderTimeout, "ResponseHeaderTimeout mismatch")
}

// TestNilConfigResponseHeaderTimeoutFallback checks that absent options select the built-in timeout.
func (s *HTTPUpstreamSuite) TestNilConfigResponseHeaderTimeoutFallback() {
	up := New(nil)
	svc := up
	entry := mustGetOrCreateClient(s.T(), svc, "", 0, 0)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), 300*time.Second, transport.ResponseHeaderTimeout, "ResponseHeaderTimeout mismatch")
}

// TestCustomResponseHeaderTimeout checks that the transport receives the configured seconds.
func (s *HTTPUpstreamSuite) TestCustomResponseHeaderTimeout() {
	*s.cfg = Options{ResponseHeaderTimeout: 7}
	svc := s.newService()
	entry := mustGetOrCreateClient(s.T(), svc, "", 0, 0)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), 7*time.Second, transport.ResponseHeaderTimeout, "ResponseHeaderTimeout mismatch")
}

// TestGetOrCreateClient_InvalidURLReturnsError checks that a malformed proxy prevents client selection.
func (s *HTTPUpstreamSuite) TestGetOrCreateClient_InvalidURLReturnsError() {
	svc := s.newService()
	_, err := svc.getClientEntry("://bad-proxy-url", 1, 1, upstreamcore.HTTPUpstreamProfileDefault, false, false)
	require.Error(s.T(), err, "expected error for invalid proxy URL")
}

func (s *HTTPUpstreamSuite) TestOpenAIProfileDefaultsToHTTP2AndNoHeaderTimeout() {
	*s.cfg = Options{
		ResponseHeaderTimeout: 600,
		OpenAIHTTP2: HTTP2Options{
			Enabled:                   true,
			AllowProxyFallbackToHTTP1: true,
		},
	}
	svc := s.newService()
	entry, err := svc.getClientEntry("", 1, 1, upstreamcore.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), time.Duration(0), transport.ResponseHeaderTimeout, "OpenAI profile should not inherit generic header timeout")
	require.True(s.T(), transport.ForceAttemptHTTP2, "OpenAI profile should prefer HTTP/2")
	require.Equal(s.T(), upstreamProtocolModeOpenAIH2, entry.protocolMode)
}

func (s *HTTPUpstreamSuite) TestOpenAIProfileCustomHeaderTimeout() {
	*s.cfg = Options{
		ResponseHeaderTimeout:       600,
		OpenAIResponseHeaderTimeout: 1800,
		OpenAIHTTP2: HTTP2Options{
			Enabled: true,
		},
	}
	svc := s.newService()
	entry, err := svc.getClientEntry("", 1, 1, upstreamcore.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), 1800*time.Second, transport.ResponseHeaderTimeout)
}

func (s *HTTPUpstreamSuite) TestOpenAIProfileTLSFingerprintDoesNotInheritGenericHeaderTimeout() {
	*s.cfg = Options{
		ResponseHeaderTimeout: 600,
		OpenAIHTTP2: HTTP2Options{
			Enabled: true,
		},
	}
	svc := s.newService()
	entry, err := svc.getClientEntryWithTLS("", 1, 1, &tlsfingerprint.Profile{Name: "test"}, upstreamcore.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), time.Duration(0), transport.ResponseHeaderTimeout, "OpenAI TLS path should not inherit generic header timeout")
	require.Equal(s.T(), upstreamProtocolModeOpenAIH1, entry.protocolMode, "未声明 h2 的 TLS 模板应保持 HTTP/1.1")
}

//nolint:staticcheck // Check the x/net/http2 transport and PING settings used with raw uTLS connections.
func (s *HTTPUpstreamSuite) TestOpenAIProfileTLSFingerprintUsesHTTP2WhenALPNAllowsH2() {
	*s.cfg = Options{
		OpenAIHTTP2: HTTP2Options{
			Enabled: true,
		},
	}
	svc := s.newService()
	entry, err := svc.getClientEntryWithTLS("", 1, 1, &tlsfingerprint.Profile{
		Name:          "h2-profile",
		ALPNProtocols: []string{"h2", "http/1.1"},
	}, upstreamcore.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	transport, ok := entry.client.Transport.(*http2.Transport)
	require.True(s.T(), ok, "声明 h2 的 TLS 模板应启用 HTTP/2 transport")
	require.NotNil(s.T(), transport.DialTLSContext)
	require.Equal(s.T(), 15*time.Second, transport.ReadIdleTimeout, "TLS 指纹 H2 也必须启用空闲 PING")
	require.Equal(s.T(), 15*time.Second, transport.PingTimeout, "TLS 指纹 H2 的 PING 必须有超时")
	require.Equal(s.T(), upstreamProtocolModeOpenAIH2, entry.protocolMode)
}

// TestOpenAIProfileTLSFingerprintHTTP2HeaderTimeout checks how configuration maps to pool options.
func (s *HTTPUpstreamSuite) TestOpenAIProfileTLSFingerprintHTTP2HeaderTimeout() {
	*s.cfg = Options{OpenAIResponseHeaderTimeout: 1, OpenAIHTTP2: HTTP2Options{Enabled: true}}
	svc := s.newService()
	req, _ := http.NewRequestWithContext(upstreamcore.WithHTTPUpstreamProfile(s.T().Context(), upstreamcore.HTTPUpstreamProfileOpenAI), http.MethodGet, "https://example.com", nil)
	opts, err := svc.transportOptions(req, "", 1, 1, &tlsfingerprint.Profile{Name: "h2-profile", ALPNProtocols: []string{"h2", "http/1.1"}})
	require.NoError(s.T(), err)
	require.True(s.T(), opts.Protocol.HTTP2)
	require.Equal(s.T(), time.Second, opts.Settings.ResponseHeaderTimeout)
}

func (s *HTTPUpstreamSuite) TestTLSFingerprintProfileHashSplitsClientCache() {
	svc := s.newService()
	profileA := &tlsfingerprint.Profile{Name: "profile-a", CipherSuites: []uint16{0x1301}}
	profileB := &tlsfingerprint.Profile{Name: "profile-b", CipherSuites: []uint16{0x1302}}

	entryA, err := svc.getClientEntryWithTLS("", 1, 1, profileA, upstreamcore.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	entryAAgain, err := svc.getClientEntryWithTLS("", 1, 1, profileA, upstreamcore.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	entryB, err := svc.getClientEntryWithTLS("", 1, 1, profileB, upstreamcore.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)

	require.Same(s.T(), entryA.client.Transport, entryAAgain.client.Transport, "相同 TLS profile 应复用 transport")
	require.NotSame(s.T(), entryA.client.Transport, entryB.client.Transport, "不同 TLS profile hash 不应复用旧 Transport")
}

func (s *HTTPUpstreamSuite) TestTLSFingerprintHTTP2FallbackSplitsClientCache() {
	*s.cfg = Options{
		OpenAIHTTP2: HTTP2Options{
			Enabled:                   true,
			AllowProxyFallbackToHTTP1: true,
			FallbackErrorThreshold:    1,
			FallbackWindowSeconds:     60,
			FallbackTTLSeconds:        600,
		},
	}
	svc := s.newService()
	proxyURL := "http://proxy.local:8080"
	profile := &tlsfingerprint.Profile{Name: "h2-profile", ALPNProtocols: []string{"h2", "http/1.1"}}

	entryH2, err := svc.getClientEntryWithTLS(proxyURL, 1, 1, profile, upstreamcore.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	svc.recordOpenAIHTTP2Failure(upstreamcore.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, entryH2.proxyKey, errors.New("http2: protocol error"))
	entryH1, err := svc.getClientEntryWithTLS(proxyURL, 1, 1, profile, upstreamcore.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)

	require.NotSame(s.T(), entryH2.client.Transport, entryH1.client.Transport, "H2 回退后应重建 TLS 指纹 transport")
	require.Equal(s.T(), upstreamProtocolModeOpenAIH1Fallback, entryH1.protocolMode)
	transport, ok := entryH1.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "回退到 HTTP/1.1 时应使用普通 http.Transport")
	require.False(s.T(), transport.ForceAttemptHTTP2)
}

func (s *HTTPUpstreamSuite) TestTLSFingerprintDefaultProfileStripsH2ALPNOutsideOpenAIH2Mode() {
	svc := s.newService()
	profile := &tlsfingerprint.Profile{Name: "h2-profile", ALPNProtocols: []string{"h2", "http/1.1"}}

	entry, err := svc.getClientEntryWithTLS("", 1, 1, profile, upstreamcore.HTTPUpstreamProfileDefault, false, false)
	require.NoError(s.T(), err)

	require.Equal(s.T(), upstreamProtocolModeDefault, entry.protocolMode)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "非 OpenAI H2 模式应保持 HTTP/1.1 transport")
	require.False(s.T(), transport.ForceAttemptHTTP2)
	require.NotEqual(s.T(), tlsfingerprint.CacheKey(profile), tlsfingerprint.CacheKey(entry.tlsProfile), "传给技术池的模板应使用剥离 h2 后的 profile hash")
}

func (s *HTTPUpstreamSuite) TestOpenAIProfileHTTP2DisabledUsesHTTP1Transport() {
	*s.cfg = Options{
		OpenAIHTTP2: HTTP2Options{Enabled: false},
	}
	svc := s.newService()
	entry, err := svc.getClientEntry("", 1, 1, upstreamcore.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.False(s.T(), transport.ForceAttemptHTTP2, "OpenAI HTTP/2 disabled should not force H2")
	require.NotNil(s.T(), transport.TLSNextProto, "HTTP/1 mode should disable automatic H2 negotiation")
	require.Equal(s.T(), upstreamProtocolModeOpenAIH1, entry.protocolMode)
}

func (s *HTTPUpstreamSuite) TestOpenAIHeaderTimeoutChangeRebuildsClient() {
	*s.cfg = Options{
		OpenAIHTTP2: HTTP2Options{Enabled: true},
	}
	svc := s.newService()
	entry1, err := svc.getClientEntry("", 1, 1, upstreamcore.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)

	s.cfg.OpenAIResponseHeaderTimeout = 1800
	entry2, err := svc.getClientEntry("", 1, 1, upstreamcore.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	require.NotSame(s.T(), entry1.client.Transport, entry2.client.Transport, "OpenAI header timeout 变化后应重建 transport")
	transport, ok := entry2.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), 1800*time.Second, transport.ResponseHeaderTimeout)
}

func (s *HTTPUpstreamSuite) TestOpenAIHTTP2TimeoutDoesNotActivateProxyFallback() {
	*s.cfg = Options{
		OpenAIHTTP2: HTTP2Options{
			Enabled:                   true,
			AllowProxyFallbackToHTTP1: true,
			FallbackErrorThreshold:    1,
			FallbackWindowSeconds:     60,
			FallbackTTLSeconds:        600,
		},
	}
	svc := s.newService()
	proxyURL := "http://proxy.local:8080"
	svc.recordOpenAIHTTP2Failure(upstreamcore.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, proxyURL, errors.New("http2: timeout awaiting response headers"))
	require.False(s.T(), svc.transportPolicy.Active(proxyURL, time.Now()), "header timeout should not be treated as H2 compatibility failure")
}

func (s *HTTPUpstreamSuite) TestOpenAIHTTP2ProxyCompatibilityErrorActivatesFallback() {
	*s.cfg = Options{
		OpenAIHTTP2: HTTP2Options{
			Enabled:                   true,
			AllowProxyFallbackToHTTP1: true,
			FallbackErrorThreshold:    1,
			FallbackWindowSeconds:     60,
			FallbackTTLSeconds:        600,
		},
	}
	svc := s.newService()
	proxyURL := "http://proxy.local:8080"
	svc.recordOpenAIHTTP2Failure(upstreamcore.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, proxyURL, errors.New("http2: protocol error"))
	require.True(s.T(), svc.transportPolicy.Active(proxyURL, time.Now()))

	entry, err := svc.getClientEntry(proxyURL, 1, 1, upstreamcore.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.False(s.T(), transport.ForceAttemptHTTP2)
	require.NotNil(s.T(), transport.TLSNextProto)
	require.Equal(s.T(), upstreamProtocolModeOpenAIH1Fallback, entry.protocolMode)
}

// TestNormalizeProxyURL_Canonicalizes checks that equivalent proxy URLs share a cache key.
func (s *HTTPUpstreamSuite) TestNormalizeProxyURL_Canonicalizes() {
	key1, _, err1 := normalizeProxyURL("http://proxy.local:8080")
	require.NoError(s.T(), err1)
	key2, _, err2 := normalizeProxyURL("http://proxy.local:8080/")
	require.NoError(s.T(), err2)
	require.Equal(s.T(), key1, key2, "expected normalized proxy keys to match")
}

// TestDo_WithoutProxy_GoesDirect checks that an empty proxy URL reaches the target server.
func (s *HTTPUpstreamSuite) TestDo_WithoutProxy_GoesDirect() {
	upstream := newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "direct")
	}))
	s.T().Cleanup(upstream.Close)

	up := New(func() *Options { return s.cfg })

	req, err := http.NewRequest(http.MethodGet, upstream.URL+"/x", nil)
	require.NoError(s.T(), err, "NewRequest")
	resp, err := up.Do(req, "", 1, 1)
	require.NoError(s.T(), err, "Do")
	defer func() {
		_ = resp.Body.Close()
	}()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(s.T(), "direct", string(b), "unexpected body")
}

// TestDo_WithHTTPProxy_UsesProxy checks that the proxy receives an absolute-form request URI.
func (s *HTTPUpstreamSuite) TestDo_WithHTTPProxy_UsesProxy() {
	seen := make(chan string, 1)

	proxySrv := newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.RequestURI
		_, _ = io.WriteString(w, "proxied")
	}))
	s.T().Cleanup(proxySrv.Close)

	*s.cfg = Options{ResponseHeaderTimeout: 1}
	up := New(func() *Options { return s.cfg })

	req, err := http.NewRequest(http.MethodGet, "http://example.com/test", nil)
	require.NoError(s.T(), err, "NewRequest")
	resp, err := up.Do(req, proxySrv.URL, 1, 1)
	require.NoError(s.T(), err, "Do")
	defer func() {
		_ = resp.Body.Close()
	}()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(s.T(), "proxied", string(b), "unexpected body")

	select {
	case uri := <-seen:
		require.Equal(s.T(), "http://example.com/test", uri, "expected absolute-form request URI")
	default:
		require.Fail(s.T(), "expected proxy to receive request")
	}
}

// TestDo_EmptyProxy_UsesDirect checks direct requests with an empty proxy string.
func (s *HTTPUpstreamSuite) TestDo_EmptyProxy_UsesDirect() {
	upstream := newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "direct-empty")
	}))
	s.T().Cleanup(upstream.Close)

	up := New(func() *Options { return s.cfg })
	req, err := http.NewRequest(http.MethodGet, upstream.URL+"/y", nil)
	require.NoError(s.T(), err, "NewRequest")
	resp, err := up.Do(req, "", 1, 1)
	require.NoError(s.T(), err, "Do with empty proxy")
	defer func() {
		_ = resp.Body.Close()
	}()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(s.T(), "direct-empty", string(b))
}

// TestProviderIsolation_DifferentProviders checks that provider IDs separate connection pools.
func (s *HTTPUpstreamSuite) TestProviderIsolation_DifferentProviders() {
	*s.cfg = Options{ConnectionPoolIsolation: "provider"}
	svc := s.newService()

	entry1 := mustGetOrCreateClient(s.T(), svc, "http://proxy.local:8080", 1, 3)
	entry2 := mustGetOrCreateClient(s.T(), svc, "http://proxy.local:8080", 2, 3)
	require.NotSame(s.T(), entry1.client.Transport, entry2.client.Transport, "不同提供商不应共享连接池")
}

// TestProviderProxyIsolation_DifferentProxy checks that proxy changes separate pools for the same provider.
func (s *HTTPUpstreamSuite) TestProviderProxyIsolation_DifferentProxy() {
	*s.cfg = Options{ConnectionPoolIsolation: "provider_proxy"}
	svc := s.newService()

	entry1 := mustGetOrCreateClient(s.T(), svc, "http://proxy-a:8080", 1, 3)
	entry2 := mustGetOrCreateClient(s.T(), svc, "http://proxy-b:8080", 1, 3)
	require.NotSame(s.T(), entry1.client.Transport, entry2.client.Transport, "提供商+代理隔离应区分不同代理")
}

// TestProviderModeProxyChangeClearsPool checks that switching a provider proxy replaces its transport.
func (s *HTTPUpstreamSuite) TestProviderModeProxyChangeClearsPool() {
	*s.cfg = Options{ConnectionPoolIsolation: "provider"}
	svc := s.newService()

	entry1 := mustGetOrCreateClient(s.T(), svc, "http://proxy-a:8080", 1, 3)
	entry2 := mustGetOrCreateClient(s.T(), svc, "http://proxy-b:8080", 1, 3)
	require.NotSame(s.T(), entry1.client.Transport, entry2.client.Transport, "提供商切换代理应创建新连接池")
}

// TestProviderConcurrencyOverridesPoolSettings checks connection limits against provider concurrency.
func (s *HTTPUpstreamSuite) TestProviderConcurrencyOverridesPoolSettings() {
	*s.cfg = Options{ConnectionPoolIsolation: "provider"}
	svc := s.newService()

	entry := mustGetOrCreateClient(s.T(), svc, "", 1, 12)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")

	require.Equal(s.T(), 12, transport.MaxConnsPerHost, "MaxConnsPerHost mismatch")
	require.Equal(s.T(), 12, transport.MaxIdleConns, "MaxIdleConns mismatch")
	require.Equal(s.T(), 12, transport.MaxIdleConnsPerHost, "MaxIdleConnsPerHost mismatch")
}

// TestProviderConcurrencyFallbackToDefault checks that zero concurrency uses global pool limits.
func (s *HTTPUpstreamSuite) TestProviderConcurrencyFallbackToDefault() {
	*s.cfg = Options{
		ConnectionPoolIsolation: "provider",
		MaxIdleConns:            77,
		MaxIdleConnsPerHost:     55,
		MaxConnsPerHost:         66,
	}
	svc := s.newService()

	entry := mustGetOrCreateClient(s.T(), svc, "", 1, 0)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), 66, transport.MaxConnsPerHost, "MaxConnsPerHost fallback mismatch")
	require.Equal(s.T(), 77, transport.MaxIdleConns, "MaxIdleConns fallback mismatch")
	require.Equal(s.T(), 55, transport.MaxIdleConnsPerHost, "MaxIdleConnsPerHost fallback mismatch")
}

// TestHTTPUpstreamSuite runs the transport suite.
func TestHTTPUpstreamSuite(t *testing.T) {
	suite.Run(t, new(HTTPUpstreamSuite))
}

func TestHTTPUpstreamDoPublicHostsOnlyRejectsPrivateDestinationBeforeConnecting(t *testing.T) {
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)

	upstream := New(nil)

	plain, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target.URL, nil)
	require.NoError(t, err)
	resp, err := upstream.Do(plain, "", 1, 1)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, int64(1), calls.Load(), "loopback stays reachable for requests without the marker")

	guarded, err := http.NewRequestWithContext(upstreamcore.WithHTTPUpstreamPublicHostsOnly(t.Context()), http.MethodGet, target.URL, nil)
	require.NoError(t, err)
	resp, err = upstream.Do(guarded, "", 1, 1)
	require.Error(t, err)
	require.Nil(t, resp)
	require.Contains(t, err.Error(), "not allowed")
	require.Equal(t, int64(1), calls.Load(), "marked request must be rejected before any connection is made")
}

func TestHTTPUpstreamPublicHostsOnlyValidatesEveryRedirectHop(t *testing.T) {
	upstream := New(nil)
	base := &http.Client{}

	plain, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://cdn.example.com/a.png", nil)
	require.NoError(t, err)
	require.Same(t, base, httpClientForEgressPolicy(upstream, base, upstream.requestPolicy(plain)))

	guarded, err := http.NewRequestWithContext(upstreamcore.WithHTTPUpstreamPublicHostsOnly(t.Context()), http.MethodGet, "https://cdn.example.com/a.png", nil)
	require.NoError(t, err)
	client := httpClientForEgressPolicy(upstream, base, upstream.requestPolicy(guarded))
	require.NotSame(t, base, client)
	require.NotNil(t, client.CheckRedirect)
	require.Nil(t, base.CheckRedirect, "the cached client must stay untouched")

	via := []*http.Request{guarded}
	for _, hop := range []string{
		"http://127.0.0.1:8080/a.png",
		"http://[::1]:8080/a.png",
		"http://10.0.0.8/a.png",
		"http://169.254.169.254/latest/meta-data/",
		"http://0.0.0.0/a.png",
	} {
		hopReq, err := http.NewRequestWithContext(guarded.Context(), http.MethodGet, hop, nil)
		require.NoError(t, err)
		require.Error(t, client.CheckRedirect(hopReq, via), "hop=%s", hop)
	}
	publicHop, err := http.NewRequestWithContext(guarded.Context(), http.MethodGet, "http://93.184.216.34/a.png", nil)
	require.NoError(t, err)
	require.NoError(t, client.CheckRedirect(publicHop, via))
	require.Error(t, client.CheckRedirect(publicHop, make([]*http.Request, 10)), "redirect chain stays capped")
}

// TestHTTPUpstreamPublicHostsOnlyPreservesExistingRedirectPolicy checks that public-host checks
// run before the existing redirect callback on a cloned client.
func TestHTTPUpstreamPublicHostsOnlyPreservesExistingRedirectPolicy(t *testing.T) {
	upstream := New(nil)
	called := false
	base := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		called = true
		return http.ErrUseLastResponse
	}}
	guarded, err := http.NewRequestWithContext(upstreamcore.WithHTTPUpstreamPublicHostsOnly(t.Context()), http.MethodGet, "https://93.184.216.34/a.png", nil)
	require.NoError(t, err)
	client := httpClientForEgressPolicy(upstream, base, upstream.requestPolicy(guarded))
	private, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/a.png", nil)
	require.NoError(t, err)
	require.Error(t, client.CheckRedirect(private, []*http.Request{guarded}))
	require.False(t, called)
	require.ErrorIs(t, client.CheckRedirect(guarded, []*http.Request{guarded}), http.ErrUseLastResponse)
	require.True(t, called)
	require.NotSame(t, base, client)
}

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// newLocalTestServer skips the test when a preliminary loopback listen fails.
func newLocalTestServer(tb testing.TB, handler http.Handler) *httptest.Server {
	tb.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Skipf("local listeners are not permitted in this environment: %v", err)
	}
	require.NoError(tb, listener.Close())
	return httptest.NewServer(handler)
}

func (p testUpstreamPool) Do(req *http.Request, opts httpclient.UpstreamRequestOptions) (*http.Response, error) {
	client := &http.Client{Transport: p.transport, CheckRedirect: opts.CheckRedirect}
	if opts.PrepareClient != nil {
		client = opts.PrepareClient(client)
	}
	resp, err := client.Do(req)
	if opts.ObserveResult != nil {
		opts.ObserveResult(err)
	}
	return resp, err
}

func (s *Client) getClientEntry(proxyURL string, providerID int64, concurrency int, profile upstreamcore.HTTPUpstreamProfile, _, _ bool) (*upstreamClientView, error) {
	return s.inspectClient(proxyURL, providerID, concurrency, profile, nil)
}

func (s *Client) getClientEntryWithTLS(
	proxyURL string,
	providerID int64,
	concurrency int,
	tlsProfile *tlsfingerprint.Profile,
	profile upstreamcore.HTTPUpstreamProfile,
	_, _ bool,
) (*upstreamClientView, error) {
	return s.inspectClient(proxyURL, providerID, concurrency, profile, tlsProfile)
}

func (s *Client) inspectClient(
	proxyURL string,
	providerID int64,
	concurrency int,
	profile upstreamcore.HTTPUpstreamProfile,
	tlsProfile *tlsfingerprint.Profile,
) (*upstreamClientView, error) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	req = req.WithContext(upstreamcore.WithHTTPUpstreamProfile(req.Context(), profile))
	opts, err := s.transportOptions(req, proxyURL, providerID, concurrency, tlsProfile)
	if err != nil {
		return nil, err
	}
	key, _, err := normalizeProxyURL(proxyURL)
	if err != nil {
		return nil, err
	}
	view := &upstreamClientView{proxyKey: key, protocolMode: opts.Protocol.CacheVariant, tlsProfile: opts.TLSProfile}
	opts.PrepareClient = func(client *http.Client) *http.Client {
		view.client = client
		clone := *client
		clone.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody, Request: req}, nil
		})
		return &clone
	}
	resp, err := s.pool.Do(req, opts)
	if err != nil {
		return nil, err
	}
	if err = resp.Body.Close(); err != nil {
		return nil, err
	}
	return view, nil
}

func mustGetOrCreateClient(t *testing.T, svc *Client, proxyURL string, providerID int64, concurrency int) *upstreamClientView {
	t.Helper()
	entry, err := svc.inspectClient(proxyURL, providerID, concurrency, upstreamcore.HTTPUpstreamProfileDefault, nil)
	require.NoError(t, err)
	return entry
}
