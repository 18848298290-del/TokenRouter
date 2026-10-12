package httpclient

import (
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
)

// UpstreamTransport executes requests with proxy, isolation, and TLS settings.
// Callers close response bodies to release pool reservations.
type UpstreamTransport interface {
	// Do executes an HTTP request without a TLS fingerprint.
	Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error)

	// DoWithTLS executes a request with the supplied fingerprint profile.
	// A nil profile uses the same behavior as Do.
	DoWithTLS(req *http.Request, proxyURL string, providerID int64, providerConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error)
}
