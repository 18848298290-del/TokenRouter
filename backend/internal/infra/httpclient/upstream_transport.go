package httpclient

import (
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
)

// Gateway transport callers supply the selected proxy, provider identity, and
// optional TLS profile for each request. Implementations reserve their
// connection-pool entry through the response lifetime, so a successful caller
// closes the body after reading or abandoning it.

// UpstreamTransport executes requests with proxy, isolation, and TLS settings.
// Callers close response bodies to release pool reservations.
type UpstreamTransport interface {
	// Do executes req with the selected proxy and provider pool settings.
	// Errors end the request attempt; after success, callers close the body to
	// release any pool reservation held by the implementation.
	Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error)

	// DoWithTLS executes req with the selected fingerprint and provider pool
	// settings. A nil profile selects the ordinary Do path; callers close a
	// successful response body to release the implementation's pool reservation.
	DoWithTLS(req *http.Request, proxyURL string, providerID int64, providerConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error)
}
