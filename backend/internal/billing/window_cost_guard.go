package billing

import (
	"context"
	"maps"
	"sync/atomic"
	"time"
)

// window_cost_guard.go checks provider spending admission and prefetches cost
// observations for scheduling.
// Provider window admission uses standard cost and distinguishes new
// scheduling from an existing sticky session's reserve. Prefetched and
// shared-cache values are keyed by provider ID alone; stored window bounds
// select a database query start only after those values miss. A storage read
// failure permits scheduling, while a successful cost observation passes
// through the ordinary/sticky/blocked classification.
//
// Prefetch batches cache reads, groups misses by Unix-second start and uses
// batch database reads when supported. Successful missing batch rows become
// zero cost, while failed individual reads remain absent for later fallback.
// Duplicate provider IDs are retained, and equal-second starts share the last
// observed anchor for that group. Returned context values clone the completed
// cost map; atomic counters record prefetch observations and cache-write
// failures remain best effort.

// sharedWindowCostMetrics accumulates process-wide prefetch observations for
// guards that request SharedWindowCostMetrics; individual counter operations
// are atomic, while a multi-counter read is not one snapshot.
var sharedWindowCostMetrics WindowCostMetrics

// WindowCostCache supplies provider-ID cost observations and best-effort
// population for scheduling guards.
type WindowCostCache interface {
	// GetWindowCost returns a provider-ID cache observation with its hit flag.
	// The guard uses a value only for a hit with nil error; misses and failed
	// reads select storage, and zero can be a valid cached cost.
	GetWindowCost(ctx context.Context, providerID int64) (cost float64, hit bool, err error)

	// SetWindowCost stores one provider-ID cost observation using the cache
	// implementation's lifetime. Guard callers discard population errors, so a
	// failed write affects later lookup work rather than the current successful
	// cost decision.
	SetWindowCost(ctx context.Context, providerID int64, cost float64) error

	// GetWindowCostBatch returns cached observations indexed by requested
	// provider ID. Successful omitted entries are misses; Prefetch discards the
	// companion map on error and queries missing costs through its database
	// source.
	GetWindowCostBatch(ctx context.Context, providerIDs []int64) (map[int64]float64, error)
}

// CostWindowInput carries a provider's scheduling ceiling, sticky reserve and
// optional query bounds.
type CostWindowInput struct {
	ID int64 // selected provider record identifier
	// Enabled is supplied by the caller's provider policy. False bypasses Allow
	// entirely and excludes this input from Prefetch, regardless of its observed
	// cost or limits.
	Enabled bool // apply this cost-window gate
	// Limit and Reserve define regular admission and its extra sticky interval. A
	// nonpositive Limit disables the gate; a positive Reserve allows bound
	// sessions after the regular ceiling until their combined cutoff.
	// Limit: regular standard-cost ceiling USD.
	// Reserve: extra sticky-session allowance USD.
	Limit, Reserve float64
	// Start and End jointly choose the storage query anchor. Both present with
	// future End retain Start unchanged; other combinations use the current local
	// hour, with no validation of the retained start's ordering.
	// Start: optional stored window start.
	// End: optional stored window end.
	Start, End *time.Time
}

type (
	// WindowCostStats carries one queried provider's aggregated standard cost
	// before its provider multiplier.
	WindowCostStats struct {
		StandardCost float64 // USD
	}
	// WindowCostSource supplies a nonnil standard-cost observation on successful
	// single-provider reads.
	WindowCostSource interface {
		// GetWindow returns standard cost for one provider from the supplied start,
		// or a query error. Success supplies nonnil stats because the guard
		// dereferences it; Allow permits scheduling on read errors.
		GetWindow(context.Context, int64, time.Time) (*WindowCostStats, error)
	}
)

// WindowCostBatchSource optionally aggregates multiple providers from a shared
// window start.
type WindowCostBatchSource interface {
	// GetWindows returns provider stats sharing the supplied query start.
	// Prefetch treats omitted or nil successful entries as zero, and a returned
	// error causes it to discard this map and query each provider individually.
	GetWindows(context.Context, []int64, time.Time) (map[int64]*WindowCostStats, error)
}

