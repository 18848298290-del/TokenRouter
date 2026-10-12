package routing

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// RequestableModel describes an eligible client model and its marketplace pricing identity.
type RequestableModel struct {
	AttributeTargets []modelcatalog.Target
	// UpstreamModels contains locally eligible final model names for display.
	UpstreamModels []string
	Protocols      []capability.ProtocolID
	// NativeProtocols contains protocols handled directly by every provider route supporting this model.
	NativeProtocols  []capability.ProtocolID
	ID               string
	PricingModel     string
	PricingAmbiguous bool
}

// RequestableModelsResult contains a group's resolved model catalogue.
// Restricted marks whitelist restriction or a failed resolution, distinguishing those from an unconfigured catalogue.
type RequestableModelsResult struct {
	Models                    []RequestableModel
	Restricted                bool
	HadExplicitProviderModels bool // Indicates whether supplied or provider-configured candidates existed.
}

// CatalogueRules supplies configured models, mapping, and platform eligibility checks.
type CatalogueRules interface {
	ConfiguredModels() []string
	Mapping() map[string]string
	Supports(context.Context, string) bool
	UpstreamModels(context.Context, string) []string
}

// CatalogueProvider is a read-only provider snapshot with group membership and catalogue rules.
type CatalogueProvider struct {
	provider.ProviderSnapshot
	GroupIDs         []int64
	ProviderGroupIDs []int64
	Passthrough      bool
	Rules            CatalogueRules
}
type CataloguePolicies interface {
	GetGroupPolicy(context.Context, int64) (*GroupPolicyView, error)
	GetPricingConfigForGroup(context.Context, int64) (*PricingConfig, error)
}

// RequestableResolver validates configured candidates or selected models against group policy.
// @project-doc docs/interfaces/model_catalog_and_marketplace.md#model_catalog_resolution
type RequestableResolver struct {
	GroupPolicies CataloguePolicies
	Warn          func(string, ...any)
}

// requestableQuery holds rules and finite candidates for one group query.
type requestableQuery struct {
	providers     []CatalogueProvider
	protocols     []capability.ProviderProtocols
	policy        *GroupPolicyView
	billingSource string
	candidates    []string
	result        RequestableModelsResult
}

// ResolveWithProviders validates concrete configured models using the supplied, optionally prefetched providers.
func (s *RequestableResolver) ResolveWithProviders(ctx context.Context, groupID *int64, platform string, baseModels []string, providers []CatalogueProvider) RequestableModelsResult {
	query := s.prepare(ctx, groupID, platform, baseModels, providers)
	result := query.result
	for _, requestedModel := range query.candidates {
		if ctx.Err() != nil {
			return RequestableModelsResult{Restricted: true}
		}
		if resolved, ok := query.resolve(ctx, requestedModel); ok {
			result.Models = append(result.Models, resolved)
		}
	}
	return result
}

// ResolveSelectedWithProviders checks selected names present in the configured candidate set.
func (s *RequestableResolver) ResolveSelectedWithProviders(ctx context.Context, groupID *int64, platform string, selected []string, providers []CatalogueProvider) RequestableModelsResult {
	query := s.prepare(ctx, groupID, platform, nil, providers)
	result := query.result
	seen := make(map[string]bool, len(selected))
	for _, model := range selected {
		if ctx.Err() != nil {
			return RequestableModelsResult{Restricted: true}
		}
		if seen[model] || !slices.Contains(query.candidates, model) {
			continue
		}
		seen[model] = true
		if resolved, ok := query.resolve(ctx, model); ok {
			result.Models = append(result.Models, resolved)
		}
	}
	return result
}

