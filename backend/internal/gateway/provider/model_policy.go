package provider

import (
	"context"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	"github.com/TokenFlux/TokenRouter/internal/gateway/modeltrace"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
	"github.com/TokenFlux/TokenRouter/internal/upstream/bedrock"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
	"github.com/TokenFlux/TokenRouter/internal/upstream/vertex"
)

// ModelPolicy contains provider model configuration and the current request route.
// Route is request-local and must be excluded from persistent records and scheduler snapshots.
type ModelPolicy struct {
	Record *provider.Record
	Route  requeststate.AttemptRoute
	models *provider.ModelRulesSnapshot
}

// Prepared captures model rules for catalogue queries and batch validation.
func (p ModelPolicy) Prepared() ModelPolicy {
	if p.models == nil {
		p.models = provider.PrepareModelRules(p.Record, provideradapter.ModelDefaults(), provideradapter.ModelRules(p.Record))
	}
	return p
}

func (p ModelPolicy) configuredModels() []string {
	return p.models.ConfiguredModels()
}

func (p ModelPolicy) supportsConfiguredModel(model string) bool {
	if p.models != nil {
		return p.models.Supports(model)
	}
	return p.Record.IsModelSupported(model, provideradapter.ModelDefaults(), provideradapter.ModelRules(p.Record))
}

func (p ModelPolicy) finalModelWhitelisted(model string) bool {
	if p.models != nil {
		return p.models.FinalModelWhitelisted(model)
	}
	return p.Record.FinalModelWhitelisted(model, provideradapter.ModelDefaults(), provideradapter.ModelRules(p.Record))
}

func (p ModelPolicy) forwardMappedModel(model string) string {
	if p.models != nil {
		return p.models.ForwardMappedModel(model)
	}
	return provider.ResolveForwardMappedModel(p.Record, model, provideradapter.ModelDefaults())
}

func (p ModelPolicy) antigravityModel(model string, thinking *bool) string {
	if p.models != nil {
		return provideradapter.FinalAntigravityModelWithRules(p.models, model, thinking)
	}
	return provideradapter.FinalAntigravityModel(p.Record, model, thinking)
}

func (p ModelPolicy) protocolTarget() provider.ProtocolTarget {
	return provider.ProtocolTarget{Record: p.Record, Protocol: p.Route.Protocol()}
}

// Mapped applies one hop of provider model mapping.
func (p ModelPolicy) Mapped(model string) string {
	if p.Record == nil {
		return model
	}
	mapped, _ := p.ResolveMapped(model)
	return mapped
}

// ResolveMapped applies one hop of mapping using captured rules or the provider's current configuration.
func (p ModelPolicy) ResolveMapped(model string) (string, bool) {
	if p.models != nil {
		if _, planned := p.Route.Candidate(); !planned {
			return p.models.ResolveMappedModel(model)
		}
		return p.Route.ResolveModel(p.Record.ID, p.Record.Platform, p.models.Mapping(), model)
	}
	return p.Route.ResolveModel(p.Record.ID, p.Record.Platform, provider.ResolveModelMapping(p.Record, provideradapter.ModelDefaults()), model)
}

func (p ModelPolicy) ForwardModel(requested, dispatchMapped string) string {
	model := requested
	if dispatchMapped = strings.TrimSpace(dispatchMapped); dispatchMapped != "" {
		model = dispatchMapped
	}
	return p.Mapped(model)
}

// NormalizeOpenAI trims the model field and supplies Grok's default when its model is empty.
func (p ModelPolicy) NormalizeOpenAI(model string) string {
	if p.Record != nil && p.Record.IsGrok() {
		return grok.NormalizeModelID(model)
	}
	return strings.TrimSpace(model)
}

// RawChat uses the resolved protocol when present, then platform rules, to select Chat forwarding.
func (p ModelPolicy) RawChat() bool {
	if p.Record != nil && p.Route.Protocol() != "" {
		return p.Route.Protocol() == protocol.ProtocolOpenAIChatCompletions
	}
	if p.Record == nil || p.Record.Type != capability.ProviderTypeAPIKey {
		return false
	}
	if p.Record.IsCNProvider() {
		switch p.protocolTarget().GetAPIProtocol() {
		case provider.APIProtocolChatCompletions:
			return true
		case provider.APIProtocolAdaptive:
			return !p.Record.SupportsNativeCNResponses()
		default:
			return false
		}
	}
	return provider.ResolveUpstreamTextProtocol(p.Record.Extra, provider.TextProtocolResponses) == provider.TextProtocolChatCompletions
}

