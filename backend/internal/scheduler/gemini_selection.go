package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// GeminiSelectionPorts supplies candidate reads, eligibility checks, and result conversion. GeminiSelector owns their order and sticky binding updates.
type GeminiSelectionPorts struct {
	Resolve     func(context.Context, *int64) (string, bool, bool, *FlowGroup, error)
	WithGroup   func(context.Context, *FlowGroup) context.Context
	Effective   func(context.Context, *int64) policy.EffectiveSettings
	Sticky      func(context.Context, *int64, string, string, string, map[int64]struct{}, string, bool) *FlowProvider
	List        func(context.Context, *int64, string, bool) ([]FlowProvider, error)
	Eligible    func(context.Context, []FlowProvider, string, map[int64]struct{}, string, bool) []*FlowProvider
	Advanced    func(context.Context, *int64, string, string, []*FlowProvider, policy.EffectiveSettings) *FlowProvider
	Unsupported func(context.Context, []FlowProvider, string, string, map[int64]struct{}, bool) error
	Hydrate     func(context.Context, *FlowProvider) (*FlowProvider, error)
}

// GeminiSelector selects within the admitted group and forced platform. The caller acquires request slots.
type GeminiSelector struct {
	ports GeminiSelectionPorts
	cache StickyCache
}

func NewGeminiSelector(ports GeminiSelectionPorts, cache StickyCache) *GeminiSelector {
	return &GeminiSelector{ports: ports, cache: cache}
}

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
	if selected == nil {
		if err := s.ports.Unsupported(ctx, values, input.RequestedModel, platform, input.ExcludedIDs, mixed); err != nil {
			return nil, err
		}
		if input.RequestedModel != "" {
			return nil, fmt.Errorf("no available Gemini providers supporting model: %s", input.RequestedModel)
		}
		return nil, errors.New("no available Gemini providers")
	}
	if input.SessionHash != "" {
		var groupID int64
		if input.GroupID != nil {
			groupID = *input.GroupID
		}
		_ = s.cache.SetSessionProviderID(ctx, groupID, cacheKey, selected.ID, time.Hour)
	}
	return s.ports.Hydrate(ctx, selected)
}

// BestGeminiCandidate compares candidates in input order by priority, unused status, OAuth preference among unused candidates, then LRU.
func BestGeminiCandidate(values []*FlowProvider) *FlowProvider {
	var selected *FlowProvider
	for _, value := range values {
		if value != nil && (selected == nil || BetterGeminiCandidate(value, selected)) {
			selected = value
		}
	}
	return selected
}

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