func (s *RequestableResolver) prepare(ctx context.Context, groupID *int64, platform string, baseModels []string, providers []CatalogueProvider) requestableQuery {
	providers = filterRequestableModelProviders(providers, platform)
	query := requestableQuery{providers: providers, billingSource: BillingModelSourceRequested}
	if len(providers) == 0 {
		return query
	}
	currentModels := ConfiguredRequestModelsFromProviders(providers, platform)
	query.result.HadExplicitProviderModels = len(baseModels) > 0 || len(currentModels) > 0
	if groupID != nil && s.GroupPolicies != nil {
		policy, err := s.GroupPolicies.GetGroupPolicy(ctx, *groupID)
		if err != nil {
			if s.Warn != nil {
				s.Warn("failed to load group policy for requestable model resolution", "group_id", *groupID, "platform", platform, "error", err)
			}
			query.result.Restricted = true
			return query
		}
		query.policy = policy
		query.billingSource = BillingModelSourceGroupMapped
		if config, err := s.GroupPolicies.GetPricingConfigForGroup(ctx, *groupID); err == nil && config != nil && config.BillingModelSource != "" {
			query.billingSource = config.BillingModelSource
		}
	}
	query.result.Restricted = query.policy != nil && query.policy.RestrictModels
	configured := make([]string, 0, len(baseModels)+len(currentModels))
	configured = append(configured, baseModels...)
	configured = append(configured, currentModels...)
	query.candidates = mergeRequestableModelCandidates(configured, providers, query.policy)
	query.protocols = make([]capability.ProviderProtocols, len(providers))
	for i := range providers {
		query.protocols[i] = providers[i].Protocols()
	}
	return query
}

