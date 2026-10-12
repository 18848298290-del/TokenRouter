package httpapi

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	forward "github.com/TokenFlux/TokenRouter/internal/gateway/provider/openaiforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	protocolbridge "github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

type openAIForwardPreludeAdapter struct {
	s        *OpenAIResponsesExecutor
	c        *gin.Context
	provider *gatewayprovider.ExecutionProvider
	ctx      context.Context
}

func openAIForwardProfile(provider *gatewayprovider.ExecutionProvider) forward.Profile {
	return forward.Profile{Platform: provider.Record.Platform, Name: provider.Record.Name, Type: provider.Record.Type, UsesCodex: provider.View().UsesOpenAICodexProtocol(), OpenAI: provider.View().IsOpenAI(), OAuth: provider.View().IsOAuth(), OAuthLike: provider.View().IsOpenAIOAuthLike(), APIKey: provider.Record.Type == capability.ProviderTypeAPIKey, Grok: provider.Record.Platform == capability.PlatformGrok, DeepSeek: provider.Record.Platform == capability.PlatformDeepseek, NativeCN: gatewayprovider.ExecutionProtocolTarget(provider).UsesNativeCNResponses(), Anthropic: gatewayprovider.ExecutionProtocolTarget(provider).IsAnthropicProtocol(), RawChat: gatewayprovider.ExecutionModelPolicy(provider).RawChat(), ResolvedChat: provider.Route.Protocol() == protocol.ProtocolOpenAIChatCompletions, Passthrough: provider.View().IsOpenAIPassthroughEnabled()}
}

func (p openAIForwardPreludeAdapter) BlockGroupImages() bool {
	if source, _ := requeststate.ClientProtocolFromContext(p.ctx); source == protocol.ProtocolOpenAIResponses {
		if key := GetExecutionAPIKey(p.c); key != nil && key.Group != nil {
			return key.Group.ResponsesImagePolicy == "block"
		}
	}
	return false
}

func (p openAIForwardPreludeAdapter) StripImages(body []byte) ([]byte, bool, error) {
	return gatewayprovider.StripOpenAIImageGenerationToolsFromRawPayload(body)
}

func (p openAIForwardPreludeAdapter) Begin() {
	BeginUpstreamResponseModelObservation(p.c)
	ClearActualOpenAIUpstreamEndpoint(p.c)
	if shouldForwardOpenAIResponsesViaRawChatCompletions(p.provider) {
		SetActualOpenAIUpstreamEndpoint(p.c, "/v1/chat/completions")
	}
}

func (p openAIForwardPreludeAdapter) ClearMappings() {
	ClearGrokResponsesClientToolMapping(p.c)
	ClearOpenAIResponsesClientToolMapping(p.c)
	ClearOpenAIResponsesNamespaceNames(p.c)
	SetCodexToolNameReverse(p.c, nil)
}

func (p openAIForwardPreludeAdapter) PrepareIdentity(ctx context.Context) error {
	_, err := PrepareCodexIdentity(ctx, p.c, p.s.Requests.Providers, p.provider)
	return err
}

func (p openAIForwardPreludeAdapter) MatchTLS() egress.TLSFingerprintRouterMatchResult {
	return p.s.Requests.MatchTLS(p.c, p.provider)
}

func (p openAIForwardPreludeAdapter) ClientAllowed(ctx context.Context, tls egress.TLSFingerprintRouterMatchResult, body []byte) (bool, string) {
	result := p.s.Requests.DetectClient(p.c, p.provider, tls)
	LogCodexCLIOnlyDetection(ctx, p.c, p.provider, APIKeyIDFromContext(p.c), result, body)
	if result.Enabled && !result.Matched {
		return false, OpenAIClientPolicyForbiddenMessage(result)
	}
	return true, ""
}

