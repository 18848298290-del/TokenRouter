package provider

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// ManagementListReader supplies paginated provider queries with server-side sorting.
type ManagementListReader interface {
	ListProviders(context.Context, int, int, string, string, string, string, int64, string, string, string) ([]Record, int64, error)
}
type ManagementListInput struct {
	Page, PageSize                         int
	Platform, ProviderType, Status, Search string
	GroupID                                int64
	PrivacyMode, SortBy, SortOrder         string
	IncludeSchedulerScore                  bool
}
type ManagementListResult struct {
	Items []RuntimeStatus
	Total int64
}

// ManagementList combines provider records with runtime status and optional scheduling scores.
type ManagementList struct {
	admin   ManagementListReader
	runtime *RuntimeStatusReader
	scores  *SchedulerScoreView
	ollama  *OllamaCloudUsageService
}

func NewManagementList(admin ManagementListReader, runtime *RuntimeStatusReader, scores *SchedulerScoreView, ollama *OllamaCloudUsageService) *ManagementList {
	return &ManagementList{admin, runtime, scores, ollama}
}

func runtimeConfig(v *Record) *RuntimeConfig {
	return &RuntimeConfig{Extra: v.Extra, Concurrency: v.Concurrency, SessionWindowStart: v.SessionWindowStart, SessionWindowEnd: v.SessionWindowEnd}
}

func (s *ManagementList) List(ctx context.Context, input ManagementListInput) (*ManagementListResult, error) {
	page, pageSize, platform, providerType, status, search, groupID, privacyMode, sortBy, sortOrder, includeSchedulerScore := input.Page, input.PageSize, input.Platform, input.ProviderType, input.Status, input.Search, input.GroupID, input.PrivacyMode, input.SortBy, input.SortOrder, input.IncludeSchedulerScore
	providers, total, err := s.admin.ListProviders(ctx, page, pageSize, platform, providerType, status, search, groupID, privacyMode, sortBy, sortOrder)
	if err != nil {
		return nil, err
	}
	if s.ollama != nil && len(providers) > 0 {
		providerPointers := make([]*Record, len(providers))
		for index := range providers {
			providerPointers[index] = &providers[index]
		}
		if err := s.ollama.ResolveProviders(ctx, providerPointers); err != nil {
			return nil, err
		}
	}

	providerIDs := make([]int64, len(providers))
	for i, acc := range providers {
		providerIDs[i] = acc.ID
	}

	concurrencyCounts := make(map[int64]int)
	var windowCosts map[int64]float64
	var activeSessions map[int64]int
	var rpmCounts map[int64]int
	// Calculate advanced scheduling scores when the caller requests that column.
	var schedulerScores map[int64]*ProviderSchedulerScore
	var schedulerGroupScores map[int64][]ProviderSchedulerGroupScore
	if includeSchedulerScore && len(providers) > 0 {
		schedulerScores, schedulerGroupScores = s.scores.Build(ctx, providers, platform, providerType, status, search, groupID, privacyMode)
	}

	// Read concurrency for the entire page when the counter is configured.
	if s.runtime.options.Concurrency != nil {
		if cc, ccErr := s.runtime.options.Concurrency(ctx, providerIDs); ccErr == nil && cc != nil {
			concurrencyCounts = cc
		}
	}

	// Query cost, session, and RPM data for Anthropic OAuth/setup-token providers with those features enabled.
	windowCostProviderIDs := make([]int64, 0)
	sessionLimitProviderIDs := make([]int64, 0)
	rpmProviderIDs := make([]int64, 0)
	sessionIdleTimeouts := make(map[int64]time.Duration) // Per-provider session idle timeouts.
	for i := range providers {
		acc := &providers[i]
		if acc.IsAnthropicOAuthOrSetupToken() {
			if runtimeConfig(acc).GetWindowCostLimit() > 0 {
				windowCostProviderIDs = append(windowCostProviderIDs, acc.ID)
			}
			if runtimeConfig(acc).GetMaxSessions() > 0 {
				sessionLimitProviderIDs = append(sessionLimitProviderIDs, acc.ID)
				sessionIdleTimeouts[acc.ID] = time.Duration(runtimeConfig(acc).GetSessionIdleTimeoutMinutes()) * time.Minute
			}
			if runtimeConfig(acc).GetBaseRPM() > 0 {
				rpmProviderIDs = append(rpmProviderIDs, acc.ID)
			}
		}
	}

	// Batch-read RPM counts for providers with a configured base RPM.
	if len(rpmProviderIDs) > 0 && s.runtime.options.RPMBatch != nil {
		rpmCounts, _ = s.runtime.options.RPMBatch(ctx, rpmProviderIDs)
		if rpmCounts == nil {
			rpmCounts = make(map[int64]int)
		}
	}

	// Batch-read active sessions for providers with session limits.
	if len(sessionLimitProviderIDs) > 0 && s.runtime.options.Sessions != nil {
		activeSessions, _ = s.runtime.options.Sessions(ctx, sessionLimitProviderIDs, sessionIdleTimeouts)
		if activeSessions == nil {
			activeSessions = make(map[int64]int)
		}
	}

	// Load costs independently so one failed query does not hide the rest of the page.
	if len(windowCostProviderIDs) > 0 {
		windowCosts = make(map[int64]float64)
		var mu sync.Mutex
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(10)

		for i := range providers {
			acc := &providers[i]
			if !acc.IsAnthropicOAuthOrSetupToken() || runtimeConfig(acc).GetWindowCostLimit() <= 0 {
				continue
			}
			accCopy := acc
			g.Go(func() error {
				// Resolve the active window start, including expiry handling.
				startTime := runtimeConfig(accCopy).GetCurrentWindowStartTime(s.runtime.options.Now())
				stats, err := s.runtime.options.WindowCost(gctx, accCopy.ID, startTime)
				if err == nil && stats != nil {
					mu.Lock()
					windowCosts[accCopy.ID] = *stats
					mu.Unlock()
				}
				return nil // Allow partial results when a provider's cost query fails.
			})
		}
		_ = g.Wait()
	}

	result := make([]RuntimeStatus, len(providers))
	for i := range providers {
		acc := &providers[i]
		s.runtime.applyQuotaAutoPauseState(ctx, acc)
		item := RuntimeStatus{
			Record:             acc,
			CurrentConcurrency: concurrencyCounts[acc.ID],
			SchedulerScore:     schedulerScores[acc.ID],
			SchedulerScores:    schedulerGroupScores[acc.ID],
		}

		if windowCosts != nil {
			if cost, ok := windowCosts[acc.ID]; ok {
				item.CurrentWindowCost = &cost
			}
		}

		if activeSessions != nil {
			if count, ok := activeSessions[acc.ID]; ok {
				item.ActiveSessions = &count
			}
		}

		if rpmCounts != nil {
			if rpm, ok := rpmCounts[acc.ID]; ok {
				item.CurrentRPM = &rpm
			}
		}

		result[i] = item
	}

	return &ManagementListResult{Items: result, Total: total}, nil
}
