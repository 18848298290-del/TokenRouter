package provider

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// Qoder's request doer carries loaded proxy and fingerprint choices to the
// shared transport. These fixtures exercise qoder_transport.go and record
// serial request parameters, while endpoint-specific user-info responses also
// support probe cases that reuse the same transport recorder.

// qoderHTTPUpstreamRecorder retains serial request arguments and
// endpoint-specific response settings.
type qoderHTTPUpstreamRecorder struct {
	body string // ordinary response fixture text
	// userInfoBody is used for GET user-info requests; an empty value supplies
	// a default identity JSON response for probe fixtures.
	userInfoBody string
	// userInfoStatusCode overrides HTTP 200 only for GET user-info requests when
	// nonzero. Other requests keep the successful response fixture status.
	userInfoStatusCode  int
	proxyURL            string // last request proxy URL
	providerID          int64  // last request provider ID
	providerConcurrency int    // last request concurrency allowance
	profileSet          bool   // last request profile presence
	// requests retains caller request pointers for subsequent fixture inspection.
	// Their bodies can be consumed by a test after transport returns.
	requests []*http.Request
}

// TestQoderGatewayRequestDoerUsesHTTPUpstreamProxyAndTLS binds a provider with a
// loaded proxy and enabled TLS fingerprinting. It checks the returned response,
// proxy URL, provider ID and selected profile reported to the shared transport.
func TestQoderGatewayRequestDoerUsesHTTPUpstreamProxyAndTLS(t *testing.T) {
	proxyID := int64(11)
	provider := &providercore.Record{
		ID:          66,
		Platform:    capability.PlatformQoder,
		Type:        capability.ProviderTypeCosy,
		Concurrency: 3,
		ProxyID:     &proxyID,
		Proxy: &egress.Proxy{
			ID:       proxyID,
			Protocol: "http",
			Host:     "proxy.example.com",
			Port:     8080,
		},
		Extra: map[string]any{
			"enable_tls_fingerprint": true,
		},
	}
	upstream := &qoderHTTPUpstreamRecorder{
		body: "data: {\"body\":\"[DONE]\"}\n\n",
	}
	doer := QoderRequestDoer(provider, upstream, &egressadapter.TLSProfiles{})
	require.NotNil(t, doer)
	req := httptest.NewRequest(http.MethodPost, "https://api1.qoder.sh/test", strings.NewReader("{}"))

	resp, err := doer(req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "http://proxy.example.com:8080", upstream.proxyURL)
	require.Equal(t, int64(66), upstream.providerID)
	require.True(t, upstream.profileSet)
}

// Do records a request through the fixture's TLS-aware response path.
// It passes a nil fingerprint profile and returns the same caller-owned body,
// allowing tests to use this recorder through either transport interface.
func (u *qoderHTTPUpstreamRecorder) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, providerID, providerConcurrency, nil)
}

// DoWithTLS retains request arguments and returns the endpoint's fixture response.
// GET user-info requests receive configured user-info text or a default identity;
// their nonzero status override replaces HTTP 200. The caller owns the response
// body, and recorder fields support serial test use.
func (u *qoderHTTPUpstreamRecorder) DoWithTLS(req *http.Request, proxyURL string, providerID int64, providerConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.proxyURL = proxyURL
	u.providerID = providerID
	u.providerConcurrency = providerConcurrency
	u.profileSet = profile != nil
	u.requests = append(u.requests, req)
	body := u.body
	if req.Method == http.MethodGet && strings.Contains(req.URL.Path, qoder.UserInfoPath) {
		body = u.userInfoBody
		if body == "" {
			body = `{"id":"user-1","name":"Qoder User"}`
		}
	}
	statusCode := http.StatusOK
	if req.Method == http.MethodGet && strings.Contains(req.URL.Path, qoder.UserInfoPath) && u.userInfoStatusCode != 0 {
		statusCode = u.userInfoStatusCode
	}
	return &http.Response{
		StatusCode: statusCode,
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}
