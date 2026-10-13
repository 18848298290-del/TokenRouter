package provider

import (
	"context"
	"fmt"
	"time"
)

// OpenAI usage display begins with stored Codex windows and can augment them
// with a regular or shadow quota probe. Missing windows, active rate limiting,
// or eligible stale observations request probing, while separate row
// timestamps throttle sequential attempts before any upstream result is known.
//
// Probe failure leaves stored display available, and a forced attempt still
// records admission time. Check and store are separate cache operations, so
// they do not guarantee a single concurrent request. Successful observations
// merge into the caller-owned record and schedule a detached conditional write
// whose version and supported mutable updates are copied before dispatch.
// Service shutdown tracks that write even after the originating request
// finishes; five-second cancellation bounds cooperative persistence, while an
// uncooperative writer can still make shutdown report unfinished work.

// OpenAIUsageOptions supplies regular and shadow quota observations for
// display refresh.
type OpenAIUsageOptions struct {
	// Probe optionally fetches ordinary quota fields for a stable borrowed record.
	// Error is returned by direct probing but suppressed by GetOpenAIUsage while
	// its stored display remains available.
	Probe func(context.Context, *Record) (map[string]any, error)
	// Shadow optionally fetches row-owned fields through parent credentials.
	// GetOpenAIUsage suppresses its query errors and only merges nonempty accepted
	// updates after checking the tracked context.
	Shadow func(context.Context, int64, time.Time) (map[string]any, error)
}

// GetOpenAIUsage builds stored Codex display and optional local statistics
// under caller/shutdown registration. Nil provider returns only the dated
// view; other records and mutable Extra stay caller-owned and stable through
// callbacks.
//
// Probe admission reserves the row timestamp before optional regular/shadow
// work, including force. Probe errors normally retain stored windows rather
// than become method errors; successful shadow updates check cancellation
// before merging, while ordinary probing checks cancellation in its nested
// operation and that error is suppressed here. Successful updates mutate local
// Extra and can schedule conditional persistence. Local statistics
// independently attach short/weekly totals, creating zero-utilization windows
// when observations are absent. Callers synchronize the record and provide
// reached callbacks without inferring probe success from nil method error.
// Pass a nonnil context when registered work or shutdown waiting is reached.
func (s *OAuthUsageService) GetOpenAIUsage(ctx context.Context, provider *Record, force bool) (*UsageInfo, error) {
	// Register before display/probe work so shutdown can cancel this admitted
	// query and wait through its synchronous callbacks before deferred completion
	// releases the tracked lifetime.
	ctx, finish, err := s.activity.begin(ctx, ErrOAuthUsageStopped)
	if err != nil {
		return nil, err
	}
	defer finish()
	now := s.options.Now()
	usage := &UsageInfo{UpdatedAt: &now}

	if provider == nil {
		return usage, nil
	}

	ApplyExtraToUsage(usage, provider.Extra, now, s.options.Now)

	// Reserve admission before querying, retaining failed-attempt age to limit
	// sequential upstream retries. Successful updates merge locally, while probe
	// failure leaves the stored observation available to display.
	if (force || ShouldRefreshOpenAICodexSnapshot(provider, usage, now)) && s.ShouldProbeOpenAICodexSnapshot(provider.ID, now, force) {
		if provider.IsShadow() {
			// Keep shadow updates on this row even when the
			// callback obtains quota using parent credentials.
			// Optional callback failure leaves stored windows
			// available without publishing a new snapshot from this
			// branch.
			if s.options.OpenAI.Shadow != nil {
				if updates, err := s.options.OpenAI.Shadow(ctx, provider.ID, now); err == nil && len(updates) > 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					provider.Extra = MergeUsageExtra(provider.Extra, updates)
					s.PersistOpenAICodexProbeSnapshot(provider, updates)
					if usage.UpdatedAt == nil {
						usage.UpdatedAt = &now
					}
					ApplyExtraToUsage(usage, provider.Extra, now, s.options.Now)
				}
			}
		} else {
			// Accept only successful ordinary updates, suppressing probe/cancellation
			// errors here so stored windows can still carry local totals. The nested
			// probe owns its conditional detached persistence before this local merge.
			if updates, err := s.ProbeOpenAICodexSnapshot(ctx, provider); err == nil && len(updates) > 0 {
				provider.Extra = MergeUsageExtra(provider.Extra, updates)
				if usage.UpdatedAt == nil {
					usage.UpdatedAt = &now
				}
				ApplyExtraToUsage(usage, provider.Extra, now, s.options.Now)
			}
		}
	}

	if s.stats == nil || s.stats.usageLogRepo == nil {
		return usage, nil
	}

	// Query totals with reset-aligned or rolling starts independently of probe
	// success. Returned partial totals can populate either absent window, keeping
	// local accounting visible when upstream quota remains unknown.
	fiveHour, sevenDay := s.stats.GetWindowPair(ctx, provider.ID,
		CodexWindowStatsStart(usage.FiveHour, 5*time.Hour, now),
		CodexWindowStatsStart(usage.SevenDay, 7*24*time.Hour, now))
	if fiveHour != nil {
		if usage.FiveHour == nil {
			usage.FiveHour = &UsageProgress{Utilization: 0}
		}
		usage.FiveHour.WindowStats = fiveHour
	}

	if sevenDay != nil {
		if usage.SevenDay == nil {
			usage.SevenDay = &UsageProgress{Utilization: 0}
		}
		usage.SevenDay.WindowStats = sevenDay
	}

	return usage, nil
}

