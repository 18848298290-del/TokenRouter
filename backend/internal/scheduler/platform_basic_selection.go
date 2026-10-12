package scheduler

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// selectBasicOnlyRoutes applies basic stickiness, eligibility rechecks, and LRU selection without acquiring slots.
func (s *PlatformSelector) selectBasicOnlyRoutes(ctx context.Context, groupID *int64, platform string, sessionHash string, requestedModel string, routingModel string, excludedIDs map[int64]struct{}, requireCompact bool, stickyProviderID int64, requiredCapability provider.OpenAIEndpointCapability) (*FlowProvider, error) {
	platform = strings.TrimSpace(platform)
	if s.ports.CheckPricing(ctx, groupID, requestedModel) {
		s.diagnostics.event("warn", "group model restriction blocked request",
			"group_id", derefGroupID(groupID),
			"model", requestedModel)
		return nil, fmt.Errorf("%w supporting model: %s (group model restriction)", ErrNoAvailableProviders, requestedModel)
	}

	if provider := s.tryBasicSticky(ctx, groupID, platform, sessionHash, requestedModel, routingModel, excludedIDs, requireCompact, stickyProviderID, requiredCapability); provider != nil {
		return provider, nil
	}

	providers, err := s.ports.ListCandidates(ctx, groupID, platform)
	if err != nil {
		return nil, fmt.Errorf("query providers failed: %w", err)
	}

	selected, compactBlocked := s.SelectBestBasic(ctx, groupID, platform, providers, requestedModel, routingModel, excludedIDs, requireCompact, requiredCapability)

	if selected == nil {
		return nil, s.ports.Unavailable(ctx, requestedModel, routingModel, compactBlocked, "", providers)
	}

	hydrated, err := s.ports.Hydrate(ctx, selected)
	if err != nil {
		return nil, err
	}

	if sessionHash != "" {
		_ = s.ports.SetSticky(ctx, groupID, sessionHash, selected.ID, s.ports.BasicStickyTTL)
	}

	return hydrated, nil
}

func (s *PlatformSelector) tryBasicSticky(ctx context.Context, groupID *int64, platform string, sessionHash, requestedModel string, routingModel string, excludedIDs map[int64]struct{}, requireCompact bool, stickyProviderID int64, requiredCapability provider.OpenAIEndpointCapability) *FlowProvider {
	if sessionHash == "" {
		return nil
	}
	platform = strings.TrimSpace(platform)

	providerID := stickyProviderID
	if providerID <= 0 {
		var err error
		providerID, err = s.ports.GetSticky(ctx, groupID, sessionHash)
		if err != nil || providerID <= 0 {
			return nil
		}
	}

	if _, excluded := excludedIDs[providerID]; excluded {
		return nil
	}

	provider, err := s.ports.GetSchedulable(ctx, providerID)
	if err != nil {
		return nil
	}

	// Clear bindings that are invalid for this provider or group.
	if s.ports.ClearSticky(provider, routingModel) || !s.ports.MatchesGroup(provider, groupID) {
		_ = s.ports.DeleteSticky(ctx, groupID, sessionHash)
		return nil
	}

	// Recheck the bound provider against this request's requirements.
	if !s.ports.BasicEligible(ctx, provider, platform, routingModel, false, requiredCapability) {
		return nil
	}
	if !s.ports.PrivacyAllowed(ctx, groupID, provider) {
		return nil
	}
	if !s.ports.ShadowAllowed(ctx, provider) || !s.ports.ParentHealthy(provider, s.ports.ParentLookup(ctx)) {
		_ = s.ports.DeleteSticky(ctx, groupID, sessionHash)
		return nil
	}
	if s.ports.RuntimeBlocked(provider, routingModel) {
		_ = s.ports.DeleteSticky(ctx, groupID, sessionHash)
		return nil
	}
	provider = s.ports.Recheck(ctx, provider, groupID, platform, routingModel, requireCompact, requiredCapability)
	if provider == nil || !s.ports.MatchesGroup(provider, groupID) {
		_ = s.ports.DeleteSticky(ctx, groupID, sessionHash)
		return nil
	}
	if groupID != nil && s.ports.NeedsGroupCheck(ctx, groupID) &&
		s.ports.GroupModelRestricted(ctx, *groupID, provider, routingModel, requireCompact) {
		_ = s.ports.DeleteSticky(ctx, groupID, sessionHash)
		return nil
	}

	// Refresh the accepted binding's TTL.

	_ = s.ports.RefreshSticky(ctx, groupID, sessionHash, s.ports.BasicStickyTTL)
	return provider
}

