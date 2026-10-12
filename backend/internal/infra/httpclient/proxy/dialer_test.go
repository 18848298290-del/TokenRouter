package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Proxy transport tests inspect routing fields after configuration rather
// than opening real proxy connections. They distinguish HTTP Proxy assignment
// from SOCKS DialContext replacement, retain unrelated pool settings, and
// confirm that caller network dialing remains available on the HTTP branch.

// errStub lets the injected HTTP network dialer return a recognizable failure
// while tests observe that proxy configuration retained the same callback.
var errStub = errors.New("stub dial")

// TestConfigureTransportProxy_Nil supplies no proxy URL to an empty
// transport. It requires success and unchanged nil routing fields, protecting
// direct setup when proxy selection is absent.
func TestConfigureTransportProxy_Nil(t *testing.T) {
	transport := &http.Transport{}
	err := ConfigureTransportProxy(transport, nil)

	require.NoError(t, err)
	assert.Nil(t, transport.Proxy, "nil proxy should not set Proxy")
	assert.Nil(t, transport.DialContext, "nil proxy should not set DialContext")
}

// TestConfigureTransportProxy_HTTP configures an HTTP proxy on an empty
// transport. It requires Proxy assignment with DialContext left nil,
// protecting delegation through the caller's ordinary network dialer.
func TestConfigureTransportProxy_HTTP(t *testing.T) {
	transport := &http.Transport{}
	proxyURL, _ := url.Parse("http://proxy.example.com:8080")

	err := ConfigureTransportProxy(transport, proxyURL)

	require.NoError(t, err)
	assert.NotNil(t, transport.Proxy, "HTTP proxy should set Proxy")
	assert.Nil(t, transport.DialContext, "HTTP proxy should not set DialContext")
}

// TestConfigureTransportProxy_HTTPS configures an HTTPS proxy on an empty
// transport. It requires Proxy assignment with DialContext left nil,
// protecting scheme selection without a SOCKS-style dial replacement.
func TestConfigureTransportProxy_HTTPS(t *testing.T) {
	transport := &http.Transport{}
	proxyURL, _ := url.Parse("https://secure-proxy.example.com:8443")

	err := ConfigureTransportProxy(transport, proxyURL)

	require.NoError(t, err)
	assert.NotNil(t, transport.Proxy, "HTTPS proxy should set Proxy")
	assert.Nil(t, transport.DialContext, "HTTPS proxy should not set DialContext")
}

// TestConfigureTransportProxy_SOCKS5 configures the SOCKS5 alias on an empty
// transport. It requires a usable DialContext field and no Proxy assignment,
// protecting SOCKS routing construction before requests begin.
func TestConfigureTransportProxy_SOCKS5(t *testing.T) {
	transport := &http.Transport{}
	proxyURL, _ := url.Parse("socks5://socks.example.com:1080")

	err := ConfigureTransportProxy(transport, proxyURL)

	require.NoError(t, err)
	assert.Nil(t, transport.Proxy, "SOCKS5 proxy should not set Proxy")
	assert.NotNil(t, transport.DialContext, "SOCKS5 proxy should set DialContext")
}

// TestConfigureTransportProxy_SOCKS5H configures SOCKS5H on an empty
// transport. It requires a usable DialContext field and no Proxy assignment,
// protecting the proxy-side hostname dialing path during construction.
func TestConfigureTransportProxy_SOCKS5H(t *testing.T) {
	transport := &http.Transport{}
	proxyURL, _ := url.Parse("socks5h://socks.example.com:1080")

	err := ConfigureTransportProxy(transport, proxyURL)

	require.NoError(t, err)
	assert.Nil(t, transport.Proxy, "SOCKS5H proxy should not set Proxy")
	assert.NotNil(t, transport.DialContext, "SOCKS5H proxy should set DialContext")
}

