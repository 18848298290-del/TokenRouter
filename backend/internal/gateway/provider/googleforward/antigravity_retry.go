package googleforward

import (
	"net/http"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// antigravityRetryAdapter binds the current attempt to the shared provider retry service.
func (s *Antigravity) antigravityRetryAdapter(p antigravityRetryLoopParams) (*antigravity.RetryAdapter, antigravity.RetryInput) {
	value := gatewayprovider.ExecutionRecord(p.provider)
	factory := s.Retry
	return factory.Bind(provideradapter.AntigravityRetryRequest{
		Context:             p.ctx,
		Provider:            value,
		ModelStore:          p.providerRepo,
		Prefix:              p.prefix,
		ProxyURL:            p.proxyURL,
		AccessToken:         p.accessToken,
		Action:              p.action,
		Body:                p.body,
		RequestedModel:      p.requestedModel,
		Thinking:            requeststate.HealthThinking(p.ctx),
		SingleProvider:      isSingleProviderRetry(p.ctx),
		Sticky:              p.isStickySession,
		UserAgent:           p.userAgent,
		PolicyModelFallback: requeststate.HealthModel(p.ctx, nil),

		Do: func(req *http.Request) (*http.Response, error) {
			// Exchange may update quota windows; synchronize them before signature recovery retries.
			resp, err := p.httpUpstream.Do(req, p.proxyURL, p.provider.Record.ID, p.provider.Record.Concurrency)
			value.Extra = gatewayprovider.ExecutionRecord(p.provider).Extra
			return resp, err
		},

		LogConfig: func() (bool, int) {
			if !s.Options.Configured {
				return false, 0
			}
			return s.Options.LogErrorBody, s.Options.LogErrorBodyMaxBytes
		},

		Changed: func(record *providercore.Record) { p.provider.Record.Extra = record.Extra },

		Observe: func(o antigravity.RetryObservation) {
			p.c.Observe(ops.OpsUpstreamErrorEvent{
				Platform:           p.provider.Record.Platform,
				ProviderID:         o.ProviderID,
				ProviderName:       o.ProviderName,
				UpstreamStatusCode: o.UpstreamStatusCode,
				UpstreamRequestID:  o.UpstreamRequestID,
				UpstreamURL:        o.UpstreamURL,
				Kind:               o.Kind,
				Message:            o.Message,
				Detail:             o.Detail,
			})
		},

		SetError: func(status int, message, detail string) {
			p.c.SetError(status, message, detail)
		},

		HandleError: func(status int, header http.Header, body []byte) {
			p.handleError(p.ctx, p.prefix, p.provider, status, header, body, p.requestedModel, p.groupID, p.sessionHash, p.isStickySession)
			value.Extra = p.provider.Record.Extra
		},

		ClearSticky: func() { s.clearStickySession(p.ctx, p.groupID, p.sessionHash) },
	})
}