func (s *PlatformSelector) SelectBestBasic(ctx context.Context, groupID *int64, platform string, providers []FlowProvider, requestedModel string, routingModel string, excludedIDs map[int64]struct{}, requireCompact bool, requiredCapability provider.OpenAIEndpointCapability) (*FlowProvider, bool) {
	platform = strings.TrimSpace(platform)
	compactBlocked := false
	needsUpstreamCheck := s.ports.NeedsGroupCheck(ctx, groupID)
	eligible := make([]*FlowProvider, 0, len(providers))

	for i := range providers {
		acc := &providers[i]

		if _, excluded := excludedIDs[acc.ID]; excluded {
			continue
		}

		fresh := s.ports.Fresh(ctx, acc, platform, routingModel, false, requiredCapability)
		if fresh == nil {
			continue
		}
		fresh = s.ports.Recheck(ctx, fresh, groupID, platform, routingModel, false, requiredCapability)
		if fresh == nil {
			continue
		}
		if !s.ports.PrivacyAllowed(ctx, groupID, fresh) {
			continue
		}
		if needsUpstreamCheck && s.ports.GroupModelRestricted(ctx, *groupID, fresh, routingModel, requireCompact) {
			continue
		}
		if requireCompact && !s.ports.CompactAllowed(fresh) {
			compactBlocked = true
			continue
		}

		eligible = append(eligible, fresh)
	}

	if len(eligible) == 0 {
		return nil, compactBlocked
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		a, b := eligible[i], eligible[j]
		return s.isBetterBasic(a, b)
	})
	return eligible[0], compactBlocked
}

func (s *PlatformSelector) isBetterBasic(candidate, current *FlowProvider) bool {
	// Lower numeric priority wins.
	if candidate.Priority < current.Priority {
		return true
	}
	if candidate.Priority > current.Priority {
		return false
	}

	// At equal priority, prefer unused providers, then the least recently used.
	switch {
	case candidate.LastUsedAt == nil && current.LastUsedAt != nil:

		return true
	case candidate.LastUsedAt != nil && current.LastUsedAt == nil:

		return false
	case candidate.LastUsedAt == nil && current.LastUsedAt == nil:
		// Keep input order when neither provider has been used.
		return false
	default:

		return candidate.LastUsedAt.Before(*current.LastUsedAt)
	}
}