// OpenAIUpstream resolves compact and ordinary model mappings; passthrough still applies model mapping.
func (p ModelPolicy) OpenAIUpstream(requested string, compact bool) string {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return ""
	}
	if p.RawChat() {
		return p.NormalizeOpenAI(p.ForwardModel(requested, ""))
	}
	if compact && p.Record != nil {
		if model, matched := p.Record.ResolveCompactMappedModel(requested); matched {
			if model = strings.TrimSpace(model); model != "" {
				return model
			}
		}
	}
	model := strings.TrimSpace(p.ForwardModel(requested, ""))
	if model == "" {
		return ""
	}
	if compact {
		compactModel := strings.TrimSpace(provider.ResolveCompactForwardModel(p.Record, model))
		if compactModel != "" && compactModel != model {
			return compactModel
		}
	}
	return p.NormalizeOpenAI(model)
}

func (p ModelPolicy) CanonicalSchedulingModel(requested string) string {
	model := strings.TrimSpace(requested)
	if p.Record == nil || model == "" {
		return model
	}
	if p.Record.IsOpenAI() {
		return p.OpenAIUpstream(model, false)
	}
	if mapped := strings.TrimSpace(p.Mapped(model)); mapped != "" {
		model = mapped
	}
	if p.Record.IsOpenAICompatible() {
		return p.NormalizeOpenAI(model)
	}
	return model
}

func (p ModelPolicy) bedrockInput(model string) *bedrock.RouteInput {
	if p.Record == nil {
		return nil
	}
	return &bedrock.RouteInput{Region: p.Record.GetCredential("aws_region"), ForceGlobal: p.Record.GetCredential("aws_force_global") == "true", Model: p.Mapped(model)}
}

func (p ModelPolicy) Bedrock(model string) (string, bool) {
	return bedrock.ResolveBedrockModelID(p.bedrockInput(model), model)
}

// BedrockRoute applies one provider mapping and resolves Bedrock routing for the configured source region.
func (p ModelPolicy) BedrockRoute(model string) (bedrock.BedrockModelRoute, error) {
	return bedrock.ResolveBedrockModelRoute(p.bedrockInput(model), model)
}

func (p ModelPolicy) AnthropicUpstream(mapped string) string {
	if p.Record == nil {
		return ""
	}
	mapped = strings.TrimSpace(mapped)
	if mapped == "" || p.Record.Platform != capability.PlatformAnthropic || p.Record.Type == capability.ProviderTypeAPIKey || p.Record.IsBedrock() {
		return mapped
	}
	if p.Record.Type == capability.ProviderTypeServiceAccount {
		return vertex.NormalizeVertexAnthropicModelID(mapped)
	}
	return mapped
}

func modelThinking(ctx context.Context) *bool {
	if value, ok := requeststate.ThinkingEnabledFromContext(ctx); ok {
		return &value
	}
	return nil
}

// Supports checks provider support for a model after group mapping.
func (p ModelPolicy) Supports(ctx context.Context, model string) bool {
	value := p.Record
	if value == nil {
		return false
	}
	if value.Platform == capability.PlatformAntigravity {
		if strings.TrimSpace(model) == "" {
			return true
		}
		return p.antigravityModel(model, modelThinking(ctx)) != ""
	}
	if value.IsBedrock() {
		if !p.supportsConfiguredModel(model) {
			return false
		}
		_, ok := p.Bedrock(model)
		return ok
	}
	if value.Platform == capability.PlatformAnthropic && value.Type != capability.ProviderTypeAPIKey {
		mapped := p.forwardMappedModel(model)
		return p.finalModelWhitelisted(p.AnthropicUpstream(mapped))
	}
	return p.supportsConfiguredModel(model)
}

