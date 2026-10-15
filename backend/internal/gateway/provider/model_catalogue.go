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

// model_catalogue.go adapts provider configuration into protocol-aware
// catalogue candidates and attribute targets.
// Model catalogues enumerate saved provider configuration and then check each
// candidate against execution routes. Attribute lookup must keep the client
// protocol separate from the chosen upstream protocol, especially when Compact
// uses a dedicated provider mapping that differs from the ordinary request
// model.
//
// Prepared rules let one catalogue query reuse the same model mapping and
// support data. Provider memberships are copied into the listing input, while
// the policy retains the provider record for protocol and attribute decisions.
// Invocation filtering separates embedding models, image-only models, and
// batch image capability so a model is not displayed for an incompatible
// client operation; request execution still performs its current eligibility
// checks.

// catalogueRules adapts prepared execution model rules to routing catalogue
// queries.
type catalogueRules struct {
	policy ModelPolicy /* prepared catalogue execution policy */
}

// AttributeTarget constructs the attribute identity for a catalogue route with
// both client and upstream protocols. Compact on OpenAI resolves its dedicated
// forwarding model before attribute lookup; other routes use the supplied
// model.
//
// The requested name and model can differ after group or provider mapping, so
// Compact receives requested for its mapping rules. Target.ClientProtocol and
// Target.UpstreamProtocol then record their separate values, allowing
// catalogue attributes to distinguish converter capabilities from the
// provider's selected wire protocol.
func (v catalogueRules) AttributeTarget(requested, model string, source, target capability.ProtocolID) modelcatalog.Target {
	// Compact can send a model selected by its own mapping. Look up attributes
	// under that final model while retaining both protocol identities on the target.
	if source == capability.ProtocolResponsesCompact && v.policy.Record.IsOpenAI() {
		_, model = v.policy.ForwardMappedModels(requested, true)
	}
	value := v.policy.Record.ModelAttributeTarget(model, string(target))
	value.ClientProtocol, value.UpstreamProtocol = string(source), string(target)
	return value
}

// SupportsClientProtocol checks the provider-mapped model's invocation shape
// against a client protocol. Embedding models require the embedding protocol,
// image endpoints require image models, and image-only models are limited to
// Responses or WS outside those endpoints.
//
// Image batches additionally require Gemini image generation. Other
// model/protocol combinations pass this shape check and continue through their
// route and provider eligibility checks. Provider alias expansion happens
// before classification so a client alias receives the shape of its mapped
// upstream target.
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

// ConfiguredModels returns concrete request model candidates from the prepared
// provider rules. The returned slice has separate storage, retaining its nil
// or empty shape, so catalogue consumers can edit the candidate list.
func (v catalogueRules) ConfiguredModels() []string {
	return v.policy.configuredModels()
}

// Mapping returns a copy of the prepared provider model map for catalogue route
// resolution. Nil mappings stay nil, and caller changes to this map leave later
// snapshot matches unchanged.
func (v catalogueRules) Mapping() map[string]string {
	return v.policy.models.Mapping()
}

// Supports delegates catalogue model eligibility to the prepared execution
// model policy.
func (v catalogueRules) Supports(ctx context.Context, model string) bool {
	return v.policy.Supports(ctx, model)
}

// UpstreamModels returns the upstream model variants exposed by the prepared
// listing policy. When no variant qualifies, the returned list is allocated
// and empty. The caller applies surrounding support and protocol checks before
// displaying a returned identity.
func (v catalogueRules) UpstreamModels(ctx context.Context, model string) []string {
	return v.policy.ListingModels(ctx, model)
}

// CatalogueRules captures model rules and returns their routing catalogue
// adapter for repeated candidate queries. Preparing an already prepared policy
// reuses its snapshot; the provider record remains borrowed for other
// decisions.
func (p ModelPolicy) CatalogueRules() routing.CatalogueRules {
	return catalogueRules{policy: p.Prepared()}
}

// CatalogueProvider prepares one nonnil provider for a catalogue query,
// capturing candidate protocols and copying both membership ID forms. The
// policy retains its provider record, so callers keep that record stable for
// the lifetime of the query.
func CatalogueProvider(value *provider.Record, route requeststate.AttemptRoute) routing.CatalogueProvider {
	policy := (ModelPolicy{Record: value, Route: route}).Prepared()
	snapshot := policy.CandidateSnapshot()
	groups := make([]int64, len(value.ProviderGroups))
	for i, g := range value.ProviderGroups {
		groups[i] = g.GroupID
	}
	return routing.CatalogueProvider{ProviderSnapshot: snapshot, GroupIDs: slices.Clone(value.GroupIDs), ProviderGroupIDs: groups, Passthrough: value.IsOpenAIPassthroughEnabled(), Rules: catalogueRules{policy}}
}

// CatalogueProviders prepares each supplied record with an empty attempt route
// for listing. Nil input remains nil, and each output gets prepared model
// rules plus copied membership identifiers for the corresponding provider.
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
