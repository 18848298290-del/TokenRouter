package provider

import (
	"context"
	"net/http"
	"time"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Codex quota headers become Extra snapshots for display and quota evaluation.
// Observing a snapshot does not itself install a provider rate limit; the
// caller decides whether the record, including a quota shadow, should
// contribute an observation.
//
// The observer builds a value patch before reserving the shared write throttle,
// then dispatches persistence with an independent five-second budget. Throttle
// reservations survive failed writes and rejected background admission, so
// repeated observations do not force another attempt inside the interval. A
// configured runner owns background tracking; its absence starts a goroutine
// directly. Store errors are discarded because the observation is best effort
// and the request has already received its upstream result.

// CodexUsageObserver schedules throttled Extra writes for caller-approved Codex
// quota observations.
type CodexUsageObserver struct {
	// Store writes captured Extra patches. An absent store skips observation, and
	// a failed asynchronous write is ignored by Observe.
	Store interface {
		// UpdateExtra writes the supplied provider Extra patch under the independent
		// update context. Observe treats returned errors as best effort and does not
		// return them to the originating request.
		UpdateExtra(context.Context, int64, map[string]any) error
	}
	// Throttle reserves write attempts by provider ID before dispatch. A nil
	// throttle allows every observation through this gate.
	Throttle *provider.WriteThrottle
	// Go optionally admits tracked background work. Its false result is ignored;
	// an absent callback starts work in a goroutine directly.
	Go func(string, func()) bool
}

// Observe builds an Extra patch and schedules a quota-snapshot write for id.
// Nil snapshots, receivers, and stores are skipped; ctx does not govern the
// background write, which receives its own five-second context.
//
// The throttle reserves admission before dispatch. A configured Go callback can
// reject work, and persistence errors are ignored; either outcome leaves that
// throttle interval reserved. The patch copies snapshot measurements before
// dispatch, so the background closure uses those captured values while later
// health decisions remain with provider services.
func (s *CodexUsageObserver) Observe(ctx context.Context, id int64, snapshot *protocolopenai.OpenAICodexUsageSnapshot) {
	if snapshot == nil || s == nil || s.Store == nil {
		return
	}
	now := time.Now()
	updates := provider.BuildCodexUsageExtraUpdates(snapshot, now)
	// Reserve a write attempt after capturing measurements but before launching
	// work. Failed persistence and rejected runner admission keep that reservation
	// until the next interval.
	if len(updates) == 0 || !s.Throttle.Allow(id, now) {
		return
	}
	work := func() {
		// The upstream observation remains eligible for storage after request
		// cancellation. Bound the detached update to five seconds so the background
		// write has a finite budget.
		updateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Snapshot storage is best effort: the response has already supplied its
		// quota observation, and a database error cannot change that response or
		// retry admission.
		_ = s.Store.UpdateExtra(updateCtx, id, updates)
	}
	// A configured runner owns admission and lifecycle tracking. The direct
	// goroutine path supports callers without that runner, retaining the same
	// independent update budget.
	if s.Go != nil {
		s.Go("service/openai_gateway_usage.go:updateCodexUsageSnapshot", work)
	} else {
		go work()
	}
}

// Headers parses Codex quota headers and passes recognized observations to
// Observe. Nonpositive IDs and nil headers are skipped, and headers without a
// parsed snapshot produce no write or scheduling change.
func (s *CodexUsageObserver) Headers(ctx context.Context, id int64, headers http.Header) {
	if id <= 0 || headers == nil {
		return
	}
	if snapshot := openai.ParseCodexRateLimitHeaders(headers); snapshot != nil {
		s.Observe(ctx, id, snapshot)
	}
}
