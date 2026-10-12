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

var (
	canListenOnce sync.Once
	canListen     bool
	canListenErr  error
)

type ClaudeUsageServiceSuite struct {
	suite.Suite
	srv     *httptest.Server
	fetcher *UsageClient
}

// usageRequestCapture holds authorization headers for assertions in the test goroutine.
type usageRequestCapture struct {
	authorization string
	anthropicBeta string
}

func (s *ClaudeUsageServiceSuite) TearDownTest() {
	if s.srv != nil {
		s.srv.Close()
		s.srv = nil
	}
}

func (s *ClaudeUsageServiceSuite) TestFetchUsage_Success() {
	var captured usageRequestCapture

	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.authorization = r.Header.Get("Authorization")
		captured.anthropicBeta = r.Header.Get("anthropic-beta")

		w.Header().Set("Content-Type", "application/json")
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

func (s *ClaudeUsageServiceSuite) TestFetchUsage_NonOK() {
	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
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

func (s *ClaudeUsageServiceSuite) TestFetchUsage_BadJSON() {
	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
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

func (s *ClaudeUsageServiceSuite) TestFetchUsage_ContextCancel() {
	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Wait for cancellation to simulate a slow upstream response.
		<-r.Context().Done()
	}))

	s.fetcher = &UsageClient{
		UsageURL:          s.srv.URL,
		AllowPrivateHosts: true,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := s.fetcher.FetchUsageWithOptions(ctx, &UsageFetchOptions{AccessToken: "at", ProxyURL: ""})
	require.Error(s.T(), err, "expected error for cancelled context")
}

func (s *ClaudeUsageServiceSuite) TestFetchUsage_InvalidProxyReturnsError() {
	s.fetcher = &UsageClient{
		UsageURL:          "http://example.com",
		AllowPrivateHosts: true,
	}

	_, err := s.fetcher.FetchUsageWithOptions(context.Background(), &UsageFetchOptions{AccessToken: "at", ProxyURL: "://bad-proxy-url"})
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "create http client failed")
}

func TestClaudeUsageServiceSuite(t *testing.T) {
	suite.Run(t, new(ClaudeUsageServiceSuite))
}

func localListenerAvailable() bool {
	canListenOnce.Do(func() {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			canListenErr = err
			canListen = false
			return
		}
		_ = ln.Close()
		canListen = true
	})
	return canListen
}

func newLocalTestServer(tb testing.TB, handler http.Handler) *httptest.Server {
	tb.Helper()
	if !localListenerAvailable() {
		tb.Skipf("local listeners are not permitted in this environment: %v", canListenErr)
	}
	return httptest.NewServer(handler)
}
