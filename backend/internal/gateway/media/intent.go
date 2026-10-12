package media

import (
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
)

// Image intent can be established by an image endpoint or requested image
// model before a body is parsed. Valid JSON bodies add tool declarations
// and tool-choice evidence, with ordinary classification accepting passive
// image namespaces and explicit classification accepting body image models,
// native declarations or selected image functions.
//
// Root JSON traversal keeps the first occurrence of each relevant member so
// duplicate fields agree with gjson lookup. Map inputs instead delegate
// their ordinary tool qualification to injected platform rules. Billing
// configuration follows its own precedence: the first native image tool, a
// permitted body-model hint, then the tool default or caller fallback.
// Those billing choices retain raw size evidence alongside a normalized
// tier and do not validate execution permission.

const (
	// OpenAIResponsesEndpoint names the canonical Responses route used by
	// gateway image policy callers and endpoint comparisons.
	OpenAIResponsesEndpoint = "/v1/responses" // canonical OpenAI Responses route

	// ResponsesLiteHeader, ResponsesLiteHeaderKey and
	// ResponsesLiteWSMetadataKey name the same opt-in signal in HTTP headers,
	// normalized header lookup and tunneled WebSocket request metadata.
	ResponsesLiteHeader        = "X-OpenAI-Internal-Codex-Responses-Lite"                   // HTTP Responses Lite header name
	ResponsesLiteHeaderKey     = "x-openai-internal-codex-responses-lite"                   // normalized Responses Lite header key
	ResponsesLiteWSMetadataKey = "ws_request_header_x_openai_internal_codex_responses_lite" // WebSocket tunneled Lite metadata key
	// ImageGenerationPermissionMessage is the client-facing denial text used
	// when the selected group disables image generation permission.
	ImageGenerationPermissionMessage = "Image generation is not enabled for this group" // group image permission denial message
)

// ImageToolRules supplies platform predicates and string extraction for
// image intent decisions.
type ImageToolRules struct {
	// IsImageType, IsNamespaceName, HasTool, ToolChoice and FirstString are
	// retained function bindings. Callers supply each binding used by their
	// chosen path and keep captured state valid for policy calls.
	IsImageType     func(string) bool // image-generation type qualification callback
	IsNamespaceName func(string) bool // image namespace name qualification callback
	// HasTool is used by ordinary map classification after endpoint and model
	// checks. Explicit map classification examines native tools directly,
	// leaving this callback out of that path.
	HasTool func(map[string]any) bool // ordinary map tool qualification callback
	// ToolChoice handles ordinary any-valued tool choice and is also consulted
	// before explicit function-reference checks. The callback determines which
	// decoded platform shapes qualify.
	ToolChoice func(any) bool // ordinary arbitrary tool-choice qualifier
	// FirstString extracts model, type and size hints from map values.
	// JSON-result methods separately require string-typed values, so custom
	// callback coercion can make the representations differ.
	FirstString func(...any) string // injected nonempty-string extraction rule
}

// ImageIntentPolicy applies configured platform tool rules to gateway image
// classifications.
type ImageIntentPolicy struct {
	// rules retains its function bindings and their captured references by
	// value. NewImageIntentPolicy performs no callback validation, so callers
	// configure the paths they invoke.
	rules ImageToolRules // retained platform image tool rules
}

// OpenAIResponsesImageBillingConfig carries selected image model and
// separate billing size evidence.
type OpenAIResponsesImageBillingConfig struct {
	// Model follows the detailed resolver's tool/body/default/fallback
	// precedence and may be empty. These resolvers select billing evidence
	// without validating that model for execution.
	Model     string // selected image billing model
	SizeTier  string // normalized image billing size tier
	InputSize string // trimmed selected raw size evidence
}

// NewImageIntentPolicy retains the supplied platform rule functions in a
// value policy. It performs no callback validation; callers provide the
// bindings reached by their JSON, map or billing-configuration paths.
func NewImageIntentPolicy(rules ImageToolRules) ImageIntentPolicy {
	return ImageIntentPolicy{rules: rules}
}

