package ws

import (
	"context"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// request_records.go tracks logical request records across WebSocket turns and
// provider retries.
// One client connection contains many logical requests, and one logical turn
// can span replacement-provider attempts. Reusing the connection request ID for
// every turn would merge those records, while allocating a new ID for each
// retry would split one user's request.
//
// entryRequestRecords allocates a child request before first-turn admission and
// retains its context while retry remains possible. Each provider attempt maps
// local turn numbers to those child contexts; a replacement attempt starts with
// the retained next context, then later turns allocate fresh identities.
// Completion updates the observed model, provider, outcome, and upstream alias
// before removing finished state. Connection cleanup marks any retained request
// incomplete, using canceled state when the session root has ended.

// entryRequestRecords retains logical child request identities across provider
// attempts and connection exits.
type entryRequestRecords struct {
	// mu protects next and pending after construction. newTurn requires this lock
	// once records are shared; it also runs before publication during initial
	// construction.
	mu   sync.Mutex      // logical request state lock
	root context.Context // connection request parent context
	// next retains the failed turn's child context during retry. Terminal
	// completion clears it when the same logical request finishes, allowing a
	// later request to get a fresh ID.
	next    context.Context            // next attempt first-turn context
	pending map[string]context.Context // unfinished logical request contexts
}

// entryAttemptRecords assigns attempt-local turn numbers to retained logical
// request contexts.
type entryAttemptRecords struct {
	// mu protects turns. Creating a later turn acquires session.mu inside this
	// lock; finish releases session.mu before acquiring this mutex for removal.
	mu      sync.Mutex              // attempt turn mapping lock
	session *entryRequestRecords    // shared connection request tracker
	turns   map[int]context.Context // local turn context mapping
}

// newEntryRequestRecords creates a tracker and eagerly allocates its first
// logical request. The nonnil root carries connection identity and capture
// callbacks; close later records an incomplete request when admission exits
// before a turn finishes.
func newEntryRequestRecords(ctx context.Context) *entryRequestRecords {
	records := &entryRequestRecords{root: ctx, pending: make(map[string]context.Context)}
	records.next = records.newTurn(time.Now())
	return records
}

// newTurn creates and registers a child request with its own ID and
// model-attribute state. It records running state and the supplied UTC start
// time before adding the context to pending.
//
// The caller holds the session mutex after the tracker becomes shared; initial
// construction invokes it before publication. Renewed model attributes belong
// to this logical request rather than the connection or an earlier turn.
// Retaining the resulting context across retries keeps its start time,
// identity, and capture state available until terminal completion.
func (r *entryRequestRecords) newTurn(at time.Time) context.Context {
	ctx := telemetry.WithChildRequest(r.root, telemetry.RequestRecord{RequestID: telemetry.NewRequestID(), State: "running", StartedAt: at.UTC()})
	ctx = routing.RenewModelAttributes(ctx)
	ctx = context.WithValue(ctx, telemetry.RequestStartedAt, at)
	mode := timing.ModeFromContext(r.root)
	ctx = timing.WithModeReader(ctx, func(context.Context) string { return mode })
	r.pending[telemetry.RequestIDValue(ctx)] = ctx
	return ctx
}

// attempt creates provider-attempt turn mapping using the session's selected
// logical request. It holds the session mutex while reading or allocating next.
//
// A pending retry already supplies next, so the replacement attempt begins with
// the failed turn's existing request ID and capture state. When no request
// remains selected, a fresh child is allocated before publishing local turn
// one. The returned attempt gets its own turn-number map while sharing the
// session tracker for later allocation and completion.
func (r *entryRequestRecords) attempt() *entryAttemptRecords {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.next == nil {
		r.next = r.newTurn(time.Now())
	}
	return &entryAttemptRecords{session: r, turns: map[int]context.Context{1: r.next}}
}

// context attaches one logical turn's identity and capture state to base for
// callbacks. Values below one select local turn one, and the caller supplies a
// nonnil base context.
//
// Existing local turns reuse their saved child context. A later unseen turn
// allocates a child under the session lock, selects it as next, and publishes
// it in the attempt map while holding the attempt lock. Copying the ID, model
// attributes, and capture onto base keeps execution context values available
// while sharing the logical request's observation state across retry attempts.
func (a *entryAttemptRecords) context(base context.Context, turn int) context.Context {
	if turn < 1 {
		turn = 1
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	ctx := a.turns[turn]
	if ctx == nil {
		a.session.mu.Lock()
		ctx = a.session.newTurn(time.Now())
		a.session.next = ctx
		a.session.mu.Unlock()
		a.turns[turn] = ctx
	}
	base = context.WithValue(base, telemetry.RequestID, telemetry.RequestIDValue(ctx))
	base = routing.CopyModelAttributes(base, ctx)
	if at, ok := ctx.Value(telemetry.RequestStartedAt).(time.Time); ok {
		base = context.WithValue(base, telemetry.RequestStartedAt, at)
	}
	mode := timing.ModeFromContext(ctx)
	base = timing.WithModeReader(base, func(context.Context) string { return mode })
	return telemetry.CopyRequestCapture(base, ctx)
}

// finish records the current attempt and either retains or finishes its logical
// request. Callers provide a positive capture.Turn and nonnil base; retries
// leave request state running and preserve its original start time.
//
// An error, missing result, or unsuccessful declared WS terminal records a
// failed attempt; the attempt list is capped while request timing continues. A
// non-retry completion sets final state and elapsed duration, records the parsed
// response ID in its own alias category, then removes pending and attempt-local
// references. Retry instead selects that context as the next provider attempt's
// first turn, preserving one logical ID across the provider switch.
func (a *entryAttemptRecords) finish(base context.Context, capture TurnCapture, providerID int64, platform string, retry bool) context.Context {
	ctx := a.context(base, capture.Turn)
	var ref telemetry.RequestAttemptRef
	telemetry.UpdateRequest(ctx, func(record *telemetry.RequestRecord) {
		record.Model = capture.OriginalModel
		record.ProviderID = providerID
		record.Platform = platform
		record.State = "running"
		outcome := "completed"
		if capture.Err != nil || capture.Result == nil || !entrySucceeded(capture.Result) {
			outcome = "failed"
		}
		number := 0
		if capture.Result != nil && capture.Result.Timings != nil {
			number = int(capture.Result.Timings.Values["attempt_number"])
		}
		index := -1
		for i := range record.Attempts {
			if (number > 0 && record.Attempts[i].Number == number && record.Attempts[i].ProviderID == providerID) || (number == 0 && record.Attempts[i].ProviderID == providerID && record.Attempts[i].Outcome == "running") {
				index = i
			}
		}
		if index < 0 && len(record.Attempts) < 256 {
			record.Attempts = append(record.Attempts, telemetry.RequestAttempt{Number: len(record.Attempts) + 1, ProviderID: providerID})
			index = len(record.Attempts) - 1
		}
		if index >= 0 {
			attempt := &record.Attempts[index]
			attempt.Outcome = outcome
			if capture.Err != nil {
				attempt.Reason = logredact.SanitizeUpstreamQueries(logredact.RedactText(capture.Err.Error()))
			}
			if (retry || capture.Result == nil) && attempt.StartedAt != nil {
				ms := time.Since(*attempt.StartedAt).Milliseconds()
				attempt.DurationMs = &ms
			}
			ref = telemetry.RequestAttemptRef{Number: attempt.Number, ProviderID: providerID, Header: attempt.RequestIDHeader}
			if capture.Result != nil {
				if capture.Result.Timings != nil {
					capture.Result.Timings.Values["attempt_number"] = int64(attempt.Number)
				}
			}
		}
		if !retry {
			now := time.Now()
			if record.ClientDisconnectedAt != nil && record.ClientDisconnectedAt.Before(now) {
				now = *record.ClientDisconnectedAt
			}
			record.FinishedAt = &now
			origin := record.StartedAt
			if started, ok := ctx.Value(telemetry.RequestStartedAt).(time.Time); ok {
				origin = started
			}
			record.DurationMs = now.Sub(origin).Milliseconds()
			record.State = outcome
			if record.ClientDisconnectedAt != nil {
				record.State = "canceled"
			}
			if outcome == "failed" {
				record.ErrorCode = "websocket_turn_failed"
			}
		}
	})
	if capture.Result != nil {
		responseID := capture.Result.ResponseID
		if responseID == "" && capture.Result.OpenAIWSMode {
			responseID = capture.Result.RequestID
		}
		telemetry.ObserveResponseID(ctx, ref, responseID)
	}
	// Publish retry identity or remove completed identity only after recording the
	// attempt and alias, so the replacement attempt inherits that logical capture.
	a.session.mu.Lock()
	if retry {
		a.session.next = ctx
	} else {
		delete(a.session.pending, telemetry.RequestIDValue(ctx))
		if telemetry.RequestIDValue(a.session.next) == telemetry.RequestIDValue(ctx) {
			a.session.next = nil
		}
	}
	a.session.mu.Unlock()
	if !retry {
		a.mu.Lock()
		delete(a.turns, capture.Turn)
		a.mu.Unlock()
	}
	return ctx
}

// close marks each remaining logical request incomplete when the connection
// exits. The caller invokes it after turn execution stops; repeated calls can
// update the retained entries again because this method does not clear pending.
//
// Each record receives a finish time, elapsed duration, and
// websocket_turn_incomplete code. Root cancellation selects canceled state,
// otherwise the record is failed. Holding the session mutex keeps pending
// membership stable while those captures are updated, including a first request
// that exited before admission or a later turn that never reached AfterTurn.
func (r *entryRequestRecords) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ctx := range r.pending {
		telemetry.UpdateRequest(ctx, func(record *telemetry.RequestRecord) {
			now := time.Now()
			if record.ClientDisconnectedAt != nil && record.ClientDisconnectedAt.Before(now) {
				now = *record.ClientDisconnectedAt
			}
			record.FinishedAt = &now
			origin := record.StartedAt
			if started, ok := ctx.Value(telemetry.RequestStartedAt).(time.Time); ok {
				origin = started
			}
			record.DurationMs = now.Sub(origin).Milliseconds()
			record.State = "failed"
			record.ErrorCode = "websocket_turn_incomplete"
			for i := range record.Attempts {
				if record.Attempts[i].Outcome == "running" {
					record.Attempts[i].Outcome = "failed"
					if record.Attempts[i].StartedAt != nil {
						ms := time.Since(*record.Attempts[i].StartedAt).Milliseconds()
						record.Attempts[i].DurationMs = &ms
					}
				}
			}
			if r.root.Err() != nil || record.ClientDisconnectedAt != nil {
				record.State = "canceled"
			}
		})
	}
}
