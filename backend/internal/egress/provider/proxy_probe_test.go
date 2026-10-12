package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// ProxyProbeServiceSuite owns serial probe fixtures and loopback proxy servers
// for endpoint and parser scenarios.
type ProxyProbeServiceSuite struct {
	suite.Suite                 // assertion and suite lifecycle support
	ctx         context.Context // fixture request context value
	// proxySrv is created by setupProxyServer when a case needs HTTP responses.
	// TearDownTest closes the server after the case and clears the retained
	// pointer.
	proxySrv *httptest.Server   // current loopback proxy server
	prober   *proxyProbeService // configured probe service fixture
}

// SetupTest creates a background request context and a fresh prober permitting
// private fixture hosts. Individual tests configure their loopback proxy server
// or exercise direct parser inputs after this setup.
func (s *ProxyProbeServiceSuite) SetupTest() {
	s.ctx = context.Background()
	s.prober = &proxyProbeService{
		allowPrivateHosts: true,
	}
}

// TearDownTest closes any retained loopback proxy server and clears its
// pointer. The suite runs cases serially so the next setup cannot reuse a
// server still owned by the completed case.
func (s *ProxyProbeServiceSuite) TearDownTest() {
	if s.proxySrv != nil {
		s.proxySrv.Close()
		s.proxySrv = nil
	}
}

// setupProxyServer starts a loopback HTTP fixture using handler and retains it
// for teardown. Cases choose response behavior for requested target URLs;
// teardown owns server closure after the case finishes.
func (s *ProxyProbeServiceSuite) setupProxyServer(handler http.HandlerFunc) {
	s.proxySrv = httptest.NewServer(handler)
}

// TestProbeProxy_InvalidProxyURL submits a malformed proxy URL before any
// network attempt. It checks that client construction fails and the returned
// error identifies proxy client creation.
func (s *ProxyProbeServiceSuite) TestProbeProxy_InvalidProxyURL() {
	_, _, err := s.prober.ProbeProxy(s.ctx, "://bad")
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "failed to create proxy client")
}

// TestProbeProxy_UnsupportedProxyScheme submits an FTP proxy URL to the prober.
// It checks the rejected scheme reaches the caller as a proxy-client
// construction error.
func (s *ProxyProbeServiceSuite) TestProbeProxy_UnsupportedProxyScheme() {
	_, _, err := s.prober.ProbeProxy(s.ctx, "ftp://127.0.0.1:1")
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "failed to create proxy client")
}

// TestProbeProxy_Success_IPAPI serves exit metadata for the first built-in
// target through a loopback proxy. It checks nonnegative header latency and
// each returned IP, city, region, country, and country-code field.
func (s *ProxyProbeServiceSuite) TestProbeProxy_Success_IPAPI() {
	s.setupProxyServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve a successful response only for the first probe target.
		if strings.Contains(r.RequestURI, "ip-api.com") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"success","query":"1.2.3.4","city":"c","regionName":"r","country":"cc","countryCode":"CC"}`)
			return
		}
		// Reject other targets so success depends on parsing ip-api.
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	info, latencyMs, err := s.prober.ProbeProxy(s.ctx, s.proxySrv.URL)
	require.NoError(s.T(), err, "ProbeProxy")
	require.GreaterOrEqual(s.T(), latencyMs, int64(0), "unexpected latency")
	require.Equal(s.T(), "1.2.3.4", info.IP)
	require.Equal(s.T(), "c", info.City)
	require.Equal(s.T(), "r", info.Region)
	require.Equal(s.T(), "cc", info.Country)
	require.Equal(s.T(), "CC", info.CountryCode)
}

// TestProbeProxy_Success_IPifyFallback rejects the first built-in target and
// serves valid ipify JSON for the second. It checks the later target supplies
// the expected exit IP and nonnegative latency.
func (s *ProxyProbeServiceSuite) TestProbeProxy_Success_IPifyFallback() {
	s.setupProxyServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return an error from the first endpoint so ProbeProxy must continue
		// to the second built-in parser before this fixture can return an exit IP.
		if strings.Contains(r.RequestURI, "ip-api.com") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// The second target succeeds.
		if strings.Contains(r.RequestURI, "api64.ipify.org") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ip": "5.6.7.8"}`)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	info, latencyMs, err := s.prober.ProbeProxy(s.ctx, s.proxySrv.URL)
	require.NoError(s.T(), err, "ProbeProxy should fallback to ipify")
	require.GreaterOrEqual(s.T(), latencyMs, int64(0), "unexpected latency")
	require.Equal(s.T(), "5.6.7.8", info.IP)
}

// TestProbeProxy_AllFailed returns an unavailable status for every requested
// target through the fixture proxy. It checks ProbeProxy reports the aggregate
// all-targets-failed error to the caller.
func (s *ProxyProbeServiceSuite) TestProbeProxy_AllFailed() {
	s.setupProxyServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	_, _, err := s.prober.ProbeProxy(s.ctx, s.proxySrv.URL)
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "all probe URLs failed")
}