// GroupImagePermission permits requests when no group is present, otherwise
// returning the supplied permission flag. Callers resolve group presence
// and authorization beforehand; this helper reads no group configuration
// itself.
func GroupImagePermission(groupPresent, allowed bool) bool { return !groupPresent || allowed }

// IsOpenAIResponsesLiteHeader recognizes the trimmed, case-insensitive
// string true as the opt-in value. Other values, including empty, return
// false; callers locate the header before passing its contents.
func (p ImageIntentPolicy) IsOpenAIResponsesLiteHeader(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "true")
}

// IsOpenAIResponsesLiteWebSocketPayload checks the tunneled Lite metadata
// key in a valid JSON payload. Empty or invalid body returns false; the
// extracted value uses the same trimmed, case-insensitive true recognition
// as headers.
func (p ImageIntentPolicy) IsOpenAIResponsesLiteWebSocketPayload(body []byte) bool {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	return p.IsOpenAIResponsesLiteHeader(gjson.GetBytes(body, "client_metadata."+ResponsesLiteWSMetadataKey).String())
}

// IsImageGenerationIntent recognizes image endpoints, requested image
// models or valid body image declarations. Endpoint/model evidence can
// return true before body validation; body scanning uses the first root
// model, tools, input and tool_choice members.
//
// Ordinary declarations include native image tools and image namespaces,
// including input additional_tools entries. Each relevant member is
// considered once, and scanning stops when positive evidence appears or all
// four members have been visited. This makes duplicate-root handling agree
// with gjson GetBytes while keeping the first image signal sufficient for
// classification.
func (p ImageIntentPolicy) IsImageGenerationIntent(endpoint string, requestedModel string, body []byte) bool {
	if p.IsImageGenerationEndpoint(endpoint) {
		return true
	}
	if IsImageGenerationModel(requestedModel) {
		return true
	}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}

	var modelSeen, toolsSeen, inputSeen, toolChoiceSeen bool
	imageIntent := false
	wirejson.ParseView(body).ForEach(func(key, value gjson.Result) bool {
		// Track the first root member for each recognized key, matching GetBytes
		// so later duplicates cannot replace the evidence already examined.
		switch key.Str {
		case "model":
			if !modelSeen {
				modelSeen = true
				imageIntent = IsImageGenerationModel(strings.TrimSpace(value.String()))
			}
		case "tools":
			if !toolsSeen {
				toolsSeen = true
				imageIntent = p.OpenAIJSONToolsContainImageGeneration(value)
			}
		case "input":
			if !inputSeen {
				inputSeen = true
				imageIntent = p.OpenAIJSONInputContainsImageGenTool(value)
			}
		case "tool_choice":
			if !toolChoiceSeen {
				toolChoiceSeen = true
				imageIntent = p.OpenAIJSONToolChoiceSelectsImageGeneration(value)
			}
		}
		return !imageIntent && (!modelSeen || !toolsSeen || !inputSeen || !toolChoiceSeen)
	})
	return imageIntent
}

// IsExplicitImageGenerationIntent recognizes image endpoints/models, native
// body tools or selected image functions. Endpoint/model checks precede
// body validation, and body scanning reads the first model, tools and
// tool_choice members.
//
// Passive namespace declarations and input additional_tools do not supply
// explicit intent through this root scan. Tool choice can select a
// namespace or a supported imagegen function reference, including a nested
// tool object. Tracking seen members prevents later duplicates from
// changing a field already considered, while positive evidence ends
// traversal immediately.
func (p ImageIntentPolicy) IsExplicitImageGenerationIntent(endpoint string, requestedModel string, body []byte) bool {
	if p.IsImageGenerationEndpoint(endpoint) || IsImageGenerationModel(requestedModel) {
		return true
	}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	// Track explicit-mode root members independently, so a later duplicate
	// cannot introduce intent after the first value for that key supplied
	// none.
	var modelSeen, toolsSeen, toolChoiceSeen bool
	imageIntent := false
	wirejson.ParseView(body).ForEach(func(key, value gjson.Result) bool {
		switch key.Str {
		case "model":
			if !modelSeen {
				modelSeen = true
				imageIntent = IsImageGenerationModel(strings.TrimSpace(value.String()))
			}
		case "tools":
			if !toolsSeen {
				toolsSeen = true
				imageIntent = p.OpenAIJSONToolsContainNativeImageGeneration(value)
			}
		case "tool_choice":
			if !toolChoiceSeen {
				toolChoiceSeen = true
				imageIntent = p.OpenAIJSONToolChoiceSelectsExplicitImageGeneration(value)
			}
		}
		return !imageIntent && (!modelSeen || !toolsSeen || !toolChoiceSeen)
	})
	return imageIntent
}

