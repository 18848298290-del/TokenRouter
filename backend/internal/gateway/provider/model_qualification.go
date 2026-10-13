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

// A provider can have a model-specific cooldown and a shared-scope cooldown
// applying to the same request. Qualification checks protocol permission and
// provider health before asking whether those scopes permit execution;
// reporting their remaining wait uses the longest active duration because
// the expiry windows run concurrently.
//
// Candidate snapshots copy enabled protocols for route resolution, while the
// current group determines OAuth, privacy, and protocol requirements. Limit
// inspection remains separate from permission: Antigravity overages can admit
// a model with an active cooldown, so Limited reports raw window activity and
// LimitRemaining reports its observed wait even when Schedulable allows work.

// CandidateSnapshot returns routing provider data with copied enabled
// protocols resolved for the current attempt. Nil records produce an empty
// snapshot; protocol selection applies the target's legacy API protocol when
// needed.
func (p ModelPolicy) CandidateSnapshot() provider.ProviderSnapshot {
	if p.Record == nil {
		return provider.ProviderSnapshot{}
	}
	snapshot := p.Record.RoutingSnapshot()
	snapshot.EnabledProtocols = slices.Clone(p.Record.UpstreamProtocolsForLegacy(p.protocolTarget().GetAPIProtocol()))
	return snapshot
}

// ProtocolRoute checks provider protocol support against the supplied group
// and client protocol using routing.Plan. Nil records return false; the result
// identifies a native or allowed single-step upstream route.
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

// AllowsProtocol checks group OAuth/privacy requirements and resolves the
// client protocol when present. Missing provider records fail; an absent
// client protocol passes after the group requirements are satisfied.
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

// Schedulable applies protocol eligibility, provider scheduling state, and
// applicable model window permission in that order. Nil records fail
// immediately, and each rejected stage prevents later eligibility work.
func (p ModelPolicy) Schedulable(ctx context.Context, model string) bool {
	if p.Record == nil {
		return false
	}
	if !p.AllowsProtocol(ctx) || !p.Record.IsSchedulable() {
		return false
	}
	return p.AllowsModel(ctx, model)
}

// Limited reports whether any model or shared-scope limit selected by
// LimitKeys is active. An empty key set yields false, keeping a missing model
// window distinct from an active one.
func (p ModelPolicy) Limited(ctx context.Context, model string) bool {
	return slices.ContainsFunc(p.LimitKeys(ctx, model), p.Record.ModelRateLimitActive)
}

// LimitRemaining returns the longest observed remaining duration across the
// model and shared-scope windows selected by LimitKeys. Empty, invalid, or
// elapsed windows leave zero, using the provider record's clock for each read.
//
// Several scopes can restrict one request simultaneously. Their waits overlap,
// so adding durations would overstate the cooldown; the maximum identifies the
// last observed expiry. This is raw window evidence, and callers use
// Schedulable separately when overage policy can permit execution during a
// model cooldown.
func (p ModelPolicy) LimitRemaining(ctx context.Context, model string) time.Duration {
	remaining := time.Duration(0)
	for _, key := range p.LimitKeys(ctx, model) {
		if value := p.Record.ModelRateLimitRemaining(key); value > remaining {
			remaining = value
		}
	}
	return remaining
}

// FinalAntigravityModel resolves the upstream model using the request's
// optional thinking hint. It delegates directly to the provider adapter, which
// owns platform mapping and unsupported-model handling.
func (p ModelPolicy) FinalAntigravityModel(ctx context.Context, model string) string {
	return provideradapter.FinalAntigravityModel(p.Record, model, modelThinking(ctx))
}