// WindowCostMetrics holds independently atomic counts for prefetch cache and
// database observations.
type WindowCostMetrics struct {
	// Hit, Miss, BatchSQL, Fallback and Errors accumulate prefetch observations
	// independently. Counts use returned cache entries, estimated missing IDs and
	// attempted queries, including duplicates. Keep these atomic values at their
	// original addresses after first use.
	// Hit: cached provider entry count.
	// Miss: estimated missing provider entries.
	// BatchSQL: attempted start-group batch queries.
	// Fallback: individual storage fallback attempts.
	// Errors: prefetch read failure observations.
	Hit, Miss, BatchSQL, Fallback, Errors atomic.Int64
}

// WindowCostGuardOptions supplies callbacks and counters retained directly by
// the scheduling guard.
type WindowCostGuardOptions struct {
	// Now is retained without a default. Supply it when missed costs need a query
	// anchor or Prefetch measures a batch query duration.
	Now func() time.Time // query-time observation callback
	// Log is retained without a nil guard. Prefetch invokes it for failed batch
	// cache or database reads; Allow's storage-error admission path does not use
	// it.
	Log func(string, ...any) // prefetch failure logging callback
	// Debug is retained without a nil guard. Successful batch database prefetch
	// invokes it with provider count, window start and elapsed milliseconds.
	Debug func(string, ...any) // successful batch-query debug callback
	// Stats is required when Prefetch reaches its read/counter paths and is
	// retained directly. Allow does not update these counters;
	// SharedWindowCostMetrics supplies the process-wide instance.
	Stats *WindowCostMetrics // prefetch observation counter pointer
}

// WindowCostGuard selects cached or queried standard cost for provider
// scheduling decisions.
type WindowCostGuard struct {
	// cache can be absent for Allow, which then queries storage. Prefetch
	// requires both cache and source and returns its original context when either
	// dependency is absent.
	cache WindowCostCache // optional provider-ID cost cache
	// source is required by Allow after both cost caches miss on an enabled gate.
	// Prefetch also requires it and uses WindowCostBatchSource when this
	// implementation exposes batch reads.
	source WindowCostSource // provider standard-cost query source
	// now, log, debug and stats retain NewWindowCostGuard's supplied options
	// directly. No defaults or synchronization wrapper are installed; callers
	// keep callbacks and counter storage usable throughout guard calls.
	now   func() time.Time     // retained query-time observation callback
	log   func(string, ...any) // retained prefetch failure logger
	debug func(string, ...any) // retained batch-query debug logger
	stats *WindowCostMetrics   // retained atomic prefetch counters
}

// windowCostPrefetchKey identifies the private context map of provider
// standard-cost observations.
type windowCostPrefetchKey struct{}

// SharedWindowCostMetrics returns the process-wide counter storage used by
// configured guards. Callers reuse this pointer and access the atomic fields
// individually; reading several counters does not produce an atomic aggregate
// snapshot.
func SharedWindowCostMetrics() *WindowCostMetrics { return &sharedWindowCostMetrics }

// NewWindowCostGuard retains cache, source and options without performing I/O
// or supplying defaults. Source and Now are needed for enabled misses; active
// Prefetch paths additionally require usable Stats and their invoked logging
// callbacks.
func NewWindowCostGuard(cache WindowCostCache, source WindowCostSource, options WindowCostGuardOptions) *WindowCostGuard {
	return &WindowCostGuard{cache: cache, source: source, now: options.Now, log: options.Log, debug: options.Debug, stats: options.Stats}
}

// WithPrefetchedWindowCosts stores a cloned scalar cost map in a child of
// nonnil ctx. Keep the input map stable during cloning; later input changes
// are independent, and the child retains its parent's cancellation/deadline
// behavior.
func WithPrefetchedWindowCosts(ctx context.Context, costs map[int64]float64) context.Context {
	return context.WithValue(ctx, windowCostPrefetchKey{}, maps.Clone(costs))
}