// UpstreamModel resolves the final platform model and records it in the request's model trace.
func (p ModelPolicy) UpstreamModel(ctx context.Context, requested string) string {
	value := p.Record
	if value == nil {
		return ""
	}
	var model string
	if value.IsBedrock() {
		mapped, ok := p.Bedrock(requested)
		if !ok {
			return ""
		}
		model = mapped
	} else if value.Platform == capability.PlatformAntigravity {
		model = p.antigravityModel(requested, modelThinking(ctx))
	} else if value.Platform == capability.PlatformOpenAI || value.Platform == capability.PlatformGrok {
		model = p.OpenAIUpstream(requested, false)
	} else {
		mapped := p.forwardMappedModel(requested)
		if value.Platform == capability.PlatformQoder {
			site, err := qoder.ParseSite(value.GetCredential("site"))
			if err != nil {
				return ""
			}
			model = qoder.ResolveQoderModelForSite(site, mapped).Key
		} else {
			model = p.AnthropicUpstream(mapped)
		}
	}
	model = strings.TrimSpace(model)
	modeltrace.RegisterStage(ctx, model)
	return model
}

// LimitKeys returns the mapped model key and applicable shared image, Fable, or Gemini rate-limit keys.
func (p ModelPolicy) LimitKeys(ctx context.Context, requested string) []string {
	value := p.Record
	if value == nil {
		return nil
	}
	key := p.Mapped(requested)
	switch value.Platform {
	case capability.PlatformOpenAI, capability.PlatformGrok:
		key = p.CanonicalSchedulingModel(requested)
	case capability.PlatformAntigravity:
		key = p.antigravityModel(requested, modelThinking(ctx))
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	keys := []string{key}
	switch value.Platform {
	case capability.PlatformAntigravity:
		if strings.HasPrefix(antigravity.NormalizeAntigravityModelName(key), "gemini-") && key != "antigravity:gemini" {
			keys = append(keys, "antigravity:gemini")
		}
	case capability.PlatformOpenAI:
		images := media.IsImageGenerationModel(requested) || media.IsImageGenerationModel(key) || requeststate.OpenAIImageGenerationIntentFromContext(ctx)
		if images && key != provider.OpenAIImageGenerationRateLimitKey {
			keys = append(keys, provider.OpenAIImageGenerationRateLimitKey)
		}
	case capability.PlatformAnthropic:
		if anthropic.IsAnthropicFableModel(key) && key != provider.AnthropicFableRateLimitKey {
			keys = append(keys, provider.AnthropicFableRateLimitKey)
		}
	}
	return keys
}

func (p ModelPolicy) AllowsModel(ctx context.Context, model string) bool {
	return p.Record.ModelRateLimitAllows(p.LimitKeys(ctx, model))
}

// ListingModels checks plain and thinking Antigravity routes separately and deduplicates in encounter order.
func (p ModelPolicy) ListingModels(ctx context.Context, requested string) []string {
	models := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" {
			return
		}
		key := strings.ToLower(model)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		models = append(models, model)
	}
	if p.Record != nil && p.Record.Platform == capability.PlatformAntigravity {
		plain := requeststate.WithThinkingEnabled(ctx, false)
		if p.AllowsModel(plain, requested) && p.Supports(plain, requested) {
			add(p.UpstreamModel(plain, requested))
		}
		thinking := requeststate.WithThinkingEnabled(ctx, true)
		if p.AllowsModel(thinking, requested) && p.Supports(thinking, requested) {
			add(p.UpstreamModel(thinking, requested))
		}
		return models
	}
	if p.Record == nil || !p.AllowsModel(ctx, requested) {
		return models
	}
	add(p.UpstreamModel(ctx, requested))
	return models
}

// ForwardMappedModels resolves the billing model and upstream model separately, applying compact mapping to the latter.
func (p ModelPolicy) ForwardMappedModels(requested string, compact bool) (billingModel, upstreamModel string) {
	requested = strings.TrimSpace(requested)
	if p.Record != nil {
		billingModel = strings.TrimSpace(p.Mapped(requested))
	}
	if billingModel == "" {
		billingModel = requested
	}
	upstreamModel = p.OpenAIUpstream(requested, compact)
	if strings.TrimSpace(upstreamModel) == "" {
		upstreamModel = billingModel
	}
	return billingModel, upstreamModel
}

// ErrorSchedulingModel prefers the observed upstream model and uses the billing model when it is absent.
func ErrorSchedulingModel(billingModel, upstreamModel string) string {
	if upstream := strings.TrimSpace(upstreamModel); upstream != "" {
		return upstream
	}
	return strings.TrimSpace(billingModel)
}
