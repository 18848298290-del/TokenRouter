package selection

import (
	"context"
	"sync/atomic"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// free_quota.go filters Grok Free OAuth candidates and publishes the advanced
// picker's shared quota gate.
// Explicit Grok Free OAuth candidates share a soft token-budget gate within
// their selection path. Basic selection uses the app-supplied gate, while an
// advanced picker lazily publishes its own gate so its observations persist
// across selection calls.
//
// Concurrent advanced callers can construct gates before one wins the atomic
// slot. Every caller then reads the published winner, keeping later usage
// decisions on one cache instance. Candidate conversion carries the provider ID
// and verified Free OAuth eligibility; the gate admits unknown usage while
// refreshing it, and filtering retains other provider types in their incoming
// order.

// filterGrokFreeQuotaProviders applies this advanced picker's lazily published
// Free OAuth gate to candidates. Missing picker, service, or factory
// dependencies return the input slice, allowing selection when this optional
// observation is unavailable.
func (s *compatiblePicker) filterGrokFreeQuotaProviders(_ context.Context, providers []gatewayprovider.ExecutionProvider) []gatewayprovider.ExecutionProvider {
	if s == nil || s.service == nil || s.service.newFreeQuotaGate == nil {
		return providers
	}
	gate := loadFreeQuotaGate(&s.freeQuotaGate, s.service.newFreeQuotaGate)
	return filterFreeQuotaProjection(gate, providers)
}

// loadFreeQuotaGate returns the gate already published in slot or constructs a
// candidate through factory. Callers supply a valid atomic slot and factory;
// the factory may be invoked by more than one concurrent caller.
//
// CompareAndSwap decides which constructed gate becomes shared. A losing caller
// reads the winner rather than retaining its own cache, so subsequent filtering
// converges on the same observation state. Factory results are passed through,
// including nil when the factory supplies no gate.
func loadFreeQuotaGate(slot *atomic.Pointer[provider.FreeQuotaGate], factory func() *provider.FreeQuotaGate) *provider.FreeQuotaGate {
	if gate := slot.Load(); gate != nil {
		return gate
	}
	gate := factory()
	// Concurrent factories may finish together. Publish one cache instance and
	// let losing callers use that winner so their quota observations stay shared.
	if slot.CompareAndSwap(nil, gate) {
		return gate
	}
	return slot.Load()
}

// filterGrokFreeQuotaProvidersForGateway applies the generic selector's
// app-supplied Free OAuth gate to candidates. A nil selector or absent gate
// returns the incoming slice for callers using selection without this optional
// quota observation.
func (s *Generic) filterGrokFreeQuotaProvidersForGateway(_ context.Context, providers []gatewayprovider.ExecutionProvider) []gatewayprovider.ExecutionProvider {
	if s == nil {
		return providers
	}
	return filterFreeQuotaProjection(s.freeQuotaGate, providers)
}

// filterFreeQuotaProjection removes candidates the gate identifies as blocked
// Free OAuth providers. A nil gate or nil blocked map returns the original
// slice; a populated result builds a new slice in incoming order, with nested
// provider data still shared.
func filterFreeQuotaProjection(gate *provider.FreeQuotaGate, providers []gatewayprovider.ExecutionProvider) []gatewayprovider.ExecutionProvider {
	if gate == nil {
		return providers
	}
	candidates := make([]provider.FreeQuotaCandidate, len(providers))
	for i := range providers {
		candidates[i] = provider.FreeQuotaCandidate{ID: providers[i].Record.ID, Eligible: provider.IsExplicitGrokFreeOAuthProvider(gatewayprovider.ExecutionProtocolRecord(&providers[i]))}
	}
	blocked := gate.Blocked(candidates)
	if blocked == nil {
		return providers
	}
	filtered := make([]gatewayprovider.ExecutionProvider, 0, len(providers))
	for i, candidate := range candidates {
		if !candidate.Eligible || !blocked[candidate.ID] {
			filtered = append(filtered, providers[i])
		}
	}
	return filtered
}