// ShouldRefreshOpenAICodexSnapshot reports whether stored usage requests
// another observation. Nil provider suppresses refresh, while nil usage,
// missing short/weekly window, or active rate limiting requests it before
// platform/capability freshness checks.
//
// Only a complete non-rate-limited view delegates to timestamp staleness:
// ordinary OpenAI OAuth needs Responses WS capability, while shadow rows
// bypass that capability gate. A missing or invalid eligible timestamp is
// stale, and age at the ten-minute boundary requests refresh. This selection
// requests work only; ShouldProbeOpenAICodexSnapshot separately controls
// admission and force can bypass that timing gate.
func ShouldRefreshOpenAICodexSnapshot(provider *Record, usage *UsageInfo, now time.Time) bool {
	if provider == nil {
		return false
	}
	if usage == nil {
		return true
	}
	if usage.FiveHour == nil || usage.SevenDay == nil {
		return true
	}
	if provider.IsRateLimited() {
		return true
	}
	return isOpenAICodexSnapshotStale(provider, now)
}

// isOpenAICodexSnapshotStale reports freshness for stable OpenAI OAuth
// records. Ordinary rows without Responses WS return false, while shadow rows
// use the same stored observation timestamp without that gate.
//
// Missing Extra, absent update time, or timestamp parse failure is stale for
// an eligible record. A valid time becomes stale at the ten-minute age
// boundary; future times remain fresh until that interval passes. This
// function does not reserve probe admission or assess missing display windows,
// leaving those independent conditions to its callers.
func isOpenAICodexSnapshotStale(provider *Record, now time.Time) bool {
	if provider == nil || !provider.IsOpenAIOAuth() {
		return false
	}
	// Require Responses WS capability for ordinary timestamp-only refresh, while
	// shadow quota comes from account usage and can expire without that transport.
	// Independent missing-window/rate-limit conditions are handled by the caller.
	if !provider.IsShadow() && !provider.SupportsResponsesWS() {
		return false
	}
	if provider.Extra == nil {
		return true
	}
	raw, ok := provider.Extra["codex_usage_updated_at"]
	if !ok {
		return true
	}
	// Treat unusable timestamp text as stale because it cannot establish an
	// observation age. This permits another eligible attempt rather than reusing
	// an unknown freshness decision indefinitely.
	ts, err := ParseUsageTime(fmt.Sprint(raw))
	if err != nil {
		return true
	}
	return now.Sub(ts) >= OAuthUsageOpenAIProbeCacheTTL
}

