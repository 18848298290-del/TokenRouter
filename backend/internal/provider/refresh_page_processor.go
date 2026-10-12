package provider

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
)

// A candidate page can contain providers from several platforms with different
// failure state and shared admission limits. Eligibility and expiry are checked
// before grouping, then each platform runs its own worker pool and reports
// successful, skipped and failed outcomes back to the page. The counts include
// records loaded from storage even when their platform has no usable refresher.
//
// Jobs and results are buffered for the complete provider group so workers can
// finish before results are consumed. Workers record platform outcomes as they
// complete, and a tripped state or canceled context skips queued work. The
// processor joins every worker before closing result channels, allowing callers
// to retain a page cursor until all admitted work has returned. Providers and
// execution callbacks remain borrowed during that joined interval.

// RefreshProviderExecution binds one platform's eligibility callbacks, failure
// state and exchange operation.
type RefreshProviderExecution struct {
	Platform string // registered platform diagnostic label
	// State, CanRefresh, NeedsRefresh and Execute are borrowed while the page runs.
	// CanRefresh may be nil to skip eligibility; reached expiry and execution paths
	// require callable callbacks and a nonnil failure state.
	State        *RefreshProviderState             // shared group failure observation
	CanRefresh   func(*Record) bool                // platform credential eligibility check
	NeedsRefresh func(*Record, time.Duration) bool // provider expiry window check
	// one provider exchange operation
	Execute func(context.Context, *Record, time.Duration, *RefreshProviderState) error
}

// RefreshPageProcessor runs joined platform workers and counts each queued
// provider's outcome.
type RefreshPageProcessor struct {
	// Concurrency needs to be positive for queued providers to reach a worker.
	// Nonpositive values create no workers and return zero outcome counts.
	Concurrency int
	Info, Warn  func(string, ...any) // optional per-provider outcome loggers
}

// ProcessPage checks eligibility and expiry, runs each platform group, and
// returns combined candidate and outcome counts. Callers keep providers,
// providerStates and reached callbacks stable until every group finishes; ctx
// needs to be nonnil.
//
// Each group borrows pointers into the page and sends one aggregate result
// through a channel sized for all groups. The wait joins platform work before
// closing that channel, so result collection cannot finish while an admitted
// exchange remains running. Total counts all loaded records, OAuth counts
// accepted CanRefresh results, and NeedsRefresh counts the subset queued for
// exchange; policies determine which completed operations count as successful or
// skipped.
func (s RefreshPageProcessor) ProcessPage(
	ctx context.Context,
	providers []Record,
	providerStates map[string]*RefreshProviderExecution,
	refreshWindow time.Duration,
) RefreshPageStats {
	stats := RefreshPageStats{Total: len(providers)}
	groups := make(map[string][]*Record)
	for i := range providers {
		provider := &providers[i]
		state := providerStates[provider.Platform]
		if state == nil || state.CanRefresh == nil || !state.CanRefresh(provider) {
			continue
		}
		stats.OAuth++
		if !state.NeedsRefresh(provider, refreshWindow) {
			continue
		}
		stats.NeedsRefresh++
		groups[provider.Platform] = append(groups[provider.Platform], provider)
	}

	// providerResult carries one joined platform group's outcomes to page
	// aggregation.
	type providerResult struct {
		refreshed int // successful group outcome count
		skipped   int // skipped group outcome count
		failed    int // failed group outcome count
	}
	// Buffer one aggregate per group so every platform can return before this
	// goroutine joins them and begins collecting their outcome counts.
	results := make(chan providerResult, len(groups))
	var wg sync.WaitGroup
	for platform, group := range groups {
		state := providerStates[platform]
		wg.Go(func() {
			refreshed, skipped, failed := s.ProcessProvider(ctx, state, group, refreshWindow)
			results <- providerResult{refreshed: refreshed, skipped: skipped, failed: failed}
		})
	}
	wg.Wait()
	close(results)
	for result := range results {
		stats.Refreshed += result.refreshed
		stats.Skipped += result.skipped
		stats.Failed += result.failed
	}
	return stats
}

// ProcessProvider returns successful, skipped and failed counts after the
// selected platform group finishes. Nil state or an empty group returns zero;
// reached workers require nonnil provider records, failure state, context and
// callable execution.
//
// A positive Concurrency starts at most that many workers, each checking
// cancellation and trip state before executing a job. Outcomes are recorded into
// the shared state before their result is queued, so later jobs can observe a
// sibling's trip. Both channels hold the full group and workers join before
// result consumption. Nil loggers become no-ops, and an executor that ignores
// cancellation keeps this call waiting for its actual return.
func (s RefreshPageProcessor) ProcessProvider(
	ctx context.Context,
	state *RefreshProviderExecution,
	providers []*Record,
	refreshWindow time.Duration,
) (refreshed, skipped, failed int) {
	if state == nil || len(providers) == 0 {
		return 0, 0, 0
	}
	if s.Info == nil {
		s.Info = func(string, ...any) {}
	}
	if s.Warn == nil {
		s.Warn = func(string, ...any) {}
	}
	// refreshResult carries one queued provider's execution result to outcome
	// classification.
	type refreshResult struct {
		providerID int64 // queued provider diagnostic ID
		err        error // classified provider execution result
	}
	// Buffer the complete group so job submission and worker result sends can
	// finish before joining; each queued provider produces at most one result.
	jobs := make(chan *Record, len(providers))
	results := make(chan refreshResult, len(providers))
	workerCount := min(s.Concurrency, len(providers))
	var wg sync.WaitGroup
	for range workerCount {
		wg.Go(func() {
			for provider := range jobs {
				// Queued jobs still emit a skip result
				// after cancellation or a sibling trip,
				// keeping the joined group's outcome
				// counts tied to submitted IDs.
				if ctx.Err() != nil || state.State.IsTripped() {
					results <- refreshResult{providerID: provider.ID, err: ErrRefreshSkipped}
					continue
				}
				// The second observation can see a sibling
				// trip after the first read. This queued
				// provider still reports a skip and leaves
				// execution unstarted.
				if state.State.IsTripped() {
					results <- refreshResult{providerID: provider.ID, err: ErrRefreshSkipped}
					continue
				}
				err := state.Execute(ctx, provider, refreshWindow, state.State)
				state.State.RecordResult(err)
				results <- refreshResult{providerID: provider.ID, err: err}
			}
		})
	}
	for _, provider := range providers {
		jobs <- provider
	}
	// End submission before joining workers, then close results after every sender
	// exits so outcome collection cannot race a still-running exchange.
	close(jobs)
	wg.Wait()
	close(results)

	for result := range results {
		switch {
		case result.err == nil:
			refreshed++
			s.Info("token_refresh.provider_refreshed", "provider_id", result.providerID, "platform", state.Platform)
		case errors.Is(result.err, ErrRefreshSkipped):
			skipped++
		default:
			failed++
			s.Warn("token_refresh.provider_refresh_failed", "provider_id", result.providerID, "platform", state.Platform, "error", logredact.RedactText(result.err.Error()))
		}
	}
	return refreshed, skipped, failed
}
