package ops

import (
	"context"
	"errors"
	"time"
)

// provider_availability.go builds provider availability details and group or
// platform totals from status and hold observations.
// Provider availability badges combine saved status, scheduling enablement, and
// observed hold deadlines. Error status clears rate-limit and overload badges
// before counting, while temporary holds still exclude availability. A positive
// group selection limits both displayed membership and aggregates; unfiltered
// providers can appear in every positive membership group.
//
// The view captures one time for active-hold decisions after reading providers,
// but computes remaining seconds later and retains the observed deadline
// pointers. Fractional seconds are truncated, so badge state and countdown
// presence can differ near expiration. Provider detail maps retain the last
// repeated identity, while platform and group totals accumulate each eligible
// entry; callers use these diagnostic counts to build alerts and reports.

// OpsProviderAvailability carries selected group counts and provider
// diagnostics for evaluator and report callers.
type OpsProviderAvailability struct {
	// Group contains the requested positive group entry on the normal service
	// path. An unfiltered request or a missing selected aggregate leaves it nil.
	Group *GroupAvailability // optional selected group diagnostics
	// Providers carries the provider-detail map from GetProviderAvailabilityStats,
	// with a nil map replaced by an allocated empty one. An injected override
	// returns its result directly.
	Providers map[int64]*ProviderAvailability // selected provider diagnostic entries
	// CollectedAt is sampled after provider retrieval and before status
	// calculation on the normal path. An injected override controls whether a
	// timestamp is supplied.
	CollectedAt *time.Time // optional observation assembly timestamp
}

