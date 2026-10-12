package provider

import (
	"context"
	"slices"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

type catalogueRules struct{ policy ModelPolicy }

func (v catalogueRules) AttributeTarget(requested, model string, source, target capability.ProtocolID) modelcatalog.Target {
	if source == capability.ProtocolResponsesCompact && v.policy.Record.IsOpenAI() {
		_, model = v.policy.ForwardMappedModels(requested, true)
	}
	value := v.policy.Record.ModelAttributeTarget(model, string(target))
	value.ClientProtocol, value.UpstreamProtocol = string(source), string(target)
	return value
}

// SupportsClientProtocol filters models by the invocation shapes supported by the adapters.
// Provider aliases are expanded before checking embedding, image, and conversational protocols.
func (v catalogueRules) SupportsClientProtocol(model string, source capability.ProtocolID) bool {
	model = v.policy.Mapped(model)
	embedding := strings.HasPrefix(strings.ToLower(model), "text-embedding-")
	if embedding || source == capability.ProtocolEmbeddings {
		return embedding && source == capability.ProtocolEmbeddings
	}
	image := upstream.IsGPTImageGenerationModel(model) || upstream.IsGrokImageGenerationModel(model)
	if source == capability.ProtocolImagesGenerations || source == capability.ProtocolImagesEdits {
		return image
	}
	if image && source != capability.ProtocolOpenAIResponses && source != capability.ProtocolResponsesWebSocket {
		return false
	}
	if source == capability.ProtocolImageBatches {
		return upstream.IsGeminiImageGenerationModel(model)
	}
	return true
}

func (v catalogueRules) ConfiguredModels() []string {
	return v.policy.configuredModels()
}

func (v catalogueRules) Mapping() map[string]string {
	return v.policy.models.Mapping()
}

func (v catalogueRules) Supports(ctx context.Context, model string) bool {
	return v.policy.Supports(ctx, model)
}

func (v catalogueRules) UpstreamModels(ctx context.Context, model string) []string {
	return v.policy.ListingModels(ctx, model)
}

// CatalogueRules captures reusable model rules for catalogue queries.
func (p ModelPolicy) CatalogueRules() routing.CatalogueRules {
	return catalogueRules{policy: p.Prepared()}
}

// CatalogueProvider prepares provider metadata and model rules for one catalogue query.
func CatalogueProvider(value *provider.Record, route requeststate.AttemptRoute) routing.CatalogueProvider {
	policy := (ModelPolicy{Record: value, Route: route}).Prepared()
	snapshot := policy.CandidateSnapshot()
	groups := make([]int64, len(value.ProviderGroups))
	for i, g := range value.ProviderGroups {
		groups[i] = g.GroupID
	}
	return routing.CatalogueProvider{ProviderSnapshot: snapshot, GroupIDs: slices.Clone(value.GroupIDs), ProviderGroupIDs: groups, Passthrough: value.IsOpenAIPassthroughEnabled(), Rules: catalogueRules{policy}}
}

func CatalogueProviders(values []provider.Record) []routing.CatalogueProvider {
	if values == nil {
		return nil
	}
	out := make([]routing.CatalogueProvider, len(values))
	for i := range values {
		out[i] = CatalogueProvider(&values[i], requeststate.AttemptRoute{})
	}
	return out
}
