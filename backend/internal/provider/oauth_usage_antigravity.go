package provider

import (
	"context"
	"fmt"
	"time"
)

// oauth_usage_antigravity.go fetches and caches Antigravity usage while
// checking the credential identity of shared results.
// Antigravity usage queries associate cached results with credential identity
// while sharing in-flight work by provider row ID. A waiter with changed
// credentials can join that work, then rejects its result identity before
// returning a copied display observation.
//
// Fresh cache reads copy display values, so countdown recalculation cannot
// mutate stored data. Direct cache hits use an unclamped integer countdown,
// while the flight-cache path uses RecalcAntigravityRemainingSeconds and
// clamps expired countdowns. New fetches run under detached thirty-second
// activity canceled by service shutdown. Query errors become enriched display
// failures and can be cached with nil returned error; context cancellation
// from shutdown stops that conversion. Callers keep the record stable and
// provide reached fetch, degradation, and enrichment callbacks.

// AntigravityUsageOptions supplies quota qualification and account-specific
// fetch, degradation, and enrichment.
type AntigravityUsageOptions struct {
	// CanFetch optionally qualifies records for quota reads. Missing or false
	// qualification returns an empty dated display before cache or protocol work.
	CanFetch func(*Record) bool
	// Fetch is required for qualified cache misses and receives detached work.
	// Success supplies nonnil display data for future cached reads; keep the
	// borrowed record stable while this callback consumes its credentials.
	Fetch func(context.Context, *Record) (*UsageInfo, error)
	// Degrade is required after fetch failure and supplies nonnil diagnostic
	// display data for cached reads. That enriched result returns with nil error.
	Degrade func(error, time.Time) *UsageInfo
	// Enrich is required after fetch or degradation and may mutate its display.
	// Query identity remains the credential observation captured before shared
	// work starts.
	Enrich func(*UsageInfo, *Record)
}

// antigravityUsageFlightResult associates shared quota display data with
// queried credential identity.
type antigravityUsageFlightResult struct {
	// Usage is shared across flight waiters. Each matching consumer copies its
	// current mutable fields before returning independently editable display data.
	Usage    *UsageInfo
	Identity string // queried credential identity digest
}

// GetAntigravityUsage returns an independently mutable quota display for a
// stable provider record. Missing or false CanFetch yields an
// observation-time-only result; a qualifying input must be nonnil and requires
// the service cache and reached callbacks.
//
// Cache age and identity decide reuse. Row-keyed flights can be joined by
// different credentials, so their captured identity is checked before copying
// nonnil results. Detached fetch cancellation returns an error; other failures
// may become enriched cached diagnostics with nil error, including deadline
// outcomes. Direct cache countdowns can be negative, while flight-cache
// countdowns clamp at zero. Wrong or nil flight values become an
// observation-time-only view. Supply a nonnil context when a cache miss
// reaches detached activity registration.
func (s *OAuthUsageService) GetAntigravityUsage(ctx context.Context, provider *Record) (*UsageInfo, error) {
	if s.options.Antigravity.CanFetch == nil || !s.options.Antigravity.CanFetch(provider) {
		now := s.options.Now()
		return &UsageInfo{UpdatedAt: &now}, nil
	}

	identity := UsageCacheIdentity(provider)
	// Identity prevents a cached account observation from crossing a credential
	// change. LoadAntigravity already copied the view before this countdown
	// update.
	if cache, ok := s.cache.LoadAntigravity(provider.ID); ok && cache.Identity == identity {
		ttl := AntigravityCacheTTL(cache.UsageInfo)
		if s.options.Now().Sub(cache.Timestamp) < ttl {
			usage := cache.UsageInfo
			if usage.FiveHour != nil && usage.FiveHour.ResetsAt != nil {
				// Keep signed truncated seconds here, allowing
				// already elapsed resets to remain visible
				// through this direct cache-hit countdown.
				usage.FiveHour.RemainingSeconds = int(usage.FiveHour.ResetsAt.Sub(s.options.Now()).Seconds())
			}
			return usage, nil
		}
	}

	// Row-key sharing lets changed credentials join an existing flight. Its
	// completed identity check below rejects that mismatched observation before
	// returning data.
	flightKey := fmt.Sprintf("ag-usage:%d", provider.ID)
	result, flightErr, _ := s.cache.DoAntigravity(flightKey, func() (any, error) {
		// A completed peer may have filled the cache before this flight begins.
		// Recheck age and identity so the new leader can reuse that copied
		// observation.
		if cache, ok := s.cache.LoadAntigravity(provider.ID); ok && cache.Identity == identity {
			ttl := AntigravityCacheTTL(cache.UsageInfo)
			if s.options.Now().Sub(cache.Timestamp) < ttl {
				usage := cache.UsageInfo
				// This flight-cache path clamps expired
				// countdowns through the helper; the direct
				// cache-hit path above instead retains its
				// signed duration conversion.
				RecalcAntigravityRemainingSeconds(usage, s.options.Now)
				return antigravityUsageFlightResult{usage, identity}, nil
			}
		}

		// Detach the shared fetch from caller cancellation while preserving context
		// values. Service shutdown still cancels this registered thirty-second
		// budgeted work.
		fetchCtx, fetchCancel, beginErr := s.BeginDetached(ctx, 30*time.Second)
		if beginErr != nil {
			return nil, beginErr
		}
		defer fetchCancel()

		fetchedUsage, err := s.options.Antigravity.Fetch(fetchCtx, provider)
		if fetchCtx.Err() == context.Canceled {
			return nil, fetchCtx.Err()
		}
		// Fetch failure other than shutdown cancellation becomes display data.
		// Enrichment and copied cache publication retain that diagnostic for later
		// reads while the shared result itself reports nil error.
		if err != nil {
			degraded := s.options.Antigravity.Degrade(err, s.options.Now())
			s.options.Antigravity.Enrich(degraded, provider)
			s.cache.StoreAntigravity(provider.ID, &OAuthAntigravityUsageCache{
				Identity:  identity,
				UsageInfo: CloneUsageInfo(degraded),
				Timestamp: s.options.Now(),
			})
			return antigravityUsageFlightResult{CloneUsageInfo(degraded), identity}, nil
		}

		s.options.Antigravity.Enrich(fetchedUsage, provider)
		s.cache.StoreAntigravity(provider.ID, &OAuthAntigravityUsageCache{
			Identity:  identity,
			UsageInfo: CloneUsageInfo(fetchedUsage),
			Timestamp: s.options.Now(),
		})
		return antigravityUsageFlightResult{CloneUsageInfo(fetchedUsage), identity}, nil
	})

	if flightErr != nil {
		return nil, flightErr
	}
	completed, ok := result.(antigravityUsageFlightResult)
	if !ok || completed.Usage == nil {
		now := s.options.Now()
		return &UsageInfo{UpdatedAt: &now}, nil
	}
	if completed.Identity != identity {
		return nil, ErrUsageObservationChanged
	}
	return CloneUsageInfo(completed.Usage), nil
}
