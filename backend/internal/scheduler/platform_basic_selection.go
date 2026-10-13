package scheduler

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Basic platform selection can return a provider before any physical slot is
// acquired. Auxiliary callers recheck current eligibility and use priority
// plus last-use ordering, while request selection adds observed load, actual
// acquisition, and bounded wait plans. Compact-disabled snapshots remain
// available for current-record rechecks, allowing an administrator's
// reenablement to become visible during selection.
//
// Load-aware hard affinity can spill one request to another provider when its
// sticky queue is full. That path remembers the spillover and suppresses later
// rebinding. Accepted ordinary acquisitions complete execution data before
// writing stickiness, relying on CompleteAcquired to return capacity if
// hydration fails. The path without batch load observations begins with
// auxiliary selection, whose binding effects can already have happened before
// request acquisition or waiting.

// selectBasicOnlyRoutes returns basic provider metadata after pricing and
// current eligibility checks, with no capacity acquisition. A usable sticky
// record is returned after tryBasicSticky rechecks it; an ordinary selected
// record passes Hydrate before return.
//
// SelectBestBasic uses relaxed compact checks during fresh and database reads,
// then applies current compact eligibility, so stale disabled snapshots can be
// reconsidered. An ordinary success attempts binding after hydration, while a
// sticky success refreshes its lifetime. Cache writes are best effort and do
// not reject the provider. The caller supplies callbacks whose group, model,
// privacy, and capability checks describe this request.
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
		// This ordinary auxiliary choice has already passed hydration. Binding
		// failure leaves that provider available, and request callers may acquire
		// its capacity afterward.
		_ = s.ports.SetSticky(ctx, groupID, sessionHash, selected.ID, s.ports.BasicStickyTTL)
	}

	return hydrated, nil
}

// tryBasicSticky returns current metadata for an eligible session binding, or
// nil so ordinary selection can continue. A positive supplied provider ID
// bypasses the cache lookup; read errors also produce nil. The provider is
// checked against current group membership and request eligibility before
// Recheck.
//
// Provider/group invalidation, shadow-parent failure, runtime blocking, failed
// recheck, and upstream group restriction request best-effort binding
// deletion. A request-specific BasicEligible or privacy miss leaves the stored
// binding available for another request. Successful rechecks refresh lifetime
// best effort and return that associated record, whose execution data remains
// owned by the invocation's adapter.
func (s *PlatformSelector) tryBasicSticky(ctx context.Context, groupID *int64, platform string, sessionHash, requestedModel string, routingModel string, excludedIDs map[int64]struct{}, requireCompact bool, stickyProviderID int64, requiredCapability provider.OpenAIEndpointCapability) *FlowProvider {
	if sessionHash == "" {
		return nil
	}
	platform = strings.TrimSpace(platform)

	providerID := stickyProviderID
	if providerID <= 0 {
		var err error
		// Without a supplied binding ID, a cache read error prevents this sticky
		// attempt but leaves ordinary candidate selection available.
		providerID, err = s.ports.GetSticky(ctx, groupID, sessionHash)
		if err != nil || providerID <= 0 {
			return nil
		}
	}

	if _, excluded := excludedIDs[providerID]; excluded {
		return nil
	}

	// The bound provider can have disappeared since affinity was stored. Suppress
	// its read error here so ordinary selection can replace this failed attempt.
	provider, err := s.ports.GetSchedulable(ctx, providerID)
	if err != nil {
		return nil
	}

	// Provider or group invalidation starts the binding-deletion paths below.
	// Shadow, runtime, recheck, and upstream-group failures also request deletion
	// best effort; a failed delete cannot make this attempt eligible.
	if s.ports.ClearSticky(provider, routingModel) || !s.ports.MatchesGroup(provider, groupID) {
		_ = s.ports.DeleteSticky(ctx, groupID, sessionHash)
		return nil
	}

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

	// The binding is eligible for this request after current-record checks. A
	// failed lifetime refresh leaves that returned provider usable until later
	// requests re-evaluate affinity.
	_ = s.ports.RefreshSticky(ctx, groupID, sessionHash, s.ports.BasicStickyTTL)
	return provider
}

