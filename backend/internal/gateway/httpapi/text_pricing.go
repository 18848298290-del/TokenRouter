package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// CheckTextModelPricing checks the model selected by this attempt's mapping and billing source.
// Pass a CompactExecutor for Compact requests; nil uses ordinary request pricing.
func CheckTextModelPricing(ctx context.Context, prices *admission.ModelPricing, key *apikey.APIKey, target *gatewayprovider.ExecutionProvider, requested, mapped string, compact *CompactExecutor, source protocol.ProtocolID) error {
	if prices == nil {
		return nil
	}
	// Media pricing handles image models; text requests with image tools still need text pricing.
	if media.IsImageGenerationModel(mapped) {
		return nil
	}
	// Preflight and execution use the same request route rules to resolve upstream protocol and model.
	target, err := gatewayprovider.ProviderForProtocolAttempt(ctx, target)
	if err != nil {
		return err
	}
	mapping := routing.GroupMappingResult{MappedModel: mapped}
	if plan, ok := requeststate.RoutePlanFromContext(ctx); ok {
		mapping = plan.Mapping()
		requested = plan.Models().RequestedModel
	}
	policy := gatewayprovider.ExecutionModelPolicy(target)
	result := &completion.Result{Model: mapped}
	if target.View().IsOpenAICompatible() {
		result.BillingModel, result.UpstreamModel = policy.ForwardMappedModels(mapped, compact != nil)
	} else if target.Record.Platform == provider.PlatformGemini {
		result.UpstreamModel = policy.Mapped(mapped)
	} else {
		result.UpstreamModel = policy.UpstreamModel(ctx, mapped)
	}
	switch source {
	case protocol.ProtocolResponsesWebSocket:
		result.BillingModel = ws.EntryBillingModel(nil, mapping, requested, result.UpstreamModel)
	case protocol.ProtocolOpenAIResponses:
		profile := openAIForwardProfile(target)
		if compact != nil && target.View().IsOpenAICompatible() && !profile.Grok && !profile.RawChat && !profile.Anthropic {
			// Compact provider mappings take precedence over its global model; ordinary Responses uses regular mappings.
			if model := compact.ResolveModel(target, mapped); model != "" {
				result.UpstreamModel = model
			}
		}
		if profile.Passthrough && !profile.RawChat && !profile.Anthropic {
			// HTTP passthrough completion uses the group-mapped request name as Model.
			result.BillingModel = ""
		}
	}
	model := completion.OpenAIUsageBillingModel(result, mapping.ToUsageFields(requested, result.UpstreamModel))
	var groupID *int64
	if key != nil {
		groupID = key.GroupID
	}
	return prices.Check(ctx, groupID, model)
}

// checkPricing writes a missing-price error in the client's protocol before text execution.
func (e *UnifiedTextExecutor) checkPricing(c *gin.Context, target *gatewayprovider.ExecutionProvider, body []byte, source protocol.ProtocolID) error {
	if e.Pricing == nil {
		return nil
	}
	model := gjson.GetBytes(body, "model").String()
	key, _ := EffectiveAPIKey(c)
	var compact *CompactExecutor
	if source == protocol.ProtocolOpenAIResponses && IsOpenAIResponsesCompactPath(c) {
		compact = &CompactExecutor{}
		if e.OpenAI != nil && e.OpenAI.Text != nil && e.OpenAI.Text.Compact != nil {
			compact = e.OpenAI.Text.Compact
		}
	}
	err := CheckTextModelPricing(c.Request.Context(), e.Pricing, key, target, model, model, compact, source)
	if !errors.Is(err, admission.ErrModelPricingRejected) {
		return err
	}
	MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
	if source == protocol.ProtocolAnthropicMessages {
		DefaultOpenAIErrorOutput().WriteAnthropicStreamingError(c, http.StatusBadRequest, "invalid_request_error", admission.ModelPricingUnavailableMessage, c.Writer.Written())
	} else {
		DefaultOpenAIErrorOutput().WriteStreamingErrorWithCode(c, http.StatusBadRequest, "invalid_request_error", "model_pricing_unavailable", admission.ModelPricingUnavailableMessage, c.Writer.Written(), false)
	}
	return err
}
