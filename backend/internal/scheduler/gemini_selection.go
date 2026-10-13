package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// Gemini selection uses the admitted group and optional forced platform to
// build an eligible provider pool. Basic groups compare numeric priority,
// unused status, authentication kind among unused peers, and exact recency.
// Advanced groups delegate scoring with the effective settings. Basic mode
// tries hard session reuse first, and advanced mode does so when weighted
// affinity is disabled.
//
// The core chooses metadata without occupying request slots or registering
// sessions. Adapter callbacks preserve execution-record associations through
// eligibility checks and hydration. A newly selected provider can be written
// to the session cache before hydration, so a later hydration failure can
// follow a successful binding write. Callers acquire execution capacity
// separately and handle the final provider error under their own request
// lifecycle.

// GeminiSelectionPorts supplies synchronous group, eligibility, ranking, and
// hydration callbacks for one invocation.
type GeminiSelectionPorts struct {
	// Resolve returns the admitted group with platform, mixed, and forced
	// selection flags. Selection errors stop processing before sticky lookup or
	// candidate reads.
	// admitted group and platform resolution
	Resolve func(context.Context, *int64) (string, bool, bool, *FlowGroup, error)
	// WithGroup attaches the resolved group when one is supplied. Later
	// eligibility and effective-policy callbacks use this context and the same
	// per-invocation group association.
	// admitted group context attachment
	WithGroup func(context.Context, *FlowGroup) context.Context
	// Effective reads the request's resolved scheduler settings. SelectOnly calls
	// it for either mode, then uses weighted-affinity policy to decide whether
	// hard sticky reuse runs first.
	// effective advanced scheduling policy
	Effective func(context.Context, *int64) policy.EffectiveSettings
	// Sticky returns an eligible bound provider or nil to continue selection. Its
	// adapter performs the required sticky-provider checks, and an accepted
	// result returns before listing candidates.
	// hard session reuse lookup
	Sticky func(context.Context, *int64, string, string, string, map[int64]struct{}, string, bool) *FlowProvider
	// List returns candidate values for the admitted group and platform
	// restriction. Candidate read failures stop selection; later callbacks use
	// the same invocation's record associations.
	// group provider candidate lookup
	List func(context.Context, *int64, string, bool) ([]FlowProvider, error)
	// Eligible filters candidate values by request model, exclusions, platform,
	// and mixed policy. Returned provider pointers belong to this invocation and
	// are passed to basic or advanced ranking.
	// model and policy eligibility filtering
	Eligible func(context.Context, []FlowProvider, string, map[int64]struct{}, string, bool) []*FlowProvider
	// Advanced chooses from eligible metadata using the supplied effective
	// settings. A nil result enters unsupported-model diagnosis and the
	// no-provider error path.
	// advanced metadata ranking callback
	Advanced func(context.Context, *int64, string, string, []*FlowProvider, policy.EffectiveSettings) *FlowProvider
	// Unsupported may return a specific model-availability error after ranking
	// yields no provider. A nil error lets SelectOnly produce its ordinary
	// model-qualified or generic no-provider failure.
	// empty pool model diagnosis
	Unsupported func(context.Context, []FlowProvider, string, string, map[int64]struct{}, bool) error
	// Hydrate returns the final provider metadata associated with its completed
	// execution record. It runs after the optional binding write, and its errors
	// are returned to the selection caller.
	// selected execution record completion
	Hydrate func(context.Context, *FlowProvider) (*FlowProvider, error)
}

// GeminiSelector chooses request-scoped provider metadata through shared
// policy and sticky-cache callbacks.
type GeminiSelector struct {
	// ports is supplied by the adapter that retains execution-record
	// associations. Callbacks run synchronously within SelectOnly and use the
	// same lookup scope.
	ports GeminiSelectionPorts // per-invocation selection callback bindings
	// cache receives a binding after ranking when SessionHash is nonempty.
	// Callers supply a usable cache for that path and keep it available through
	// selection.
	cache StickyCache // shared session provider bindings
}

// NewGeminiSelector binds metadata selection callbacks and a shared sticky
// cache. The caller supplies one invocation's record associations and a usable
// cache whenever session input can cause a binding write.
func NewGeminiSelector(ports GeminiSelectionPorts, cache StickyCache) *GeminiSelector {
	return &GeminiSelector{ports: ports, cache: cache}
}

