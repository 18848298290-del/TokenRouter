package openaiforward

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

type TransformResult struct {
	Body                                                                                     []byte
	View                                                                                     requeststate.OpenAIRequestView
	Decoded                                                                                  map[string]any
	Model, RequestedModel, BillingModel, UpstreamModel, PromptCacheKey, ClientPromptCacheKey string
	ImageIntent                                                                              bool
	Fingerprint                                                                              *openai.FingerprintIDs
}
type FastDecision struct {
	DeleteField bool
	Tier        string
	Blocked     error
}

func TransformRequest(ctx context.Context, prepared *Prelude, profile Profile, p TransformPorts) (*TransformResult, error) {
	body := prepared.Body
	requestView := prepared.View
	reqModel, promptCacheKey := requestView.Model, requestView.PromptCacheKey
	canonicalImageIntentBody := prepared.CanonicalImageIntentBody
	codexImageGenerationExplicitToolPolicy := prepared.ImageToolPolicy
	isCodexCLI, compactPath, compatMessagesBridge, nativeCNResponses := prepared.CodexCLI, prepared.Compact, prepared.MessagesBridge, profile.NativeCN
	wsDecision := prepared.Transport
	bodyModified := false
	clientPromptCacheKey := promptCacheKey
	var reqBody map[string]any
	ensureReqBody := func() (map[string]any, error) {
		if requestView.HasPatches() {
			patchedBody, patchErr := requestView.ApplyPatches()
			if patchErr != nil {
				return nil, patchErr
			}
			body = patchedBody
			requestView = requeststate.NewOpenAIRequestView(body)
			reqBody = nil
			bodyModified = false
		}
		if reqBody != nil {
			return reqBody, nil
		}
		decoded, decodeErr := p.Decode(requestView.Bytes())
		if decodeErr != nil {
			return nil, decodeErr
		}
		reqBody = decoded
		return reqBody, nil
	}
	markPatchSet := func(path string, value any) {
		bodyModified = true
		if requestView.PatchesDisabled() {
			if reqBody != nil {
				openai.SetOpenAIRequestMapPath(reqBody, path, value)
			}
			return
		}
		requestView.MarkPatchSet(path, value)
	}
	markPatchDelete := func(path string) {
		bodyModified = true
		if requestView.PatchesDisabled() {
			if reqBody != nil {
				openai.DeleteOpenAIRequestMapPath(reqBody, path)
			}
			return
		}
		requestView.MarkPatchDelete(path)
	}
	disablePatch := func() {
		requestView.DisablePatches()
	}
	markDecodedModified := func() {
		bodyModified = true
		disablePatch()
	}

	codexImageGenerationExplicitToolPolicy = p.GroupImagePolicy(codexImageGenerationExplicitToolPolicy)
	imageGenerationAllowed := p.ImageAllowed()
	codexImageGenerationBridgeEnabled := isCodexCLI &&
		!p.LiteHeader() &&
		imageGenerationAllowed &&
		codexImageGenerationExplicitToolPolicy != "strip" &&
		p.BridgeEnabled(ctx)
	var imageIntent bool
	canonicalImageIntent := p.ImageIntentHint(reqModel, canonicalImageIntentBody)
	// Explicit image intent controls permissions; broad intent controls namespace tools and image billing.
	explicitImageIntent := p.IsExplicitImageGenerationIntent("/v1/responses", reqModel, canonicalImageIntentBody)
	if codexImageGenerationExplicitToolPolicy == "strip" {
		decoded, decodeErr := ensureReqBody()
		if decodeErr != nil {
			return nil, decodeErr
		}
		if openai.StripOpenAIImageGenerationTools(decoded) {
			markDecodedModified()
			p.Log("[OpenAI] Stripped /responses image_generation tool for Codex client by provider policy")
		}
		imageIntent = p.IsImageGenerationIntentMap("/v1/responses", reqModel, decoded)
		explicitImageIntent = p.IsExplicitImageGenerationIntentMap("/v1/responses", reqModel, decoded)
	} else {
		imageIntent = canonicalImageIntent
	}
	if explicitImageIntent && !imageGenerationAllowed {
		p.Reject(Rejection{Status: 403, Type: "permission_error", Message: p.ImagePermissionMessage(), FeatureDenied: true})
		return nil, errors.New("image generation disabled for group")
	}

	instructions := gjson.GetBytes(body, "instructions")
	instructionsEmpty := !instructions.Exists() || instructions.Type != gjson.String || strings.TrimSpace(instructions.String()) == ""
	if instructionsEmpty && profile.UsesCodex && !compatMessagesBridge && !nativeCNResponses {
		markPatchSet("instructions", openai.DefaultCodexSynthInstructions(reqModel))
	}

	isCompactRequest := compactPath
	requestedModel := reqModel
	billingModel, upstreamModel := p.Models(requestedModel, isCompactRequest)
	if isCompactRequest {
		if compactModel := p.CompactModel(requestedModel); compactModel != "" {
			upstreamModel = compactModel
		}
	}
	attributes, attributesErr := p.ModelAttributes(ctx, upstreamModel, body, isCompactRequest)
	if attributesErr != nil {
		return nil, attributesErr
	}
	for _, field := range []string{"reasoning.effort", "reasoning_effort"} {
		if effort := gjson.GetBytes(body, field); effort.Exists() {
			if mapped := attributes.Rules.EffortAliases[effort.String()]; mapped != "" {
				markPatchSet(field, mapped)
			}
		}
	}
	if attributes.Attributes.Temperature != nil && !*attributes.Attributes.Temperature {
		markPatchDelete("temperature")
	}
	if attributes.Attributes.TopP != nil && !*attributes.Attributes.TopP {
		markPatchDelete("top_p")
	}
	if billingModel != requestedModel {
		p.Log("[OpenAI] Model mapping applied: %s -> %s (provider: %s, isCodexCLI: %v)", requestedModel, billingModel, profile.Name, isCodexCLI)
	}
	reqModel = billingModel
	if upstreamModel != requestedModel {
		markPatchSet("model", upstreamModel)
	}
	if upstreamModel != billingModel {
		if isCompactRequest {
			p.Log("[OpenAI] Compact model mapping applied: %s -> %s (provider: %s, isCodexCLI: %v)", requestedModel, upstreamModel, profile.Name, isCodexCLI)
		} else {
			p.Log("[OpenAI] Upstream model resolved: %s -> %s (provider: %s, type: %s, isCodexCLI: %v)", billingModel, upstreamModel, profile.Name, profile.Type, isCodexCLI)
		}
	}
	if strings.TrimSpace(gjson.GetBytes(body, "text.format.type").String()) == "json_schema" ||
		strings.TrimSpace(gjson.GetBytes(body, "response_format.type").String()) == "json_schema" {
		decoded, decodeErr := ensureReqBody()
		if decodeErr != nil {
			return nil, decodeErr
		}
		if openai.NormalizeOpenAIResponseFormatSchemas(decoded) {
			markDecodedModified()
			p.Log("[OpenAI] Normalized Responses JSON schema compatibility")
		}
	}

	imageIntent = imageIntent || p.IsImageGenerationIntent("/v1/responses", reqModel, nil) || p.IsOpenAIImageGenerationModel(upstreamModel)
	explicitImageIntent = explicitImageIntent || p.IsExplicitImageGenerationIntent("/v1/responses", reqModel, nil) || p.IsOpenAIImageGenerationModel(upstreamModel)
	if explicitImageIntent && !imageGenerationAllowed {
		p.Reject(Rejection{Status: 403, Type: "permission_error", Message: p.ImagePermissionMessage(), FeatureDenied: true})
		return nil, errors.New("image generation disabled for group")
	}

	if imageGenerationAllowed && !isCompactRequest && (codexImageGenerationBridgeEnabled || p.IsOpenAIImageGenerationModel(requestView.Model) || p.OpenAIRequestBodyImageGenerationToolNeedsNormalization(body) || p.IsOpenAIImageGenerationModel(upstreamModel)) {
		decoded, decodeErr := ensureReqBody()
		if decodeErr != nil {
			return nil, decodeErr
		}
		if codexImageGenerationBridgeEnabled && p.EnsureOpenAIResponsesImageGenerationTool(decoded) {
			markDecodedModified()
			p.Log("[OpenAI] Injected /responses image_generation tool for Codex client")
		}
		if codexImageGenerationBridgeEnabled && p.EnsureOpenAIResponsesImageGenerationToolChoiceAuto(decoded) {
			markDecodedModified()
			p.Log("[OpenAI] Set /responses image_generation tool_choice=auto for Codex client")
		}
		if openai.NormalizeOpenAIResponsesImageGenerationTools(decoded) {
			markDecodedModified()
			p.Log("[OpenAI] Normalized /responses image_generation tool payload")
		}
		if p.NormalizeOpenAIResponsesImageOnlyModel(decoded) {
			markDecodedModified()
			if model, ok := decoded["model"].(string); ok {
				upstreamModel = strings.TrimSpace(model)
			}
			p.Log("[OpenAI] Normalized /responses image-only model request inbound_model=%s image_model=%s upstream_model=%s", requestView.Model, billingModel, upstreamModel)
		}
		if err := p.ValidateOpenAIResponsesImageModel(decoded, upstreamModel); err != nil {
			p.Reject(Rejection{Status: 400, Type: "invalid_request_error", Message: err.Error(), Param: "model", ObserveUpstream: true})
			return nil, err
		}
		if openai.HasOpenAIImageGenerationTool(decoded) {
			imageIntent = true
			p.Log("[OpenAI] /responses image_generation request inbound_model=%s mapped_model=%s provider_type=%s", requestView.Model, upstreamModel, profile.Type)
		}
		if codexImageGenerationBridgeEnabled && p.ApplyCodexImageGenerationBridgeInstructions(decoded) {
			markDecodedModified()
			p.Log("[OpenAI] Added Codex image_generation bridge instructions")
		}
	} else if imageGenerationAllowed && imageIntent && p.OpenAIRequestBodyHasImageGenerationDeclaration(body) {
		// A complete image tool declaration can be read from raw JSON; validation, bridging, or legacy migration requires decoding input.
		p.Log("[OpenAI] /responses image_generation request inbound_model=%s mapped_model=%s provider_type=%s", requestView.Model, upstreamModel, profile.Type)
	}

	imageToolsDisabled := attributes.Rules.ImageGenerationTool != nil && !*attributes.Rules.ImageGenerationTool
	if imageToolsDisabled && attributes.Attributes.InputModalities != nil && !slices.Contains(*attributes.Attributes.InputModalities, "image") && openai.OpenAIRequestBodyMayContainImageInput(body) {
		decoded, decodeErr := ensureReqBody()
		if decodeErr != nil {
			return nil, decodeErr
		}
		if err := p.ValidateCodexSparkInput(decoded, upstreamModel); err != nil {
			p.Reject(Rejection{Status: 400, Type: "invalid_request_error", Message: err.Error(), Param: "input", ObserveUpstream: true})
			return nil, err
		}
	}

	// Apply platform tool restrictions after the group's image policy.
	if imageToolsDisabled && p.OpenAIRequestBodyHasImageGenerationDeclaration(body) {
		decoded, decodeErr := ensureReqBody()
		if decodeErr != nil {
			return nil, decodeErr
		}
		if openai.StripCodexSparkImageGenerationTools(decoded) {
			markDecodedModified()
		}
	}

	var fingerprintIDs *openai.FingerprintIDs
	if profile.OAuth {
		decoded, decodeErr := ensureReqBody()
		if decodeErr != nil {
			return nil, decodeErr
		}
		// Promoted text system messages can be removed from input. JSON object mode keeps
		// its JSON instruction in input for upstream compatibility validation.
		omitPromotedSystemMessages := !strings.EqualFold(
			strings.TrimSpace(gjson.GetBytes(body, "text.format.type").String()),
			"json_object",
		)
		codexResult := openai.CodexTransformResult{}
		if compatMessagesBridge {
			codexResult = p.CodexTransform(decoded, openai.CodexOAuthTransformOptions{
				IsCodexCLI:                          isCodexCLI,
				IsCompact:                           isCompactRequest,
				SkipDefaultInstructions:             true,
				PreserveToolCallIDs:                 true,
				OmitPromotedSystemMessagesFromInput: omitPromotedSystemMessages,
			})
			p.EnsureCodexOAuthInstructionsField(decoded)
			markDecodedModified()
		} else {
			codexResult = p.CodexTransform(decoded, openai.CodexOAuthTransformOptions{
				IsCodexCLI:                          isCodexCLI,
				IsCompact:                           isCompactRequest,
				OmitPromotedSystemMessagesFromInput: omitPromotedSystemMessages,
			})
		}
		if codexResult.Error != nil {
			p.Reject(Rejection{Status: 400, Type: "invalid_request_error", Message: codexResult.Error.Error()})
			return nil, codexResult.Error
		}
		p.ToolNameReverse(codexResult.ToolNameReverse)
		if codexResult.Modified {
			markDecodedModified()
		}
		// Each attempt obtains fingerprint IDs for its selected provider.
		// Populate installation metadata from device_id when present; compact requests skip this step.
		if !isCompactRequest && p.ClientMetadata(decoded) {
			markDecodedModified()
		}
		if currentClientPromptCacheKey, ok := decoded["prompt_cache_key"].(string); ok {
			clientPromptCacheKey = currentClientPromptCacheKey
		}
		// Derive client identity within the selected provider's namespace after OAuth failover.
		if !isCompactRequest && p.ProviderIdentity(decoded) {
			markDecodedModified()
		}
		p.ClearFingerprint()
		if !isCompactRequest {
			var changed bool
			var resolveErr error
			fingerprintIDs, changed, resolveErr = p.Fingerprint(ctx, decoded)
			if resolveErr != nil {
				return nil, resolveErr
			}
			if changed {
				markDecodedModified()
			}
		}
		if codexResult.NormalizedModel != "" {
			upstreamModel = codexResult.NormalizedModel
		}
		if strings.TrimSpace(clientPromptCacheKey) != "" {
			// The body already has a provider-scoped key; header construction needs the saved client key to derive its namespace.
			promptCacheKey = clientPromptCacheKey
		} else if currentPromptCacheKey, ok := decoded["prompt_cache_key"].(string); ok && currentPromptCacheKey != "" {
			// Use the fingerprint-injected default when the client supplied no key.
			promptCacheKey = currentPromptCacheKey
		} else if codexResult.PromptCacheKey != "" {
			promptCacheKey = codexResult.PromptCacheKey
		}
	}

	if attributes.Rules.Verbosity != nil && !*attributes.Rules.Verbosity && gjson.GetBytes(body, "text.verbosity").Exists() {
		markPatchDelete("text.verbosity")
	}

	if !isCodexCLI {
		maxOutputTokens := gjson.GetBytes(body, "max_output_tokens")
		if maxOutputTokens.Exists() {
			switch profile.Platform {
			case "openai", "deepseek":
				// Send the Responses output limit first; bounded HTTP retries remove it if the upstream rejects it.
			case "anthropic":
				decoded, decodeErr := ensureReqBody()
				if decodeErr != nil {
					return nil, decodeErr
				}
				delete(decoded, "max_output_tokens")
				if _, hasMaxTokens := decoded["max_tokens"]; !hasMaxTokens {
					decoded["max_tokens"] = maxOutputTokens.Value()
				}
				markDecodedModified()
			case "gemini":
				markPatchDelete("max_output_tokens")
			default:
				markPatchDelete("max_output_tokens")
			}
		}
		// Responses uses max_output_tokens. Some clients send Chat's max_tokens,
		// which compatible Responses upstreams reject (issue #4417).
		// Apply this normalization to OpenAI; Anthropic uses max_tokens and is converted
		// in the platform switch above.
		if profile.Platform == "openai" {
			if maxTokens := gjson.GetBytes(body, "max_tokens"); maxTokens.Exists() {
				if !gjson.GetBytes(body, "max_output_tokens").Exists() {
					markPatchSet("max_output_tokens", maxTokens.Value())
				}
				markPatchDelete("max_tokens")
			}
		}
		if gjson.GetBytes(body, "max_completion_tokens").Exists() && (profile.Type == "apikey" || profile.Platform != "openai") {
			markPatchDelete("max_completion_tokens")
		}
		for _, unsupportedField := range []string{"prompt_cache_retention", "safety_identifier", "prompt_cache_options"} {
			if gjson.GetBytes(body, unsupportedField).Exists() {
				markPatchDelete(unsupportedField)
			}
		}
	}
	if wsDecision.Transport != "responses_websockets_v2" && (!profile.OpenAI || !profile.APIKey) && gjson.GetBytes(body, "previous_response_id").Exists() {
		markPatchDelete("previous_response_id")
	}
	if openai.OpenAIRequestBodyMayContainEmptyBase64InputImage(body) {
		decoded, decodeErr := ensureReqBody()
		if decodeErr != nil {
			return nil, decodeErr
		}
		if openai.SanitizeEmptyBase64InputImagesInOpenAIRequestBodyMap(decoded) {
			markDecodedModified()
		}
	}

	decision := p.FastDecision(ctx, upstreamModel, requestView.ServiceTier, requestView.HasServiceTier)
	if decision.Blocked != nil {
		p.FastBlocked(decision.Blocked)
		return nil, decision.Blocked
	}
	if decision.DeleteField {
		markPatchDelete("service_tier")
	} else if decision.Tier != "" && decision.Tier != requestView.ServiceTier {
		markPatchSet("service_tier", decision.Tier)
	}

	if profile.OAuth {
		decoded, decodeErr := ensureReqBody()
		if decodeErr != nil {
			return nil, decodeErr
		}
		if input, ok := decoded["input"].([]any); ok && p.SanitizeOpenAIResponsesOrphanToolOutputs(
			decoded,
			input,
			strings.TrimSpace(p.FirstNonEmptyString(decoded["previous_response_id"])) != "",
		) {
			markDecodedModified()
		}
	}
	if reqBody != nil {
		// Synchronize decoded-object edits before serializing the request body.
		if _, decodeErr := ensureReqBody(); decodeErr != nil {
			return nil, decodeErr
		}
	}

	if bodyModified {
		if requestView.HasPatches() {
			if patchedBody, patchErr := requestView.ApplyPatches(); patchErr == nil {
				body = patchedBody
				requestView = requeststate.NewOpenAIRequestView(body)
				reqBody = nil
				bodyModified = false
			}
		}
		if bodyModified {
			decoded, decodeErr := ensureReqBody()
			if decodeErr != nil {
				return nil, decodeErr
			}
			var marshalErr error
			body, marshalErr = p.Marshal(decoded)
			if marshalErr != nil {
				return nil, fmt.Errorf("serialize request body: %w", marshalErr)
			}
			requestView = requeststate.NewOpenAIRequestView(body)
		}
	}
	// Normalize the compaction trigger after rebuilding the body so it follows historical input.
	if normalizedBody, changed, normalizeErr := p.NormalizeTrigger(body); normalizeErr != nil {
		return nil, fmt.Errorf("normalize compaction trigger order: %w", normalizeErr)
	} else if changed {
		body = normalizedBody
		requestView = requeststate.NewOpenAIRequestView(body)
		reqBody = nil
	}

	return &TransformResult{Body: body, View: requestView, Decoded: reqBody, Model: reqModel, RequestedModel: requestedModel, BillingModel: billingModel, UpstreamModel: upstreamModel, PromptCacheKey: promptCacheKey, ClientPromptCacheKey: clientPromptCacheKey, ImageIntent: imageIntent, Fingerprint: fingerprintIDs}, nil
}
