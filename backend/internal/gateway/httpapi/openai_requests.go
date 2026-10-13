package httpapi

import (
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// OpenAI request construction applies startup URL and header permissions to an
// already selected provider. The same request service supplies protocol
// builders with credential, transport, session, and client-policy dependencies
// used by HTTP execution and auxiliary requests.

// OpenAIRequestOptions contains startup permissions for outgoing OpenAI-
// compatible requests.
type OpenAIRequestOptions struct {
	ForceCLI            bool                     // configured Codex CLI emulation
	AllowTimeoutHeaders bool                     // passthrough client-timeout permission
	URLPolicy           egress.OperatorURLPolicy // operator target-validation rules
}

// OpenAIRequests binds request construction to a selected provider and shared
// app services. Builders use these references synchronously while preparing
// each outgoing attempt.
type OpenAIRequests struct {
	// Providers, Identity, Credentials, Transport, Failure, Turns,
	// Profiles, Routers, Readers, Detector, ClientPolicy, and GrokRoutes
	// reference services supplied by app assembly. Request preparation
	// consults them for the current selected provider; the app controls
	// their wider lifetimes.
	Options      OpenAIRequestOptions                   // startup outgoing-request permissions
	Providers    gatewayadapter.ExecutionProviderStore  // execution-provider record reader
	Identity     *gatewayadapter.ExecutionAgentIdentity // upstream authentication-header builder
	Credentials  *provider.OpenAIExecutionCredentials   // selected execution credential resolver
	Transport    httpclient.UpstreamTransport           // shared upstream exchange transport
	Failure      *UpstreamTransportFailure              // exchange-error client output adapter
	Turns        *CodexTurnStateHeaders                 // response-turn provenance header guard
	Profiles     *egressprovider.TLSProfiles            // outgoing TLS profile reader
	Routers      *egress.TLSFingerprintRouterService    // outgoing TLS fingerprint-rule matcher
	Readers      *gatewayadapter.RuntimeReaders         // dynamic gateway policy readers
	Detector     provider.ClientRestrictionDetector     // configured caller-identity restriction detector
	ClientPolicy *provideradapter.OpenAIProbePolicy     // TLS-router client-policy input builder
	GrokRoutes   gatewayadapter.GrokRoutes              // Grok-specific proxy resolution functions
}

// ValidateBaseURL validates and normalizes an operator-configured upstream base
// URL. It uses ValidateURL's policy and wraps failures with invalid base_url so
// callers can distinguish configuration errors during target construction.
func (s *OpenAIRequests) ValidateBaseURL(raw string) (string, error) {
	normalized, err := s.ValidateURL(raw)
	if err != nil {
		return "", fmt.Errorf("invalid base_url: %w", err)
	}
	return normalized, nil
}

// AllowTimeoutHeaders reports whether passthrough may forward client timeout
// headers. A nil service disables this permission. Request builders separately
// check whether the header is one of the recognized timeout names.
func (s *OpenAIRequests) AllowTimeoutHeaders() bool { return s != nil && s.Options.AllowTimeoutHeaders }

// ValidateURL checks an outgoing URL using the startup operator policy. A nil
// service selects HTTPS-only format validation; configured services select
// allowlist or format validation through their URLPolicy.
func (s *OpenAIRequests) ValidateURL(raw string) (string, error) {
	if s == nil {
		return egress.ValidateURLFormat(raw, false)
	}
	return s.Options.URLPolicy.Validate(raw)
}