// SelectOnly chooses provider metadata within the resolved group and platform
// restriction. It tries hard sticky reuse for basic mode or disabled weighted
// affinity, then lists and filters candidates before basic comparison or
// advanced ranking. It returns specific unsupported-model errors when the
// adapter provides them.
//
// The execution caller acquires request slots separately from this metadata
// selection. A newly ranked provider with a nonempty session installs a
// one-hour binding before final hydration; cache write errors are ignored, while
// hydration errors are returned even if that binding was already written.
// Adapter callbacks retain each successive record's association so the final
// result corresponds to the hydrated execution provider.
func (s *GeminiSelector) SelectOnly(ctx context.Context, input SelectionInput) (*FlowProvider, error) {
	platform, mixed, forced, group, err := s.ports.Resolve(ctx, input.GroupID)
	if err != nil {
		return nil, err
	}
	if group != nil {
		ctx = s.ports.WithGroup(ctx, group)
	}
	cacheKey := "gemini:" + input.SessionHash
	advanced := group != nil && group.UsesAdvancedScheduler()
	settings := s.ports.Effective(ctx, input.GroupID)
	// A basic group or disabled weighted affinity gives its eligible binding
	// first choice. A rejected binding leaves the ordinary candidate pool
	// available.
	if !advanced || !settings.StickyWeightedEnabled {
		if value := s.ports.Sticky(ctx, input.GroupID, input.SessionHash, cacheKey, input.RequestedModel, input.ExcludedIDs, platform, mixed); value != nil {
			return value, nil
		}
	}
	values, err := s.ports.List(ctx, input.GroupID, platform, forced)
	if err != nil {
		return nil, fmt.Errorf("query providers failed: %w", err)
	}
	eligible := s.ports.Eligible(ctx, values, input.RequestedModel, input.ExcludedIDs, platform, mixed)
	var selected *FlowProvider
	if advanced {
		selected = s.ports.Advanced(ctx, input.GroupID, input.SessionHash, cacheKey, eligible, settings)
	} else {
		selected = BestGeminiCandidate(eligible)
	}
	// Diagnose the unselected model from the original listed pool. Filtering may
	// have removed the records needed to explain an unsupported name before a
	// generic no-provider error is returned.
	if selected == nil {
		if err := s.ports.Unsupported(ctx, values, input.RequestedModel, platform, input.ExcludedIDs, mixed); err != nil {
			return nil, err
		}
		if input.RequestedModel != "" {
			return nil, fmt.Errorf("no available Gemini providers supporting model: %s", input.RequestedModel)
		}
		return nil, errors.New("no available Gemini providers")
	}
	// Establish affinity from the ranked candidate before completing its execution
	// record. A failed cache write leaves selection usable, while a later hydration error can follow this write.
	if input.SessionHash != "" {
		var groupID int64
		if input.GroupID != nil {
			groupID = *input.GroupID
		}
		_ = s.cache.SetSessionProviderID(ctx, groupID, cacheKey, selected.ID, time.Hour)
	}
	return s.ports.Hydrate(ctx, selected)
}

// BestGeminiCandidate returns the best nonnil candidate while retaining input
// pointers. An empty or all-nil pool returns nil; candidates tied under
// BetterGeminiCandidate retain the first record's position as the selected
// result.
func BestGeminiCandidate(values []*FlowProvider) *FlowProvider {
	var selected *FlowProvider
	for _, value := range values {
		if value != nil && (selected == nil || BetterGeminiCandidate(value, selected)) {
			selected = value
		}
	}
	return selected
}

// BetterGeminiCandidate reports whether candidate wins Gemini's basic metadata
// comparison against current. Both pointers are nonnil, and lower numeric
// priority takes precedence over every recency or kind observation. Nil use
// timestamps represent unused records and precede populated timestamps.
//
// OAuth preference breaks a tie only when both records are unused. When both
// have recorded use times, their exact timestamps decide and equal times leave
// the current record selected. This keeps a provider's authentication kind
// from overriding the recency decision for already-used candidates.
func BetterGeminiCandidate(candidate, current *FlowProvider) bool {
	if candidate.Priority < current.Priority {
		return true
	}
	if candidate.Priority > current.Priority {
		return false
	}
	switch {
	case candidate.LastUsedAt == nil && current.LastUsedAt != nil:
		return true
	case candidate.LastUsedAt != nil && current.LastUsedAt == nil:
		return false
	case candidate.LastUsedAt == nil && current.LastUsedAt == nil:
		return candidate.Type == capability.ProviderTypeOAuth && current.Type != capability.ProviderTypeOAuth
	default:
		return candidate.LastUsedAt.Before(*current.LastUsedAt)
	}
}
