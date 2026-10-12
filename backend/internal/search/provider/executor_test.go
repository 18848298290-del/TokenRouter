package provider

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Connectivity classification can match plain error text even when an error
// lacks a network type. These cases exercise those message fragments and
// construct clients for direct, HTTP, and SOCKS routing; client construction
// does not establish live proxy connectivity.

// TestIsProxyError_Nil passes no error to the connectivity heuristic. It
// reports false, keeping a successful supplier outcome from entering Manager's
// configured proxy-failure handling branches.
func TestIsProxyError_Nil(t *testing.T) {
	require.False(t, isProxyError(nil))
}

// TestIsProxyError_ConnectionRefused uses a plain error message containing
// connection refusal. The heuristic classifies it as connectivity-related even
// though the fixture has no wrapped network error type.
func TestIsProxyError_ConnectionRefused(t *testing.T) {
	require.True(t, isProxyError(fmt.Errorf("dial tcp: connection refused")))
}

// TestIsProxyError_Timeout uses a plain timeout message mentioning a proxy.
// Classification returns true, protecting message-based connectivity detection
// before Manager decides configured versus shared proxy handling.
func TestIsProxyError_Timeout(t *testing.T) {
	require.True(t, isProxyError(fmt.Errorf("i/o timeout while connecting to proxy")))
}

// TestIsProxyError_SOCKS uses a plain message naming a failed SOCKS connection.
// The classifier returns true based on that fragment; the case does not attempt
// a SOCKS dial.
func TestIsProxyError_SOCKS(t *testing.T) {
	require.True(t, isProxyError(fmt.Errorf("socks connect failed")))
}

// TestIsProxyError_TLSHandshake uses a plain TLS handshake timeout message.
// Classification returns true through text matching; a live TLS negotiation or
// typed error chain is outside this assertion.
func TestIsProxyError_TLSHandshake(t *testing.T) {
	require.True(t, isProxyError(fmt.Errorf("tls handshake timeout")))
}

// TestIsProxyError_APIError_NotProxy uses a supplier API rate-limit message
// with no connectivity fragment. The heuristic returns false so that message
// alone does not become a proxy failure classification.
func TestIsProxyError_APIError_NotProxy(t *testing.T) {
	require.False(t, isProxyError(fmt.Errorf("API rate limit exceeded")))
}

// TestNewHTTPClient_NoProxy constructs a client with an empty proxy URL. It
// returns a nonnil client and nil error before any supplier request or network
// connection occurs.
func TestNewHTTPClient_NoProxy(t *testing.T) {
	c, err := newHTTPClient("")
	require.NoError(t, err)
	require.NotNil(t, c)
}

// TestNewHTTPClient_InvalidProxy constructs a client with malformed proxy text.
// It returns an invalid-proxy-URL error, protecting setup rejection before a
// supplier adapter can perform an exchange.
func TestNewHTTPClient_InvalidProxy(t *testing.T) {
	_, err := newHTTPClient("://bad-url")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid proxy URL")
}

// TestNewHTTPClient_ValidHTTPProxy supplies a syntactically valid HTTP proxy
// URL. Client construction succeeds with a nonnil result; the test does not
// contact that proxy host.
func TestNewHTTPClient_ValidHTTPProxy(t *testing.T) {
	c, err := newHTTPClient("http://proxy.example.com:8080")
	require.NoError(t, err)
	require.NotNil(t, c)
}

// TestNewHTTPClient_ValidSOCKS5Proxy supplies a syntactically valid SOCKS5
// proxy URL. It receives a nonnil client with nil error, checking configuration
// acceptance before any actual dial.
func TestNewHTTPClient_ValidSOCKS5Proxy(t *testing.T) {
	c, err := newHTTPClient("socks5://proxy.example.com:1080")
	require.NoError(t, err)
	require.NotNil(t, c)
}