// SelectBestBasic returns the best current provider and whether a candidate
// was blocked specifically by compact eligibility. Exclusions run before Fresh
// and Recheck; those reads initially use relaxed compact requirements so a
// stale disabled snapshot can reach a current record. Privacy and optional
// upstream restrictions then apply to that record.
//
// For a compact request, only currently allowed records join the final pool,
// and rejected compact records set compactBlocked. Eligible candidates compare
// global priority and then last use; stable equal comparisons retain input
// order. The returned pointer belongs to a current adapter conversion, while
// an empty eligible pool returns nil with the accumulated compact indication.
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

		// Fresh starts with relaxed compact requirements so a disabled snapshot can
		// reach the current record. Recheck and the consuming path then enforce
		// compact eligibility.
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

// isBetterBasic reports whether candidate precedes current in the auxiliary
// basic order. Both providers are nonnil and have passed the caller's
// eligibility checks. Lower numeric priority wins before recency is examined,
// so an older last-use timestamp cannot compensate for a worse priority.
//
// Within one priority, a nil LastUsedAt denotes never used and beats every
// observed time. Present timestamps compare at their full time.Time precision.
// Exact ties return false, allowing SelectBestBasic's stable sort to retain the
// source order.
func (s *PlatformSelector) isBetterBasic(candidate, current *FlowProvider) bool {
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
		return false
	default:

		return candidate.LastUsedAt.Before(*current.LastUsedAt)
	}
}