// IsImageGenerationIntentForPlatform chooses ordinary or explicit
// classification using explicitOnly. Callers decide that platform flag;
// ordinary mode accepts passive declarations, while explicit mode uses
// endpoint/model/native-tool or selected-function evidence.
func (p ImageIntentPolicy) IsImageGenerationIntentForPlatform(endpoint string, requestedModel string, body []byte, explicitOnly bool) bool {
	if !explicitOnly {
		return p.IsImageGenerationIntent(endpoint, requestedModel, body)
	}
	return p.IsExplicitImageGenerationIntent(endpoint, requestedModel, body)
}

// IsImageGenerationIntentMap recognizes endpoint/model evidence before
// consulting a decoded request map. Nil map returns false after those
// checks; ordinary map tool and choice qualification delegate to the
// required injected HasTool and ToolChoice rules.
func (p ImageIntentPolicy) IsImageGenerationIntentMap(endpoint string, requestedModel string, reqBody map[string]any) bool {
	if p.IsImageGenerationEndpoint(endpoint) {
		return true
	}
	if IsImageGenerationModel(requestedModel) {
		return true
	}
	if reqBody == nil {
		return false
	}
	if IsImageGenerationModel(p.rules.FirstString(reqBody["model"])) {
		return true
	}
	if p.rules.HasTool(reqBody) {
		return true
	}
	return p.OpenAIAnyToolChoiceSelectsImageGeneration(reqBody["tool_choice"])
}

// IsExplicitImageGenerationIntentMap checks endpoint/model, decoded body
// model, native tools and explicit tool choice. Nil map supplies no body
// evidence; passive declaration qualification through HasTool is skipped on
// this path.
func (p ImageIntentPolicy) IsExplicitImageGenerationIntentMap(endpoint string, requestedModel string, reqBody map[string]any) bool {
	if p.IsImageGenerationEndpoint(endpoint) || IsImageGenerationModel(requestedModel) {
		return true
	}
	if reqBody == nil {
		return false
	}
	if IsImageGenerationModel(p.rules.FirstString(reqBody["model"])) {
		return true
	}
	if p.OpenAIAnyToolsContainNativeImageGeneration(reqBody["tools"]) {
		return true
	}
	return p.OpenAIAnyToolChoiceSelectsExplicitImageGeneration(reqBody["tool_choice"])
}

// IsImageGenerationEndpoint recognizes generation/edit routes with or
// without the v1 prefix after normalization. The normalizer accepts the
// OpenAI HTTPS origin spelling, optional query and trailing slashes; other
// normalized routes return false.
func (p ImageIntentPolicy) IsImageGenerationEndpoint(endpoint string) bool {
	switch p.NormalizeImageGenerationEndpoint(endpoint) {
	case "/v1/images/generations", "/v1/images/edits", "/images/generations", "/images/edits":
		return true
	default:
		return false
	}
}

// NormalizeImageGenerationEndpoint trims and lowercases endpoint text,
// removes the OpenAI HTTPS origin prefix and query, then drops trailing
// slashes. Empty input returns empty; it does not generally parse or
// validate arbitrary URLs.
func (p ImageIntentPolicy) NormalizeImageGenerationEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(strings.ToLower(endpoint))
	if endpoint == "" {
		return ""
	}
	endpoint = strings.TrimPrefix(endpoint, "https://api.openai.com")
	if idx := strings.IndexByte(endpoint, '?'); idx >= 0 {
		endpoint = endpoint[:idx]
	}
	return strings.TrimRight(endpoint, "/")
}