func (s *PlatformSelector) selectBasicRoutes(ctx context.Context, groupID *int64, platform string, sessionHash string, requestedModel string, routingModel string, excludedIDs map[int64]struct{}, requireCompact bool, requiredCapability provider.OpenAIEndpointCapability) (*FlowSelection, error) {
	platform = strings.TrimSpace(platform)
	if s.ports.CheckPricing(ctx, groupID, requestedModel) {
		s.diagnostics.event("warn", "group model restriction blocked request",
			"group_id", derefGroupID(groupID),
			"model", requestedModel)
		return nil, fmt.Errorf("%w supporting model: %s (group model restriction)", ErrNoAvailableProviders, requestedModel)
	}

	cfg := s.ports.Options()
	needsUpstreamCheck := s.ports.NeedsGroupCheck(ctx, groupID)
	var stickyProviderID int64
	if sessionHash != "" && s.ports.CacheAvailable {
		if providerID, err := s.ports.GetSticky(ctx, groupID, sessionHash); err == nil {
			stickyProviderID = providerID
		}
	}
	if s.concurrency == nil || !cfg.LoadBatchEnabled {
		provider, err := s.selectBasicOnlyRoutes(ctx, groupID, platform, sessionHash, requestedModel, routingModel, excludedIDs, requireCompact, stickyProviderID, requiredCapability)
		if err != nil {
			return nil, err
		}
		if !s.ports.PrivacyAllowed(ctx, groupID, provider) {
			return nil, s.ports.Unavailable(ctx, requestedModel, routingModel, false, "", nil)
		}
		result, err := s.ports.Acquire(ctx, provider.ID, provider.Concurrency)
		if err == nil && result != nil && result.Acquired {
			return s.ports.CompleteAcquired(ctx, provider, result.ReleaseFunc)
		}
		if stickyProviderID > 0 && stickyProviderID == provider.ID && s.concurrency != nil {
			waitingCount, _ := s.concurrency.GetProviderWaitingCount(ctx, provider.ID)
			if waitingCount < cfg.StickySessionMaxWaiting {
				return s.ports.Complete(ctx, provider, false, nil, &ProviderWaitPlan{
					ProviderID:     provider.ID,
					MaxConcurrency: provider.Concurrency,
					Timeout:        cfg.StickySessionWaitTimeout,
					MaxWaiting:     cfg.StickySessionMaxWaiting,
				})
			}
		}
		return s.ports.Complete(ctx, provider, false, nil, &ProviderWaitPlan{
			ProviderID:     provider.ID,
			MaxConcurrency: provider.Concurrency,
			Timeout:        cfg.FallbackWaitTimeout,
			MaxWaiting:     cfg.FallbackMaxWaiting,
		})
	}

	providers, err := s.ports.ListCandidates(ctx, groupID, platform)
	if err != nil {
		return nil, err
	}
	if len(providers) == 0 {
		return nil, s.ports.Unavailable(
			ctx,
			requestedModel,
			routingModel,
			false,
			PlatformFilterStats{}.Summary(""),
			providers,
		)
	}

	isExcluded := func(providerID int64) bool {
		if excludedIDs == nil {
			return false
		}
		_, excluded := excludedIDs[providerID]
		return excluded
	}

	// When the sticky provider's bounded queue is full, this request may use another provider.
	// The persistent session binding continues to point to the sticky provider.
	stickySpillover := false
	if sessionHash != "" {
		providerID := stickyProviderID
		if providerID > 0 && !isExcluded(providerID) {
			provider, err := s.ports.GetSchedulable(ctx, providerID)
			if err == nil {
				clearSticky := s.ports.ClearSticky(provider, routingModel) || !s.ports.MatchesGroup(provider, groupID)
				if clearSticky {
					_ = s.ports.DeleteSticky(ctx, groupID, sessionHash)
				}
				if !clearSticky && s.ports.BasicEligible(ctx, provider, platform, routingModel, false, requiredCapability) && s.ports.PrivacyAllowed(ctx, groupID, provider) {
					provider = s.ports.Recheck(ctx, provider, groupID, platform, routingModel, requireCompact, requiredCapability)
					if provider == nil {
						_ = s.ports.DeleteSticky(ctx, groupID, sessionHash)
					} else if !s.ports.MatchesGroup(provider, groupID) {
						_ = s.ports.DeleteSticky(ctx, groupID, sessionHash)
					} else if s.ports.RuntimeBlocked(provider, routingModel) {
						_ = s.ports.DeleteSticky(ctx, groupID, sessionHash)
					} else if needsUpstreamCheck && s.ports.GroupModelRestricted(ctx, *groupID, provider, routingModel, requireCompact) {
						_ = s.ports.DeleteSticky(ctx, groupID, sessionHash)
					} else if !s.ports.ShadowAllowed(ctx, provider) || !s.ports.ParentHealthy(provider, s.ports.ParentLookup(ctx)) {
						_ = s.ports.DeleteSticky(ctx, groupID, sessionHash)
					} else {
						result, err := s.ports.Acquire(ctx, providerID, provider.Concurrency)
						if err == nil && result != nil && result.Acquired {
							selection, selectErr := s.ports.CompleteAcquired(ctx, provider, result.ReleaseFunc)
							if selectErr != nil {
								return nil, selectErr
							}
							_ = s.ports.RefreshSticky(ctx, groupID, sessionHash, s.ports.BasicStickyTTL)
							return selection, nil
						}

						waitingCount, _ := s.concurrency.GetProviderWaitingCount(ctx, providerID)
						if waitingCount < cfg.StickySessionMaxWaiting {
							return s.ports.Complete(ctx, provider, false, nil, &ProviderWaitPlan{
								ProviderID:     providerID,
								MaxConcurrency: provider.Concurrency,
								Timeout:        cfg.StickySessionWaitTimeout,
								MaxWaiting:     cfg.StickySessionMaxWaiting,
							})
						}
						stickySpillover = true
					}
				}
			}
		}
	}

	parentCacheL2 := make(map[int64]*FlowProvider)
	parentLookupL2 := func(id int64) *FlowProvider {
		if a, ok := parentCacheL2[id]; ok {
			return a
		}
		if s.ports.ReadProviderDB == nil {
			return nil
		}
		a, _ := s.ports.ReadProviderDB(ctx, id)
		parentCacheL2[id] = a
		return a
	}
	baseCandidateCount := 0
	filterStats := PlatformFilterStats{Pool: len(providers)}
	candidates := make([]*FlowProvider, 0, len(providers))
	for i := range providers {
		acc := &providers[i]
		if isExcluded(acc.ID) {
			filterStats.Exclude("excluded")
			continue
		}
		// Snapshots can be stale; recheck model, quota, and scheduling eligibility before batch selection.
		if reason := s.ports.BasicFailureReason(ctx, acc, platform, routingModel, false, requiredCapability); reason != "" {
			filterStats.Exclude(reason)
			continue
		}
		if !s.ports.PrivacyAllowed(ctx, groupID, acc) {
			filterStats.Exclude("privacy_not_set")
			continue
		}
		if !s.ports.ShadowAllowed(ctx, acc) || !s.ports.ParentHealthy(acc, parentLookupL2) {
			filterStats.Exclude("shadow_parent_unhealthy")
			continue
		}
		if s.ports.RuntimeBlocked(acc, routingModel) {
			filterStats.Exclude("runtime_blocked")
			continue
		}
		if needsUpstreamCheck && s.ports.GroupModelRestricted(ctx, *groupID, acc, routingModel, requireCompact) {
			filterStats.Exclude("group_upstream_restricted")
			continue
		}
		baseCandidateCount++
		candidates = append(candidates, acc)
	}

	if len(candidates) == 0 {
		return nil, s.ports.Unavailable(
			ctx,
			requestedModel,
			routingModel,
			false,
			filterStats.Summary(""),
			providers,
		)
	}
	providerLoads := make([]ProviderWithConcurrency, 0, len(candidates))
	for _, acc := range candidates {
		providerLoads = append(providerLoads, ProviderWithConcurrency{
			ID:             acc.ID,
			MaxConcurrency: acc.EffectiveLoadFactor(),
		})
	}

	tryAcquireFromLoadMap := func(loadMap map[int64]*ProviderLoadInfo) (*FlowSelection, bool, error) {
		var available []FlowLoad
		for _, acc := range candidates {
			loadInfo := loadMap[acc.ID]
			if loadInfo == nil {
				loadInfo = &ProviderLoadInfo{ProviderID: acc.ID}
			}
			if loadInfo.LoadRate < 100 {
				available = append(available, FlowLoad{
					Provider: acc,
					LoadInfo: loadInfo,
				})
			}
		}

		if len(available) == 0 {
			return nil, false, nil
		}

		sort.SliceStable(available, func(i, j int) bool {
			a, b := available[i], available[j]
			if a.Provider.Priority != b.Provider.Priority {
				return a.Provider.Priority < b.Provider.Priority
			}
			if a.LoadInfo.LoadRate != b.LoadInfo.LoadRate {
				return a.LoadInfo.LoadRate < b.LoadInfo.LoadRate
			}
			switch {
			case a.Provider.LastUsedAt == nil && b.Provider.LastUsedAt != nil:
				return true
			case a.Provider.LastUsedAt != nil && b.Provider.LastUsedAt == nil:
				return false
			case a.Provider.LastUsedAt == nil && b.Provider.LastUsedAt == nil:
				return false
			default:
				return a.Provider.LastUsedAt.Before(*b.Provider.LastUsedAt)
			}
		})
		flowShuffleWithinSortGroups(available)
		selectionOrder := make([]FlowLoad, 0, len(available))
		if requireCompact {
			// Try snapshot-enabled providers first, then recheck stale snapshots that an administrator may have enabled.
			appendEnabled := func(out []FlowLoad, enabled bool) []FlowLoad {
				for _, item := range available {
					if s.ports.CompactAllowed(item.Provider) == enabled {
						out = append(out, item)
					}
				}
				return out
			}
			selectionOrder = appendEnabled(selectionOrder, true)
			selectionOrder = appendEnabled(selectionOrder, false)
		} else {
			selectionOrder = append(selectionOrder, available...)
		}

		for _, item := range selectionOrder {
			fresh := s.ports.Fresh(ctx, item.Provider, platform, routingModel, false, requiredCapability)
			if fresh == nil {
				continue
			}
			fresh = s.ports.Recheck(ctx, fresh, groupID, platform, routingModel, requireCompact, requiredCapability)
			if fresh == nil {
				continue
			}
			if needsUpstreamCheck && s.ports.GroupModelRestricted(ctx, *groupID, fresh, routingModel, requireCompact) {
				continue
			}
			result, err := s.ports.Acquire(ctx, fresh.ID, fresh.Concurrency)
			if err == nil && result != nil && result.Acquired {
				selection, selectErr := s.ports.CompleteAcquired(ctx, fresh, result.ReleaseFunc)
				if selectErr != nil {
					return nil, true, selectErr
				}
				if sessionHash != "" && !stickySpillover {
					_ = s.ports.SetSticky(ctx, groupID, sessionHash, fresh.ID, s.ports.BasicStickyTTL)
				}
				return selection, true, nil
			}
		}
		return nil, true, nil
	}

	loadMap, err := s.concurrency.GetProvidersLoadBatch(ctx, providerLoads)
	if err != nil {
		ordered := append([]*FlowProvider(nil), candidates...)
		flowSortProvidersByPriorityAndLastUsed(ordered, false)
		if requireCompact {
			ordered = s.prioritizeBasicCompact(ordered)
		}
		for _, acc := range ordered {
			fresh := s.ports.Fresh(ctx, acc, platform, routingModel, false, requiredCapability)
			if fresh == nil {
				continue
			}
			fresh = s.ports.Recheck(ctx, fresh, groupID, platform, routingModel, requireCompact, requiredCapability)
			if fresh == nil {
				continue
			}
			if needsUpstreamCheck && s.ports.GroupModelRestricted(ctx, *groupID, fresh, routingModel, requireCompact) {
				continue
			}
			result, err := s.ports.Acquire(ctx, fresh.ID, fresh.Concurrency)
			if err == nil && result != nil && result.Acquired {
				selection, selectErr := s.ports.CompleteAcquired(ctx, fresh, result.ReleaseFunc)
				if selectErr != nil {
					return nil, selectErr
				}
				if sessionHash != "" && !stickySpillover {
					_ = s.ports.SetSticky(ctx, groupID, sessionHash, fresh.ID, s.ports.BasicStickyTTL)
				}
				return selection, nil
			}
		}
	} else {
		if selection, attempted, selectErr := tryAcquireFromLoadMap(loadMap); selectErr != nil {
			return nil, selectErr
		} else if selection != nil {
			return selection, nil
		} else if attempted {
			if freshLoadMap, loadErr := s.concurrency.GetProvidersLoadBatchFresh(ctx, providerLoads); loadErr == nil {
				if selection, _, selectErr := tryAcquireFromLoadMap(freshLoadMap); selectErr != nil {
					return nil, selectErr
				} else if selection != nil {
					return selection, nil
				}
			}
		}
	}

	flowSortProvidersByPriorityAndLastUsed(candidates, false)
	if requireCompact {
		candidates = s.prioritizeBasicCompact(candidates)
	}
	for _, acc := range candidates {
		fresh := s.ports.Fresh(ctx, acc, platform, routingModel, false, requiredCapability)
		if fresh == nil {
			continue
		}
		fresh = s.ports.Recheck(ctx, fresh, groupID, platform, routingModel, requireCompact, requiredCapability)
		if fresh == nil {
			continue
		}
		if needsUpstreamCheck && s.ports.GroupModelRestricted(ctx, *groupID, fresh, routingModel, requireCompact) {
			continue
		}
		return s.ports.Complete(ctx, fresh, false, nil, &ProviderWaitPlan{
			ProviderID:     fresh.ID,
			MaxConcurrency: fresh.Concurrency,
			Timeout:        cfg.FallbackWaitTimeout,
			MaxWaiting:     cfg.FallbackMaxWaiting,
		})
	}

	if requireCompact && baseCandidateCount > 0 {
		return nil, ErrNoAvailableCompactProviders
	}
	return nil, s.ports.Unavailable(ctx, requestedModel, routingModel, false, "", providers)
}

