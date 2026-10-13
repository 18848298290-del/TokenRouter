package scheduler

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// A ranked candidate still needs a physical provider slot and current
// eligibility before execution. Selection acquires capacity first, then asks
// the adapter for fresh metadata and a database recheck. Snapshot load
// observations can skip already-full candidates, but a successful acquisition
// remains the admission decision for candidates that proceed.
//
// Each acquired slot immediately belongs to an AttemptLease under the request
// lease. Rejected rechecks, exhausted budgets, and Compact failures release
// that attempt before selection continues. A changed concurrency limit returns
// the earlier slot and reacquires with the refreshed limit. The returned
// attempt owns successful capacity for execution, and parent cleanup can
// release it earlier. Binding updates occur after eligibility checks and
// respect the request's preservation flag.

// SelectionCandidate associates request-scoped provider metadata with load
// observations and a resolved route.
type SelectionCandidate struct {
	// Snapshot is borrowed from the current selection scope. Fresh and database
	// callbacks return the metadata associated with their new execution records
	// rather than mutating this candidate.
	Snapshot *provider.ProviderSnapshot // candidate provider eligibility snapshot
	// ProjectionID belongs to the adapter's current invocation. Fresh and
	// database callbacks preserve their own association so a selected provider
	// resolves to the exact record rechecked.
	ProjectionID uint64 // per-invocation execution record association
	// Load and LoadKnown travel together as one observed capacity input. When
	// LoadKnown is true, Select reads Load.CurrentConcurrency and requires a
	// nonnil stable observation; a false flag keeps the pointer unused in that
	// capacity check.
	Load      *ProviderLoadInfo      // observed provider slot counts
	LoadKnown bool                   // confirmed load observation presence
	Plan      *routing.CandidatePlan // resolved candidate routing plan
}

// SelectionInput carries admitted routing requirements and candidate order for
// one selection invocation.
type SelectionInput struct {
	GroupID        *int64            // admitted persistent group identifier
	RequestedModel string            // client requested model name
	RoutePlan      routing.RoutePlan // admitted request routing value
	// Candidates supplies the order already chosen by the owning selector. Select
	// borrows its records and reads load observations while callbacks can return
	// separate refreshed candidates.
	Candidates     []SelectionCandidate // ordered provider metadata candidates
	ExcludedIDs    map[int64]struct{}   // previously rejected provider identifiers
	RequireCompact bool                 // Compact capability requirement switch
	SessionHash    string               // request session binding key
	// PreserveStickyBinding suppresses BindSticky after successful selection.
	// Higher-level sticky escape or parent reuse sets it so a temporary choice
	// leaves that binding available for subsequent requests.
	PreserveStickyBinding bool // existing binding preservation switch
}

// AttemptSelectionPorts supplies synchronous slot and eligibility callbacks
// for a selection invocation. The adapter maintains record associations and
// handles binding failures, while the lease selector owns callback ordering
// and acquired capacity cleanup.
type AttemptSelectionPorts struct {
	// Acquire returns slot admission and whether its budget allowed an attempt. A
	// false attempted result supplies no acquired capacity; it ends initial
	// selection but skips a candidate when changed-limit reacquisition was
	// denied. Successful capacity includes cleanup for immediate lease ownership.
	// bounded provider slot acquisition
	Acquire func(context.Context, int64, int) (*AcquireResult, bool, error)
	// Fresh returns a refreshed candidate and eligibility result while preserving
	// the adapter's record association. A false result releases the acquired
	// attempt before another candidate is tried.
	// current candidate metadata lookup
	Fresh func(context.Context, SelectionCandidate) (SelectionCandidate, bool)
	// CanRecheck admits the next database eligibility probe. A false result
	// releases current capacity and ends this pass, keeping slot ownership
	// independent from probe-budget availability.
	CanRecheck func() bool // database probe budget admission
	// Recheck returns authoritative candidate data after Fresh. Its successful
	// result has a nonnil Snapshot, and a false result releases capacity before
	// the selector continues.
	// database candidate eligibility lookup
	Recheck func(context.Context, SelectionCandidate) (SelectionCandidate, bool)
	// CompactAllowed evaluates the rechecked provider when Compact is requested.
	// A false result retains diagnostic evidence and rejects that candidate
	// before any binding update.
	CompactAllowed func(SelectionCandidate) bool // refreshed Compact capability check
	// BindSticky may be nil and runs after successful eligibility and capacity
	// checks. The owning adapter handles write errors; Select invokes it for a
	// nonempty session when preservation is false.
	BindSticky func(context.Context, SelectionCandidate) // accepted session binding update
}

