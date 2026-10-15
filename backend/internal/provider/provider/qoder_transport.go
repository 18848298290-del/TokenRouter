package provider

import (
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// qoder_transport.go binds Qoder token and inference requests to the provider
// proxy and TLS profile. The request doer resolves those choices once when
// bound, then forwards each request through the shared transport with the
// provider's current ID and concurrency value.

// QoderTransport sends a request using the supplied proxy and TLS fingerprint
// profile.
type QoderTransport interface {
	// DoWithTLS sends the prepared request through the selected provider transport.
	// The caller supplies proxy and profile choices and consumes or closes the
	// returned response body; transport failures return an error.
	DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error)
}

// QoderRequestDoer captures proxy and TLS selection for a provider's upstream
// requests.
// Nil transport or record returns nil, allowing upstream clients to use their
// own default doer. Proxy and profile are captured at binding; provider ID and
// concurrency are read from the borrowed record when the callback runs.
func QoderRequestDoer(value *provider.Record, transport QoderTransport, profiles *egressprovider.TLSProfiles) qoder.RequestDoer {
	if transport == nil || value == nil {
		return nil
	}
	proxyURL := ""
	if value.ProxyID != nil && value.Proxy != nil {
		proxyURL = value.Proxy.URL()
	}
	var profile *tlsfingerprint.Profile
	if profiles != nil {
		profile = profiles.ResolveRequestTLS(egress.TLSSelection{
			Enabled:         value.IsTLSFingerprintEnabled(),
			DirectProfileID: value.GetTLSFingerprintProfileID(),
		})
	}
	return func(req *http.Request) (*http.Response, error) {
		return transport.DoWithTLS(req, proxyURL, value.ID, value.Concurrency, profile)
	}
}