// OpenAIJSONToolsContainImageGeneration checks an array for qualified image
// types or image namespace declarations. Nonarrays return false, and the
// first qualifying entry ends traversal through the injected type/name
// rules.
func (p ImageIntentPolicy) OpenAIJSONToolsContainImageGeneration(tools gjson.Result) bool {
	if !tools.IsArray() {
		return false
	}
	found := false
	tools.ForEach(func(_, item gjson.Result) bool {
		if p.IsOpenAIImageGenerationType(p.OpenAIJSONString(item.Get("type"))) {
			found = true
			return false
		}
		if p.IsImageGenNamespaceTool(item) {
			found = true
			return false
		}
		return true
	})
	return found
}

// OpenAIJSONToolsContainNativeImageGeneration checks array entries for
// qualified native image-generation types. Nonarrays return false;
// namespace declarations alone do not qualify, and the first matching type
// ends traversal.
func (p ImageIntentPolicy) OpenAIJSONToolsContainNativeImageGeneration(tools gjson.Result) bool {
	if !tools.IsArray() {
		return false
	}
	found := false
	tools.ForEach(func(_, item gjson.Result) bool {
		found = p.IsOpenAIImageGenerationType(p.OpenAIJSONString(item.Get("type")))
		return !found
	})
	return found
}

// OpenAIAnyToolsContainNativeImageGeneration accepts a decoded []any
// containing map[string]any tools with qualified type strings. Other
// container/entry shapes are skipped, and the required
// FirstString/IsImageType callbacks determine qualification during
// scanning.
func (p ImageIntentPolicy) OpenAIAnyToolsContainNativeImageGeneration(rawTools any) bool {
	tools, ok := rawTools.([]any)
	if !ok {
		return false
	}
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if ok && p.IsOpenAIImageGenerationType(p.rules.FirstString(tool["type"])) {
			return true
		}
	}
	return false
}

// IsOpenAIImageGenerationType delegates type qualification to the
// configured platform predicate.
func (p ImageIntentPolicy) IsOpenAIImageGenerationType(value string) bool {
	return p.rules.IsImageType(value)
}

// IsOpenAIImageGenNamespaceName delegates namespace-name qualification to
// the configured platform predicate.
func (p ImageIntentPolicy) IsOpenAIImageGenNamespaceName(value string) bool {
	return p.rules.IsNamespaceName(value)
}

// IsImageGenNamespaceTool recognizes exact namespace type with a qualified
// image namespace name. JSON values must be strings after trimming, while
// name recognition follows the configured platform rule.
func (p ImageIntentPolicy) IsImageGenNamespaceTool(tool gjson.Result) bool {
	return p.OpenAIJSONString(tool.Get("type")) == "namespace" &&
		p.IsOpenAIImageGenNamespaceName(p.OpenAIJSONString(tool.Get("name")))
}

// OpenAIJSONInputContainsImageGenTool checks array entries whose string
// type is additional_tools for ordinary image declarations. Nonarrays or
// other item types contribute no evidence, and the first matching nested
// tools array ends scanning.
func (p ImageIntentPolicy) OpenAIJSONInputContainsImageGenTool(input gjson.Result) bool {
	if !input.IsArray() {
		return false
	}
	found := false
	input.ForEach(func(_, item gjson.Result) bool {
		if p.OpenAIJSONString(item.Get("type")) != "additional_tools" {
			return true
		}
		found = p.OpenAIJSONToolsContainImageGeneration(item.Get("tools"))
		return !found
	})
	return found
}

