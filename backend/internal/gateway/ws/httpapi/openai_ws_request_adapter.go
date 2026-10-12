package httpapi

import (
	"context"
	"encoding/json"
	"strings"

	coderws "github.com/coder/websocket"
	"github.com/tidwall/sjson"

	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewaymedia "github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
	providerconfig "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// wsRequestAdapter supplies provider and group eligibility data and invokes the platform codec.
type wsRequestAdapter struct {
	*wsPassthroughAdapter
	client  *coderws.Conn
	isCodex bool
}

func (p *wsRequestAdapter) Mutate(current []byte, path, value string) ([]byte, error) {
	next, err := sjson.SetBytes(current, path, value)
	if err == nil {
		return next, nil
	}

	// If sjson cannot update the payload, decode and rewrite the supported fields through a map.
	payload := make(map[string]any)
	if unmarshalErr := json.Unmarshal(current, &payload); unmarshalErr != nil {
		return nil, err
	}
	switch path {
	case "type", "model":
		payload[path] = value
	case "client_metadata." + openai.WSTurnMetadataHeader:
		openai.SetOpenAIWSTurnMetadata(payload, value)
	default:
		return nil, err
	}
	rebuilt, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return nil, marshalErr
	}
	return rebuilt, nil
}

func (p *wsRequestAdapter) RequestedEffort(body []byte, model string) *string {
	return requeststate.CanonicalRequestedReasoningEffort(body)
}

func (p *wsRequestAdapter) ClassifyPrevious(id string) string {
	return protocolopenai.ClassifyOpenAIPreviousResponseIDKind(id)
}

func (p *wsRequestAdapter) TurnMetadata() string {
	return strings.TrimSpace(p.request.GetHeader(openai.WSTurnMetadataHeader))
}

func (p *wsRequestAdapter) ImagePolicy(ctx context.Context, body []byte) gatewayws.ImagePolicy {
	apiKey := gatewayhttp.GetExecutionAPIKey(p.request)
	allowed := routing.GroupAllowsResponsesImages(gatewayprovider.APIKeyGroup(apiKey))
	explicit := providerconfig.CodexImagePolicyAllow
	if p.isCodex {
		explicit = gatewayprovider.ExecutionProtocolRecord(p.provider).CodexImageGenerationExplicitToolPolicy()
	}
	explicit = gatewayprovider.GroupResponsesExplicitToolPolicy(gatewayprovider.ResponsesPolicyGroup(ctx, gatewayprovider.APIKeyGroup(apiKey)), explicit)
	bridge := p.isCodex && !gatewayprovider.ImageIntent().IsOpenAIResponsesLiteWebSocketPayload(body) && allowed && explicit != providerconfig.CodexImagePolicyStrip && p.service.ImageBridge.Enabled(ctx, p.provider, apiKey)
	return gatewayws.ImagePolicy{Allowed: allowed, Explicit: explicit, Bridge: bridge}
}

func (p *wsRequestAdapter) BridgeImages(normalized []byte) ([]byte, error) {
	payloadMap := make(map[string]any)
	if err := wirejson.DecodeUseNumber(normalized, &payloadMap); err != nil {
		return nil, err
	}
	bridgeModified := false
	if gatewayprovider.EnsureOpenAIResponsesImageGenerationTool(payloadMap) {
		bridgeModified = true
		gatewayprovider.LogOpenAIWSModeInfo("ingress_ws_codex_image_tool_injected provider_id=%d", p.provider.Record.ID)
	}
	if gatewayprovider.EnsureOpenAIResponsesImageGenerationToolChoiceAuto(payloadMap) {
		bridgeModified = true
		gatewayprovider.LogOpenAIWSModeInfo("ingress_ws_codex_image_tool_choice_auto provider_id=%d", p.provider.Record.ID)
	}
	if openai.NormalizeOpenAIResponsesImageGenerationTools(payloadMap) {
		bridgeModified = true
	}
	if gatewayprovider.ApplyCodexImageGenerationBridgeInstructions(payloadMap) {
		bridgeModified = true
		gatewayprovider.LogOpenAIWSModeInfo("ingress_ws_codex_image_bridge_instructions_added provider_id=%d", p.provider.Record.ID)
	}
	if bridgeModified {
		rebuilt, marshalErr := json.Marshal(payloadMap)
		if marshalErr != nil {
			return nil, marshalErr
		}
		normalized = rebuilt
	}

	return normalized, nil
}

func (p *wsRequestAdapter) StripImages(body []byte) ([]byte, bool, error) {
	return gatewayprovider.StripOpenAIImageGenerationToolsFromRawPayload(body)
}

func (p *wsRequestAdapter) StripSparkImages(body []byte, model string) ([]byte, bool, error) {
	return stripCodexSparkImageGenerationToolFromRawPayload(body, model)
}

func (p *wsRequestAdapter) ImageIntent(routing, upstream string, body []byte) ([]byte, bool, bool) {
	return openAIWSImageIntentForRoutingModel(routing, upstream, body, p.provider.Record.Platform)
}

func (p *wsRequestAdapter) FeatureDenied() {
	gatewayhttp.MarkOpsClientBusinessLimited(p.request, gatewayhttp.OpsClientBusinessLimitedReasonLocalFeatureGate)
}

func (p *wsRequestAdapter) ImageDeniedMessage() string {
	return gatewaymedia.ImageGenerationPermissionMessage
}

func (p *wsRequestAdapter) ImageBilling(body []byte, model string) (gatewayws.ImageBilling, error) {
	result, err := gatewayprovider.ImageIntent().ResolveOpenAIResponsesImageBillingConfigDetailedFromBody(body, model)
	if err != nil {
		return gatewayws.ImageBilling{}, err
	}
	return gatewayws.ImageBilling{Model: result.Model, SizeTier: result.SizeTier, InputSize: result.InputSize}, nil
}

func (p *wsRequestAdapter) WriteBlocked(ctx context.Context, body []byte) {
	writeCtx, cancel := newOpenAIWSDownstreamWriteContext(ctx, p.hooks, p.service.openAIWSWriteTimeout())
	defer cancel()
	_ = p.client.Write(writeCtx, coderws.MessageText, body)
}

func (p *wsRequestAdapter) Log(message string) { gatewayprovider.LogOpenAIWSModeInfo("%s", message) }
