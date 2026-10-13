package provider

import (
	"context"
	"time"
)

// Administrative provider status combines a copied record with optional live
// observations. OpenAI quota display is recalculated from dynamic defaults,
// while Anthropic OAuth and Setup Token records read spending, retained
// sessions, and RPM only when their corresponding provider limits are active.

// RuntimeStatusOptions supplies optional counter, spending, clock, and quota
// readers for provider administration.
type RuntimeStatusOptions struct {
	// RPMBatch reads provider request counts for ManagementList pages. Read
	// uses the individual RPM callback, so configuring this callback alone
	// leaves its single-record RPM field absent.
	RPMBatch func(context.Context, []int64) (map[int64]int, error)
	Now      func() time.Time // display evaluation clock callback
	// QuotaSettings is read for each OpenAI display calculation when configured.
	// Nil supplies zero defaults while provider-level ratios still participate.
	QuotaSettings func(context.Context) QuotaAutoPauseSettings // OpenAI quota default reader
	// Concurrency: batch occupied-slot count reader
	Concurrency func(context.Context, []int64) (map[int64]int, error)
	// WindowCost reads local spending for a provider and selected window start.
	// Read retains its successful returned pointer directly, so callers respect
	// that callback's sharing rules. Read skips nil callbacks; ManagementList
	// requires this reader when an Anthropic OAuth or Setup Token record has a
	// positive spending limit.
	WindowCost func(context.Context, int64, time.Time) (*float64, error)
	// Sessions: batch retained-session count reader
	Sessions func(context.Context, []int64, map[int64]time.Duration) (map[int64]int, error)
	RPM      func(context.Context, int64) (int, error) // individual RPM count reader
}

// RuntimeStatus combines provider data, optional observed counters, and
// administrative scheduling score views.
type RuntimeStatus struct {
	// SchedulerScore and SchedulerScores describe base and advanced-group views
	// attached by ManagementList. Read leaves both absent, while page scoring
	// associates them with matching provider IDs.
	SchedulerScore  *ProviderSchedulerScore       // filtered-pool base score
	SchedulerScores []ProviderSchedulerGroupScore // advanced group score views
	// Record comes from CloneRecord in Read and the owned page records in
	// ManagementList. Its nested values follow those producers' supported-copy
	// rules, including retained unsupported dynamic values.
	Record             *Record // provider administration record pointer
	CurrentConcurrency int     // observed occupied provider slots
	// CurrentWindowCost is nil when spending is unavailable or its check is
	// inactive. Read retains successful callback pointers, while ManagementList
	// allocates values from its page observations.
	CurrentWindowCost *float64 // USD
	// ActiveSessions and CurrentRPM distinguish an unavailable observation from
	// an observed zero with optional pointers. Read allocates each scalar after
	// a successful corresponding lookup.
	ActiveSessions, CurrentRPM *int // observed session count and observed request count
}

// RuntimeStatusReader retains optional live readers used to assemble one
// provider's display state.
type RuntimeStatusReader struct {
	options RuntimeStatusOptions // retained administration reader callbacks
}

// NewRuntimeStatusReader retains administration callbacks and supplies
// time.Now when the clock is absent. Other nil callbacks skip their
// observations; callers keep captured dependencies usable for later
// synchronous reads.
func NewRuntimeStatusReader(options RuntimeStatusOptions) *RuntimeStatusReader {
	if options.Now == nil {
		options.Now = time.Now
	}
	return &RuntimeStatusReader{options: options}
}

// Read clones a stable provider record and builds its optional administration
// observations synchronously. Nil input returns an empty status; OpenAI quota
// display is recalculated on the clone, and Anthropic reads require positive
// matching limits.
//
// Counter and spending errors leave optional pointers absent; concurrency
// stays zero on failure or a missing ID. Spending pointers are passed through
// from the callback, while session and RPM values receive new scalar
// pointers. Record copying follows CloneRecord's supported-value sharing
// rules, and this entry point leaves scheduler score fields empty.
func (s *RuntimeStatusReader) Read(ctx context.Context, value *Record) RuntimeStatus {
	v := CloneRecord(value)
	out := RuntimeStatus{Record: v}
	if v == nil {
		return out
	}
	// Recalculate quota display on the copied record so dynamic defaults can
	// affect this response while the caller's original provider stays available.
	s.applyQuotaAutoPauseState(ctx, v)
	// Optional observation errors leave display counters absent or zero;
	// administration still returns the copied provider when live storage fails.
	if s.options.Concurrency != nil {
		if counts, err := s.options.Concurrency(ctx, []int64{v.ID}); err == nil {
			out.CurrentConcurrency = counts[v.ID]
		}
	}
	if v.IsAnthropicOAuthOrSetupToken() {
		cfg := RuntimeConfig{Extra: v.Extra, Concurrency: v.Concurrency, SessionWindowStart: v.SessionWindowStart, SessionWindowEnd: v.SessionWindowEnd}
		if s.options.WindowCost != nil && cfg.GetWindowCostLimit() > 0 {
			// A failed spending read leaves the cost pointer absent so the
			// administration response can still return its provider data and
			// distinguish unavailable spending from an observed zero.
			if cost, err := s.options.WindowCost(ctx, v.ID, cfg.GetCurrentWindowStartTime(s.options.Now())); err == nil {
				out.CurrentWindowCost = cost
			}
		}
		if s.options.Sessions != nil && cfg.GetMaxSessions() > 0 {
			// Session counting receives the provider's idle interval as a duration.
			idle := map[int64]time.Duration{v.ID: time.Duration(cfg.GetSessionIdleTimeoutMinutes()) * time.Minute}
			// Missing IDs and lookup failures leave session counts unobserved; a
			// successful zero remains distinct through its allocated pointer.
			if counts, err := s.options.Sessions(ctx, []int64{v.ID}, idle); err == nil {
				if n, ok := counts[v.ID]; ok {
					out.ActiveSessions = &n
				}
			}
		}
		if s.options.RPM != nil && cfg.GetBaseRPM() > 0 {
			// Discard any count accompanying a lookup error so failed RPM storage does
			// not appear as a completed observation in this display.
			if n, err := s.options.RPM(ctx, v.ID); err == nil {
				out.CurrentRPM = &n
			}
		}
	}
	return out
}

// applyQuotaAutoPauseState recalculates QuotaAutoPaused for an OpenAI record
// using this read's clock and defaults. An absent settings callback supplies
// zero defaults; callers pass a nonnil mutable display record.
func (s *RuntimeStatusReader) applyQuotaAutoPauseState(ctx context.Context, v *Record) {
	if v.Platform == PlatformOpenAI {
		var settings QuotaAutoPauseSettings
		if s.options.QuotaSettings != nil {
			settings = s.options.QuotaSettings(ctx)
		}
		v.QuotaAutoPaused, _ = EvaluateQuotaAutoPause(v.Platform, v.Extra, settings, s.options.Now())
	}
}