// TestConfigureTransportProxy_CaseInsensitive supplies mixed-case spellings
// of each supported scheme through URL parsing. It requires the expected HTTP
// or SOCKS routing field and leaves the other field nil.
func TestConfigureTransportProxy_CaseInsensitive(t *testing.T) {
	testCases := []struct {
		scheme   string // supplied proxy URL text
		useProxy bool   // expect HTTP Proxy routing field
	}{
		{"HTTP://proxy.example.com:8080", true},
		{"Http://proxy.example.com:8080", true},
		{"HTTPS://proxy.example.com:8443", true},
		{"Https://proxy.example.com:8443", true},
		{"SOCKS5://socks.example.com:1080", false},
		{"Socks5://socks.example.com:1080", false},
		{"SOCKS5H://socks.example.com:1080", false},
		{"Socks5h://socks.example.com:1080", false},
	}

	for _, tc := range testCases {
		t.Run(tc.scheme, func(t *testing.T) {
			transport := &http.Transport{}
			proxyURL, _ := url.Parse(tc.scheme)

			err := ConfigureTransportProxy(transport, proxyURL)

			require.NoError(t, err)
			if tc.useProxy {
				assert.NotNil(t, transport.Proxy)
				assert.Nil(t, transport.DialContext)
			} else {
				assert.Nil(t, transport.Proxy)
				assert.NotNil(t, transport.DialContext)
			}
		})
	}
}

// TestConfigureTransportProxy_Unsupported supplies FTP, file, and unknown
// proxy schemes. It requires an unsupported-scheme error for each case,
// protecting rejection before these routes can be used by transport callers.
func TestConfigureTransportProxy_Unsupported(t *testing.T) {
	testCases := []string{
		"ftp://ftp.example.com",
		"file:///path/to/file",
		"unknown://example.com",
	}

	for _, tc := range testCases {
		t.Run(tc, func(t *testing.T) {
			transport := &http.Transport{}
			proxyURL, _ := url.Parse(tc)

			err := ConfigureTransportProxy(transport, proxyURL)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "unsupported proxy scheme")
		})
	}
}

// TestConfigureTransportProxy_WithAuth constructs a SOCKS proxy URL carrying
// username and password. It requires successful routing configuration and an
// installed dial callback, checking construction rather than a remote
// authentication exchange.
func TestConfigureTransportProxy_WithAuth(t *testing.T) {
	transport := &http.Transport{}
	proxyURL, _ := url.Parse("socks5://user:password@socks.example.com:1080")

	err := ConfigureTransportProxy(transport, proxyURL)

	require.NoError(t, err)
	assert.NotNil(t, transport.DialContext, "SOCKS5 with auth should set DialContext")
}

// TestConfigureTransportProxy_EmptyScheme supplies a manually constructed URL
// with host and no scheme. It requires an unsupported-scheme error,
// protecting callers from assuming a host alone identifies proxy protocol.
func TestConfigureTransportProxy_EmptyScheme(t *testing.T) {
	transport := &http.Transport{}
	// A manually constructed URL can have a host with no scheme.
	proxyURL := &url.URL{Host: "proxy.example.com:8080"}

	err := ConfigureTransportProxy(transport, proxyURL)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported proxy scheme")
}

// TestConfigureTransportProxy_PreservesExistingConfig starts with nondefault
// idle-connection limits before SOCKS routing configuration. It requires both
// limits to remain and DialContext to be installed, protecting unrelated pool
// settings during route setup.
func TestConfigureTransportProxy_PreservesExistingConfig(t *testing.T) {
	// Proxy setup must retain unrelated transport pool settings.
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
	}
	proxyURL, _ := url.Parse("socks5://socks.example.com:1080")

	err := ConfigureTransportProxy(transport, proxyURL)

	require.NoError(t, err)
	assert.Equal(t, 100, transport.MaxIdleConns, "MaxIdleConns should be preserved")
	assert.Equal(t, 10, transport.MaxIdleConnsPerHost, "MaxIdleConnsPerHost should be preserved")
	assert.NotNil(t, transport.DialContext, "DialContext should be set")
}