// selectBasicRoutes returns an acquired basic provider or a rechecked wait
// plan for the admitted group. Pricing restrictions stop selection before
// provider reads. When batch loads are unavailable, auxiliary selection runs
// first and may bind or refresh a session before physical acquisition;
// acquisition failure can become a wait plan.
//
// With batch loads, hard affinity attempts current capacity before queue
// comparison. A full sticky queue sets stickySpillover, suppressing rebinding
// after another provider successfully completes. Ordinary candidates compare
// priority, observed load, and last use, with slot acquisition using the
// physical limit rather than the load denominator. CompleteAcquired owns
// cleanup when execution hydration fails.
//
// Load-read failure falls back to priority/LRU acquisition. Failed attempts
// against an available observed pool permit one fresh batch-load retry; absent
// available candidates proceed to waiting directly. Both acquisition and final
// waiting recheck current records, including stale compact-disabled metadata,
// before returning a result.
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
		// An unreadable binding leaves a zero sticky ID. Candidate selection and
		// queue fallback remain available under the request's ordinary policy.
		if providerID, err := s.ports.GetSticky(ctx, groupID, sessionHash); err == nil {
			stickyProviderID = providerID
		}
	}
	// The auxiliary choice can already write stickiness. Try physical acquisition
	// afterward; errors or denied capacity select a wait policy rather than
	// undoing that earlier binding.
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
			// Compare the returned queue count despite a read error; Redis failures
			// yield zero. Later waiting still admits the plan under its count and
			// capacity limits.
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

	// When current sticky capacity and its bounded queue cannot serve this
	// request, remember spillover. Later successful ordinary acquisition skips
	// SetSticky so subsequent requests can reuse the existing binding.
	stickySpillover := false
	if sessionHash != "" {
		providerID := stickyProviderID
		if providerID > 0 && !isExcluded(providerID) {
			// A sticky record read failure leaves the ordinary pool usable. Current
			// invalidation paths below request binding deletion best effort before
			// considering another provider.
			provider, err := s.ports.GetSchedulable(ctx, providerID)
			if err == nil {
				clearSticky := s.ports.ClearSticky(provider, routingModel) || !s.ports.MatchesGroup(provider, groupID)
				// ClearSticky and current membership can disqualify affinity before
				// acquisition. The following failed-recheck, group, runtime, model, and
				// parent paths also delete best effort and continue ordinary selection.
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
							// Execution completion succeeded before this expiry update. Failed
							// refresh leaves the accepted slot usable and its cleanup attached to
							// the selection result.
							_ = s.ports.RefreshSticky(ctx, groupID, sessionHash, s.ports.BasicStickyTTL)
							return selection, nil
						}

						// Denied or failed acquisition reaches queue policy. Compare the
						// returned count even on error; Redis failures yield zero. Observed
						// queue fullness sets spillover before ordinary selection.
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
		// Memoize the returned parent record for this invocation, including nil on
		// read failure. Shadow eligibility can then reject a missing parent without
		// repeated database probes.
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
		// These snapshot checks build the observed-load pool using ordinary
		// eligibility. Later Fresh and Recheck calls decide the current record
		// before acquisition or waiting.
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

	// Observed availability produces an acquisition attempt even when every
	// current-record check later rejects it. The attempted flag permits one fresh
	// load retry, while successful completion binds only when no sticky spillover
	// occurred.
	tryAcquireFromLoadMap := func(loadMap map[int64]*ProviderLoadInfo) (*FlowSelection, bool, error) {
		var available []FlowLoad
		for _, acc := range candidates {
			loadInfo := loadMap[acc.ID]
			// A missing load record becomes zero observed usage for basic ordering.
			// Actual slot acquisition still enforces the current provider's physical
			// concurrency limit.
			if loadInfo == nil {
				loadInfo = &ProviderLoadInfo{ProviderID: acc.ID}
			}
			// The observation denominator can differ from physical Concurrency. Keep
			// sub-full samples for immediate basic probing; acquisition still checks
			// the selected provider's actual slot limit.
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
			// Give snapshot-enabled compact candidates the first attempts, then retain
			// disabled snapshots for current-record rechecks so administrative
			// reenablement can become visible.
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

		// The ordered pool can change before acquisition. Fresh and Recheck retain
		// current eligibility; failed acquisitions advance the order, and
		// CompleteAcquired returns capacity on hydration failure.
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
					// Completion has produced a usable execution result. Rebinding is
					// suppressed after sticky spillover; otherwise a failed cache write
					// leaves this acquired result accepted.
					_ = s.ports.SetSticky(ctx, groupID, sessionHash, fresh.ID, s.ports.BasicStickyTTL)
				}
				return selection, true, nil
			}
		}
		return nil, true, nil
	}

	// If batch observations fail, the basic path probes a priority/LRU order
	// instead. Current eligibility checks and physical limits still apply to each
	// resulting acquisition.
	loadMap, err := s.concurrency.GetProvidersLoadBatch(ctx, providerLoads)
	if err != nil {
		ordered := append([]*FlowProvider(nil), candidates...)
		flowSortProvidersByPriorityAndLastUsed(ordered, false)
		if requireCompact {
			ordered = s.prioritizeBasicCompact(ordered)
		}
		// Load failure leaves these priority/LRU candidates eligible for physical
		// probing. Acquisition errors or denied capacity advance this order;
		// completion failure returns the slot through the adapter.
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
					// Completion has produced a usable execution result. Rebinding is
					// suppressed after sticky spillover; otherwise a failed cache write
					// leaves this acquired result accepted.
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
			// An observed available pool failed to produce a result. Refresh its load
			// sample once before waiting; a refresh error leaves the final wait
			// fallback available.
			if freshLoadMap, loadErr := s.concurrency.GetProvidersLoadBatchFresh(ctx, providerLoads); loadErr == nil {
				if selection, _, selectErr := tryAcquireFromLoadMap(freshLoadMap); selectErr != nil {
					return nil, selectErr
				} else if selection != nil {
					return selection, nil
				}
			}
		}
	}

	// All immediate choices failed or appeared full. Build an ordinary wait plan
	// only after fresh and database eligibility checks in priority/LRU order.
	flowSortProvidersByPriorityAndLastUsed(candidates, false)
	if requireCompact {
		candidates = s.prioritizeBasicCompact(candidates)
	}
	for _, acc := range candidates {
		// Fresh starts with relaxed compact requirements so a disabled snapshot can
		// reach the current record. Recheck and the consuming path then enforce
		// compact eligibility.
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

// prioritizeBasicCompact returns a new slice with compact-enabled providers
// before disabled ones, preserving order inside each set. Empty input returns
// nil; providers remain borrowed and are rechecked by the consuming selection
// path.
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

// SelectBasicOnly delegates basic group, model, privacy, capability, and
// current-record selection for auxiliary callers. It invokes no slot or
// session admission here, while sticky cache updates can still occur before
// the provider is returned.
func (s *PlatformSelector) SelectBasicOnly(ctx context.Context, input PlatformSelectionInput) (*FlowProvider, error) {
	return s.selectBasicOnlyRoutes(ctx, input.GroupID, input.Platform, input.SessionHash, input.RequestedModel, input.RoutingModel, input.ExcludedIDs, input.RequireCompact, input.StickyProviderID, input.RequiredCapability)
}

// SelectBasic delegates basic request selection using the input's group,
// models, capability, exclusions, and session hash. Acquired results carry
// adapter cleanup; waiting results carry the queue policy for later admission.
func (s *PlatformSelector) SelectBasic(ctx context.Context, input PlatformSelectionInput) (*FlowSelection, error) {
	return s.selectBasicRoutes(ctx, input.GroupID, input.Platform, input.SessionHash, input.RequestedModel, input.RoutingModel, input.ExcludedIDs, input.RequireCompact, input.RequiredCapability)
}
