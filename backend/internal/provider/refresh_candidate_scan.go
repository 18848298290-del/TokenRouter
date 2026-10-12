package provider

import (
	"context"
	"sync"
	"time"
)

// A periodic refresh scan resumes by provider ID when its budget expires during
// a page. Storage supplies cursor metadata independently of loaded records,
// allowing the scan to advance past IDs whose details disappeared between
// selection and loading. Returned records still need strictly increasing IDs
// above the preceding cursor.
//
// The scan advances only after processing returns with a live context, so an
// interrupted page is visited again on the next cycle. Coordinated exchanges
// reread eligibility and expiry before another exchange. Reaching the final page
// resets the cursor to begin the next complete pass, while repository errors and
// invalid metadata leave the last completed position available. The mutex
// protects cursor access; callers coordinate overlapping Run invocations.

// OAuthRefreshCandidatePager supplies ID-ordered candidate records with
// independently selected page metadata.
type OAuthRefreshCandidatePager interface {
	// ListOAuthRefreshCandidatePage loads eligible candidates after the supplied
	// ID cursor within its page limit. NextAfterID reflects selected IDs even if
	// details disappear, and HasMore tells callers whether another selected page
	// may remain.
	ListOAuthRefreshCandidatePage(context.Context, OAuthRefreshPageOptions) (*OAuthRefreshCandidatePage, error)
}

// RefreshScanOptions supplies the budget, page size, platforms, and log
// callbacks for one scan.
type RefreshScanOptions struct {
	Timeout  time.Duration // complete scan cancellation budget
	PageSize int           // repository candidate page limit
	// Platforms is borrowed through repository paging; callers keep its entries
	// stable for the scan so every page uses the same registered candidates.
	Platforms                []string
	Debug, Info, Warn, Error func(string, ...any) // optional scan outcome loggers
}

// RefreshCandidateScan retains the last completed cursor position between
// periodic scan cycles.
type RefreshCandidateScan struct {
	// mu protects afterID for observation and publication. Zero starts a complete
	// pass; the mutex does not serialize entire Run calls, which callers
	// coordinate.
	mu      sync.Mutex // resume cursor synchronization
	afterID int64      // last completed selected ID
}

// RefreshPageStats records eligibility counts and worker outcomes for one
// processed candidate page.
type RefreshPageStats struct {
	Total        int // loaded candidate record count
	OAuth        int // records accepted by CanRefresh
	NeedsRefresh int // eligible records requiring exchange
	Refreshed    int // worker results classified successful
	Skipped      int // worker results classified skipped
	Failed       int // worker results classified failed
}

// Position reads the current resume cursor under its mutex.
func (s *RefreshCandidateScan) Position() int64 { s.mu.Lock(); defer s.mu.Unlock(); return s.afterID }

// SetPosition publishes the supplied resume cursor under its mutex.
func (s *RefreshCandidateScan) SetPosition(id int64) { s.mu.Lock(); s.afterID = id; s.mu.Unlock() }

