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

// OpenAIRequestOptions holds startup target and passthrough settings.
type OpenAIRequestOptions struct {
	ForceCLI            bool
	AllowTimeoutHeaders bool
	URLPolicy           egress.OperatorURLPolicy
}

// OpenAIRequests builds protocol requests and transport parameters for an already selected provider.
type OpenAIRequests struct {
	Options      OpenAIRequestOptions
	Providers    gatewayadapter.ExecutionProviderStore
	Identity     *gatewayadapter.ExecutionAgentIdentity
	Credentials  *provider.OpenAIExecutionCredentials
	Transport    httpclient.UpstreamTransport
	Failure      *UpstreamTransportFailure
	Turns        *CodexTurnStateHeaders
	Profiles     *egressprovider.TLSProfiles
	Routers      *egress.TLSFingerprintRouterService
	Readers      *gatewayadapter.RuntimeReaders
	Detector     provider.ClientRestrictionDetector
	ClientPolicy *provideradapter.OpenAIProbePolicy
	GrokRoutes   gatewayadapter.GrokRoutes
}

// ValidateBaseURL validates the URL through egress and prefixes errors with invalid base_url.
func (s *OpenAIRequests) ValidateBaseURL(raw string) (string, error) {
	normalized, err := s.ValidateURL(raw)
	if err != nil {
		return "", fmt.Errorf("invalid base_url: %w", err)
	}
	return normalized, nil
}

func (s *OpenAIRequests) AllowTimeoutHeaders() bool { return s != nil && s.Options.AllowTimeoutHeaders }

// ValidateURL requires HTTPS when the request service is nil.
func (s *OpenAIRequests) ValidateURL(raw string) (string, error) {
	if s == nil {
		return egress.ValidateURLFormat(raw, false)
	}
	return s.Options.URLPolicy.Validate(raw)
}