// ShouldProbeOpenAICodexSnapshot admits an attempt and stores the supplied
// time before fetching. Nil service/cache or nonpositive ID returns true
// without storage; only the first optional force flag is used.
//
// Ordinary attempts reject a typed cached time younger than ten minutes. Force
// bypasses that check but still writes admission time, and failed or absent
// upstream work retains the reservation. Cache load and store are separate
// operations, so concurrent callers can both admit; this throttle does not
// claim atomic execution ownership. Callers needing one simultaneous attempt
// supply their own coordination.
func (s *OAuthUsageService) ShouldProbeOpenAICodexSnapshot(providerID int64, now time.Time, force ...bool) bool {
	if s == nil || s.cache == nil || providerID <= 0 {
		return true
	}
	forceProbe := len(force) > 0 && force[0]
	if !forceProbe {
		if cached, ok := s.cache.LoadOpenAIProbe(providerID); ok {
			if ts, ok := cached.(time.Time); ok && now.Sub(ts) < OAuthUsageOpenAIProbeCacheTTL {
				return false
			}
		}
	}
	// Store admission rather than completion time so failed attempts retain their
	// retry age. This separate load/store sequence can admit simultaneous callers
	// and does not supply an atomic request claim.
	s.cache.StoreOpenAIProbe(providerID, now)
	return true
}

// ProbeOpenAICodexSnapshot registers an ordinary probe under caller/shutdown
// cancellation. Missing Probe returns nil data; query or post-query
// cancellation propagates and does not schedule persistence.
//
// A nonempty accepted map schedules conditional detached writeback before a
// supported-container copy is returned. Persistence outcome is independent and
// cannot change this returned probe result. CloneValues preserves opaque
// unsupported values by reference, so callers keep those values and record
// identity stable through callback/copying; no provider Extra mutation occurs
// inside this method itself. Nested registration is distinct from an outer
// GetOpenAIUsage admission.
// Pass a nonnil context when registered work or shutdown waiting is reached.
func (s *OAuthUsageService) ProbeOpenAICodexSnapshot(ctx context.Context, value *Record) (map[string]any, error) {
	// Track direct probing separately from any outer usage request, so shutdown
	// can reject a newly reached nested operation and owns cancellation until
	// this synchronous callback and scheduling step finish.
	ctx, finish, err := s.activity.begin(ctx, ErrOAuthUsageStopped)
	if err != nil {
		return nil, err
	}
	defer finish()
	if s.options.OpenAI.Probe == nil {
		return nil, nil
	}
	updates, err := s.options.OpenAI.Probe(ctx, value)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(updates) > 0 {
		s.PersistOpenAICodexProbeSnapshot(value, updates)
	}
	return CloneValues(updates), nil
}

// PersistOpenAICodexProbeSnapshot schedules best-effort conditional
// persistence for a stable observed record. Missing service/repository,
// invalid record ID, or empty updates skips work; missing UsageExtraWriter
// emits Warn and skips dispatch.
//
// ObserveUsageVersion and CloneValues capture identity and supported mutable
// updates before a detached five-second registration starts the goroutine. The
// supplied record must remain stable through that capture, and unsupported
// opaque values can remain borrowed. Shutdown registration precedes dispatch
// so stop can cancel and wait for the writer; canceled work skips the write,
// while writer false/error results are discarded. Cancellation is cooperative,
// so a blocked writer can outlive the budget and produce retained unfinished
// shutdown status.
func (s *OAuthUsageService) PersistOpenAICodexProbeSnapshot(value *Record, updates map[string]any) {
	if s == nil || s.providerRepo == nil || value == nil || value.ID <= 0 || len(updates) == 0 {
		return
	}
	// Capture the observed identity and supported update containers before the
	// asynchronous boundary, preventing later supported map edits from changing
	// the conditional request sent by this detached observer.
	version := ObserveUsageVersion(value)
	updates = CloneValues(updates)
	writer, ok := s.providerRepo.(UsageExtraWriter)
	if !ok {
		s.options.Warn("openai usage conditional writer is missing")
		return
	}
	// Register the bounded write before goroutine dispatch so shutdown owns its
	// lifetime. Registration failure suppresses this best-effort persistence while
	// the accepted probe result remains independently usable.
	ctx, finish, err := s.BeginDetached(context.Background(), 5*time.Second)
	if err != nil {
		return
	}
	// Skip canceled dispatch and discard conditional miss/storage error, because
	// this observer does not revoke its earlier live quota result. Completion is
	// deferred until the actual writer returns, including an uncooperative wait.
	go func() {
		defer finish()
		if ctx.Err() == nil {
			_, _ = writer.UpdateUsageExtraIfUnchanged(ctx, version, updates)
		}
	}()
}