// SelectionResult returns the accepted candidate, owned attempt, and observed
// Compact rejection flag.
type SelectionResult struct {
	Candidate SelectionCandidate // accepted refreshed provider metadata
	// Attempt owns accepted capacity under the request lease. Execution callers
	// finish or release it after the upstream attempt; request cleanup can also
	// release it.
	Attempt *AttemptLease // accepted provider resource ownership
	// CompactBlocked records at least one refreshed Compact rejection during the
	// pass. It can remain true even when a later candidate succeeds, allowing the
	// caller to retain that diagnostic evidence.
	CompactBlocked bool // observed Compact rejection flag
}

// Select tries candidates in their supplied order and returns an accepted
// candidate with its attempt. Callers supply a nonnil context, synchronous
// callbacks, valid refreshed snapshots, and nonnil load data when LoadKnown is
// true. An attempted acquisition error returns immediately, and a denied slot
// moves to the next candidate.
//
// Capacity is attached to an attempt before fresh and database checks. Initial
// acquisition-budget exhaustion or a refused database probe ends the pass;
// after a changed concurrency limit, reacquisition-budget exhaustion skips
// that candidate. Rechecks and Compact rejection return capacity before
// another candidate is tried, and Compact rejection remains in the result
// after a later success.
//
// Binding follows accepted eligibility unless preservation is requested.
// Parent cleanup can release the attempt during preparation, so the execution
// caller honors its request's cancellation and completion policy before using
// the returned candidate.
func (l *Lease) Select(ctx context.Context, input SelectionInput, ports AttemptSelectionPorts) (SelectionResult, bool, error) {
	ctx = WithRequestLease(ctx, l)
	output := SelectionResult{}
	for _, candidate := range input.Candidates {
		if candidate.Snapshot == nil {
			continue
		}
		if _, excluded := input.ExcludedIDs[candidate.Snapshot.ID]; excluded {
			continue
		}
		if candidate.LoadKnown && candidate.Snapshot.Concurrency > 0 && candidate.Load.CurrentConcurrency >= candidate.Snapshot.Concurrency {
			continue
		}
		acquired, attempted, err := ports.Acquire(ctx, candidate.Snapshot.ID, candidate.Snapshot.Concurrency)
		if !attempted {
			break
		}
		if err != nil {
			return output, false, err
		}
		if acquired == nil || !acquired.Acquired {
			continue
		}
		// Fresh and database reads can reject a slot already acquired from the
		// candidate. Attach cleanup first so request ownership and every preparation
		// failure share that capacity.
		attempt := NewAttemptLease(l, nil, acquired.ReleaseFunc)
		fresh, ok := ports.Fresh(ctx, candidate)
		// Fresh eligibility can invalidate the candidate after slot acquisition.
		// Return its capacity before continuing so rejected preparation does not
		// retain a provider slot.
		if !ok {
			attempt.Release()
			continue
		}
		// The database probe budget ended after acquisition. Return this unused slot
		// before leaving the pass because execution cannot accept unconfirmed
		// provider metadata.
		if !ports.CanRecheck() {
			attempt.Release()
			break
		}
		// Recheck eligibility against the database after the fresh read. A rejected
		// result releases the acquired attempt before selection can continue with
		// another provider.
		fresh, ok = ports.Recheck(ctx, fresh)
		if !ok {
			attempt.Release()
			continue
		}
		// Compact eligibility is evaluated on the rechecked record. Retain rejection
		// evidence and return the slot before other candidates are attempted.
		if input.RequireCompact && !ports.CompactAllowed(fresh) {
			output.CompactBlocked = true
			attempt.Release()
			continue
		}
		// The accepted record changed its physical capacity limit. Return the
		// candidate's slot before reacquiring so admission uses the current limit
		// and only one attempt retains capacity.
		if fresh.Snapshot.Concurrency != candidate.Snapshot.Concurrency {
			attempt.Release()
			acquired, attempted, err = ports.Acquire(ctx, fresh.Snapshot.ID, fresh.Snapshot.Concurrency)
			if !attempted {
				continue
			}
			if err != nil {
				return output, false, err
			}
			if acquired == nil || !acquired.Acquired {
				continue
			}
			attempt = NewAttemptLease(l, nil, acquired.ReleaseFunc)
		}
		// Publish affinity after the final eligibility and admission checks succeed.
		// Escape and parent-reuse requests preserve their earlier binding so later
		// requests can continue to use it.
		if input.SessionHash != "" && !input.PreserveStickyBinding && ports.BindSticky != nil {
			ports.BindSticky(ctx, fresh)
		}
		output.Candidate = fresh
		output.Attempt = attempt
		return output, true, nil
	}
	return output, false, nil
}
