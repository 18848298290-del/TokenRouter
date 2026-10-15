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

// antigravity_retry.go binds retries while transferring updated provider
// extras between retry and execution records.
// Antigravity platform retry receives a provider record view while transport
// and health callbacks use the execution target. A callback can replace Extra
// on that target, so the retry view needs the resulting reference before it
// considers another model window or signature recovery.
//
// Transport and health callbacks refresh the view after their work, while the
// retry service's Changed callback copies Extra back to the execution target.
// These are synchronous reference assignments within an attempt. Credential
// selection occurs before retry binding; each request keeps the application
// services, prepared bytes, context, and diagnostic output usable until retries
// and response processing finish.

// antigravityRetryAdapter binds one attempt to the shared provider retry
// service. Callers keep the selected provider, output, context, prepared bytes,
// transport, and reached observation callbacks usable through synchronous
// retry.
//
// The retry service receives a record view distinct from the execution target.
// Do and HandleError refresh that view's Extra reference after their callbacks;
// Changed publishes the retry service's Extra reference back to the target.
// Replacing a model-window map during transport therefore affects later
// recovery prechecks instead of leaving them on the earlier map.
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
			// Transport callbacks can update execution quota
			// windows; copy the current Extra reference back into
			// the retry view before later signature recovery
			// inspects those windows.
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
