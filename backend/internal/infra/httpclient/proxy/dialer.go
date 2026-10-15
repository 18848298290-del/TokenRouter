package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// dialer.go configures HTTP transports to connect through HTTP, HTTPS, or
// SOCKS proxies.
// SOCKS proxy routing replaces an HTTP transport's DialContext, so a caller's
// original dial budget no longer governs that route. A shared forward
// net.Dialer supplies its own ten-second connection limit and keepalive
// interval beneath SOCKS negotiation; HTTP and HTTPS proxy selection retains
// the caller's configured network dialer.

// SOCKS forward connection settings bound proxy TCP setup and retain periodic
// keepalive probes after its replacement DialContext takes over transport dialing.
const (
	// socks5DialTimeout bounds TCP connection setup to the SOCKS5 proxy.
	socks5DialTimeout = 10 * time.Second
	// socks5DialKeepAlive sets the TCP keepalive interval to 30 seconds.
	socks5DialKeepAlive = 30 * time.Second
)

// socks5ForwardDialer supplies bounded TCP dialing beneath the SOCKS5 dialer.
//
// The default proxy.FromURL forward dialer has no dial timeout.
// An unreachable proxy can therefore wait for the operating system's TCP retries.
// SOCKS replaces Transport.DialContext, so the timeout on the caller's transport
// must be supplied by this forward dialer for SOCKS connections.
var socks5ForwardDialer = &net.Dialer{
	Timeout:   socks5DialTimeout,
	KeepAlive: socks5DialKeepAlive,
}

// ConfigureTransportProxy mutates the caller-owned transport to use proxyURL,
// leaving nil proxy selection unchanged. Configure before concurrent
// requests; HTTP/HTTPS sets Proxy while SOCKS5/SOCKS5H installs a SOCKS
// DialContext and retains any existing Proxy field.
//
// The pinned SOCKS implementation accepts context-aware dialing and forwards
// hostnames to the proxy. A dialer lacking that interface uses a
// noncancelable adapter; construction or unsupported-scheme errors are
// returned for caller policy. This function changes routing fields rather
// than clearing unrelated previous routing configuration.
func ConfigureTransportProxy(transport *http.Transport, proxyURL *url.URL) error {
	if proxyURL == nil {
		return nil
	}

	scheme := strings.ToLower(proxyURL.Scheme)
	switch scheme {
	case "http", "https":
		transport.Proxy = http.ProxyURL(proxyURL)
		return nil

	case "socks5", "socks5h":
		dialer, err := proxy.FromURL(proxyURL, socks5ForwardDialer)
		if err != nil {
			return fmt.Errorf("create socks5 dialer: %w", err)
		}
		// SOCKS replaces the caller's DialContext. Use the dialer's context-aware
		// path when available so the request budget reaches proxy connection setup.
		if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
			transport.DialContext = contextDialer.DialContext
		} else {
			// A non-context dialer still fits the transport callback through this
			// adapter, but Dial receives no request cancellation signal or deadline.
			transport.DialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			}
		}
		return nil

	default:
		return fmt.Errorf("unsupported proxy scheme: %s", scheme)
	}
}
