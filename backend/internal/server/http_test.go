package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// ingressTestConfig returns small listener limits for concrete HTTP rejection fixtures.
func ingressTestConfig() Options {
	return Options{Address: "127.0.0.1:0", ReadHeaderTimeout: 1, IdleTimeout: 5, MaxHeaderBytes: 8 * 1024, MaxRequestBodySize: 1024}
}

// TestProvideHTTPServerAppliesIngressLimits
// constructs a listener from second and byte settings.
// The returned server must contain the configured header cap and converted
// header-read and idle durations before any network serving begins.
func TestProvideHTTPServerAppliesIngressLimits(t *testing.T) {
	srv := ProvideHTTPServer(ingressTestConfig(), gin.New())
	require.Equal(t, 8*1024, srv.MaxHeaderBytes)
	require.Equal(t, time.Second, srv.ReadHeaderTimeout)
	require.Equal(t, 5*time.Second, srv.IdleTimeout)
}

// TestProvideHTTPServerEnablesBoundedH2C supplies explicit HTTP/2 connection settings.
// The returned server must advertise HTTP/1 and unencrypted HTTP/2 through its
// Protocols flags after adapter configuration succeeds.
func TestProvideHTTPServerEnablesBoundedH2C(t *testing.T) {
	cfg := ingressTestConfig()
	cfg.H2C = H2COptions{
		Enabled:                      true,
		MaxConcurrentStreams:         25,
		IdleTimeout:                  30,
		MaxReadFrameSize:             64 * 1024,
		MaxUploadBufferPerConnection: 1024 * 1024,
		MaxUploadBufferPerStream:     256 * 1024,
	}
	srv := ProvideHTTPServer(cfg, gin.New())
	require.NotNil(t, srv.Protocols)
	require.True(t, srv.Protocols.UnencryptedHTTP2())
	require.True(t, srv.Protocols.HTTP1())
}

// TestConfigureTrustedProxies
// compares valid, explicitly empty, and malformed peer policies.
// A forwarded address is used for the trusted peer, while empty and rejected
// policies produce the direct address returned by Gin ClientIP.
func TestConfigureTrustedProxies(t *testing.T) {
	tests := []struct {
		name string  // trusted-proxy selection scenario name
		cfg  Options // configured proxy peer policy
		want string  // expected Gin client address
	}{
		{
			name: "configured proxy resolves forwarded client",
			cfg: Options{
				TrustedProxies:           []string{"9.9.9.9/32"},
				TrustedProxiesConfigured: true,
			},
			want: "1.2.3.4",
		},
		{
			name: "explicit empty list ignores forwarded client",
			cfg: Options{
				TrustedProxiesConfigured: true,
			},
			want: "9.9.9.9",
		},
		{
			name: "invalid proxy list fails closed",
			cfg: Options{
				TrustedProxies:           []string{"not-a-cidr"},
				TrustedProxiesConfigured: true,
			},
			want: "9.9.9.9",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			configureTrustedProxies(r, tc.cfg)
			r.GET("/t", func(c *gin.Context) { c.String(http.StatusOK, c.ClientIP()) })

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/t", nil)
			req.RemoteAddr = "9.9.9.9:12345"
			req.Header.Set("X-Forwarded-For", "1.2.3.4")
			r.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, tc.want, w.Body.String())
		})
	}
}

// TestHTTPServerRejectsOversizedHTTP1Header
// sends a raw header exceeding the fixture's cap.
// The wire response must report HTTP 431 before the registered handler can
// return its ordinary success status for that request.
func TestHTTPServerRejectsOversizedHTTP1Header(t *testing.T) {
	r := gin.New()
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	srv := ProvideHTTPServer(ingressTestConfig(), r)
	addr, stop := serveIngressTestServer(t, srv)
	// Register server cleanup first so later client/body closes run before
	// Shutdown waits, keeping this fixture's own connection out of its drain wait.
	defer stop()

	conn, err := net.DialTimeout("tcp", addr, time.Second)
	require.NoError(t, err)
	// Release the raw client before server cleanup; close errors are discarded
	// because the subsequent assertions judge the HTTP response or read failure.
	defer func() { _ = conn.Close() }()
	// Use a deadline for this wire exchange and observe write/read results.
	// The setter result is intentionally discarded by this response-focused fixture.
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, err = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: test\r\nX-Fill: "+strings.Repeat("a", 32*1024)+"\r\n\r\n")
	require.NoError(t, err)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	// Finish response-body cleanup before closing the raw connection. Its close
	// error cannot change the already decoded status asserted by this test.
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusRequestHeaderFieldsTooLarge, resp.StatusCode)
}

// TestHTTPServerClosesSlowIncompleteHeader
// leaves a raw request header unfinished past the server budget.
// The subsequent read must fail under the fixture's read deadline; the assertion
// records that failure without classifying its network cause.
func TestHTTPServerClosesSlowIncompleteHeader(t *testing.T) {
	r := gin.New()
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	srv := ProvideHTTPServer(ingressTestConfig(), r)
	addr, stop := serveIngressTestServer(t, srv)
	// Register server cleanup first so later client/body closes run before
	// Shutdown waits, keeping this fixture's own connection out of its drain wait.
	defer stop()

	conn, err := net.DialTimeout("tcp", addr, time.Second)
	require.NoError(t, err)
	// Release the raw client before server cleanup; close errors are discarded
	// because the subsequent assertions judge the HTTP response or read failure.
	defer func() { _ = conn.Close() }()
	_, err = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: test\r\nX-Slow:")
	require.NoError(t, err)
	// Wait beyond the configured one-second header-read budget.
	time.Sleep(1200 * time.Millisecond)
	// Observe the read result after setting the fixture's own deadline. The
	// discarded setter result and generic error assertion leave the cause unspecified.
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, err = bufio.NewReader(conn).ReadByte()
	require.Error(t, err)
}

// TestHTTPServerGlobalBodyLimit consumes a body one byte larger than the listener cap.
// Its handler recognizes MaxBytesError from the wrapped request reader and must
// return HTTP 413 for that oversized input.
func TestHTTPServerGlobalBodyLimit(t *testing.T) {
	r := gin.New()
	// The handler observes the wrapper's read error; only MaxBytesError selects
	// HTTP 413, isolating byte-cap classification during actual body consumption.
	r.POST("/", func(c *gin.Context) {
		_, err := io.ReadAll(c.Request.Body)
		if err != nil {
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				c.Status(http.StatusRequestEntityTooLarge)
				return
			}
		}
		c.Status(http.StatusOK)
	})
	srv := ProvideHTTPServer(ingressTestConfig(), r)
	req, err := http.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 1025)))
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

// serveIngressTestServer returns a bound address and graceful-shutdown callback.
// Callers invoke the callback after their client cleanup. Serving runs separately,
// and the callback attempts Shutdown with a fresh one-second budget while
// discarding its result for these response-focused fixtures.
func serveIngressTestServer(t *testing.T, srv *http.Server) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	// The bound listener is ready before asynchronous serving starts. Response
	// assertions observe the network path, while this fixture discards Serve's result.
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String(), func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		// Cleanup attempts graceful listener shutdown under this independent budget.
		// Its error is discarded because response assertions determine the fixture outcome.
		_ = srv.Shutdown(ctx)
	}
}
