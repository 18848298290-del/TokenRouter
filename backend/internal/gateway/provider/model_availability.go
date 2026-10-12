package provider

import (
	"context"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// AvailabilityProviders reads candidate providers from persistent configuration.
type AvailabilityProviders interface {
	ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]provider.Record, error)
}

type modelRejectionRules struct{ *provider.Record }

// NewModelAvailability binds provider queries and group model mapping.
// @project-doc docs/architecture/provider_scheduling_and_cache.md#advanced_scheduler_selection
func NewModelAvailability(source AvailabilityProviders, groupPolicies *routing.PricingConfigService, compatible bool) *routing.ModelAvailability {
	result := &routing.ModelAvailability{
		MapModel: groupPolicies.ResolveRoutingModel,
	}
	if source == nil {
		return result
	}
	result.Read = func(ctx context.Context, group *int64, platforms []string, grouped bool) ([]routing.AvailabilityProvider, error) {
		if forced, ok := apikey.ForcePlatformFromContext(ctx); ok && strings.TrimSpace(forced) != "" {
			platforms = []string{forced}
		}
		values, err := source.ListModelAvailabilityCandidates(ctx, group, platforms, grouped)
		if err != nil {
			return nil, err
		}
		out := make([]routing.AvailabilityProvider, len(values))
		for i := range values {
			record := &values[i]
			out[i] = routing.AvailabilityProvider{
				Platform: record.Platform,
				Supports: func(ctx context.Context, model string) bool {
					policy := ModelPolicy{Record: record}
					if !policy.AllowsProtocol(ctx) {
						return false
					}
					if compatible {
						return policy.SupportsCompatibleRouting(ctx, model)
					}
					return policy.Supports(ctx, model)
				},
			}
		}
		return out, nil
	}
	return result
}

// SupportsCompatibleRouting applies platform model rules, including model scopes for passthrough providers.
func (p ModelPolicy) SupportsCompatibleRouting(ctx context.Context, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return true
	}
	if p.Record == nil {
		return false
	}
	return p.Supports(ctx, model)
}

func (r modelRejectionRules) GetConfiguredRequestModels() []string {
	return r.Record.GetConfiguredRequestModels(provideradapter.ModelDefaults())
}

func (r modelRejectionRules) IsModelSupported(model string) bool {
	return r.Record.IsModelSupported(model, provideradapter.ModelDefaults(), provideradapter.ModelRules(r.Record))
}

// ModelRejectionProvider supplies routing with provider data for model rejection checks.
func ModelRejectionProvider(value *provider.Record) routing.ModelRejectionSource {
	return routing.ModelRejectionSource{Platform: value.Platform, Rules: modelRejectionRules{value}, Defaults: func(platform string) ([]string, error) {
		return provideradapter.DefaultProviderModels(value), nil
	}}
}