// OpenAIRequestBodyHasImageGenerationDeclaration checks tools,
// additional_tools input and tool_choice in valid JSON. Empty or invalid
// bytes return false; endpoint and model evidence are handled by other
// intent entry points.
func (p ImageIntentPolicy) OpenAIRequestBodyHasImageGenerationDeclaration(body []byte) bool {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	return p.OpenAIJSONToolsContainImageGeneration(gjson.GetBytes(body, "tools")) ||
		p.OpenAIJSONInputContainsImageGenTool(gjson.GetBytes(body, "input")) ||
		p.OpenAIJSONToolChoiceSelectsImageGeneration(gjson.GetBytes(body, "tool_choice"))
}

// OpenAIRequestBodyImageGenerationToolNeedsNormalization finds exact
// image_generation tool declarations carrying format or compression
// members. Empty, invalid or nonarray tools input returns false; member
// existence qualifies even when its value is null.
func (p ImageIntentPolicy) OpenAIRequestBodyImageGenerationToolNeedsNormalization(body []byte) bool {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	needsNormalization := false
	tools.ForEach(func(_, item gjson.Result) bool {
		if p.OpenAIJSONString(item.Get("type")) != "image_generation" {
			return true
		}
		// Existing format or compression members require map-based normalization
		// before forwarding, including null values whose presence still marks the
		// legacy tool shape.
		if item.Get("format").Exists() || item.Get("compression").Exists() {
			needsNormalization = true
			return false
		}

		return true
	})
	return needsNormalization
}

// OpenAIJSONToolChoiceSelectsImageGeneration recognizes qualified
// string/type, namespace and function-name choices, including a nested tool
// object. Missing or incompatible shapes return false; injected type/name
// rules determine accepted spellings after JSON string extraction.
func (p ImageIntentPolicy) OpenAIJSONToolChoiceSelectsImageGeneration(choice gjson.Result) bool {
	if !choice.Exists() {
		return false
	}
	if choice.Type == gjson.String {
		return p.IsOpenAIImageGenerationType(choice.String())
	}
	if !choice.IsObject() {
		return false
	}
	choiceType := p.OpenAIJSONString(choice.Get("type"))
	if p.IsOpenAIImageGenerationType(choiceType) {
		return true
	}
	if choiceType == "namespace" &&
		(p.IsOpenAIImageGenNamespaceName(p.OpenAIJSONString(choice.Get("name"))) ||
			p.IsOpenAIImageGenNamespaceName(p.OpenAIJSONString(choice.Get("namespace")))) {
		return true
	}
	if tool := choice.Get("tool"); tool.IsObject() && p.OpenAIJSONToolChoiceSelectsImageGeneration(tool) {
		return true
	}
	if p.IsOpenAIImageGenerationType(p.OpenAIJSONString(choice.Get("function.name"))) {
		return true
	}
	return false
}

// OpenAIJSONToolChoiceSelectsExplicitImageGeneration accepts ordinary
// selected image choices, then additional imagegen function-reference
// forms. Object choices can nest tool or function objects; incompatible
// shapes return false after ordinary qualification.
func (p ImageIntentPolicy) OpenAIJSONToolChoiceSelectsExplicitImageGeneration(choice gjson.Result) bool {
	if p.OpenAIJSONToolChoiceSelectsImageGeneration(choice) {
		return true
	}
	if !choice.IsObject() {
		return false
	}
	if tool := choice.Get("tool"); tool.IsObject() && p.OpenAIJSONToolChoiceSelectsExplicitImageGeneration(tool) {
		return true
	}
	if p.IsOpenAIImageGenFunctionReference(
		p.OpenAIJSONString(choice.Get("namespace")),
		p.OpenAIJSONString(choice.Get("name")),
	) {
		return true
	}
	if fn := choice.Get("function"); fn.IsObject() {
		return p.IsOpenAIImageGenFunctionReference(
			p.OpenAIJSONString(fn.Get("namespace")),
			p.OpenAIJSONString(fn.Get("name")),
		)
	}
	return false
}

