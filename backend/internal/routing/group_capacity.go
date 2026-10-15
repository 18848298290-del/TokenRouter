package routing

import (
	"context"
	"slices"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
)

// group_capacity.go aggregates provider limits and runtime counters into group
// capacity summaries.
// Group capacity combines configured provider limits with runtime occupancy,
// sessions, and request counts. A provider shared by several groups contributes
// its limits and complete observed usage to each group, so group summaries
// cannot be added as disjoint usage totals.
//
// Batch construction deduplicates memberships and reads each provider's
// counters once across the selected groups. Session and RPM observations are
// requested when a group's combined limit is positive, including other members
// of that group. Counter failures leave any returned entries usable and missing
// entries at zero; provider-query failures propagate on the batch path, while
// the sequential listing retains successful group rows and omits failed reads.

// GroupCapacitySummary contains provider limits and observed usage aggregated
// for one group.
type GroupCapacitySummary = accessview.GroupCapacitySummary

// groupCapacityProviderRef identifies one provider membership when attributing
// shared runtime usage.
type groupCapacityProviderRef struct {
	// groupID and providerID form the membership identity used for deduplication.
	// Runtime counters remain keyed by providerID so the same provider can
	// contribute to several group summaries.
	groupID    int64 // member group row ID
	providerID int64 // member provider row ID
}

// CapacityProviderRow associates a group with one provider capacity snapshot.
type CapacityProviderRow struct {
	GroupID  int64                     // associated group row ID
	Provider provider.CapacitySnapshot // member provider capacity snapshot
}

// CapacityProviders reads schedulable provider capacity for one saved group.
type CapacityProviders interface {
	// ListSchedulableByGroupID returns provider capacity snapshots for the
	// requested group. CapacityService filters the slice in place and sums each
	// remaining row, so implementations return disposable storage with one row per
	// provider.
	ListSchedulableByGroupID(context.Context, int64) ([]provider.CapacitySnapshot, error)
}

// CapacityBatchProviders supplies optional batched provider capacity reads
// across groups.
type CapacityBatchProviders interface {
	// ListSchedulableCapacityByGroupIDs returns provider capacity rows for the
	// requested group IDs.
	ListSchedulableCapacityByGroupIDs(context.Context, []int64) ([]CapacityProviderRow, error)
}

// CapacityGroups supplies the active group IDs used for capacity listings.
type CapacityGroups interface {
	// ListActiveIDs returns persistent IDs of groups enabled for routing.
	ListActiveIDs(context.Context) ([]int64, error)
}

// CapacityConcurrency reads current provider occupancy for capacity summary
// construction.
type CapacityConcurrency interface {
	// GetProviderConcurrencyBatch returns occupied concurrency by provider row ID.
	// CapacityService uses any entries returned alongside an error and lets
	// missing IDs contribute zero to the optional usage summary.
	GetProviderConcurrencyBatch(context.Context, []int64) (map[int64]int, error)
}

// CapacitySessions counts provider sessions under each supplied idle timeout.
type CapacitySessions interface {
	// GetActiveSessionCountBatch returns provider session counts using the
	// supplied per-provider idle durations. Capacity callers may include all
	// members of a group with a positive aggregate session limit, including IDs
	// without a configured timeout entry. Entries returned alongside an error still
	// contribute; missing provider counts are zero in CapacityService summaries.
	GetActiveSessionCountBatch(context.Context, []int64, map[int64]time.Duration) (map[int64]int, error)
}

// CapacityRPM reads current per-provider request counts for capacity summaries.
type CapacityRPM interface {
	// GetRPMBatch returns observed request counts by provider row ID.
	// CapacityService uses any entries returned alongside an error and lets
	// missing IDs contribute zero to the optional usage summary.
	GetRPMBatch(context.Context, []int64) (map[int64]int, error)
}

// CapacityService combines group memberships, provider limits, and optional
// runtime observations.
type CapacityService struct {
	providerRepo CapacityProviders // group provider capacity reader
	groupRepo    CapacityGroups    // active group ID reader
	// concurrencyService, sessionLimitCache, and rpmCache supply independent
	// optional observations. Nil skips the corresponding query and leaves that
	// reader's usage contribution at zero while configured limits remain
	// available.
	concurrencyService CapacityConcurrency // optional provider occupancy reader
	sessionLimitCache  CapacitySessions    // optional provider session reader
	rpmCache           CapacityRPM         // optional provider request-count reader
}

