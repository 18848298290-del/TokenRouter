package scheduler

import (
	"context"
	"fmt"
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// generic_selection_routes.go selects provider execution data from model
// routes, session bindings, and the ordinary group pool.
// A model route supplies a preferred provider list inside the already admitted
// group. When that list has no eligible provider, basic selection can use the
// group's ordinary list, ordered by global priority and last-used time.
// Existing session bindings get their own eligibility checks before either
// list is selected.
//
// A selected basic candidate can update stickiness before its execution data
// is hydrated by selectRoutes. Binding failures leave the candidate usable,
// while hydration failures return an error to the caller. Advanced groups
// enter the shared Select path with select-only context, so its scoring uses
// the same group settings and eligibility callbacks as a request that acquires
// capacity.

// selectRoutes returns a hydrated provider for the admitted group and optional
// forced platform. A forced nonempty platform narrows the group pool;
// otherwise ResolveGatewayGroup supplies the current group. Group resolution
// and model restriction errors stop selection.
//
// Advanced groups use Select with select-only context and release any returned
// cleanup handle before returning its provider. Basic groups use
// SelectPlatform, which may write a binding before hydration. The caller
// therefore receives hydration errors after candidate selection and must
// retain the invocation's provider lookup until these callbacks finish.
func (s *GenericSelector) selectRoutes(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}) (*FlowProvider, error) {
	var platform string
	var resolvedGroup *FlowGroup
	forcePlatform, hasForcePlatform := s.ports.ForcePlatform(ctx)
	if hasForcePlatform && forcePlatform != "" {
		platform = forcePlatform
		if groupID != nil {
			group, err := s.ports.ResolveGroupByID(ctx, *groupID)
			if err != nil {
				return nil, err
			}
			ctx = s.ports.WithGroupContext(ctx, group)
			resolvedGroup = group
		}
	} else if groupID != nil {
		group, resolvedGroupID, err := s.ports.ResolveGatewayGroup(ctx, groupID)
		if err != nil {
			return nil, err
		}
		groupID = resolvedGroupID
		ctx = s.ports.WithGroupContext(ctx, group)
		resolvedGroup = group
		platform = ""
	} else {
		platform = ""
	}

	// The advanced request path retains group-specific scoring for auxiliary
	// probes. Select-only context lets the adapter skip physical slot and session
	// admission.
	if resolvedGroup != nil && resolvedGroup.UsesAdvancedScheduler() {
		selection, err := s.Select(WithSelectOnly(ctx), SelectionInput{GroupID: groupID, SessionHash: sessionHash, RequestedModel: requestedModel, ExcludedIDs: excludedIDs})
		if err != nil {
			return nil, err
		}
		if selection == nil || selection.Provider == nil {
			return nil, ErrNoAvailableProviders
		}
		// A completion adapter may attach cleanup even to a probe; finish it before
		// handing the provider to this slot-free caller.
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
		return selection.Provider, nil
	}

	// The gateway has already admitted this group. A model restriction rejects
	// this selection rather than authorizing another group inside the selector.
	if s.ports.CheckGroupModelRestriction(ctx, groupID, requestedModel) {
		s.diagnostics.event("warn", "group model restriction blocked request",
			"group_id", derefGroupID(groupID),
			"model", requestedModel)
		return nil, fmt.Errorf("%w supporting model: %s (group model restriction)", ErrNoAvailableProviders, requestedModel)
	}

	provider, err := s.SelectPlatform(ctx, groupID, sessionHash, requestedModel, excludedIDs, platform)
	if err != nil {
		return nil, err
	}
	return s.ports.HydrateSelectedProvider(ctx, provider)
}