// TestProbeProxy_InvalidJSON returns malformed JSON for both built-in probe
// targets. It checks parser failures continue through the target sequence and
// produce the aggregate failure error.
func (s *ProxyProbeServiceSuite) TestProbeProxy_InvalidJSON() {
	s.setupProxyServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.RequestURI, "ip-api.com") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "not-json")
			return
		}
		// Return invalid JSON from the fallback target too.
		if strings.Contains(r.RequestURI, "api64.ipify.org") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "not-json")
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	_, _, err := s.prober.ProbeProxy(s.ctx, s.proxySrv.URL)
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "all probe URLs failed")
}

// TestProbeProxy_ProxyServerClosed closes its fixture proxy before invoking
// ProbeProxy. It checks that the unreachable proxy produces an error instead of
// a successful exit-information result.
func (s *ProxyProbeServiceSuite) TestProbeProxy_ProxyServerClosed() {
	s.setupProxyServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	// Close the fixture before probing so the cached or newly built client
	// observes a network failure rather than a response supplied by this handler.
	s.proxySrv.Close()

	_, _, err := s.prober.ProbeProxy(s.ctx, s.proxySrv.URL)
	require.Error(s.T(), err, "expected error when proxy server is closed")
}

// TestParseIPAPI_Success supplies a successful payload with complete exit
// metadata and fixed latency. It checks all mapped location values and the
// unchanged latency returned by the parser.
func (s *ProxyProbeServiceSuite) TestParseIPAPI_Success() {
	body := []byte(`{"status":"success","query":"1.2.3.4","city":"Beijing","regionName":"Beijing","country":"China","countryCode":"CN"}`)
	info, latencyMs, err := s.prober.parseIPAPI(body, 100)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(100), latencyMs)
	require.Equal(s.T(), "1.2.3.4", info.IP)
	require.Equal(s.T(), "Beijing", info.City)
	require.Equal(s.T(), "Beijing", info.Region)
	require.Equal(s.T(), "China", info.Country)
	require.Equal(s.T(), "CN", info.CountryCode)
}

// TestParseIPAPI_Failure supplies a remote failure payload with a rate-limit
// message. It checks parsing rejects the non-success status and includes that
// remote failure reason in the error.
func (s *ProxyProbeServiceSuite) TestParseIPAPI_Failure() {
	body := []byte(`{"status":"fail","message":"rate limited"}`)
	_, _, err := s.prober.parseIPAPI(body, 100)
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "rate limited")
}

// TestParseIPify_Success supplies an IPv6 exit string with a fixed latency. It
// checks the parser returns that exact text and retains the provided latency
// value.
func (s *ProxyProbeServiceSuite) TestParseIPify_Success() {
	body := []byte(`{"ip": "2001:db8::1"}`)
	info, latencyMs, err := s.prober.parseIPify(body, 50)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(50), latencyMs)
	require.Equal(s.T(), "2001:db8::1", info.IP)
}

// TestParseIPify_NoIP supplies valid JSON with an empty IP value. It checks the
// parser rejects the result and reports that the expected exit address is
// absent.
func (s *ProxyProbeServiceSuite) TestParseIPify_NoIP() {
	body := []byte(`{"ip": ""}`)
	_, _, err := s.prober.parseIPify(body, 50)
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "no IP found")
}

// TestParseChatGPTTrace_Success supplies newline-delimited trace metadata with
// ip and loc keys. It checks the returned exit IP, country code, and unchanged
// latency derived from those sampled fields.
func (s *ProxyProbeServiceSuite) TestParseChatGPTTrace_Success() {
	body := []byte("fl=abc\nh=chatgpt.com\nip=203.0.113.5\nts=1700000000\nloc=US\ntz=UTC\n")
	info, latencyMs, err := s.prober.parseChatGPTTrace(body, 320)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(320), latencyMs)
	require.Equal(s.T(), "203.0.113.5", info.IP)
	require.Equal(s.T(), "US", info.CountryCode)
}

// TestParseChatGPTTrace_NoIP supplies trace metadata containing location but no
// ip key. It checks the parser returns the missing-address error instead of
// accepting location-only information.
func (s *ProxyProbeServiceSuite) TestParseChatGPTTrace_NoIP() {
	body := []byte("fl=abc\nh=chatgpt.com\nloc=US\n")
	_, _, err := s.prober.parseChatGPTTrace(body, 100)
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "chatgpt-trace: no ip= found")
}

// TestProxyProbeServiceSuite runs the serial proxy reachability and
// response-parser scenarios. The suite builds and tears down loopback proxy
// fixtures for network cases and checks direct parser samples separately.
func TestProxyProbeServiceSuite(t *testing.T) {
	suite.Run(t, new(ProxyProbeServiceSuite))
}