// NewCapacityService retains provider, group, and optional runtime-count
// readers. Supply the provider reader for capacity queries and the active-group
// reader for GetAllGroupCapacity; missing runtime readers leave their observed
// usage at zero.
func NewCapacityService(providers CapacityProviders, groups CapacityGroups, concurrency CapacityConcurrency, sessions CapacitySessions, rpm CapacityRPM) *CapacityService {
	return &CapacityService{providerRepo: providers, groupRepo: groups, concurrencyService: concurrency, sessionLimitCache: sessions, rpmCache: rpm}
}

// GetAllGroupCapacity returns summaries for active group IDs from the
// configured reader. A supported batch query is used directly and its errors
// propagate; otherwise groups are read individually and unreadable groups are
// omitted.
func (s *CapacityService) GetAllGroupCapacity(ctx context.Context) ([]GroupCapacitySummary, error) {
	groupIDs, err := s.groupRepo.ListActiveIDs(ctx)
	if err != nil {
		return nil, err
	}

	// A batch reader supplies one provider-query outcome for the whole listing.
	// Return that outcome directly; individual reads are selected only when this
	// reader lacks batch support.
	if lister, ok := s.providerRepo.(CapacityBatchProviders); ok {
		return s.getGroupCapacitiesBatch(ctx, groupIDs, lister)
	}

	return s.getGroupCapacitiesSequential(ctx, groupIDs), nil
}

// getGroupCapacitiesSequential reads group capacity in the supplied ID order
// and keeps successful rows. A failed individual read is skipped, so callers
// receive a partial list with each retained summary assigned its group ID.
func (s *CapacityService) getGroupCapacitiesSequential(ctx context.Context, groupIDs []int64) []GroupCapacitySummary {
	results := make([]GroupCapacitySummary, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		cap, err := s.GetGroupCapacity(ctx, groupID)
		if err != nil {
			// Keep summaries already read and continue to later groups, since this
			// sequential administrative listing can expose partial capacity when one
			// provider query fails.
			continue
		}
		cap.GroupID = groupID
		results = append(results, cap)
	}
	return results
}