// GetProviderAvailabilityStats returns provider availability diagnostics after
// monitoring and provider-read checks. Repository filtering supplies platform
// selection; a positive group is reapplied to memberships and controls which
// group totals are returned. Availability requires active status, scheduling
// enabled, and no future rate-limit, overload, or temporary hold at one
// captured time.
//
// Error status clears rate-limit and overload display badges before counts are
// accumulated. Nonempty platform entries form platform totals, and each
// eligible membership forms an overlapping group total. Repeated provider IDs
// replace earlier details while all entries can contribute to aggregates.
// Deadline pointers are retained from provider observations; remaining seconds
// use later clock reads and may be absent while an initially active badge
// remains true.
func (s *OpsService) GetProviderAvailabilityStats(ctx context.Context, platformFilter string, groupIDFilter *int64) (
	map[string]*PlatformAvailability,
	map[int64]*GroupAvailability,
	map[int64]*ProviderAvailability,
	*time.Time,
	error,
) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, nil, nil, nil, err
	}

	providers, err := s.listAllProvidersForOps(ctx, platformFilter, groupIDFilter)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	now := time.Now()
	collectedAt := now

	platform := make(map[string]*PlatformAvailability)
	group := make(map[int64]*GroupAvailability)
	provider := make(map[int64]*ProviderAvailability)

	for _, acc := range providers {
		if acc.ID <= 0 {
			continue
		}

		groupsForAggregation := acc.Groups
		var displayGroup *GroupObservation
		if groupIDFilter != nil && *groupIDFilter > 0 {
			groupsForAggregation = nil
			for _, grp := range acc.Groups {
				if grp != nil && grp.ID == *groupIDFilter {
					groupsForAggregation = []*GroupObservation{grp}
					displayGroup = grp
					break
				}
			}
			// Recheck membership because the paginated provider reader may return a
			// broader set. Restrict both the displayed group and aggregation memberships
			// to the selected positive identity.
			if displayGroup == nil {
				continue
			}
		} else if len(acc.Groups) > 0 {
			displayGroup = acc.Groups[0]
		}

		isTempUnsched := false
		if acc.TempUnschedulableUntil != nil && now.Before(*acc.TempUnschedulableUntil) {
			isTempUnsched = true
		}

		isRateLimited := acc.RateLimitResetAt != nil && now.Before(*acc.RateLimitResetAt)
		isOverloaded := acc.OverloadUntil != nil && now.Before(*acc.OverloadUntil)
		hasError := acc.Status == "error"

		// Error status selects the primary diagnostic badge. Clear the two hold
		// badges before counting so one errored entry does not also appear in the
		// rate-limit total.
		if hasError {
			isRateLimited = false
			isOverloaded = false
		}

		// The Ops view reports a diagnostic status-and-hold check. Callers making
		// protocol-specific admission decisions need the gateway additional
		// eligibility rules.
		isAvailable := acc.Status == "active" && acc.Schedulable && !isRateLimited && !isOverloaded && !isTempUnsched

		if acc.Platform != "" {
			if _, ok := platform[acc.Platform]; !ok {
				platform[acc.Platform] = &PlatformAvailability{
					Platform: acc.Platform,
				}
			}
			p := platform[acc.Platform]
			p.TotalProviders++
			if isAvailable {
				p.AvailableCount++
			}
			if isRateLimited {
				p.RateLimitCount++
			}
			if hasError {
				p.ErrorCount++
			}
		}

		for _, grp := range groupsForAggregation {
			if grp == nil || grp.ID <= 0 {
				continue
			}
			if _, ok := group[grp.ID]; !ok {
				group[grp.ID] = &GroupAvailability{
					GroupID:   grp.ID,
					GroupName: grp.Name,
				}
			}
			g := group[grp.ID]
			g.TotalProviders++
			if isAvailable {
				g.AvailableCount++
			}
			if isRateLimited {
				g.RateLimitCount++
			}
			if hasError {
				g.ErrorCount++
			}
		}

		displayGroupID := int64(0)
		displayGroupName := ""
		if displayGroup != nil {
			displayGroupID = displayGroup.ID
			displayGroupName = displayGroup.Name
		}

		item := &ProviderAvailability{
			ProviderID:   acc.ID,
			ProviderName: acc.Name,
			Platform:     acc.Platform,
			GroupID:      displayGroupID,
			GroupName:    displayGroupName,
			Status:       acc.Status,

			IsAvailable:   isAvailable,
			IsRateLimited: isRateLimited,
			IsOverloaded:  isOverloaded,
			HasError:      hasError,

			ErrorMessage: acc.ErrorMessage,
		}

		// Rate-limit and overload detail retain the observed deadline pointers. Their
		// countdowns use later clock reads and truncate fractional seconds, so an
		// active badge can have no positive remaining-second value.
		if isRateLimited && acc.RateLimitResetAt != nil {
			item.RateLimitResetAt = acc.RateLimitResetAt
			remainingSec := int64(time.Until(*acc.RateLimitResetAt).Seconds())
			if remainingSec > 0 {
				item.RateLimitRemainingSec = &remainingSec
			}
		}
		if isOverloaded && acc.OverloadUntil != nil {
			item.OverloadUntil = acc.OverloadUntil
			remainingSec := int64(time.Until(*acc.OverloadUntil).Seconds())
			if remainingSec > 0 {
				item.OverloadRemainingSec = &remainingSec
			}
		}
		if isTempUnsched && acc.TempUnschedulableUntil != nil {
			item.TempUnschedulableUntil = acc.TempUnschedulableUntil
		}

		provider[acc.ID] = item
	}

	return platform, group, provider, &collectedAt, nil
}

// GetProviderAvailability returns the provider map and optional selected group
// used by evaluator and report queries. Nil service returns an error. An
// injected override is called directly; otherwise live statistics apply
// monitoring checks, a positive group selects its aggregate, and nil provider
// maps become allocated empty maps.
func (s *OpsService) GetProviderAvailability(ctx context.Context, platformFilter string, groupIDFilter *int64) (*OpsProviderAvailability, error) {
	if s == nil {
		return nil, errors.New("ops service is nil")
	}

	if s.getProviderAvailability != nil {
		return s.getProviderAvailability(ctx, platformFilter, groupIDFilter)
	}

	_, groupStats, providerStats, collectedAt, err := s.GetProviderAvailabilityStats(ctx, platformFilter, groupIDFilter)
	if err != nil {
		return nil, err
	}

	var group *GroupAvailability
	if groupIDFilter != nil && *groupIDFilter > 0 {
		group = groupStats[*groupIDFilter]
	}

	if providerStats == nil {
		providerStats = map[int64]*ProviderAvailability{}
	}

	return &OpsProviderAvailability{
		Group:       group,
		Providers:   providerStats,
		CollectedAt: collectedAt,
	}, nil
}
