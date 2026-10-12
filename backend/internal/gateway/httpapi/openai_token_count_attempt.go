package httpapi

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
)

type openAICountAttempt struct {
	ports    OpenAITokenPorts
	c        *gin.Context
	call     OpenAICountCall
	key      *apikey.APIKey
	provider OpenAICountTarget
}

func (p OpenAITokenPorts) CountExecution(c *gin.Context, call OpenAICountCall) textflow.SingleCountPorts {
	return &openAICountAttempt{ports: p, c: c, call: call, key: apikey.CopyAPIKey(call.Key)}
}

func (p *openAICountAttempt) Select() (bool, error) {
	// Token counting uses slot-free selection with profit checks disabled.
	provider, err := p.ports.Execution.SelectCount(p.c.Request.Context(), p.key.GroupID, p.call.SessionHash, p.call.ProviderLayerModel, p.call.Platform)
	p.provider = provider
	return provider != nil, err
}

func (p *openAICountAttempt) Selected() {
	SetOpsLatencyMs(p.c, OpsAuthLatencyMsKey, time.Since(p.call.StartedAt).Milliseconds())
}

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

func (p *openAICountAttempt) Forward() error {
	selected := p.provider.Snapshot()
	SetOpsSelectedProvider(p.c, selected.ID, selected.Platform)
	body := p.call.MappedBody(p.call.Mapping.Mapped, p.call.Mapping.MappedModel)
	return p.provider.ForwardCount(p.c.Request.Context(), p.c, body, p.call.ProviderLayerModel)
}

func (p *openAICountAttempt) ForwardFailed(err error) {
	p.call.Log.Error("openai_count_tokens.forward_failed", zap.Int64("provider_id", p.provider.Snapshot().ID), zap.Error(err))
}
