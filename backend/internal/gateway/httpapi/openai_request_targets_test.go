package httpapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
)

// Raw Chat finalizes headers immediately before its transport sends the
// request. A recording transport captures that point so the fixture can compare
// the caller's OpenCode conversation ID with a conflicting provider override on
// the trusted origin.

// openCodeSessionHTTPUpstream records the outgoing Chat request and returns
// empty JSON.
type openCodeSessionHTTPUpstream struct {
	// request retains the supplied request pointer for assertions after
	// SendChat. The test reads its final headers once the synchronous
	// exchange completes.
	request *http.Request
}

// Do records the outgoing request and supplies a successful JSON response. The
// fixture retains the request pointer for header assertions; callers own the
// returned synthetic response body and close it after inspection.
func (u *openCodeSessionHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.request = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{}`)),
	}, nil
}

// DoWithTLS delegates TLS-aware fixture exchanges to the same request recording
// path.
func (u *openCodeSessionHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, providerID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, providerID, concurrency)
}

// TestOpenCodeSessionForwardedByRawChatCompletionsAfterProviderOverride sends
// Chat with conflicting conversation metadata. It checks the recorded transport
// request contains one caller-supplied OpenCode session header after provider
// overrides.
func TestOpenCodeSessionForwardedByRawChatCompletionsAfterProviderOverride(t *testing.T) {
	upstream := &openCodeSessionHTTPUpstream{}
	svc := openCodeSessionTestService()
	svc.Requests.Transport = upstream
	if svc.Grok != nil {
		svc.Grok.Transport = svc.Requests.Transport
	}
	provider := openCodeSessionTestProvider("https://opencode.ai/zen/v1")
	c := newOpenCodeSessionTestContext(t, "conversation-789")

	resp, err := svc.Requests.SendChat(
		context.Background(), c, provider,
		"https://opencode.ai/zen/v1/chat/completions", []byte(`{"model":"gpt-5"}`),
		false, "token", "", "",
	)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.NotNil(t, upstream.request)
	requireSingleOpenCodeSessionHeader(t, upstream.request.Header, "conversation-789")
}