// getGroupCapacitiesBatch builds summaries in the supplied group-ID order,
// including groups with no provider rows. Callers supply distinct IDs because
// the group-index map selects the final slot for a repeated ID; provider-query
// errors propagate before runtime counters are consulted.
//
// Membership pairs are deduplicated per group, while provider IDs are
// deduplicated across the batch for count queries. A shared provider's complete
// usage contributes to every associated group, matching each group's access to
// that provider capacity. Session and RPM counters are queried for all members
// of groups with a positive aggregate limit; returned counter entries remain
// usable even when their reader also reports an ignored error.
func (s *CapacityService) getGroupCapacitiesBatch(ctx context.Context, groupIDs []int64, lister CapacityBatchProviders) ([]GroupCapacitySummary, error) {
	results := make([]GroupCapacitySummary, len(groupIDs))
	groupIndex := make(map[int64]int, len(groupIDs))
	for i, groupID := range groupIDs {
		results[i].GroupID = groupID
		groupIndex[groupID] = i
	}
	if len(groupIDs) == 0 {
		return results, nil
	}

	rows, err := lister.ListSchedulableCapacityByGroupIDs(ctx, groupIDs)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return results, nil
	}

	refs := make([]groupCapacityProviderRef, 0, len(rows))
	seenGroupProvider := make(map[groupCapacityProviderRef]struct{}, len(rows))
	providerIDSet := make(map[int64]struct{}, len(rows))
	providerIDs := make([]int64, 0, len(rows))
	sessionTimeouts := make(map[int64]time.Duration)

	for _, row := range rows {
		idx, ok := groupIndex[row.GroupID]
		if !ok || row.Provider.ID <= 0 {
			continue
		}

		acc := row.Provider
		if acc.QuotaAutoPaused {
			continue
		}

		// Deduplicate each membership before adding limits, while retaining a shared
		// provider once for every different group that exposes its capacity.
		ref := groupCapacityProviderRef{groupID: row.GroupID, providerID: row.Provider.ID}
		if _, ok := seenGroupProvider[ref]; ok {
			continue
		}
		seenGroupProvider[ref] = struct{}{}
		refs = append(refs, ref)

		if _, ok := providerIDSet[row.Provider.ID]; !ok {
			providerIDSet[row.Provider.ID] = struct{}{}
			providerIDs = append(providerIDs, row.Provider.ID)
		}

		results[idx].ConcurrencyMax += acc.Concurrency

		if maxSessions := acc.MaxSessions; maxSessions > 0 {
			results[idx].SessionsMax += maxSessions
			// Bounded session counts use the default idle duration when the provider
			// snapshot supplies no positive timeout for interpreting active sessions.
			timeout := time.Duration(acc.SessionIdleTimeoutMinutes) * time.Minute
			if timeout <= 0 {
				timeout = 5 * time.Minute
			}
			sessionTimeouts[acc.ID] = timeout
		}

		if rpm := acc.BaseRPM; rpm > 0 {
			results[idx].RPMMax += rpm
		}
	}

	if len(providerIDs) == 0 {
		return results, nil
	}

	// Runtime readers may fail independently; keep any returned entries and let
	// missing provider IDs contribute zero while configured limits remain
	// available.
	concurrencyMap := map[int64]int{}
	if s.concurrencyService != nil {
		concurrencyMap, _ = s.concurrencyService.GetProviderConcurrencyBatch(ctx, providerIDs)
	}

	sessionProviderIDs := providerIDsForGroupsWithLimit(refs, groupIndex, results, func(summary GroupCapacitySummary) bool {
		return summary.SessionsMax > 0
	})
	var sessionsMap map[int64]int
	if len(sessionProviderIDs) > 0 && s.sessionLimitCache != nil {
		sessionsMap, _ = s.sessionLimitCache.GetActiveSessionCountBatch(ctx, sessionProviderIDs, sessionTimeouts)
	}

	rpmProviderIDs := providerIDsForGroupsWithLimit(refs, groupIndex, results, func(summary GroupCapacitySummary) bool {
		return summary.RPMMax > 0
	})
	var rpmMap map[int64]int
	if len(rpmProviderIDs) > 0 && s.rpmCache != nil {
		rpmMap, _ = s.rpmCache.GetRPMBatch(ctx, rpmProviderIDs)
	}

	for _, ref := range refs {
		idx := groupIndex[ref.groupID]
		results[idx].ConcurrencyUsed += concurrencyMap[ref.providerID]
		if sessionsMap != nil && results[idx].SessionsMax > 0 {
			results[idx].SessionsUsed += sessionsMap[ref.providerID]
		}
		if rpmMap != nil && results[idx].RPMMax > 0 {
			results[idx].RPMUsed += rpmMap[ref.providerID]
		}
	}
	return results, nil
}

// GetGroupCapacityByIDs returns a map for positive distinct requested group
// IDs. A nil service or empty input returns an empty map, and cancellation is
// checked while collecting usable IDs before querying.
//
// Supported batch reads preserve empty groups and return query errors. The
// sequential path omits unreadable groups and retains successful summaries, so
// a missing map entry can indicate a failed group read rather than zero
// capacity. Every retained result receives its requested group identity.
func (s *CapacityService) GetGroupCapacityByIDs(ctx context.Context, groupIDs []int64) (map[int64]GroupCapacitySummary, error) {
	results := make(map[int64]GroupCapacitySummary, len(groupIDs))
	if s == nil || len(groupIDs) == 0 {
		return results, nil
	}

	normalized := make([]int64, 0, len(groupIDs))
	seen := make(map[int64]struct{}, len(groupIDs))
	for _, groupID := range groupIDs {
		if groupID <= 0 {
			continue
		}
		if _, ok := seen[groupID]; ok {
			continue
		}
		seen[groupID] = struct{}{}

		if err := ctx.Err(); err != nil {
			return results, err
		}
		normalized = append(normalized, groupID)
	}

	if lister, ok := s.providerRepo.(CapacityBatchProviders); ok {
		summaries, err := s.getGroupCapacitiesBatch(ctx, normalized, lister)
		if err != nil {
			return results, err
		}
		for _, summary := range summaries {
			results[summary.GroupID] = summary
		}
		return results, nil
	}

	for _, groupID := range normalized {
		capacity, err := s.GetGroupCapacity(ctx, groupID)
		if err != nil {
			// A failed single-group lookup leaves no map entry, while successful earlier
			// groups remain available to this optional capacity consumer.
			continue
		}
		capacity.GroupID = groupID
		results[groupID] = capacity
	}

	return results, nil
}

