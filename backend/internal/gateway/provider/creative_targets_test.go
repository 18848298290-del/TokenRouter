package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// creativeDownloadRequests validates download URLs and rejects generation-only
// request operations.
type creativeDownloadRequests struct{}

// creativeDownloadTransport delegates image downloads to a caller-supplied
// assertion callback.
type creativeDownloadTransport struct {
	// asserted download transport callback
	do func(*http.Request, string, int64, int) (*http.Response, error)
}

// ValidateBaseURL validates a fixture URL using egress URL-format rules with
// HTTP disabled. It returns the validated URL or the validation error to the
// image download path.
func (creativeDownloadRequests) ValidateBaseURL(raw string) (string, error) {
	return egress.ValidateURLFormat(raw, false)
}

// ImagesURL rejects generation URL lookup by panicking when invoked. Download
// tests supply this fixture to require the public image fetch path, so
// generation request preparation cannot silently run during an assertion.
func (creativeDownloadRequests) ImagesURL(*ExecutionProvider, string) (string, error) {
	panic("unexpected generation URL lookup")
}

// TLSProfile rejects a generation TLS-profile lookup by panicking when
// invoked. The download fixture expects ordinary public image transport, so
// this failure exposes an accidental transition to the generation request
// path.
func (creativeDownloadRequests) TLSProfile(*ExecutionProvider, ...egress.TLSFingerprintRouterMatchResult) *tlsfingerprint.Profile {
	panic("unexpected generation TLS lookup")
}

// Do passes the request and provider isolation parameters to the configured
// callback.
func (t creativeDownloadTransport) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	return t.do(req, proxy, id, concurrency)
}

// DoWithTLS rejects generation transport by panicking on any invocation.
// Image-download tests expect Do to receive their assertions, so the panic
// identifies an unexpected path before transport parameters escape
// observation.
func (creativeDownloadTransport) DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error) {
	panic("unexpected generation transport")
}

// TestCreativeImageDownloadBinding fetches a signed public image URL through a
// provider carrying credentials and a proxy. Assertions cover the exact URL,
// proxy and isolation parameters, credential-free headers, public-host
// marking, and returned Base64 bytes.
func TestCreativeImageDownloadBinding(t *testing.T) {
	proxyID := int64(3)
	value := &ExecutionProvider{Record: provider.Record{
		ID: 7, Platform: "openai", Type: "apikey", Concurrency: 1,
		Credentials: map[string]any{"api_key": "secret"},
		ProxyID:     &proxyID,
		Proxy:       &egress.Proxy{Protocol: "http", Host: "proxy.example.com", Port: 8080},
	}}
	image := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	rawURL := "https://cdn.example.com/a%2Fb/?sig=abc%2B123"
	calls := 0
	targets := &CreativeTargets{
		Requests: creativeDownloadRequests{},
		Transport: creativeDownloadTransport{do: func(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
			calls++
			require.Equal(t, http.MethodGet, req.Method)
			require.Equal(t, rawURL, req.URL.String())
			require.Equal(t, value.Record.Proxy.URL(), proxy)
			require.Equal(t, int64(7), id)
			require.Equal(t, 1, concurrency)
			require.True(t, upstream.HTTPUpstreamPublicHostsOnly(req.Context()))
			require.Empty(t, req.Header.Get("Authorization"))
			require.Empty(t, req.Header.Get("Cookie"))
			require.Equal(t, http.Header{"Accept": []string{"image/*,*/*;q=0.8"}}, req.Header)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(image))}, nil
		}},
	}
	encoded, err := targets.ForProvider(value).OpenAI.FetchImage(context.Background(), rawURL)
	require.NoError(t, err)
	require.Equal(t, base64.StdEncoding.EncodeToString(image), encoded)
	require.Equal(t, 1, calls)
}

// TestCreativeImageDownloadRejectsUnsafeResults supplies private-address and
// file URLs, then a public URL returning HTML. Unsafe URLs fail before
// transport, and the fetched HTML is rejected as a disallowed image format.
func TestCreativeImageDownloadRejectsUnsafeResults(t *testing.T) {
	value := &ExecutionProvider{Record: provider.Record{ID: 7, Platform: "openai", Type: "apikey"}}
	calls := 0
	targets := &CreativeTargets{
		Requests: creativeDownloadRequests{},
		Transport: creativeDownloadTransport{do: func(*http.Request, string, int64, int) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("<html>not an image</html>"))}, nil
		}},
	}
	fetch := targets.ForProvider(value).OpenAI.FetchImage
	for _, raw := range []string{"https://127.0.0.1/image.png", "https://10.0.0.1/image.png", "https://[::1]/image.png", "file:///etc/passwd"} {
		_, err := fetch(context.Background(), raw)
		require.Error(t, err)
	}
	require.Zero(t, calls)
	_, err := fetch(context.Background(), "https://cdn.example.com/image.png")
	require.ErrorContains(t, err, "not an allowed image format")
	require.Equal(t, 1, calls)
}