// TestConfigureTransportProxy_IPv6 supplies bracketed IPv6 proxy authorities
// with explicit ports for HTTP and SOCKS schemes. It requires parsing and
// construction to succeed for loopback and global IPv6 fixtures.
func TestConfigureTransportProxy_IPv6(t *testing.T) {
	testCases := []struct {
		name     string // case display name
		proxyURL string // supplied proxy URL text
	}{
		{"SOCKS5H with IPv6 loopback", "socks5h://[::1]:1080"},
		{"SOCKS5 with full IPv6", "socks5://[2001:db8::1]:1080"},
		{"HTTP with IPv6", "http://[::1]:8080"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			transport := &http.Transport{}
			proxyURL, err := url.Parse(tc.proxyURL)
			require.NoError(t, err, "URL should be parseable")

			err = ConfigureTransportProxy(transport, proxyURL)
			require.NoError(t, err)
		})
	}
}

// TestConfigureTransportProxy_SpecialCharsInPassword supplies percent-encoded
// reserved characters in SOCKS credentials. It requires successful URL
// parsing and dial callback installation, checking accepted construction
// spelling before any actual proxy exchange.
func TestConfigureTransportProxy_SpecialCharsInPassword(t *testing.T) {
	testCases := []struct {
		name     string // case display name
		proxyURL string // supplied proxy URL text
	}{
		// An encoded @ remains part of the password.
		{"password with @", "socks5://user:p%40ssword@proxy.example.com:1080"},
		// An encoded colon remains part of the password.
		{"password with :", "socks5://user:pass%3Aword@proxy.example.com:1080"},
		// An encoded slash remains part of the password.
		{"password with /", "socks5://user:pass%2Fword@proxy.example.com:1080"},
		// Cover several encoded reserved characters together.
		{"complex password", "socks5h://admin:P%40ss%3Aw0rd%2F123@proxy.example.com:1080"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			transport := &http.Transport{}
			proxyURL, err := url.Parse(tc.proxyURL)
			require.NoError(t, err, "URL should be parseable")

			err = ConfigureTransportProxy(transport, proxyURL)
			require.NoError(t, err)
			assert.NotNil(t, transport.DialContext, "SOCKS5 should set DialContext")
		})
	}
}

// TestSOCKS5ForwardDialerHasBoundedTimeout inspects the shared SOCKS forward
// network dialer. It requires the positive configured timeout and keepalive
// values that apply after SOCKS replaces a transport's original dial
// callback.
func TestSOCKS5ForwardDialerHasBoundedTimeout(t *testing.T) {
	require.Greater(t, socks5ForwardDialer.Timeout, time.Duration(0))
	require.Equal(t, socks5DialTimeout, socks5ForwardDialer.Timeout)
	require.Equal(t, socks5DialKeepAlive, socks5ForwardDialer.KeepAlive)
}

// TestConfigureTransportProxySOCKS5SetsDialContext configures both SOCKS
// aliases against a loopback authority. It requires DialContext assignment
// and a nil Proxy field for each, protecting the expected transport routing
// representation.
func TestConfigureTransportProxySOCKS5SetsDialContext(t *testing.T) {
	for _, scheme := range []string{"socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			proxyURL, err := url.Parse(scheme + "://127.0.0.1:1080")
			require.NoError(t, err)

			transport := &http.Transport{}
			require.NoError(t, ConfigureTransportProxy(transport, proxyURL))
			require.NotNil(t, transport.DialContext)
			require.Nil(t, transport.Proxy, "SOCKS5 不应设置 Transport.Proxy")
		})
	}
}

// TestConfigureTransportProxyHTTPPreservesDialContext configures HTTP routing
// on a transport with an injected network callback. It requires both proxy
// selection and the injected callback to remain callable, then observes that
// callback being invoked.
func TestConfigureTransportProxyHTTPPreservesDialContext(t *testing.T) {
	proxyURL, err := url.Parse("http://127.0.0.1:8080")
	require.NoError(t, err)

	called := false
	transport := &http.Transport{}
	transport.DialContext = func(_ context.Context, _, _ string) (net.Conn, error) {
		called = true
		return nil, errStub
	}

	require.NoError(t, ConfigureTransportProxy(transport, proxyURL))
	require.NotNil(t, transport.Proxy)
	require.NotNil(t, transport.DialContext)

	_, _ = transport.DialContext(context.Background(), "tcp", "127.0.0.1:1")
	require.True(t, called, "HTTP 代理分支不应替换调用方的 DialContext")
}
