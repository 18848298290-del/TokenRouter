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

// Client response.create frames receive group and provider image policy after
// model eligibility is resolved. Inspection uses the routing model while the
// forwarded body keeps its upstream model, so image permission and billing
// cannot be inferred from a provider alias alone. Codex frames can also
// receive a permitted image tool and bridge instructions.
//
// Raw string mutation normally patches one JSON path. A failed patch can
// rebuild supported fields through a decoded map, which uses ordinary JSON
// number representation; image bridge preparation uses DecodeUseNumber
// instead. Policy rejection writes an error under the original client lifetime
// and ignores delivery failure, after which the core still returns its chosen
// close instruction.

// wsRequestAdapter supplies provider and group eligibility data and invokes the platform codec.
type wsRequestAdapter struct {
	*wsPassthroughAdapter // shared provider normalization operations
	// client is borrowed from the HTTP handler and remains open through turn
	// execution. WriteBlocked attempts delivery; final transport cleanup belongs
	// to that handler after the entry loop returns.
	client  *coderws.Conn
	isCodex bool // official Codex client policy flag
}

// Mutate writes a supported string field while normally retaining surrounding
// request bytes through sjson. On successful patch it returns immediately.
// Patch failure decodes a map and rebuilds only type, model, or
// client_metadata's turn metadata path; unsupported paths and map decoding
// failure return the original patch error.
//
// The map path supports requests for which the direct patch cannot be applied,
// but ordinary json.Unmarshal uses float64 for numbers and reencoding changes
// byte representation. Turn metadata uses the upstream helper's nested-map
// handling. Callers treat any error as a rejected local payload rather than
// forwarding partially rebuilt bytes.
func (p *wsRequestAdapter) Mutate(current []byte, path, value string) ([]byte, error) {
	next, err := sjson.SetBytes(current, path, value)
	if err == nil {
		return next, nil
	}

	// Direct string patching failed; rebuild the supported fields through a map
	// so compatible object input can still supply model and turn metadata.
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

// RequestedEffort returns canonical client-requested reasoning effort before
// policy rewriting. Missing or unrecognized effort returns nil and explicit
// none remains a value; the model parameter is unused by this extraction.
func (p *wsRequestAdapter) RequestedEffort(body []byte, model string) *string {
	return requeststate.CanonicalRequestedReasoningEffort(body)
}

// ClassifyPrevious labels a continuation ID using the shared OpenAI protocol
// spelling rules.
func (p *wsRequestAdapter) ClassifyPrevious(id string) string {
	return protocolopenai.ClassifyOpenAIPreviousResponseIDKind(id)
}

// TurnMetadata returns the trimmed inbound Codex turn metadata header for
// normalization.
func (p *wsRequestAdapter) TurnMetadata() string {
	return strings.TrimSpace(p.request.GetHeader(openai.WSTurnMetadataHeader))
}

// ImagePolicy returns current group image permission, explicit-tool action and
// Codex bridge eligibility. Codex providers supply the initial explicit
// action, then group policy overrides it. Bridging requires a non-Lite Codex
// frame, permitted images, an action other than strip, and enabled shared
// bridge policy.
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

// BridgeImages prepares permitted Codex image tools, tool choice and bridge
// instructions in JSON request bytes. DecodeUseNumber retains numeric values
// through map conversion. Each shared helper reports modification, and the
// payload is reencoded only after at least one tool, choice, normalization or
// instruction change.
//
// The caller selects this operation after ImagePolicy enables bridging, so it
// does not independently grant image permission. Helper diagnostics identify
// injected controls for the selected provider. With no modification it returns
// the supplied bytes; decoding or encoding failure rejects the local request
// before any transformed image request can be forwarded.
func (p *wsRequestAdapter) BridgeImages(normalized []byte) ([]byte, error) {
	payloadMap := make(map[string]any)
	if err := wirejson.DecodeUseNumber(normalized, &payloadMap); err != nil {
		return nil, err
	}
	bridgeModified := false
	// Apply tool, choice, schema and instruction changes before a single encode,
	// keeping numeric JSON values intact and returning original bytes when all agree.
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

// StripImages removes explicit image-generation declarations through the
// shared raw-payload helper. Its changed flag controls replacement in
// normalization; failures reject the turn, while an unchanged result retains
// the request's current bytes.
func (p *wsRequestAdapter) StripImages(body []byte) ([]byte, bool, error) {
	return gatewayprovider.StripOpenAIImageGenerationToolsFromRawPayload(body)
}

// StripSparkImages removes incompatible image declarations for the resolved
// Spark model. Other models return the body unchanged with false; the core has
// already resolved provider model before selecting this check.
func (p *wsRequestAdapter) StripSparkImages(body []byte, model string) ([]byte, bool, error) {
	return stripCodexSparkImageGenerationToolFromRawPayload(body, model)
}

// ImageIntent returns the routing-model inspection body with broad and
// explicit image decisions. The underlying helper substitutes a differing
// provider model only for inspection; callers keep the actual upstream request
// body separately.
func (p *wsRequestAdapter) ImageIntent(routing, upstream string, body []byte) ([]byte, bool, bool) {
	return openAIWSImageIntentForRoutingModel(routing, upstream, body, p.provider.Record.Platform)
}

// FeatureDenied marks a local Responses image-feature denial in the request's
// Ops data.
func (p *wsRequestAdapter) FeatureDenied() {
	gatewayhttp.MarkOpsClientBusinessLimited(p.request, gatewayhttp.OpsClientBusinessLimitedReasonLocalFeatureGate)
}

// ImageDeniedMessage returns the fixed explanation for forbidden Responses
// image generation.
func (p *wsRequestAdapter) ImageDeniedMessage() string {
	return gatewaymedia.ImageGenerationPermissionMessage
}

// ImageBilling resolves image model, pricing tier and input size from the
// routing-model inspection body. Resolution errors stop normalization;
// successful labels are retained separately from the actual upstream text
// model for result accounting.
func (p *wsRequestAdapter) ImageBilling(body []byte, model string) (gatewayws.ImageBilling, error) {
	result, err := gatewayprovider.ImageIntent().ResolveOpenAIResponsesImageBillingConfigDetailedFromBody(body, model)
	if err != nil {
		return gatewayws.ImageBilling{}, err
	}
	return gatewayws.ImageBilling{Model: result.Model, SizeTier: result.SizeTier, InputSize: result.InputSize}, nil
}

// WriteBlocked attempts a synchronous policy rejection frame using the client
// lifetime and configured write timeout. It cancels the write context
// afterward and ignores the write error; the core still returns the policy
// close instruction after this best-effort presentation.
func (p *wsRequestAdapter) WriteBlocked(ctx context.Context, body []byte) {
	writeCtx, cancel := newOpenAIWSDownstreamWriteContext(ctx, p.hooks, p.service.openAIWSWriteTimeout())
	defer cancel()
	// Error-frame delivery is best effort because the following core close
	// instruction remains the authoritative policy rejection when this write fails.
	_ = p.client.Write(writeCtx, coderws.MessageText, body)
}

// Log writes provider-specific request-normalization diagnostics to the WS
// information logger.
func (p *wsRequestAdapter) Log(message string) { gatewayprovider.LogOpenAIWSModeInfo("%s", message) }