func (p openAIForwardPreludeAdapter) Reject(v forward.Rejection) {
	if v.PolicyDenied {
		MarkOpsClientBusinessLimited(p.c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
	}
	if v.FeatureDenied {
		MarkOpsClientBusinessLimited(p.c, OpsClientBusinessLimitedReasonLocalFeatureGate)
	}
	if v.ObserveUpstream {
		SetOpsUpstreamError(p.c, v.Status, v.Message, "")
	}
	WriteOpenAIForwardRejection(p.c, v.Status, v.Type, v.Message, v.Param)
}

func (p openAIForwardPreludeAdapter) ToolSchemas(body []byte) ([]byte, bool, error) {
	return gatewayprovider.SanitizeOpenAIResponsesToolSchemasForPlatform(body, p.provider.Record.Platform)
}

func (p openAIForwardPreludeAdapter) LiteHeader() bool {
	return gatewayprovider.ImageIntent().IsOpenAIResponsesLiteHeader(p.c.GetHeader(media.ResponsesLiteHeader))
}

func (p openAIForwardPreludeAdapter) LitePayload(body []byte) ([]byte, bool, string, error) {
	updated, changed, err := gatewayprovider.NormalizeResponsesLiteForProvider(p.provider.View(), body)
	param := "tools"
	if validation, ok := errors.AsType[*openai.ResponsesLiteValidationError](err); ok {
		param = validation.Parameter()
	}
	return updated, changed, param, err
}

func (p openAIForwardPreludeAdapter) Transport() forward.TransportDecision {
	return forward.TransportDecision{Transport: "http_sse", Reason: "client_protocol_http"}
}

func (p openAIForwardPreludeAdapter) CompactPath() bool {
	return IsOpenAIResponsesCompactPath(p.c)
}

func (p openAIForwardPreludeAdapter) CompactBody(body []byte) ([]byte, bool, error) {
	return openai.NormalizeOpenAICompactRequestBody(body)
}

func (p openAIForwardPreludeAdapter) CompactAPIKeyReplay(body []byte) ([]byte, bool, error) {
	return openai.NormalizeOpenAIAPIKeyStoreFalseReasoningReplay(body, true)
}

func (p openAIForwardPreludeAdapter) FlattenRequired(v forward.TransportDecision, passthrough, compact bool) bool {
	return gatewayprovider.ShouldFlattenOpenAIResponsesNamespaces(p.provider, egress.OpenAIUpstreamTransport(v.Transport), passthrough, compact)
}

func (p openAIForwardPreludeAdapter) Flatten(body []byte) ([]byte, error) {
	return FlattenOpenAIResponsesNamespaces(p.c, body)
}

func (p openAIForwardPreludeAdapter) StripNamespacesRequired(v forward.TransportDecision, passthrough bool) bool {
	return gatewayprovider.ShouldStripOpenAIResponsesInputNamespaces(p.provider, egress.OpenAIUpstreamTransport(v.Transport), passthrough)
}

func (p openAIForwardPreludeAdapter) KeepNamespaces(v forward.TransportDecision, passthrough, compact bool, body []byte) bool {
	return gatewayprovider.ShouldKeepOpenAIResponsesToolCallNamespaces(p.provider, egress.OpenAIUpstreamTransport(v.Transport), passthrough, compact, body)
}

func (p openAIForwardPreludeAdapter) StripNamespaces(body []byte, keep bool) ([]byte, error) {
	return protocolbridge.StripOpenAIResponsesInputNamespaces(body, keep)
}

func (p openAIForwardPreludeAdapter) NeedsClientTools(body []byte) bool {
	return protocolbridge.NeedsOpenAIResponsesClientToolAdaptation(body)
}

func (p openAIForwardPreludeAdapter) AdaptClientTools(body []byte) ([]byte, error) {
	body, mapping, err := protocolbridge.AdaptOpenAIResponsesClientTools(body)
	if err == nil {
		SetOpenAIResponsesClientToolMapping(p.c, mapping)
	}
	return body, err
}

func (p openAIForwardPreludeAdapter) ValidateEffort(body []byte, model string) error {
	return requeststate.ValidateOpenAIReasoningEffort(body, model)
}

func (p openAIForwardPreludeAdapter) ReasoningReplay(body []byte) ([]byte, bool, error) {
	return openai.NormalizeOpenAIResponsesReasoningContentReplay(body)
}

func (p openAIForwardPreludeAdapter) InputItemIDs(body []byte) ([]byte, bool, error) {
	return gatewayprovider.SanitizeOpenAIResponsesInputItemIDs(body)
}

func (p openAIForwardPreludeAdapter) MessagesBridge(body []byte) bool {
	return gatewayprovider.IsOpenAICompatMessagesBridgeBody(body)
}

func (p openAIForwardPreludeAdapter) BindMessagesBridge(v bool) {
	SetOpenAICompatMessagesBridgeContext(p.c, v)
}

func (p openAIForwardPreludeAdapter) CodexClient() bool {
	return openai.IsCodexOfficialClientByHeaders(p.c.GetHeader("User-Agent"), p.c.GetHeader("originator")) || p.s.Requests.Options.ForceCLI
}

func (p openAIForwardPreludeAdapter) ImageToolPolicy() string {
	return gatewayprovider.ExecutionProtocolRecord(p.provider).CodexImageGenerationExplicitToolPolicy()
}

func (p openAIForwardPreludeAdapter) ObserveTransport(v forward.TransportDecision, model string, stream bool) {
	if p.c != nil {
		p.c.Set("openai_ws_transport_decision", v.Transport)
		p.c.Set("openai_ws_transport_reason", v.Reason)
	}
	if v.Transport == string(egress.OpenAIUpstreamTransportResponsesWebsocketV2) {
		gatewayprovider.LogOpenAIWSModeDebug("selected provider_id=%d provider_type=%s transport=%s reason=%s model=%s stream=%v", p.provider.Record.ID, p.provider.Record.Type, gatewayprovider.NormalizeOpenAIWSLogValue(v.Transport), gatewayprovider.NormalizeOpenAIWSLogValue(v.Reason), model, stream)
	}
}

func (p openAIForwardPreludeAdapter) MappedModel(model string) string {
	return gatewayprovider.ExecutionModelPolicy(p.provider).Mapped(model)
}

func (p openAIForwardPreludeAdapter) PassthroughEffort(body []byte, model string) *string {
	return gatewayprovider.ApplyThinkingEnabledFallback(requeststate.ExtractOpenAIReasoningEffortFromBody(body), body, model)
}

func (p openAIForwardPreludeAdapter) Log(format string, args ...any) {
	logging.LegacyPrintf("service.openai_gateway", format, args...)
}