// IsOpenAIImageGenFunctionReference recognizes trimmed namespace/name
// image_gen and imagegen, or the qualified names image_gen.imagegen and
// image_gen__imagegen. Matching uses those case-sensitive literal
// spellings; other namespace/name pairs and function names return false.
func (p ImageIntentPolicy) IsOpenAIImageGenFunctionReference(namespace string, name string) bool {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "image_gen" && name == "imagegen" {
		return true
	}
	switch name {
	case "image_gen.imagegen", "image_gen__imagegen":
		return true
	default:
		return false
	}
}

// OpenAIAnyToolChoiceSelectsImageGeneration delegates decoded choice
// qualification to the configured platform rule.
func (p ImageIntentPolicy) OpenAIAnyToolChoiceSelectsImageGeneration(choice any) bool {
	return p.rules.ToolChoice(choice)
}

// OpenAIAnyToolChoiceSelectsExplicitImageGeneration first delegates
// ordinary qualification, then checks map-based nested tool/function
// references. Nonmaps return false after that check; callers supply acyclic
// decoded request maps for recursive tool traversal.
func (p ImageIntentPolicy) OpenAIAnyToolChoiceSelectsExplicitImageGeneration(choice any) bool {
	if p.OpenAIAnyToolChoiceSelectsImageGeneration(choice) {
		return true
	}
	choiceMap, ok := choice.(map[string]any)
	if !ok {
		return false
	}
	if tool, ok := choiceMap["tool"].(map[string]any); ok && p.OpenAIAnyToolChoiceSelectsExplicitImageGeneration(tool) {
		return true
	}
	if p.IsOpenAIImageGenFunctionReference(
		p.rules.FirstString(choiceMap["namespace"]),
		p.rules.FirstString(choiceMap["name"]),
	) {
		return true
	}
	if fn, ok := choiceMap["function"].(map[string]any); ok {
		return p.IsOpenAIImageGenFunctionReference(
			p.rules.FirstString(fn["namespace"]),
			p.rules.FirstString(fn["name"]),
		)
	}
	return false
}

// ResolveOpenAIResponsesImageBillingConfigDetailed chooses image model and
// raw size from a decoded request map, returning a normalized size tier.
// The first exact image_generation tool supplies model/size; missing tool
// size falls back to the body size.
//
// A missing image model uses the body model when it is an image billing
// alias or no native tool exists. Otherwise a present tool defaults to
// gpt-image-2, followed by trimmed fallbackModel if still empty. Model may
// remain empty and is not validated; unknown size retains its text while
// tier normalization uses the shared default. The returned error is nil.
func (p ImageIntentPolicy) ResolveOpenAIResponsesImageBillingConfigDetailed(reqBody map[string]any, fallbackModel string) (OpenAIResponsesImageBillingConfig, error) {
	imageModel := ""
	imageSize := ""
	hasImageTool := false
	if reqBody != nil {
		rawTools, _ := reqBody["tools"].([]any)
		for _, rawTool := range rawTools {
			toolMap, ok := rawTool.(map[string]any)
			if !ok || strings.TrimSpace(p.rules.FirstString(toolMap["type"])) != "image_generation" {
				continue
			}
			hasImageTool = true
			imageModel = strings.TrimSpace(p.rules.FirstString(toolMap["model"]))
			imageSize = strings.TrimSpace(p.rules.FirstString(toolMap["size"]))
			break
		}
		if imageSize == "" {
			imageSize = strings.TrimSpace(p.rules.FirstString(reqBody["size"]))
		}
	}
	if imageModel == "" && reqBody != nil {
		bodyModel := strings.TrimSpace(p.rules.FirstString(reqBody["model"]))
		if p.IsOpenAIImageBillingModelAlias(bodyModel) || !hasImageTool {
			imageModel = bodyModel
		}
	}
	// A native image tool with no selected model uses gpt-image-2, separating
	// its image billing identity from a surrounding text-model request.
	if imageModel == "" && hasImageTool {
		imageModel = "gpt-image-2"
	}
	// When tool and body evidence still supply no model, use the caller's
	// trimmed fallback while retaining an empty result if that fallback is
	// also blank.
	if imageModel == "" {
		imageModel = strings.TrimSpace(fallbackModel)
	}
	sizeTier := NormalizeImageSizeTier(imageSize)
	return OpenAIResponsesImageBillingConfig{
		Model:     imageModel,
		SizeTier:  sizeTier,
		InputSize: imageSize,
	}, nil
}

