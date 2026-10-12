package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// @project-doc docs/interfaces/grok_upstream.md#grok_account_contract
// GrokExecutor binds single-attempt platform dependencies; the request owner handles failover and completion.
type GrokExecutor struct {
	Attributes  *routing.ModelAttributeService
	FastPolicy  *gatewayadapter.ExecutionFastPolicy
	Credentials *gatewayadapter.RequestCredentials
	Transport   httpclient.UpstreamTransport
	Failure     *UpstreamTransportFailure
	Output      *OpenAIResponseOutput
	Health      *provideradapter.GrokHealth
	Routes      gatewayadapter.GrokRoutes
	TLS         *egressprovider.TLSProfiles
	Dialer      openai.WSClientDialer
	Enter       func() (func(), error)
}

func (s *GrokExecutor) TLSProfile(target *gatewayadapter.ExecutionProvider, matches ...egress.TLSFingerprintRouterMatchResult) *tlsfingerprint.Profile {
	if s == nil || s.TLS == nil {
		return nil
	}
	return s.TLS.ResolveRequestTLS(gatewayadapter.ExecutionTLSSelection(target, matches))
}

// BuildResponsesRequest resolves the current default URL and forwards allowed request headers.
func (s *GrokExecutor) BuildResponsesRequest(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider, body []byte, token, identity string, runtimeDefault bool) (*http.Request, error) {
	targetURL, err := s.Routes.Responses(target, runtimeDefault)
	if err != nil {
		return nil, err
	}
	beta := ""
	if c != nil {
		beta = c.GetHeader("OpenAI-Beta")
	}
	return grok.BuildResponsesRequest(ctx, body, grok.ResponsesRequestOptions{
		URL: targetURL, Token: token, CacheIdentity: identity, OAuth: target.View().IsGrokOAuth(), OpenAIBeta: beta,
		Profile: func(ctx context.Context) context.Context {
			return upstream.WithHTTPUpstreamProfile(ctx, upstream.HTTPUpstreamProfileGrok)
		},
		ApplyOverrides: gatewayadapter.BindExecutionHeaders(target),
	})
}
