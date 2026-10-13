package ollama

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A fixed Ollama settings URL can be exercised through a local TLS server by
// substituting the transport destination while retaining logical request
// metadata. The cases check outgoing session headers, failure classification,
// bounded reading, retry seconds, and response-body release for each successful
// transport result.

// fetchBody wraps a fixture response body with shared close-attempt counting.
type fetchBody struct {
	io.ReadCloser // wrapped fixture response body
	// closed is shared with the final resource-release assertions. Atomic
	// counting supports the sender and fixture observations using this same
	// counter throughout a case.
	closed *atomic.Int64
}

// Close increments the shared counter before closing the wrapped response body.
// Delegated errors are returned, and every invocation remains counted so the
// fixture can compare acquired responses with close attempts.
func (b *fetchBody) Close() error { b.closed.Add(1); return b.ReadCloser.Close() }

// TestFetchUsageLocalTLSContract sends settings requests through a local TLS
// fixture with success, redirect, authorization, sign-in HTML, and oversized
// bodies. It checks logical URL and headers, one body close per response,
// redacted input formatting, and selected result data or retry timing.
func TestFetchUsageLocalTLSContract(t *testing.T) {
	tests := []struct {
		name   string // table case description
		status int    // fixture HTTP status
		// body and failure pair the supplied page bytes with their
		// expected fetch classification. A successful fixture body
		// expects empty failure, while rejected content or status names
		// the corresponding observation. body: supplied settings page
		// text. failure: expected observation reason.
		body, failure string
		unauthorized  bool // expected session authorization flag
	}{
		{"success", 200, string(ollamaUsageFixture(t)), "", false},
		{"redirect", 302, "", "redirect_blocked", false},
		{"unauthorized", 401, "", "unauthorized", true},
		{"sign in HTML", 200, "<p>Sign in to Ollama</p>", "unauthorized", true},
		{"oversized", 200, strings.Repeat("x", MaxBodyBytes+1), "response_too_large", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls, closed atomic.Int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				assert.Equal(t, "/settings", r.URL.Path)
				assert.Equal(t, "wos-session=cookie-secret", r.Header.Get("Cookie"))
				assert.Equal(t, "tokenrouter-ollama-usage/1", r.UserAgent())
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			// Release the case's TLS listener and connections after
			// fetch assertions, including failures, so later cases
			// own independent local server resources.
			defer server.Close()
			target, err := url.Parse(server.URL)
			require.NoError(t, err)
			input := FetchInput{ObservedAt: time.Now(), Cookie: "wos-session=cookie-secret"}
			result, err := FetchUsage(context.Background(), input, FetchOptions{Context: func(ctx context.Context) context.Context { return ctx }, Do: func(r *http.Request) (*http.Response, error) {
				assert.Equal(t, SettingsURL, r.URL.String())
				// Route the physical connection to the local
				// TLS fixture using a copied URL, while the
				// fetcher continues to see its fixed logical
				// settings target.
				forward := r.Clone(r.Context())
				next := *r.URL
				next.Scheme = target.Scheme
				next.Host = target.Host
				forward.URL = &next
				resp, err := server.Client().Do(forward)
				if resp != nil {
					// Restore the original logical request
					// on the fixture response so final-URL
					// validation exercises the settings
					// target instead of rejecting the local
					// server's address.
					resp.Request = r
					resp.Body = &fetchBody{resp.Body, &closed}
				}
				return resp, err
			}})
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, tt.failure, result.Failure)
			require.Equal(t, tt.unauthorized, result.Unauthorized)
			require.EqualValues(t, 1, calls.Load())
			require.Equal(t, calls.Load(), closed.Load())
			require.NotContains(t, fmt.Sprintf("%v %#v", input, input), "cookie-secret")
			if tt.name == "success" {
				require.Equal(t, "max", result.Data.Plan)
			}
			if tt.name == "redirect" {
				require.Equal(t, 30*time.Second, result.RetryAfter)
			}
		})
	}
}
