package anthropic

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// The listener probe caches one environment-level availability result through
// sync.Once so all local HTTP server tests share the same skip decision.
var (
	canListenOnce sync.Once
	canListen     bool
	canListenErr  error
)

// ClaudeUsageServiceSuite exercises usage responses through local HTTP test
// servers.
type ClaudeUsageServiceSuite struct {
	suite.Suite // assertion and subtest support
	// srv owns the current local HTTP fixture; TearDownTest closes it after each
	// suite test and clears this pointer before reuse.
	srv     *httptest.Server // current local test server
	fetcher *UsageClient     // configured usage HTTP client
}

// usageRequestCapture holds credential Headers observed by the usage test
// server.
type usageRequestCapture struct {
	authorization string // observed bearer Header
	anthropicBeta string // observed OAuth beta
}

// TearDownTest closes the current local server after a suite test. A missing
// server is skipped, and clearing the pointer prevents later teardown from
// closing the same fixture again.
func (s *ClaudeUsageServiceSuite) TearDownTest() {
	if s.srv != nil {
		s.srv.Close()
		s.srv = nil
	}
}

// TestFetchUsage_Success serves three usage windows from a local HTTP fixture.
// Their utilization values decode correctly, and captured Headers contain the
// supplied bearer token and OAuth beta.
func (s *ClaudeUsageServiceSuite) TestFetchUsage_Success() {
	var captured usageRequestCapture

	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.authorization = r.Header.Get("Authorization")
		captured.anthropicBeta = r.Header.Get("anthropic-beta")

		w.Header().Set("Content-Type", "application/json")
		// Client-side decoding and Header assertions evaluate this fixture response.
		// Write failures are discarded here and can reach the fetching client as
		// incomplete response bytes.
		_, _ = io.WriteString(w, `{
  "five_hour": {"utilization": 12.5, "resets_at": "2025-01-01T00:00:00Z"},
  "seven_day": {"utilization": 34.0, "resets_at": "2025-01-08T00:00:00Z"},
  "seven_day_sonnet": {"utilization": 56.0, "resets_at": "2025-01-08T00:00:00Z"}
}`)
	}))

	s.fetcher = &UsageClient{
		UsageURL:          s.srv.URL,
		AllowPrivateHosts: true,
	}

	resp, err := s.fetcher.FetchUsageWithOptions(context.Background(), &UsageFetchOptions{AccessToken: "at", ProxyURL: ""})
	require.NoError(s.T(), err, "FetchUsage")
	require.Equal(s.T(), 12.5, resp.FiveHour.Utilization, "FiveHour utilization mismatch")
	require.Equal(s.T(), 34.0, resp.SevenDay.Utilization, "SevenDay utilization mismatch")
	require.Equal(s.T(), 56.0, resp.SevenDaySonnet.Utilization, "SevenDaySonnet utilization mismatch")

	require.Equal(s.T(), "Bearer at", captured.authorization, "Authorization header mismatch")
	require.Equal(s.T(), "oauth-2025-04-20", captured.anthropicBeta, "anthropic-beta header mismatch")
}

// TestFetchUsage_NonOK serves an unauthorized response with a short body from a
// local fixture. The returned error includes both its 401 status and readable
// body text.
func (s *ClaudeUsageServiceSuite) TestFetchUsage_NonOK() {
	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		// The fixture sends status before its body, so a discarded write failure can
		// leave the client with the unauthorized status and whatever body bytes
		// reached it.
		_, _ = io.WriteString(w, "nope")
	}))

	s.fetcher = &UsageClient{
		UsageURL:          s.srv.URL,
		AllowPrivateHosts: true,
	}

	_, err := s.fetcher.FetchUsageWithOptions(context.Background(), &UsageFetchOptions{AccessToken: "at", ProxyURL: ""})
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "status 401")
	require.ErrorContains(s.T(), err, "nope")
}

// TestFetchUsage_BadJSON serves a successful status with malformed JSON from a
// local fixture. Fetching returns a decode-response error, so an HTTP success
// cannot supply an apparently valid usage result.
func (s *ClaudeUsageServiceSuite) TestFetchUsage_BadJSON() {
	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The test asserts the client's decoder error; this handler discards write
		// errors, leaving client-side response inspection to expose an incomplete
		// fixture body as well.
		_, _ = io.WriteString(w, "not-json")
	}))

	s.fetcher = &UsageClient{
		UsageURL:          s.srv.URL,
		AllowPrivateHosts: true,
	}

	_, err := s.fetcher.FetchUsageWithOptions(context.Background(), &UsageFetchOptions{AccessToken: "at", ProxyURL: ""})
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "decode response failed")
}

// TestFetchUsage_ContextCancel calls fetching with a context cancelled before
// dispatch and a handler that could wait for cancellation. The assertion
// requires an error, without depending on that handler being entered.
func (s *ClaudeUsageServiceSuite) TestFetchUsage_ContextCancel() {
	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If dispatched, keep this response pending until its request context ends,
		// giving the client a slow upstream whose lifetime follows cancellation.
		<-r.Context().Done()
	}))

	s.fetcher = &UsageClient{
		UsageURL:          s.srv.URL,
		AllowPrivateHosts: true,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the fetch begins

	_, err := s.fetcher.FetchUsageWithOptions(ctx, &UsageFetchOptions{AccessToken: "at", ProxyURL: ""})
	require.Error(s.T(), err, "expected error for cancelled context")
}

// TestFetchUsage_InvalidProxyReturnsError supplies a malformed proxy URL to an
// ordinary usage query. It returns a client-creation error before a successful
// response can be decoded for that request.
func (s *ClaudeUsageServiceSuite) TestFetchUsage_InvalidProxyReturnsError() {
	s.fetcher = &UsageClient{
		UsageURL:          "http://example.com",
		AllowPrivateHosts: true,
	}

	_, err := s.fetcher.FetchUsageWithOptions(context.Background(), &UsageFetchOptions{AccessToken: "at", ProxyURL: "://bad-proxy-url"})
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "create http client failed")
}

// TestClaudeUsageServiceSuite runs local HTTP usage fixtures and
// transport-failure cases. Suite assertions cover decoded windows and Headers,
// non-200 bodies, malformed JSON, pre-cancelled context, and invalid proxy
// configuration.
func TestClaudeUsageServiceSuite(t *testing.T) {
	suite.Run(t, new(ClaudeUsageServiceSuite))
}

// localListenerAvailable caches whether this test process can open a loopback
// TCP listener. sync.Once publishes one result, a failed probe stores its
// error, and a successful probe closes its temporary listener.
func localListenerAvailable() bool {
	canListenOnce.Do(func() {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			// Save the environment error alongside a false probe result so server
			// helpers can skip consistently and report the reason for unavailable local
			// listeners.
			canListenErr = err
			canListen = false
			return
		}
		// The availability probe opens a listener only to test permission; close it
		// immediately so subsequent server fixtures can allocate their own
		// lifetime-bound listener.
		_ = ln.Close()
		canListen = true
	})
	return canListen
}

// newLocalTestServer opens an HTTP fixture after checking the cached listener
// probe. Unavailable environments skip through testing.TB with the saved error;
// callers close the returned server after the test.
func newLocalTestServer(tb testing.TB, handler http.Handler) *httptest.Server {
	tb.Helper()
	if !localListenerAvailable() {
		tb.Skipf("local listeners are not permitted in this environment: %v", canListenErr)
	}
	return httptest.NewServer(handler)
}
