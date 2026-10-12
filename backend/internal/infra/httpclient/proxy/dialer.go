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

// ConfigureTransportProxy configures proxy routing on transport.
//
// Supported schemes:
//   - http/https set transport.Proxy and retain the caller's DialContext.
//   - socks5 sets DialContext through x/net/proxy and sends hostnames to the proxy.
//   - socks5h uses the same dialer and hostname resolution behavior.
//
// Arguments:
//   - transport is the caller-owned transport to configure.
//   - proxyURL selects routing; nil leaves the transport unchanged.
//
// Errors:
//   - Unsupported schemes and dialer construction failures return an error.
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
		// Use ContextDialer when available so requests can cancel proxy dialing.
		if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
			transport.DialContext = contextDialer.DialContext
		} else {
			// Adapt a dialer that lacks ContextDialer to the transport signature.
			// This adapter cannot forward request cancellation or deadlines to Dial.
			transport.DialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			}
		}
		return nil

	default:
		return fmt.Errorf("unsupported proxy scheme: %s", scheme)
	}
}
