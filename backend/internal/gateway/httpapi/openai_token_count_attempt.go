package httpapi

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
)

// Compatibility token counting selects one target and records preparation
// latency before checking that selection's result. The adapter publishes
// selected-provider observations, supplies the group-mapped body to the
// controlled target, and reports count failures after the target has handled
// its HTTP output.

// openAICountAttempt retains one count's key, selected target, and HTTP
// observations.
type openAICountAttempt struct {
	ports OpenAITokenPorts // shared token operation bindings
	c     *gin.Context     // current counting HTTP context
	call  OpenAICountCall  // route and diagnostic inputs
	key   *apikey.APIKey   // copied effective API key
	// provider retains the most recent target returned by Select, including
	// a nonnil target returned alongside an error that prevents Forward.
	provider OpenAICountTarget
}

// CountExecution constructs the synchronous adapter for one Messages-compatible
// count. It copies the call's API key and retains the call's body reader and
// HTTP context. Callers provide funding-approved input and invoke it through
// RunSingleCountTokens.
func (p OpenAITokenPorts) CountExecution(c *gin.Context, call OpenAICountCall) textflow.SingleCountPorts {
	return &openAICountAttempt{ports: p, c: c, call: call, key: apikey.CopyAPIKey(call.Key)}
}

// Select captures a count target through the slot-free selection operation.
// Callers configure a nonnil effective key and Execution service. The
// availability result reflects the returned target even when selection also
// returns an error.
func (p *openAICountAttempt) Select() (bool, error) {
	// Funding passed at HTTP entry before this slot-free selection. Token
	// queries can choose an eligible provider while leaving inference
	// execution capacity available to generating requests.
	provider, err := p.ports.Execution.SelectCount(p.c.Request.Context(), p.key.GroupID, p.call.SessionHash, p.call.ProviderLayerModel, p.call.Platform)
	p.provider = provider
	return provider != nil, err
}

// Selected records total preparation latency when count selection finishes.
// RunSingleCountTokens invokes it before checking availability or error, so no
// selected target is required to record the elapsed time.
func (p *openAICountAttempt) Selected() {
	SetOpsLatencyMs(p.c, OpsAuthLatencyMsKey, time.Since(p.call.StartedAt).Milliseconds())
}

// SelectionFailed diagnoses missing model support or routing capacity and
// writes Anthropic output. It accepts nil error for an unavailable result.
// Diagnostics use the provider-layer model while client text uses the requested
// model; capacity marking follows that result.
func (p *openAICountAttempt) SelectionFailed(err error) {
	if err != nil {
		p.call.Log.Warn("openai_count_tokens.provider_select_failed", zap.Error(OpenAICompatibleSelectionErrorForLog(err, p.call.Platform)))
	}
	cls := tokenSelectionError(p.c, p.ports.Diagnoser, p.key, p.call.ProviderLayerModel, p.call.Model)
	if !cls.ModelNotFound {
		if err != nil {
			MarkOpsRoutingCapacityLimitedIfNoAvailable(p.c, err)
		} else {
			MarkOpsRoutingCapacityLimited(p.c)
		}
	}
	WriteAnthropicError(p.c, cls.Status, cls.ErrType, "", cls.Message)
}

// Forward invokes the selected count target with the cached group-mapped body.
// It records provider identity first and passes the provider-layer model
// separately. Callers invoke it only after successful selection; the target
// owns count response output.
func (p *openAICountAttempt) Forward() error {
	selected := p.provider.Snapshot()
	SetOpsSelectedProvider(p.c, selected.ID, selected.Platform)
	body := p.call.MappedBody(p.call.Mapping.Mapped, p.call.Mapping.MappedModel)
	return p.provider.ForwardCount(p.c.Request.Context(), p.c, body, p.call.ProviderLayerModel)
}

// ForwardFailed logs the selected provider's returned counting error for
// diagnostics.
func (p *openAICountAttempt) ForwardFailed(err error) {
	p.call.Log.Error("openai_count_tokens.forward_failed", zap.Int64("provider_id", p.provider.Snapshot().ID), zap.Error(err))
}
