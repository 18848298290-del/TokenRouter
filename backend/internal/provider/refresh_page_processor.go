package provider

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
)

// RefreshProviderExecution supplies platform eligibility checks and one-provider refresh execution.
type RefreshProviderExecution struct {
	Platform     string
	State        *RefreshProviderState
	CanRefresh   func(*Record) bool
	NeedsRefresh func(*Record, time.Duration) bool
	Execute      func(context.Context, *Record, time.Duration, *RefreshProviderState) error
}

// RefreshPageProcessor groups candidates by platform, runs workers, handles cancellation and platform blocks,
// and counts results.
type RefreshPageProcessor struct {
	Concurrency int
	Info, Warn  func(string, ...any)
}

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

	type providerResult struct {
		refreshed int
		skipped   int
		failed    int
	}
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
	type refreshResult struct {
		providerID int64
		err        error
	}
	jobs := make(chan *Record, len(providers))
	results := make(chan refreshResult, len(providers))
	workerCount := min(s.Concurrency, len(providers))
	var wg sync.WaitGroup
	for range workerCount {
		wg.Go(func() {
			for provider := range jobs {
				if ctx.Err() != nil || state.State.IsTripped() {
					results <- refreshResult{providerID: provider.ID, err: ErrRefreshSkipped}
					continue
				}
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
