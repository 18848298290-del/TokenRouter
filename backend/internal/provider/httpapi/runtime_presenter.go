package httpapi

import (
	"context"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider/httpapi/dto"
)

// Provider administration combines redacted configuration with runtime fields
// calculated by the provider use cases. Parent account hints are filled in
// place for loaded credential shadows; failed parent lookup leaves the response
// usable with its existing fields, while Ollama presentation enriches the
// already mapped usage state.

// ProviderWithConcurrency combines a redacted provider view with live
// scheduling and usage display observations.
type ProviderWithConcurrency struct {
	// Provider is shared when this wrapper is copied. Parent enrichment mutates
	// the pointed-to DTO, so callers finish those edits before encoding any copy.
	*dto.Provider          // shared redacted provider view
	CurrentConcurrency int `json:"current_concurrency"` // current occupied provider request slots
	// SchedulerScore, SchedulerScores, CurrentWindowCost, ActiveSessions and
	// CurrentRPM retain pointers or slice storage from the loaded RuntimeStatus.
	// Callers keep all five observations stable until response encoding finishes.
	// optional filter-pool scheduler score
	SchedulerScore *ProviderSchedulerScore `json:"scheduler_score,omitempty"`
	// optional per-group scheduler scores
	SchedulerScores []ProviderSchedulerGroupScore `json:"scheduler_scores,omitempty"`
	// CurrentWindowCost, ActiveSessions and CurrentRPM appear when the runtime use
	// case obtains their configured Anthropic OAuth or Setup Token observations.
	// Missing optional observations remain omitted in JSON.
	CurrentWindowCost *float64 `json:"current_window_cost,omitempty"` // USD
	ActiveSessions    *int     `json:"active_sessions,omitempty"`     // current active session count
	CurrentRPM        *int     `json:"current_rpm,omitempty"`         // current counted requests per minute
}

// Provider use cases calculate these scheduler observations before the
// presenter borrows them for administrator JSON output, with no further scoring.
type (
	// ProviderSchedulerScore is the provider use case's filter-pool scheduling
	// observation, reused here for administrator response encoding.
	ProviderSchedulerScore = providercore.ProviderSchedulerScore
	// ProviderSchedulerGroupScore is the provider use case's group-specific
	// scheduling observation, reused here for administrator response encoding.
	ProviderSchedulerGroupScore = providercore.ProviderSchedulerGroupScore
)

// RuntimePresenter borrows status readers and parent lookup to build
// administrator provider responses. Its status, parents and optional ollama
// dependencies stay usable until presentation calls finish; their owners manage
// shutdown separately.
type RuntimePresenter struct {
	status *providercore.RuntimeStatusReader // borrowed runtime observation reader
	// parents is borrowed for shadow hints. Lookup failures are suppressed, so
	// omitted hints do not prevent a provider list or detail response.
	parents interface { // borrowed batch parent reader
		// GetProvidersByIDs reads loaded parent records for the requested identifiers.
		// Missing records are skipped during enrichment, and a read error leaves
		// existing response hints unchanged in the presenter.
		GetProvidersByIDs(context.Context, []int64) ([]*providercore.Record, error)
	}
	ollama *providercore.OllamaCloudUsageService // optional Ollama response-state enrichment
}

// NewRuntimePresenter borrows runtime status, parent reads and optional Ollama
// usage enrichment. The caller keeps these dependencies available while
// responses are built; this constructor neither loads provider data nor starts
// maintenance.
func NewRuntimePresenter(status *providercore.RuntimeStatusReader, parents interface {
	// GetProvidersByIDs reads loaded parent records for the requested identifiers.
	// Missing records are skipped during enrichment, and a read error leaves
	// existing response hints unchanged in the presenter.
	GetProvidersByIDs(context.Context, []int64) ([]*providercore.Record, error)
}, ollama *providercore.OllamaCloudUsageService,
) *RuntimePresenter {
	return &RuntimePresenter{status, parents, ollama}
}

// Present reads runtime observations and returns a redacted management view for
// the supplied provider. Parent enrichment uses a copied wrapper with the same
// embedded DTO pointer, so its in-place hint updates are visible in the
// returned item.
func (p *RuntimePresenter) Present(ctx context.Context, v *providercore.Record) ProviderWithConcurrency {
	state := p.status.Read(ctx, v)
	item := p.Project(state)
	p.EnrichShadowParents(ctx, []ProviderWithConcurrency{item})
	return item
}

// EnrichShadowParentInfo fills parent account and privacy hints in matching
// shadow DTOs. Nil DTOs, ordinary providers and missing parents are skipped;
// matching fields are overwritten from the supplied parent record. Wrapper
// copies share those DTOs, so callers synchronize edits and keep parent maps
// and records stable throughout enrichment.
func EnrichShadowParentInfo(items []ProviderWithConcurrency, parents map[int64]*providercore.Record) {
	for i := range items {
		a := items[i].Provider
		if a == nil || a.ParentProviderID == nil {
			continue
		}
		p := parents[*a.ParentProviderID]
		if p == nil {
			continue
		}
		a.ParentEmail = p.GetCredential("email")
		a.ParentPlanType = p.GetCredential("plan_type")
		a.ParentSubscriptionExpiresAt = p.GetCredential("subscription_expires_at")
		a.ParentChatGPTAccountID = p.GetCredential("chatgpt_account_id")
		a.ParentPrivacyMode = p.GetExtraString("privacy_mode")
	}
}

// EnrichShadowParents reads distinct parent IDs as one batch and updates
// matching DTO pointers in place. A failed parent read is suppressed, leaving
// existing hint fields available; callers synchronize mutable item and record
// access.
func (p *RuntimePresenter) EnrichShadowParents(ctx context.Context, items []ProviderWithConcurrency) {
	seen := make(map[int64]struct{})
	for i := range items {
		a := items[i].Provider
		if a == nil || a.ParentProviderID == nil {
			continue
		}
		seen[*a.ParentProviderID] = struct{}{}
	}
	if len(seen) == 0 {
		return
	}
	parentIDs := make([]int64, 0, len(seen))
	for pid := range seen {
		parentIDs = append(parentIDs, pid)
	}
	parents, err := p.parents.GetProvidersByIDs(ctx, parentIDs)
	if err != nil {
		// Parent hints supplement the usable provider view. Retain existing fields
		// when a batch read fails so optional account metadata cannot suppress the response.
		return
	}
	pmap := make(map[int64]*providercore.Record, len(parents))
	for _, p := range parents {
		pmap[p.ID] = p
	}
	EnrichShadowParentInfo(items, pmap)
}

// Project maps loaded runtime state to a management view and applies optional
// Ollama display enrichment. A nil mapped record returns the wrapper as built;
// runtime pointer and slice observations remain borrowed during response
// encoding.
func (p *RuntimePresenter) Project(state providercore.RuntimeStatus) ProviderWithConcurrency {
	item := ProviderWithConcurrency{Provider: dto.ProviderFromRecord(state.Record), CurrentConcurrency: state.CurrentConcurrency, CurrentWindowCost: state.CurrentWindowCost, ActiveSessions: state.ActiveSessions, CurrentRPM: state.CurrentRPM, SchedulerScore: state.SchedulerScore, SchedulerScores: state.SchedulerScores}
	if item.Provider == nil {
		return item
	}
	if p.ollama != nil {
		p.ollama.EnrichState(item.OllamaCloudUsage)
	}
	return item
}