// PrefetchedWindowCost returns a stored provider observation and whether that
// ID exists in the private context map. Nil context, nonpositive IDs or
// absent/empty maps return zero and false; a present zero cost remains a hit.
func PrefetchedWindowCost(ctx context.Context, id int64) (float64, bool) {
	if ctx == nil || id <= 0 {
		return 0, false
	}
	m, ok := ctx.Value(windowCostPrefetchKey{}).(map[int64]float64)
	if !ok || len(m) == 0 {
		return 0, false
	}
	v, found := m[id]
	return v, found
}

// Prefetch collects enabled providers, skipping limits at or below zero. Nil
// context, empty input or missing cache/source
// returns the input context unchanged. The provider ID sequence retains
// duplicates, and the last included input for each ID supplies its bounds;
// finite cost/limit inputs are the caller's responsibility.
//
// Successful batch cache values seed the map, and cache-read errors discard
// their companion values. Misses are grouped by Unix seconds of
// CurrentCostWindowStart, retaining the last observed full time for each
// second key; subsecond-distinct starts can therefore share one query anchor.
// Clock observations occur per miss rather than as one frozen time for the
// whole call.
//
// A supported successful batch query maps missing/nil rows to zero. Batch
// absence or failure queries each missing ID, retaining only successful nonnil
// observations and leaving failed IDs absent for Allow's later lookup. Cache
// population errors are ignored; counters describe attempts/returned entries,
// not unique provider IDs. The final context clones collected scalar values
// before publication.
func (s *WindowCostGuard) Prefetch(ctx context.Context, providers []CostWindowInput) context.Context {
	if ctx == nil || len(providers) == 0 || s.cache == nil || s.source == nil {
		return ctx
	}

	providerByID := make(map[int64]*CostWindowInput)
	providerIDs := make([]int64, 0, len(providers))
	for i := range providers {
		provider := &providers[i]
		if provider == nil || !provider.Enabled {
			continue
		}
		if provider.Limit <= 0 {
			continue
		}
		providerByID[provider.ID] = provider
		providerIDs = append(providerIDs, provider.ID)
	}
	if len(providerIDs) == 0 {
		return ctx
	}

	costs := make(map[int64]float64, len(providerIDs))
	// Try one batch cache read for the collected IDs. An errored response
	// contributes no companion values, leaving its requested observations for
	// storage fallback.
	cacheValues, err := s.cache.GetWindowCostBatch(ctx, providerIDs)
	if err == nil {
		maps.Copy(costs, cacheValues)
		s.stats.Hit.Add(int64(len(cacheValues)))
	} else {
		s.stats.Errors.Add(1)
		s.log("window_cost batch cache read failed: %v", err)
	}
	// Count estimated misses from raw input and returned-map sizes, so duplicate
	// IDs or extra response entries can differ from a unique-provider count.
	cacheMissCount := max(len(providerIDs)-len(costs), 0)
	s.stats.Miss.Add(int64(cacheMissCount))

	missingByStart := make(map[int64][]int64)
	startTimes := make(map[int64]time.Time)
	for _, providerID := range providerIDs {
		if _, ok := costs[providerID]; ok {
			continue
		}
		provider := providerByID[providerID]
		if provider == nil {
			continue
		}
		startTime := CurrentCostWindowStart(provider.Start, provider.End, s.now())
		// Group queries at whole-second precision; starts that differ only below a
		// second share the latest full timestamp retained for that second key.
		startKey := startTime.Unix()
		missingByStart[startKey] = append(missingByStart[startKey], providerID)
		startTimes[startKey] = startTime
	}
	if len(missingByStart) == 0 {
		return WithPrefetchedWindowCosts(ctx, costs)
	}

	batchReader, hasBatch := s.source.(WindowCostBatchSource)
	for startKey, ids := range missingByStart {
		startTime := startTimes[startKey]

		if hasBatch {
			s.stats.BatchSQL.Add(1)
			queryStart := s.now()
			statsByProvider, err := batchReader.GetWindows(ctx, ids, startTime)
			if err == nil {
				// Convert the separately observed batch duration to whole milliseconds for
				// debug metadata; this timing value does not participate in cost
				// admission.
				s.debug("window_cost_batch_query_ok",
					"providers", len(ids),
					"window_start", startTime.Format(time.RFC3339),
					"duration_ms", s.now().Sub(queryStart).Milliseconds())
				for _, providerID := range ids {
					// A successful batch's missing row denotes zero standard cost here, so
					// record a zero observation rather than schedule another individual read
					// for that ID.
					stats := statsByProvider[providerID]
					cost := 0.0
					if stats != nil {
						cost = stats.StandardCost
					}
					costs[providerID] = cost
					// Cache population failure leaves the successful queried observation in
					// this request's map; later requests can read storage again instead of
					// failing current prefetch.
					_ = s.cache.SetWindowCost(ctx, providerID, cost)
				}
				continue
			}
			s.stats.Errors.Add(1)
			s.log("window_cost batch db query failed: start=%s err=%v", startTime.Format(time.RFC3339), err)
		}

		// Batch absence or error leaves this start group for individual reads.
		// Failed individual observations remain absent, allowing Allow to attempt
		// its own storage lookup later.
		s.stats.Fallback.Add(int64(len(ids)))
		for _, providerID := range ids {
			stats, err := s.source.GetWindow(ctx, providerID, startTime)
			if err != nil {
				// Skip a failed single-provider read instead of caching zero for that
				// failure, so partial prefetch does not invent an available cost
				// observation.
				s.stats.Errors.Add(1)
				continue
			}
			cost := stats.StandardCost
			costs[providerID] = cost
			// Retain each successful individual cost in the context even when cache
			// population fails; this best-effort write does not determine the current
			// observed amount.
			_ = s.cache.SetWindowCost(ctx, providerID, cost)
		}
	}

	return WithPrefetchedWindowCosts(ctx, costs)
}