// SelectPlatform selects basic metadata from the requested group's provider
// source. Model routes are preferred, with ordinary candidates considered when
// the routed list produces no result. Sticky candidates need current group
// membership and the platform equality check used here, along with model,
// quota, window, and RPM eligibility.
//
// Remaining candidates compare priority before last-used time; Gemini OAuth
// preference resolves the equal-priority, both-unused case. Reading schedGroup
// is best effort, so privacy filtering uses it when available. Successful new
// choices attempt to write stickiness before the caller hydrates execution
// data, and binding errors are logged while the choice is returned.
func (s *GenericSelector) SelectPlatform(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, platform string) (*FlowProvider, error) {
	preferOAuth := platform == capability.PlatformGemini
	routingProviderIDs := s.ports.RoutingProviderIDsForRequest(ctx, groupID, requestedModel, platform)

	var schedGroup *FlowGroup
	if groupID != nil && s.ports.ReadGroup != nil {
		// The candidate query still runs if this auxiliary group read fails. Privacy
		// filtering below consequently depends on a successfully returned
		// schedGroup.
		schedGroup, _ = s.ports.ReadGroup(ctx, *groupID)
	}
	// The same callback checks the provider-layer final model in both lists. Its
	// positive result also guarantees the group ID used by this closure is
	// available.
	needsUpstreamCheck := s.ports.NeedsUpstreamGroupRestrictionCheck(ctx, groupID)
	isUpstreamAllowed := func(provider *FlowProvider) bool {
		return !needsUpstreamCheck || !s.ports.IsUpstreamModelRestrictedByGroup(ctx, *groupID, provider, requestedModel)
	}

	var providers []FlowProvider
	providersLoaded := false

	if len(routingProviderIDs) > 0 {
		if s.ports.DebugModelRoutingEnabled() {
			s.diagnostics.printf("service.gateway", "[ModelRoutingDebug] legacy routed begin: group_id=%v model=%s platform=%s session=%s routed_ids=%v",
				derefGroupID(groupID), requestedModel, platform, shortFlowSessionHash(sessionHash), routingProviderIDs)
		}

		if sessionHash != "" && s.cache != nil {
			// A missing or unreadable binding leaves candidate selection available;
			// positive cached IDs still need provider and request eligibility checks.
			providerID, err := s.cache.GetSessionProviderID(ctx, derefGroupID(groupID), sessionHash)
			if err == nil && providerID > 0 && slices.Contains(routingProviderIDs, providerID) {
				if _, excluded := excludedIDs[providerID]; !excluded {
					// A stale binding can point to an unavailable record. Ignore that read
					// error here so the preferred or ordinary pool can supply another
					// candidate.
					provider, err := s.ports.GetSchedulableProvider(ctx, providerID)

					if err == nil {
						clearSticky := s.ports.ShouldClearStickySessionForProviderLayer(ctx, provider, requestedModel)
						if clearSticky {
							// A current sticky-clear condition disqualifies this binding for the
							// request even if deleting the cached binding fails.
							_ = s.cache.DeleteSessionProviderID(ctx, derefGroupID(groupID), sessionHash)
						}
						if !clearSticky && s.ports.IsProviderInGroup(provider, groupID) && provider.Platform == platform && (requestedModel == "" || s.ports.IsModelSupportedByProviderWithContext(ctx, provider, requestedModel)) && isUpstreamAllowed(provider) && s.ports.IsProviderSchedulableForModelSelection(ctx, provider, requestedModel) && s.ports.IsProviderSchedulableForQuota(provider) && s.ports.IsProviderSchedulableForWindowCost(ctx, provider, true) && s.ports.IsProviderSchedulableForRPM(ctx, provider, true) {
							if s.ports.DebugModelRoutingEnabled() {
								s.diagnostics.printf("service.gateway", "[ModelRoutingDebug] legacy routed sticky hit: group_id=%v model=%s session=%s provider=%d", derefGroupID(groupID), requestedModel, shortFlowSessionHash(sessionHash), providerID)
							}
							return provider, nil
						}
					}
				}
			}
		}

		forcePlatform, hasForcePlatform := s.ports.ForcePlatform(ctx)
		if hasForcePlatform && forcePlatform == "" {
			hasForcePlatform = false
		}
		var err error
		providers, _, err = s.ports.ListSchedulableProviders(ctx, groupID, platform, hasForcePlatform)
		if err != nil {
			return nil, fmt.Errorf("query providers failed: %w", err)
		}
		providersLoaded = true

		ctx = s.ports.WithWindowCostPrefetch(ctx, providers)
		ctx = s.ports.WithRPMPrefetch(ctx, providers)

		routingSet := make(map[int64]struct{}, len(routingProviderIDs))
		for _, id := range routingProviderIDs {
			if id > 0 {
				routingSet[id] = struct{}{}
			}
		}

		var selected *FlowProvider
		for i := range providers {
			acc := &providers[i]
			if _, ok := routingSet[acc.ID]; !ok {
				continue
			}
			if _, excluded := excludedIDs[acc.ID]; excluded {
				continue
			}

			if !s.ports.IsProviderSchedulableForSelection(acc) {
				continue
			}

			if schedGroup != nil && schedGroup.RequirePrivacySet && !acc.IsPrivacySet() {
				// Reject privacy-ineligible metadata for this request regardless of
				// whether the administrative provider error can be persisted.
				_ = s.ports.SetProviderError(ctx, acc.ID,
					fmt.Sprintf("Privacy not set, required by group [%s]", schedGroup.Name))
				continue
			}
			if requestedModel != "" && !s.ports.IsModelSupportedByProviderWithContext(ctx, acc, requestedModel) {
				continue
			}
			if !isUpstreamAllowed(acc) {
				continue
			}
			if !s.ports.IsProviderSchedulableForModelSelection(ctx, acc, requestedModel) {
				continue
			}
			if !s.ports.IsProviderSchedulableForQuota(acc) {
				continue
			}
			if !s.ports.IsProviderSchedulableForWindowCost(ctx, acc, false) {
				continue
			}
			if !s.ports.IsProviderSchedulableForRPM(ctx, acc, false) {
				continue
			}
			if selected == nil {
				selected = acc
				continue
			}
			if acc.Priority < selected.Priority {
				selected = acc
			} else if acc.Priority == selected.Priority {
				switch {
				case acc.LastUsedAt == nil && selected.LastUsedAt != nil:
					selected = acc
				case acc.LastUsedAt != nil && selected.LastUsedAt == nil:

				case acc.LastUsedAt == nil && selected.LastUsedAt == nil:
					if preferOAuth && acc.Type != selected.Type && acc.Type == capability.ProviderTypeOAuth {
						selected = acc
					}
				default:
					if acc.LastUsedAt.Before(*selected.LastUsedAt) {
						selected = acc
					}
				}
			}
		}

		if selected != nil {
			if sessionHash != "" && s.cache != nil {
				// The binding is attempted before execution hydration. A cache failure is
				// logged, while this eligible metadata choice remains available to the
				// caller.
				if err := s.cache.SetSessionProviderID(ctx, derefGroupID(groupID), sessionHash, selected.ID, stickySessionTTL); err != nil {
					s.diagnostics.printf("service.gateway", "set session provider failed: session=%s provider_id=%d err=%v", sessionHash, selected.ID, err)
				}
			}
			if s.ports.DebugModelRoutingEnabled() {
				s.diagnostics.printf("service.gateway", "[ModelRoutingDebug] legacy routed select: group_id=%v model=%s session=%s provider=%d", derefGroupID(groupID), requestedModel, shortFlowSessionHash(sessionHash), selected.ID)
			}
			return selected, nil
		}
		// Exhausting the preferred route changes the candidate list within this
		// group. Reuse its already loaded providers for ordinary eligibility and
		// priority selection.
		s.diagnostics.printf("service.gateway", "[ModelRouting] No routed providers available for model=%s, falling back to normal selection", requestedModel)
	}

	if sessionHash != "" && s.cache != nil {
		// A missing or unreadable binding leaves candidate selection available;
		// positive cached IDs still need provider and request eligibility checks.
		providerID, err := s.cache.GetSessionProviderID(ctx, derefGroupID(groupID), sessionHash)
		if err == nil && providerID > 0 {
			if _, excluded := excludedIDs[providerID]; !excluded {
				// A stale binding can point to an unavailable record. Ignore that read
				// error here so the preferred or ordinary pool can supply another
				// candidate.
				provider, err := s.ports.GetSchedulableProvider(ctx, providerID)

				if err == nil {
					clearSticky := s.ports.ShouldClearStickySessionForProviderLayer(ctx, provider, requestedModel)
					if clearSticky {
						// A current sticky-clear condition disqualifies this binding for the
						// request even if deleting the cached binding fails.
						_ = s.cache.DeleteSessionProviderID(ctx, derefGroupID(groupID), sessionHash)
					}
					if !clearSticky && s.ports.IsProviderInGroup(provider, groupID) && provider.Platform == platform && (requestedModel == "" || s.ports.IsModelSupportedByProviderWithContext(ctx, provider, requestedModel)) && isUpstreamAllowed(provider) && s.ports.IsProviderSchedulableForModelSelection(ctx, provider, requestedModel) && s.ports.IsProviderSchedulableForQuota(provider) && s.ports.IsProviderSchedulableForWindowCost(ctx, provider, true) && s.ports.IsProviderSchedulableForRPM(ctx, provider, true) {
						return provider, nil
					}
				}
			}
		}
	}

	if !providersLoaded {
		forcePlatform, hasForcePlatform := s.ports.ForcePlatform(ctx)
		if hasForcePlatform && forcePlatform == "" {
			hasForcePlatform = false
		}
		var err error
		providers, _, err = s.ports.ListSchedulableProviders(ctx, groupID, platform, hasForcePlatform)
		if err != nil {
			return nil, fmt.Errorf("query providers failed: %w", err)
		}
	}

	ctx = s.ports.WithWindowCostPrefetch(ctx, providers)
	ctx = s.ports.WithRPMPrefetch(ctx, providers)

	var selected *FlowProvider
	for i := range providers {
		acc := &providers[i]
		if _, excluded := excludedIDs[acc.ID]; excluded {
			continue
		}

		if !s.ports.IsProviderSchedulableForSelection(acc) {
			continue
		}

		if schedGroup != nil && schedGroup.RequirePrivacySet && !acc.IsPrivacySet() {
			// Reject privacy-ineligible metadata for this request regardless of whether
			// the administrative provider error can be persisted.
			_ = s.ports.SetProviderError(ctx, acc.ID,
				fmt.Sprintf("Privacy not set, required by group [%s]", schedGroup.Name))
			continue
		}
		if requestedModel != "" && !s.ports.IsModelSupportedByProviderWithContext(ctx, acc, requestedModel) {
			continue
		}
		if !isUpstreamAllowed(acc) {
			continue
		}
		if !s.ports.IsProviderSchedulableForModelSelection(ctx, acc, requestedModel) {
			continue
		}
		if !s.ports.IsProviderSchedulableForQuota(acc) {
			continue
		}
		if !s.ports.IsProviderSchedulableForWindowCost(ctx, acc, false) {
			continue
		}
		if !s.ports.IsProviderSchedulableForRPM(ctx, acc, false) {
			continue
		}
		if selected == nil {
			selected = acc
			continue
		}
		if acc.Priority < selected.Priority {
			selected = acc
		} else if acc.Priority == selected.Priority {
			switch {
			case acc.LastUsedAt == nil && selected.LastUsedAt != nil:
				selected = acc
			case acc.LastUsedAt != nil && selected.LastUsedAt == nil:

			case acc.LastUsedAt == nil && selected.LastUsedAt == nil:
				if preferOAuth && acc.Type != selected.Type && acc.Type == capability.ProviderTypeOAuth {
					selected = acc
				}
			default:
				if acc.LastUsedAt.Before(*selected.LastUsedAt) {
					selected = acc
				}
			}
		}
	}

	if selected == nil {
		if err := s.ports.GroupModelUnsupportedErrorIfApplicable(ctx, providers, requestedModel, platform, excludedIDs, false, groupID, schedGroup); err != nil {
			return nil, err
		}
		stats := s.ports.LogDetailedSelectionFailure(ctx, groupID, sessionHash, requestedModel, platform, providers, excludedIDs, false)
		if requestedModel != "" {
			return nil, fmt.Errorf("%w supporting model: %s (%s)", ErrNoAvailableProviders, requestedModel, stats)
		}
		return nil, ErrNoAvailableProviders
	}

	if sessionHash != "" && s.cache != nil {
		// The binding is attempted before execution hydration. A cache failure is
		// logged, while this eligible metadata choice remains available to the
		// caller.
		if err := s.cache.SetSessionProviderID(ctx, derefGroupID(groupID), sessionHash, selected.ID, stickySessionTTL); err != nil {
			s.diagnostics.printf("service.gateway", "set session provider failed: session=%s provider_id=%d err=%v", sessionHash, selected.ID, err)
		}
	}

	return selected, nil
}
