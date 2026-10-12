package httpapi

import (
	"context"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider/httpapi/dto"
)

// ProviderWithConcurrency includes live concurrency and scheduler display fields for administrators.
type ProviderWithConcurrency struct {
	*dto.Provider
	CurrentConcurrency int                           `json:"current_concurrency"`
	SchedulerScore     *ProviderSchedulerScore       `json:"scheduler_score,omitempty"`
	SchedulerScores    []ProviderSchedulerGroupScore `json:"scheduler_scores,omitempty"`
	// These fields are returned for Anthropic OAuth/Setup Token providers when the corresponding feature is enabled.
	CurrentWindowCost *float64 `json:"current_window_cost,omitempty"` // Cost in the current window.
	ActiveSessions    *int     `json:"active_sessions,omitempty"`     // Active session count.
	CurrentRPM        *int     `json:"current_rpm,omitempty"`         // Requests counted in the current minute.
}

// Provider use cases calculate scheduler display values for JSON encoding here.
type (
	ProviderSchedulerScore      = providercore.ProviderSchedulerScore
	ProviderSchedulerGroupScore = providercore.ProviderSchedulerGroupScore
)

// RuntimePresenter converts runtime and parent-provider data into management DTOs; provider use cases perform queries and threshold calculations.
type RuntimePresenter struct {
	status  *providercore.RuntimeStatusReader
	parents interface {
		GetProvidersByIDs(context.Context, []int64) ([]*providercore.Record, error)
	}
	ollama *providercore.OllamaCloudUsageService
}

func NewRuntimePresenter(status *providercore.RuntimeStatusReader, parents interface {
	GetProvidersByIDs(context.Context, []int64) ([]*providercore.Record, error)
}, ollama *providercore.OllamaCloudUsageService,
) *RuntimePresenter {
	return &RuntimePresenter{status, parents, ollama}
}

func (p *RuntimePresenter) Present(ctx context.Context, v *providercore.Record) ProviderWithConcurrency {
	state := p.status.Read(ctx, v)
	item := p.Project(state)
	p.EnrichShadowParents(ctx, []ProviderWithConcurrency{item})
	return item
}

// EnrichShadowParentInfo fills parent_* fields in items from the supplied parent map.
// It mutates matching shadow DTOs and leaves non-shadows or entries with missing parents unchanged.
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

// EnrichShadowParents loads distinct parent IDs in one batch and fills shadow display fields.
// If the lookup fails, the fields remain unchanged and no error is returned.
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
		return
	}
	pmap := make(map[int64]*providercore.Record, len(parents))
	for _, p := range parents {
		pmap[p.ID] = p
	}
	EnrichShadowParentInfo(items, pmap)
}

// Project converts loaded runtime status into administrator display fields.
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