// Allow reports whether an enabled provider's observed standard cost permits
// this scheduling attempt. Disabled gates and nonpositive limits pass
// immediately. Otherwise it chooses a prefetched provider-ID observation, then
// a successful shared-cache hit, then a storage query using
// CurrentCostWindowStart.
//
// Cache hits are reused without checking the supplied window bounds because
// their key contains provider ID alone. A storage error, including a returned
// cancellation error, permits scheduling; successful storage reads require
// nonnil stats and may populate the cache best effort. Ordinary classification
// allows new and sticky requests below the regular limit, only sticky requests
// in a positive reserve interval, and neither at the final cutoff. This method
// updates no prefetch metrics and validates no finite monetary inputs.
func (s *WindowCostGuard) Allow(ctx context.Context, provider CostWindowInput, isSticky bool) bool {
	if !provider.Enabled {
		return true
	}

	limit := provider.Limit
	if limit <= 0 {
		return true
	}

	// Reuse a request's prefetched cost before shared-cache lookup. Both
	// observations identify provider ID alone, so successful hits bypass
	// window-bound query selection.
	var currentCost float64
	if cost, ok := PrefetchedWindowCost(ctx, provider.ID); ok {
		currentCost = cost
		goto checkSchedulability
	}
	if s.cache != nil {
		if cost, hit, err := s.cache.GetWindowCost(ctx, provider.ID); err == nil && hit {
			currentCost = cost
			goto checkSchedulability
		}
	}

	// Neither prefetched nor shared cache supplied a successful observation.
	// Select the stored active bounds or current local hour before asking for
	// standard cost.
	{
		startTime := CurrentCostWindowStart(provider.Start, provider.End, s.now())

		stats, err := s.source.GetWindow(ctx, provider.ID, startTime)
		if err != nil {
			// A failed storage read leaves cost unknown and this guard permits
			// scheduling, including returned cancellation errors; downstream request
			// work still owns its context handling.
			return true
		}

		// Compare the queried standard-cost amount directly with this provider
		// window's ceiling; provider-specific billing multipliers are outside this
		// observed cost basis.
		currentCost = stats.StandardCost

		// Populate shared cache best effort after a successful read. A failed write
		// keeps the current query amount for classification and only leaves later
		// lookup work.
		if s.cache != nil {
			_ = s.cache.SetWindowCost(ctx, provider.ID, currentCost)
		}
	}

checkSchedulability:
	schedulability := CheckWindowCost(currentCost, provider.Limit, provider.Reserve)

	switch schedulability {
	case WindowCostSchedulable:
		return true
	case WindowCostStickyOnly:
		return isSticky
	case WindowCostNotSchedulable:
		return false
	}
	return true
}