// providerIDsForGroupsWithLimit returns distinct providers belonging to groups
// accepted by include. Callers supply a valid group-index map and a non-nil
// predicate over the corresponding summaries.
//
// Filtering is based on aggregate group limits, not each provider's individual
// limit. A group with one bounded provider can therefore include other members
// in its runtime-count query, while a provider shared by several accepted
// groups is requested once. IDs retain first-membership order so counter
// requests can be inspected deterministically.
func providerIDsForGroupsWithLimit(refs []groupCapacityProviderRef, groupIndex map[int64]int, summaries []GroupCapacitySummary, include func(GroupCapacitySummary) bool) []int64 {
	seen := make(map[int64]struct{})
	providerIDs := make([]int64, 0)
	for _, ref := range refs {
		idx, ok := groupIndex[ref.groupID]
		if !ok || !include(summaries[idx]) {
			continue
		}
		if _, ok := seen[ref.providerID]; ok {
			continue
		}
		seen[ref.providerID] = struct{}{}
		providerIDs = append(providerIDs, ref.providerID)
	}
	return providerIDs
}

// GetGroupCapacity combines one group's eligible provider limits with optional
// usage counters. Provider-read errors propagate, and an empty or wholly
// quota-paused pool produces a zero summary with GroupID left unset for the
// enclosing caller.
//
// All remaining members contribute concurrency. Positive session and RPM limits
// are summed, and their counters are read for the whole pool when the aggregate
// is positive. Count errors are discarded while returned entries still
// contribute; missing IDs contribute zero. Session idle timeouts default to
// five minutes for bounded providers whose configured duration is nonpositive.
func (s *CapacityService) GetGroupCapacity(ctx context.Context, groupID int64) (GroupCapacitySummary, error) {
	providers, err := s.providerRepo.ListSchedulableByGroupID(ctx, groupID)
	if err != nil {
		return GroupCapacitySummary{}, err
	}
	if len(providers) == 0 {
		return GroupCapacitySummary{}, nil
	}
	// Remove quota-paused snapshots before adding limits because those providers
	// are unavailable to new scheduling. Filtering changes the reader's returned
	// slice storage, which this service treats as disposable.
	providers = slices.DeleteFunc(providers, func(a provider.CapacitySnapshot) bool { return a.QuotaAutoPaused })
	if len(providers) == 0 {
		return GroupCapacitySummary{}, nil
	}

	providerIDs := make([]int64, 0, len(providers))
	sessionTimeouts := make(map[int64]time.Duration)
	var concurrencyMax, sessionsMax, rpmMax int

	for i := range providers {
		acc := &providers[i]
		providerIDs = append(providerIDs, acc.ID)
		concurrencyMax += acc.Concurrency

		if ms := acc.MaxSessions; ms > 0 {
			sessionsMax += ms
			// Apply the same idle-duration default as batch summaries so bounded
			// provider sessions use comparable counting windows on either read path.
			timeout := time.Duration(acc.SessionIdleTimeoutMinutes) * time.Minute
			if timeout <= 0 {
				timeout = 5 * time.Minute
			}
			sessionTimeouts[acc.ID] = timeout
		}

		if rpm := acc.BaseRPM; rpm > 0 {
			rpmMax += rpm
		}
	}

	// Ignore counter errors while using any returned entries, keeping configured
	// limits and observations from the other independent readers available in the
	// summary.
	concurrencyMap := map[int64]int{}
	if s.concurrencyService != nil {
		concurrencyMap, _ = s.concurrencyService.GetProviderConcurrencyBatch(ctx, providerIDs)
	}

	var sessionsMap map[int64]int
	if sessionsMax > 0 && s.sessionLimitCache != nil {
		sessionsMap, _ = s.sessionLimitCache.GetActiveSessionCountBatch(ctx, providerIDs, sessionTimeouts)
	}

	var rpmMap map[int64]int
	if rpmMax > 0 && s.rpmCache != nil {
		rpmMap, _ = s.rpmCache.GetRPMBatch(ctx, providerIDs)
	}

	var concurrencyUsed, sessionsUsed, rpmUsed int
	for _, id := range providerIDs {
		concurrencyUsed += concurrencyMap[id]
		if sessionsMap != nil {
			sessionsUsed += sessionsMap[id]
		}
		if rpmMap != nil {
			rpmUsed += rpmMap[id]
		}
	}

	return GroupCapacitySummary{
		ConcurrencyUsed: concurrencyUsed,
		ConcurrencyMax:  concurrencyMax,
		SessionsUsed:    sessionsUsed,
		SessionsMax:     sessionsMax,
		RPMUsed:         rpmUsed,
		RPMMax:          rpmMax,
	}, nil
}
