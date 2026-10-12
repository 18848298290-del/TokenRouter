package scheduler

import (
	"context"
	"fmt"
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// selectRoutes selects basic single-platform or mixed-platform candidates using stickiness, priority, and last-used time.
func (s *GenericSelector) selectRoutes(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}) (*FlowProvider, error) {
	// A forced platform from the context takes precedence, as on /antigravity routes.
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
		// Requests without a group have no candidate pool.
		platform = ""
	}

	// count_tokens and availability probes use slot-free selection with the same advanced group, eligibility checks, and scoring.
	if resolvedGroup != nil && resolvedGroup.UsesAdvancedScheduler() {
		selection, err := s.Select(WithSelectOnly(ctx), SelectionInput{GroupID: groupID, SessionHash: sessionHash, RequestedModel: requestedModel, ExcludedIDs: excludedIDs})
		if err != nil {
			return nil, err
		}
		if selection == nil || selection.Provider == nil {
			return nil, ErrNoAvailableProviders
		}
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
		return selection.Provider, nil
	}

	// The entry point has admitted any fallback group; check the currently authorized group.
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

func (s *GenericSelector) SelectPlatform(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, platform string) (*FlowProvider, error) {
	preferOAuth := platform == capability.PlatformGemini
	routingProviderIDs := s.ports.RoutingProviderIDsForRequest(ctx, groupID, requestedModel, platform)

	var schedGroup *FlowGroup
	if groupID != nil && s.ports.ReadGroup != nil {
		schedGroup, _ = s.ports.ReadGroup(ctx, *groupID)
	}
	// Routed, sticky, and ordinary candidates all use the upstream model restriction.
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
			providerID, err := s.cache.GetSessionProviderID(ctx, derefGroupID(groupID), sessionHash)
			if err == nil && providerID > 0 && slices.Contains(routingProviderIDs, providerID) {
				if _, excluded := excludedIDs[providerID]; !excluded {
					provider, err := s.ports.GetSchedulableProvider(ctx, providerID)

					if err == nil {
						clearSticky := s.ports.ShouldClearStickySessionForProviderLayer(ctx, provider, requestedModel)
						if clearSticky {
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
				if err := s.cache.SetSessionProviderID(ctx, derefGroupID(groupID), sessionHash, selected.ID, stickySessionTTL); err != nil {
					s.diagnostics.printf("service.gateway", "set session provider failed: session=%s provider_id=%d err=%v", sessionHash, selected.ID, err)
				}
			}
			if s.ports.DebugModelRoutingEnabled() {
				s.diagnostics.printf("service.gateway", "[ModelRoutingDebug] legacy routed select: group_id=%v model=%s session=%s provider=%d", derefGroupID(groupID), requestedModel, shortFlowSessionHash(sessionHash), selected.ID)
			}
			return selected, nil
		}
		s.diagnostics.printf("service.gateway", "[ModelRouting] No routed providers available for model=%s, falling back to normal selection", requestedModel)
	}

	if sessionHash != "" && s.cache != nil {
		providerID, err := s.cache.GetSessionProviderID(ctx, derefGroupID(groupID), sessionHash)
		if err == nil && providerID > 0 {
			if _, excluded := excludedIDs[providerID]; !excluded {
				provider, err := s.ports.GetSchedulableProvider(ctx, providerID)

				if err == nil {
					clearSticky := s.ports.ShouldClearStickySessionForProviderLayer(ctx, provider, requestedModel)
					if clearSticky {
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
		if err := s.cache.SetSessionProviderID(ctx, derefGroupID(groupID), sessionHash, selected.ID, stickySessionTTL); err != nil {
			s.diagnostics.printf("service.gateway", "set session provider failed: session=%s provider_id=%d err=%v", sessionHash, selected.ID, err)
		}
	}

	return selected, nil
}
