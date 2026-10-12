package provider

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// Catalog downloads carry conditional validators and reject failed status or
// oversized bodies before parsing. Local HTTP fixtures compare the returned
// validator with the body state and exercise request cancellation, while proxy
// construction fixtures check whether the policy returns an HTTP wrapper or a
// permanent error client.
//
// Cancellation is triggered after the server handler has observed the request,
// so the client has in-flight HTTP work to cancel. The handler waits for its
// request context to end, and the test joins the client result before closing
// the server. Environments that cannot bind a loopback listener skip HTTP
// fixtures after a cached availability probe.

// Loopback availability values retain one process-wide bind result so HTTP
// fixtures can skip when listeners are unavailable.
var (
	canListenOnce sync.Once // one-time loopback availability probe
	canListen     bool      // retained loopback bind result
	canListenErr  error     // retained loopback bind failure
)

// TestFetchCatalogHTTP returns successful, unchanged, failed-status, and
// oversized responses from a local server. FetchCatalog must retain the old
// validator for 304, return response metadata for success, and reject failed
// or oversized candidates.
func TestFetchCatalogHTTP(t *testing.T) {
	for _, tc := range []struct {
		name      string // named HTTP response scenario
		status    int    // response HTTP status code
		body      string // response catalog body fixture
		unchanged bool   // expected unchanged body flag
		wantError bool   // expected fetch failure result
	}{
		{"success", http.StatusOK, `{"providers":{}}`, false, false},
		{"unchanged", http.StatusNotModified, "", true, false},
		{"server error", http.StatusInternalServerError, "", false, true},
		{"too large", http.StatusOK, strings.Repeat("x", 32*1024*1024+1), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, `"v1"`, r.Header.Get("If-None-Match"))
				w.Header().Set("ETag", `"v2"`)
				w.WriteHeader(tc.status)
				// Oversized downloads can close their body before the fixture
				// finishes writing. Assert the fetch result instead of treating
				// that server write error as a test failure.
				_, _ = w.Write([]byte(tc.body))
			}))
			// Close each fixture server after its client fetch has returned so
			// the test releases loopback resources used by the next response
			// scenario.
			defer server.Close()
			client := &remoteClient{httpClient: server.Client()}
			body, etag, unchanged, err := client.FetchCatalog(context.Background(), server.URL, `"v1"`)
			if tc.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.unchanged, unchanged)
			if unchanged {
				require.Empty(t, body)
				require.Equal(t, `"v1"`, etag)
			} else {
				require.Equal(t, tc.body, string(body))
				require.Equal(t, `"v2"`, etag)
			}
		})
	}
}

// TestFetchCatalogCancellation waits for a server to observe the request
// before canceling its client context. The fetch must return an error, and an
// invalid request URL must also fail before a download succeeds.
func TestFetchCatalogCancellation(t *testing.T) {
	started := make(chan struct{})
	server := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Signal that the request reached the handler before waiting for its
		// cancellation, so the client cancellation applies to HTTP work that
		// has reached the server.
		close(started)
		<-r.Context().Done()
	}))
	// Close the server after the canceled client result is joined, allowing
	// its handler's request context to finish before the fixture is torn down.
	defer server.Close()
	client := &remoteClient{httpClient: server.Client()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	// Fetch asynchronously so the test can wait for server admission and then
	// cancel the same context while the handler is still waiting for
	// completion.
	go func() {
		_, _, _, err := client.FetchCatalog(ctx, server.URL, "")
		done <- err
	}()
	<-started
	cancel()
	require.Error(t, <-done)
	_, _, _, err := client.FetchCatalog(context.Background(), "://invalid-url", "")
	require.Error(t, err)
}

// TestNewRemoteClient_InvalidProxy_NoFallback supplies an invalid proxy with
// replacement disabled. Construction must return a failing client whose fetch
// reports the proxy initialization error before attempting the catalog request.
func TestNewRemoteClient_InvalidProxy_NoFallback(t *testing.T) {
	client := NewRemoteClient("://bad", false)
	require.IsType(t, &remoteClientError{}, client)
	_, _, _, err := client.FetchCatalog(context.Background(), "https://example.com", "")
	require.ErrorContains(t, err, "proxy client init failed")
}

// TestNewRemoteClient_InvalidProxy_WithFallback permits replacement after an
// invalid proxy configuration. Construction must return the HTTP catalog client
// type; this test checks construction and does not perform a network fetch.
func TestNewRemoteClient_InvalidProxy_WithFallback(t *testing.T) {
	require.IsType(t, &remoteClient{}, NewRemoteClient("://bad", true))
}

// localListenerAvailable returns a cached loopback bind result for HTTP
// fixtures. The first caller records a bind failure or closes its probe socket,
// and later callers reuse that result.
func localListenerAvailable() bool {
	canListenOnce.Do(func() {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			canListenErr = err
			canListen = false
			return
		}
		// The probe's bind success determines availability; its socket serves
		// no HTTP requests. Close it before recording success so fixture
		// servers can use separate listeners.
		_ = ln.Close()
		canListen = true
	})
	return canListen
}

// newLocalTestServer starts an HTTP fixture or skips when the cached
// loopback probe failed. Callers close the returned server after completing or
// canceling requests that keep its handlers waiting for client activity.
func newLocalTestServer(tb testing.TB, handler http.Handler) *httptest.Server {
	tb.Helper()
	if !localListenerAvailable() {
		tb.Skipf("local listeners are not permitted in this environment: %v", canListenErr)
	}
	return httptest.NewServer(handler)
}
