package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	forward "github.com/TokenFlux/TokenRouter/internal/gateway/provider/openaiforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

type openAIForwardTransformAdapter struct{ openAIForwardPreludeAdapter }

func (p openAIForwardTransformAdapter) ModelAttributes(ctx context.Context, model string, body []byte, compact bool) (modelcatalog.Resolution, error) {
	result, err := gatewayadapter.ResolveModelAttributes(ctx, p.provider, model, p.s.Text.modelAttributes(), "openai_responses")
	if err != nil {
		return modelcatalog.Resolution{}, err
	}
	if compact {
		target := result.Target
		target.Endpoint = "compact"
		session := routing.ModelAttributesFromContext(ctx)
		if session == nil {
			session = p.s.Text.modelAttributes().NewSession()
		}
		var groupID int64
		if group, ok := requeststate.GroupFromContext(ctx); ok && group != nil {
			groupID = group.ID
		}
		result, err = session.Resolve(ctx, groupID, target)
		if err != nil {
			return modelcatalog.Resolution{}, err
		}
	}
	if p.provider != nil {
		p.provider.Capabilities = &result
	}
	return result.ForRequest(gatewayadapter.ModelRequestState(body)), nil
}

func (p openAIForwardTransformAdapter) Decode(body []byte) (map[string]any, error) {
	return requeststate.DecodeOpenAIRequestBody(body)
}

func (p openAIForwardTransformAdapter) GroupImagePolicy(inherited string) string {
	key := GetExecutionAPIKey(p.c)
	return gatewayadapter.GroupResponsesExplicitToolPolicy(gatewayadapter.ResponsesPolicyGroup(p.ctx, gatewayadapter.APIKeyGroup(key)), inherited)
}

func (p openAIForwardTransformAdapter) ImageAllowed() bool {
	key := GetExecutionAPIKey(p.c)
	if key == nil {
		return routing.GroupAllowsResponsesImages(nil)
	}
	return routing.GroupAllowsResponsesImages(key.Group)
}

func (p openAIForwardTransformAdapter) LiteHeader() bool {
	return p.openAIForwardPreludeAdapter.LiteHeader()
}

func (p openAIForwardTransformAdapter) BridgeEnabled(ctx context.Context) bool {
	return p.s.ImageBridge.Enabled(ctx, p.provider, GetExecutionAPIKey(p.c))
}

func (p openAIForwardTransformAdapter) ImageIntentHint(model string, body []byte) bool {
	return ResolveOpenAIImageIntentHint(p.c, model, body, gatewayadapter.ImageIntent().IsImageGenerationIntent)
}

func (p openAIForwardTransformAdapter) Models(model string, compact bool) (string, string) {
	return gatewayadapter.ExecutionModelPolicy(p.provider).ForwardMappedModels(model, compact)
}

func (p openAIForwardTransformAdapter) CompactModel(model string) string {
	return p.s.Text.Compact.ResolveModel(p.provider, model)
}

func (p openAIForwardTransformAdapter) ImagePermissionMessage() string {
	return media.ImageGenerationPermissionMessage
}

func (p openAIForwardTransformAdapter) IsImageGenerationIntent(endpoint, model string, body []byte) bool {
	return gatewayadapter.ImageIntent().IsImageGenerationIntent(endpoint, model, body)
}

func (p openAIForwardTransformAdapter) IsExplicitImageGenerationIntent(endpoint, model string, body []byte) bool {
	return gatewayadapter.ImageIntent().IsExplicitImageGenerationIntent(endpoint, model, body)
}

func (p openAIForwardTransformAdapter) IsImageGenerationIntentMap(endpoint, model string, body map[string]any) bool {
	return gatewayadapter.ImageIntent().IsImageGenerationIntentMap(endpoint, model, body)
}

func (p openAIForwardTransformAdapter) IsExplicitImageGenerationIntentMap(endpoint, model string, body map[string]any) bool {
	return gatewayadapter.ImageIntent().IsExplicitImageGenerationIntentMap(endpoint, model, body)
}

func (p openAIForwardTransformAdapter) IsOpenAIImageGenerationModel(model string) bool {
	return media.IsImageGenerationModel(model)
}

func (p openAIForwardTransformAdapter) OpenAIRequestBodyImageGenerationToolNeedsNormalization(body []byte) bool {
	return gatewayadapter.ImageIntent().OpenAIRequestBodyImageGenerationToolNeedsNormalization(body)
}

func (p openAIForwardTransformAdapter) OpenAIRequestBodyHasImageGenerationDeclaration(body []byte) bool {
	return gatewayadapter.ImageIntent().OpenAIRequestBodyHasImageGenerationDeclaration(body)
}

func (p openAIForwardTransformAdapter) EnsureOpenAIResponsesImageGenerationTool(body map[string]any) bool {
	return gatewayadapter.EnsureOpenAIResponsesImageGenerationTool(body)
}

