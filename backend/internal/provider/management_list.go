package provider

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// Provider administration first selects a sorted page, then adds observations
// for those provider records. Ollama resolution can fail the page read, while
// optional scheduling scores and runtime counters are assembled through their
// own readers. Missing counter results leave their corresponding presentation
// values at zero or absent.
//
// Anthropic OAuth and Setup Token records request window spending, session
// counts, and RPM only when each feature is enabled. Window-cost queries run
// concurrently with a limit of ten and publish successful values under a local
// mutex. Individual observation failures retain the rest of the page, after
// which quota pause rules and the collected values are attached to each
// returned record.

// ManagementListReader supplies sorted provider pages before runtime
// observations are attached.
type ManagementListReader interface {
	// ListProviders returns a filtered, sorted provider page and matching total.
	// ManagementList enriches these records and their Extra maps, so readers
	// supply values safe for this call's mutable presentation work.
	ListProviders(context.Context, int, int, string, string, string, string, int64, string, string, string) ([]Record, int64, error)
}

// ManagementListInput selects a provider page and optional scheduler-score
// observations.
type ManagementListInput struct {
	Page, // requested provider page index
	PageSize int // requested provider page size
	Platform, // upstream platform selection filter
	ProviderType, // provider authentication selection filter
	Status, // persisted provider status filter
	Search string // administrator provider text search
	GroupID      int64 // routing group membership filter
	PrivacyMode, // stored privacy mode filter
	SortBy, // provider list sort field
	SortOrder string // provider list sort direction
	// IncludeSchedulerScore requests filter-pool and group-pool score diagnostics
	// for a nonempty page. The ManagementList requires its score reader for that
	// request.
	IncludeSchedulerScore bool
}

// ManagementListResult returns the selected provider page enriched with runtime
// status.
type ManagementListResult struct {
	// Items contains runtime statuses pointing into this call's selected provider
	// slice. Observations and quota pause presentation are attached after storage
	// paging completes.
	Items []RuntimeStatus
	Total int64 // matching provider record count
}

// ManagementList combines administrator paging with runtime observations and
// optional usage resolution.
type ManagementList struct {
	admin   ManagementListReader // sorted administrative page reader
	runtime *RuntimeStatusReader // required runtime status reader
	// scores supplies optional scheduling diagnostics. List uses it when
	// IncludeSchedulerScore is true and the selected provider page contains
	// records.
	scores *SchedulerScoreView
	// ollama resolves managed usage for the selected page when supplied. Nil skips
	// resolution; a resolution error fails the listing request.
	ollama *OllamaCloudUsageService
}

// NewManagementList binds paging, runtime readers, scheduling diagnostics, and
// optional Ollama resolution. The runtime reader is required; callers supply
// scores when requesting diagnostics and configure WindowCost for providers
// with spending limits.
func NewManagementList(admin ManagementListReader, runtime *RuntimeStatusReader, scores *SchedulerScoreView, ollama *OllamaCloudUsageService) *ManagementList {
	return &ManagementList{admin, runtime, scores, ollama}
}

// runtimeConfig wraps a record's settings for runtime-limit reads. Extra and
// session-window pointers remain borrowed from the record, so this temporary
// wrapper reads the same configuration selected for presentation.
func runtimeConfig(v *Record) *RuntimeConfig {
	return &RuntimeConfig{Extra: v.Extra, Concurrency: v.Concurrency, SessionWindowStart: v.SessionWindowStart, SessionWindowEnd: v.SessionWindowEnd}
}

// List reads the requested sorted provider page and enriches its records with
// runtime observations. Provider and Ollama-resolution errors fail the request.
// Scheduler scores are queried when requested and the page is nonempty; other
// counters use configured readers where available.
//
// Window spending is queried with up to ten concurrent calls, retaining
// successful observations when another call fails. Counter and spending
// failures produce partial presentation values rather than removing records.
// Returned RuntimeStatus values point into this call's provider slice, and
// optional pointers distinguish supplied measurements from absent observations.
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

	var schedulerScores map[int64]*ProviderSchedulerScore
	var schedulerGroupScores map[int64][]ProviderSchedulerGroupScore
	if includeSchedulerScore && len(providers) > 0 {
		schedulerScores, schedulerGroupScores = s.scores.Build(ctx, providers, platform, providerType, status, search, groupID, privacyMode)
	}

	// Runtime counters supplement the selected page. Keep the initial zero map
	// when concurrency lookup fails so provider records remain available for
	// administration.
	if s.runtime.options.Concurrency != nil {
		if cc, ccErr := s.runtime.options.Concurrency(ctx, providerIDs); ccErr == nil && cc != nil {
			concurrencyCounts = cc
		}
	}

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
				// Session counting receives durations converted from the
				// configured minute unit.
				sessionIdleTimeouts[acc.ID] = time.Duration(runtimeConfig(acc).GetSessionIdleTimeoutMinutes()) * time.Minute
			}
			if runtimeConfig(acc).GetBaseRPM() > 0 {
				rpmProviderIDs = append(rpmProviderIDs, acc.ID)
			}
		}
	}

	// Retain any RPM values returned even with an error. Missing maps become
	// empty observations so a failed optional lookup does not remove the
	// provider page.
	if len(rpmProviderIDs) > 0 && s.runtime.options.RPMBatch != nil {
		rpmCounts, _ = s.runtime.options.RPMBatch(ctx, rpmProviderIDs)
		if rpmCounts == nil {
			rpmCounts = make(map[int64]int)
		}
	}

	// Retain any session counts returned even with an error. Missing maps
	// become empty observations so optional counter failures do not prevent
	// provider administration.
	if len(sessionLimitProviderIDs) > 0 && s.runtime.options.Sessions != nil {
		activeSessions, _ = s.runtime.options.Sessions(ctx, sessionLimitProviderIDs, sessionIdleTimeouts)
		if activeSessions == nil {
			activeSessions = make(map[int64]int)
		}
	}

	// Window spending queries can fail independently. Retain successful
	// observations under a result mutex and return nil from workers so the
	// selected page still completes.
	if len(windowCostProviderIDs) > 0 {
		windowCosts = make(map[int64]float64)
		var mu sync.Mutex
		g, gctx := errgroup.WithContext(ctx)
		// Bound concurrent spending reads for one selected administration page.
		g.SetLimit(10)

		for i := range providers {
			acc := &providers[i]
			if !acc.IsAnthropicOAuthOrSetupToken() || runtimeConfig(acc).GetWindowCostLimit() <= 0 {
				continue
			}
			accCopy := acc
			g.Go(func() error {
				startTime := runtimeConfig(accCopy).GetCurrentWindowStartTime(s.runtime.options.Now())
				stats, err := s.runtime.options.WindowCost(gctx, accCopy.ID, startTime)
				if err == nil && stats != nil {
					mu.Lock()
					windowCosts[accCopy.ID] = *stats
					mu.Unlock()
				}
				return nil
			})
		}
		// Each worker records available spending and returns nil. Wait for
		// their mutations to finish before constructing statuses that read the
		// shared observation map.
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
