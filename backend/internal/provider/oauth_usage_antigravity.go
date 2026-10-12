package provider

import (
	"context"
	"fmt"
	"time"
)

// AntigravityUsageOptions supplies quota eligibility, fetching, error conversion, and enrichment.
type AntigravityUsageOptions struct {
	CanFetch func(*Record) bool
	Fetch    func(context.Context, *Record) (*UsageInfo, error)
	Degrade  func(error, time.Time) *UsageInfo
	Enrich   func(*UsageInfo, *Record)
}

// antigravityUsageFlightResult identifies the credentials behind a shared query result.
type antigravityUsageFlightResult struct {
	Usage    *UsageInfo
	Identity string
}

// GetAntigravityUsage returns cached or freshly fetched Antigravity quota information.
func (s *OAuthUsageService) GetAntigravityUsage(ctx context.Context, provider *Record) (*UsageInfo, error) {
	if s.options.Antigravity.CanFetch == nil || !s.options.Antigravity.CanFetch(provider) {
		now := s.options.Now()
		return &UsageInfo{UpdatedAt: &now}, nil
	}

	identity := UsageCacheIdentity(provider)
	// Reuse a fresh result only when its credential identity matches.
	if cache, ok := s.cache.LoadAntigravity(provider.ID); ok && cache.Identity == identity {
		ttl := AntigravityCacheTTL(cache.UsageInfo)
		if s.options.Now().Sub(cache.Timestamp) < ttl {
			usage := cache.UsageInfo
			if usage.FiveHour != nil && usage.FiveHour.ResetsAt != nil {
				usage.FiveHour.RemainingSeconds = int(usage.FiveHour.ResetsAt.Sub(s.options.Now()).Seconds())
			}
			return usage, nil
		}
	}

	// Coalesce concurrent queries for this provider.
	flightKey := fmt.Sprintf("ag-usage:%d", provider.ID)
	result, flightErr, _ := s.cache.DoAntigravity(flightKey, func() (any, error) {
		// Another query may have filled the cache before this flight started.
		if cache, ok := s.cache.LoadAntigravity(provider.ID); ok && cache.Identity == identity {
			ttl := AntigravityCacheTTL(cache.UsageInfo)
			if s.options.Now().Sub(cache.Timestamp) < ttl {
				usage := cache.UsageInfo
				// Recalculate the countdown from the current clock.
				RecalcAntigravityRemainingSeconds(usage, s.options.Now)
				return antigravityUsageFlightResult{usage, identity}, nil
			}
		}

		// The shared fetch has its own timeout and is canceled when the service stops.
		fetchCtx, fetchCancel, beginErr := s.BeginDetached(ctx, 30*time.Second)
		if beginErr != nil {
			return nil, beginErr
		}
		defer fetchCancel()

		fetchedUsage, err := s.options.Antigravity.Fetch(fetchCtx, provider)
		if fetchCtx.Err() == context.Canceled {
			return nil, fetchCtx.Err()
		}
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