func (s *PlatformSelector) prioritizeBasicCompact(providers []*FlowProvider) []*FlowProvider {
	if len(providers) == 0 {
		return nil
	}
	enabled := make([]*FlowProvider, 0, len(providers))
	disabled := make([]*FlowProvider, 0, len(providers))
	for _, provider := range providers {
		if s.ports.CompactAllowed(provider) {
			enabled = append(enabled, provider)
		} else {
			disabled = append(disabled, provider)
		}
	}
	return append(enabled, disabled...)
}

// SelectBasicOnly selects and hydrates a provider without acquiring slots or registering sessions.
func (s *PlatformSelector) SelectBasicOnly(ctx context.Context, input PlatformSelectionInput) (*FlowProvider, error) {
	return s.selectBasicOnlyRoutes(ctx, input.GroupID, input.Platform, input.SessionHash, input.RequestedModel, input.RoutingModel, input.ExcludedIDs, input.RequireCompact, input.StickyProviderID, input.RequiredCapability)
}

func (s *PlatformSelector) SelectBasic(ctx context.Context, input PlatformSelectionInput) (*FlowSelection, error) {
	return s.selectBasicRoutes(ctx, input.GroupID, input.Platform, input.SessionHash, input.RequestedModel, input.RoutingModel, input.ExcludedIDs, input.RequireCompact, input.RequiredCapability)
}