// Run processes ID-ordered pages until the scan budget, repository failure or
// final page ends the cycle. Nil parent selects context.Background, loggers
// default to no-ops, and callers supply a usable page size, positive timeout and
// callable process callback.
//
// Page metadata must advance beyond the prior cursor, and every loaded record ID
// must increase above it. The cursor advances after process returns with an
// uncanceled context; interruption retains that page for another cycle. Empty
// terminal metadata resets the pass, as does a processed final page. Errors are
// logged with partial statistics, and overlapping Run calls require caller
// coordination because cursor locking covers individual reads and writes.
func (s *RefreshCandidateScan) Run(parent context.Context, pager OAuthRefreshCandidatePager, options RefreshScanOptions, process func(context.Context, []Record) RefreshPageStats) {
	noop := func(string, ...any) {}
	if options.Debug == nil {
		options.Debug = noop
	}
	if options.Info == nil {
		options.Info = noop
	}
	if options.Warn == nil {
		options.Warn = noop
	}
	if options.Error == nil {
		options.Error = noop
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, options.Timeout)
	defer cancel()

	if pager == nil {
		options.Error("token_refresh.candidate_pager_missing")
		return
	}
	platforms := options.Platforms
	if len(platforms) == 0 {
		options.Error("token_refresh.provider_registry_empty")
		return
	}

	pageSize := options.PageSize
	stats := RefreshPageStats{}
	afterID := s.Position()
	for {
		if ctx.Err() != nil {
			options.Warn("token_refresh.cycle_stopped", "error", ctx.Err(), "resume_after_id", afterID)
			break
		}
		page, err := pager.ListOAuthRefreshCandidatePage(ctx, OAuthRefreshPageOptions{
			Platforms:            platforms,
			AfterID:              afterID,
			Limit:                pageSize,
			ActiveOnly:           true,
			IncludeSetupToken:    true,
			RequireRefreshToken:  true,
			ExcludeRetryCooldown: true,
		})
		if err != nil {
			options.Error("token_refresh.list_providers_failed", "error", err, "after_id", afterID)
			break
		}
		if page == nil {
			options.Error("token_refresh.nil_candidate_page", "after_id", afterID)
			break
		}
		providers := page.Providers
		// A terminal empty SQL page means the pass reached its end. Reset before
		// the next cycle so providers with smaller IDs are eligible for another
		// scan.
		if !page.HasMore && page.NextAfterID == 0 && len(providers) == 0 {
			s.SetPosition(0)
			break
		}
		if page.NextAfterID <= afterID {
			options.Error("token_refresh.invalid_candidate_page_metadata", "after_id", afterID)
			break
		}
		if !IsStrictlyIncreasingProviderPage(providers, afterID) {
			options.Error("token_refresh.invalid_candidate_page", "after_id", afterID, "count", len(providers))
			break
		}

		pageStats := process(ctx, providers)
		stats.Total += pageStats.Total
		stats.OAuth += pageStats.OAuth
		stats.NeedsRefresh += pageStats.NeedsRefresh
		stats.Refreshed += pageStats.Refreshed
		stats.Skipped += pageStats.Skipped
		stats.Failed += pageStats.Failed

		// Leave an interrupted page at its prior cursor so unprocessed candidates
		// remain reachable next cycle; OAuthRefreshAPI rereads expiry and eligibility
		// before exchanging any credentials from the repeated page.
		if ctx.Err() != nil {
			break
		}
		afterID = page.NextAfterID
		s.SetPosition(afterID)
		// The final processed page completes this pass; reset its cursor so the
		// next cycle revisits earlier providers whose credentials may need
		// refresh.
		if !page.HasMore {
			s.SetPosition(0)
			break
		}
	}

	if stats.NeedsRefresh == 0 && stats.Failed == 0 {
		options.Debug("token_refresh.cycle_completed",
			"total", stats.Total, "oauth", stats.OAuth,
			"needs_refresh", stats.NeedsRefresh, "refreshed", stats.Refreshed,
			"skipped", stats.Skipped, "failed", stats.Failed)
	} else {
		options.Info("token_refresh.cycle_completed",
			"total", stats.Total, "oauth", stats.OAuth,
			"needs_refresh", stats.NeedsRefresh, "refreshed", stats.Refreshed,
			"skipped", stats.Skipped, "failed", stats.Failed)
	}
}

// IsStrictlyIncreasingProviderPage reports whether all record IDs increase above
// afterID. An empty page returns true, leaving metadata validation to the caller
// because selected rows can disappear before detail loading.
func IsStrictlyIncreasingProviderPage(providers []Record, afterID int64) bool {
	previous := afterID
	for i := range providers {
		if providers[i].ID <= previous {
			return false
		}
		previous = providers[i].ID
	}
	return true
}