// ConfiguredRequestModelsFromProviders returns sorted, deduplicated models supplied by provider rules.
func ConfiguredRequestModelsFromProviders(providers []CatalogueProvider, platform string) []string {
	modelSet := make(map[string]struct{})
	hasConfiguredModels := false
	for i := range providers {
		provider := &providers[i]
		if platform != "" && provider.Platform != platform {
			continue
		}
		requestModels := provider.Rules.ConfiguredModels()
		if len(requestModels) == 0 {
			continue
		}
		hasConfiguredModels = true
		for _, model := range requestModels {
			modelSet[model] = struct{}{}
		}
	}
	if !hasConfiguredModels {
		return nil
	}
	models := make([]string, 0, len(modelSet))
	for model := range modelSet {
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

func filterRequestableModelProviders(providers []CatalogueProvider, platform string) []CatalogueProvider {
	platform = strings.TrimSpace(platform)
	if platform == "" {
		return providers
	}
	filtered := make([]CatalogueProvider, 0, len(providers))
	for i := range providers {
		if matchesCataloguePlatform(&providers[i], platform) {
			filtered = append(filtered, providers[i])
		}
	}
	return filtered
}

// mergeRequestableModelCandidates combines concrete names from provider rules, group policy, and custom lists.
// Wildcard patterns constrain later matching; they are excluded from candidate IDs.
func mergeRequestableModelCandidates(baseModels []string, providers []CatalogueProvider, policy *GroupPolicyView) []string {
	candidates := make([]string, 0, len(baseModels)+16)
	seen := make(map[string]struct{}, len(baseModels)+16)
	appendModels := func(models ...string) {
		for _, model := range models {
			model = strings.TrimSpace(model)
			if model == "" || strings.Contains(model, "*") {
				continue
			}
			key := strings.ToLower(model)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			candidates = append(candidates, model)
		}
	}

	appendModels(baseModels...)
	if policy != nil {
		appendModels(policy.AllowedModels...)
		appendModels(policy.ModelsList...)
		if mapping := policy.ModelMapping; len(mapping) > 0 {
			sources := sortedModelMappingSources(mapping)
			appendModels(sources...)
			// A concrete target remains eligible for validation as a request name even when its wildcard source cannot be
			// enumerated.
			for _, source := range sources {
				appendModels(mapping[source])
			}
		}
	}

	for i := range providers {
		appendModels(sortedModelMappingSources(providers[i].Rules.Mapping())...)
	}

	return candidates
}

func sortedModelMappingSources(mapping map[string]string) []string {
	if len(mapping) == 0 {
		return nil
	}
	models := make([]string, 0, len(mapping))
	for model := range mapping {
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool {
		left := strings.ToLower(models[i])
		right := strings.ToLower(models[j])
		if left != right {
			return left < right
		}
		return models[i] < models[j]
	})
	return models
}

// resolve validates one request model using this query's prepared rules.
func (q *requestableQuery) resolve(ctx context.Context, requestedModel string) (RequestableModel, bool) {
	policy, providers, billingSource := q.policy, q.providers, q.billingSource
	groupMappedModel := requestedModel
	if mapped := strings.TrimSpace(policy.ResolveModel(requestedModel)); mapped != "" {
		groupMappedModel = mapped
	}

	if policy != nil && policy.RestrictModels && policy.RestrictionSource() != BillingModelSourceUpstream {
		pricingModel := ModelForRestriction(policy.RestrictionSource(), requestedModel, groupMappedModel)
		if policy.IsModelRestricted(pricingModel) {
			return RequestableModel{}, false
		}
	}

	upstreamModels := make([]string, 0, len(providers))
	var attributeTargets []modelcatalog.Target
	var protocols []capability.ProtocolID
	// nativeByProtocol is true only when every supporting provider uses the protocol directly.
	nativeByProtocol := map[capability.ProtocolID]bool{}
	for i := range providers {
		provider := &providers[i]
		var candidateProtocols []capability.ProtocolID
		var nativeCandidates []capability.ProtocolID
		targetProtocols := map[capability.ProtocolID]capability.ProtocolID{}
		if policy != nil && policy.AllowedProtocols != nil {
			if policy.RequireOAuthOnly && provider.Type == capability.ProviderTypeAPIKey {
				continue
			}
			for _, source := range policy.AllowedProtocols {
				target, ok := capability.ResolveRoute(q.protocols[i], source, policy.ProtocolFallbacks)
				if !ok {
					continue
				}
				if aware, ok := provider.Rules.(interface {
					SupportsClientProtocol(string, capability.ProtocolID) bool
				}); ok && !aware.SupportsClientProtocol(groupMappedModel, source) {
					continue
				}
				candidateProtocols = append(candidateProtocols, source)
				targetProtocols[source] = target
				if target == source {
					nativeCandidates = append(nativeCandidates, source)
				}
			}
			if len(candidateProtocols) == 0 {
				continue
			}
		}
		if !provider.Rules.Supports(ctx, groupMappedModel) {
			continue
		}
		contributed := false
		for _, upstreamModel := range provider.Rules.UpstreamModels(ctx, groupMappedModel) {
			if policy != nil && policy.RestrictModels && policy.RestrictionSource() == BillingModelSourceUpstream &&
				policy.IsModelRestricted(upstreamModel) {
				continue
			}
			upstreamModels = append(upstreamModels, upstreamModel)
			if aware, ok := provider.Rules.(interface {
				AttributeTarget(string, string, capability.ProtocolID, capability.ProtocolID) modelcatalog.Target
			}); ok {
				for _, source := range candidateProtocols {
					attributeTargets = append(attributeTargets, aware.AttributeTarget(groupMappedModel, upstreamModel, source, targetProtocols[source]))
				}
				if len(candidateProtocols) == 0 {
					attributeTargets = append(attributeTargets, aware.AttributeTarget(groupMappedModel, upstreamModel, "", ""))
				}
			}
			contributed = true
		}
		if !contributed {
			continue
		}
		for _, source := range candidateProtocols {
			native := slices.Contains(nativeCandidates, source)
			if previous, seen := nativeByProtocol[source]; seen {
				nativeByProtocol[source] = previous && native
				continue
			}
			nativeByProtocol[source] = native
			protocols = append(protocols, source)
		}
	}
	if len(upstreamModels) == 0 {
		return RequestableModel{}, false
	}

	var nativeProtocols []capability.ProtocolID
	for _, source := range protocols {
		if nativeByProtocol[source] {
			nativeProtocols = append(nativeProtocols, source)
		}
	}
	slices.Sort(upstreamModels)
	resolved := RequestableModel{ID: requestedModel, Protocols: protocols, NativeProtocols: nativeProtocols, UpstreamModels: slices.Compact(upstreamModels), AttributeTargets: attributeTargets}
	switch billingSource {
	case BillingModelSourceRequested:
		resolved.PricingModel = requestedModel
	case BillingModelSourceUpstream:
		resolved.PricingModel, resolved.PricingAmbiguous = uniquePricingModel(upstreamModels)
	default:
		resolved.PricingModel = groupMappedModel
	}
	return resolved, true
}

func uniquePricingModel(models []string) (string, bool) {
	var selected string
	selectedKey := ""
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if selectedKey == "" {
			selected = model
			selectedKey = key
			continue
		}
		if key != selectedKey {
			return "", true
		}
	}
	return selected, false
}

// RequestableModelIDs returns client model IDs in resolution order.
func RequestableModelIDs(models []RequestableModel) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

// matchesCataloguePlatform applies the optional forced-platform filter.
func matchesCataloguePlatform(provider *CatalogueProvider, platform string) bool {
	return platform == "" || provider.Platform == platform
}
