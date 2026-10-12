package provider

import (
	"context"
	"slices"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// CandidateSnapshot returns a provider snapshot with the protocols enabled for this attempt.
func (p ModelPolicy) CandidateSnapshot() provider.ProviderSnapshot {
	if p.Record == nil {
		return provider.ProviderSnapshot{}
	}
	snapshot := p.Record.RoutingSnapshot()
	snapshot.EnabledProtocols = slices.Clone(p.Record.UpstreamProtocolsForLegacy(p.protocolTarget().GetAPIProtocol()))
	return snapshot
}

// ProtocolRoute checks the candidate against group protocol rules and returns its upstream protocol.
func (p ModelPolicy) ProtocolRoute(group *routing.Group, source protocol.ProtocolID) (protocol.ProtocolID, bool) {
	if p.Record == nil {
		return "", false
	}
	var projected *routing.Group
	if group != nil {
		projected = &routing.Group{ID: group.ID, SchedulerType: group.SchedulerType, AllowedProtocols: group.AllowedProtocols, ProtocolFallbacks: group.ProtocolFallbacks}
	}
	plan := routing.Plan(routing.PlanInput{Group: projected, ClientProtocol: source})
	candidate, ok := plan.ResolveCandidate(p.CandidateSnapshot())
	return candidate.UpstreamProtocol, ok
}

// AllowsProtocol checks provider eligibility for the request's group and client protocol.
func (p ModelPolicy) AllowsProtocol(ctx context.Context) bool {
	group, _ := requeststate.GroupFromContext(ctx)
	if p.Record == nil || group != nil && (group.RequireOAuthOnly && !p.Record.IsOAuth() || group.RequirePrivacySet && !p.Record.IsPrivacySet()) {
		return false
	}
	source, _ := requeststate.ClientProtocolFromContext(ctx)
	if source == "" {
		return true
	}
	_, ok := p.ProtocolRoute(group, source)
	return ok
}

// Schedulable checks protocol eligibility, provider status, and model rate limits in order.
func (p ModelPolicy) Schedulable(ctx context.Context, model string) bool {
	if p.Record == nil {
		return false
	}
	if !p.AllowsProtocol(ctx) || !p.Record.IsSchedulable() {
		return false
	}
	return p.AllowsModel(ctx, model)
}

// Limited reports whether any applicable model rate-limit window is active.
func (p ModelPolicy) Limited(ctx context.Context, model string) bool {
	return slices.ContainsFunc(p.LimitKeys(ctx, model), p.Record.ModelRateLimitActive)
}

// LimitRemaining returns the longest remaining duration among applicable model rate-limit windows.
func (p ModelPolicy) LimitRemaining(ctx context.Context, model string) time.Duration {
	remaining := time.Duration(0)
	for _, key := range p.LimitKeys(ctx, model) {
		if value := p.Record.ModelRateLimitRemaining(key); value > remaining {
			remaining = value
		}
	}
	return remaining
}

// FinalAntigravityModel resolves the upstream model using this request's thinking setting.
func (p ModelPolicy) FinalAntigravityModel(ctx context.Context, model string) string {
	return provideradapter.FinalAntigravityModel(p.Record, model, modelThinking(ctx))
}