// ResolveOpenAIResponsesImageBillingConfigFromBody returns the selected
// model and normalized size tier from the detailed byte resolver. Raw
// selected size evidence is omitted from this pair; fallbackModel
// participates when body evidence supplies no model.
func (p ImageIntentPolicy) ResolveOpenAIResponsesImageBillingConfigFromBody(body []byte, fallbackModel string) (string, string, error) {
	cfg, err := p.ResolveOpenAIResponsesImageBillingConfigDetailedFromBody(body, fallbackModel)
	if err != nil {
		return "", "", err
	}
	return cfg.Model, cfg.SizeTier, nil
}

// ResolveOpenAIResponsesImageBillingConfigDetailedFromBody selects image
// billing evidence from valid JSON, returning model, normalized tier and
// raw size. The first native image tool supplies model/size; absent tool
// size uses body size, and the first root members follow gjson lookup.
//
// Missing model accepts a body image alias or any body model when no native
// tool exists. A native tool with no usable model then defaults to
// gpt-image-2 before caller fallback. Invalid or empty body uses
// fallbackModel and default size tier. Model selection performs no
// execution validation, and the returned error is nil.
func (p ImageIntentPolicy) ResolveOpenAIResponsesImageBillingConfigDetailedFromBody(body []byte, fallbackModel string) (OpenAIResponsesImageBillingConfig, error) {
	imageModel := ""
	imageSize := ""
	hasImageTool := false
	if len(body) > 0 && gjson.ValidBytes(body) {
		tools := gjson.GetBytes(body, "tools")
		if tools.IsArray() {
			tools.ForEach(func(_, item gjson.Result) bool {
				if p.OpenAIJSONString(item.Get("type")) != "image_generation" {
					return true
				}
				hasImageTool = true
				imageModel = p.OpenAIJSONString(item.Get("model"))
				imageSize = p.OpenAIJSONString(item.Get("size"))
				return false
			})
		}
		if imageSize == "" {
			imageSize = p.OpenAIJSONString(gjson.GetBytes(body, "size"))
		}
		if imageModel == "" {
			bodyModel := p.OpenAIJSONString(gjson.GetBytes(body, "model"))
			if p.IsOpenAIImageBillingModelAlias(bodyModel) || !hasImageTool {
				imageModel = bodyModel
			}
		}
	}
	// A native image tool with no selected model uses gpt-image-2, separating
	// its image billing identity from a surrounding text-model request.
	if imageModel == "" && hasImageTool {
		imageModel = "gpt-image-2"
	}
	// When tool and body evidence still supply no model, use the caller's
	// trimmed fallback while retaining an empty result if that fallback is
	// also blank.
	if imageModel == "" {
		imageModel = strings.TrimSpace(fallbackModel)
	}
	return OpenAIResponsesImageBillingConfig{
		Model:     imageModel,
		SizeTier:  NormalizeImageSizeTier(imageSize),
		InputSize: imageSize,
	}, nil
}

// IsOpenAIImageBillingModelAlias recognizes trimmed, case-insensitive image
// model families or names containing image. Empty returns false; this broad
// billing hint does not validate an upstream image execution model.
func (p ImageIntentPolicy) IsOpenAIImageBillingModelAlias(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return false
	}
	return IsImageGenerationModel(normalized) || strings.Contains(normalized, "image")
}

// OpenAIJSONString returns trimmed content only for a JSON string result.
// Missing, null and nonstring values return empty, keeping byte-based
// policy extraction distinct from any coercion supplied by map callbacks.
func (p ImageIntentPolicy) OpenAIJSONString(value gjson.Result) string {
	if value.Type != gjson.String {
		return ""
	}
	return strings.TrimSpace(value.String())
}