func (p openAIForwardTransformAdapter) EnsureOpenAIResponsesImageGenerationToolChoiceAuto(body map[string]any) bool {
	return gatewayadapter.EnsureOpenAIResponsesImageGenerationToolChoiceAuto(body)
}

func (p openAIForwardTransformAdapter) NormalizeOpenAIResponsesImageOnlyModel(body map[string]any) bool {
	return gatewayadapter.NormalizeOpenAIResponsesImageOnlyModel(body)
}

func (p openAIForwardTransformAdapter) ValidateOpenAIResponsesImageModel(body map[string]any, model string) error {
	return gatewayadapter.ValidateOpenAIResponsesImageModel(body, model)
}

func (p openAIForwardTransformAdapter) ValidateCodexSparkInput(body map[string]any, model string) error {
	return gatewayadapter.ValidateCodexSparkInput(body, model)
}

func (p openAIForwardTransformAdapter) ApplyCodexImageGenerationBridgeInstructions(body map[string]any) bool {
	return gatewayadapter.ApplyCodexImageGenerationBridgeInstructions(body)
}

func (p openAIForwardTransformAdapter) CodexTransform(body map[string]any, options openai.CodexOAuthTransformOptions) openai.CodexTransformResult {
	if p.provider != nil && p.provider.Capabilities != nil {
		options.Attributes = &p.provider.Capabilities.Attributes
		options.Rules = &p.provider.Capabilities.Rules
	}
	return gatewayadapter.ApplyCodexOAuthTransformWithOptions(body, options)
}

func (p openAIForwardTransformAdapter) EnsureCodexOAuthInstructionsField(body map[string]any) {
	protocolopenai.EnsureCodexInstructionsField(body)
}

func (p openAIForwardTransformAdapter) ToolNameReverse(mapping map[string]string) {
	SetCodexToolNameReverse(p.c, mapping)
}

func (p openAIForwardTransformAdapter) ClientMetadata(body map[string]any) bool {
	return gatewayadapter.ApplyCodexClientMetadata(body, p.provider)
}

func (p openAIForwardTransformAdapter) ProviderIdentity(body map[string]any) bool {
	return openai.ApplyCodexProviderIdentityClientMetadataMap(body, provideradapter.CodexIdentityNamespace(CodexIdentityRecord(p.c, p.provider.View())), APIKeyIDFromContext(p.c))
}

func (p openAIForwardTransformAdapter) ClearFingerprint() {
	StageCodexFingerprintIDs(p.c, nil)
}

func (p openAIForwardTransformAdapter) Fingerprint(ctx context.Context, body map[string]any) (*openai.FingerprintIDs, bool, error) {
	provider, err := gatewayadapter.CredentialProvider(ctx, p.s.Requests.Providers, p.provider)
	if err != nil {
		return nil, false, fmt.Errorf("resolve Codex fingerprint provider: %w", err)
	}
	changed := gatewayadapter.ApplyCodexClientMetadata(body, provider)
	var headers http.Header
	if p.c != nil && p.c.Request != nil {
		headers = p.c.Request.Header
	}
	ids := provideradapter.CodexFingerprintIDsFromRequest(provider.View(), headers)
	if openai.ApplyCodexFingerprintClientMetadata(body, ids) {
		changed = true
	}
	return ids, changed, nil
}

func (p openAIForwardTransformAdapter) FastDecision(ctx context.Context, model, tier string, hasTier bool) forward.FastDecision {
	decision := tierpolicy.Resolve(p.s.Text.FastPolicy.Input(ctx, p.provider, model), tier, hasTier)
	value := forward.FastDecision{DeleteField: decision.DeleteField, Tier: decision.Tier}
	if decision.Blocked != nil {
		value.Blocked = decision.Blocked
	}
	return value
}

func (p openAIForwardTransformAdapter) FastBlocked(err error) {
	if blocked, ok := errors.AsType[*tierpolicy.BlockedError](err); ok {
		WriteFastPolicyBlockedResponse(p.c, blocked)
	}
}

func (p openAIForwardTransformAdapter) SanitizeOpenAIResponsesOrphanToolOutputs(body map[string]any, input []any, hasPrevious bool) bool {
	return gatewayadapter.SanitizeOpenAIResponsesOrphanToolOutputs(body, input, hasPrevious)
}

func (p openAIForwardTransformAdapter) FirstNonEmptyString(values ...any) string {
	return openai.FirstNonEmptyString(values...)
}

func (p openAIForwardTransformAdapter) Marshal(body map[string]any) ([]byte, error) {
	return wirejson.Marshal(body)
}

func (p openAIForwardTransformAdapter) NormalizeTrigger(body []byte) ([]byte, bool, error) {
	return protocolopenai.NormalizeCompactionTriggerInputOrder(body)
}
