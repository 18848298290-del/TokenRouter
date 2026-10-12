package scheduler

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// SelectionCandidate holds a snapshot and observations; the selection call owns its correlation ID.
type SelectionCandidate struct {
	Snapshot     *provider.ProviderSnapshot
	ProjectionID uint64
	Load         *ProviderLoadInfo
	LoadKnown    bool
	Plan         *routing.CandidatePlan
}

// SelectionInput contains the admitted group, request requirements, and ordered candidates.
type SelectionInput struct {
	GroupID               *int64
	RequestedModel        string
	RoutePlan             routing.RoutePlan
	Candidates            []SelectionCandidate
	ExcludedIDs           map[int64]struct{}
	RequireCompact        bool
	SessionHash           string
	PreserveStickyBinding bool
}

// AttemptSelectionPorts supplies slot acquisition, fresh reads, and database rechecks with budget
// checks.
// The gateway executes upstream requests and retries using the returned candidate.
type AttemptSelectionPorts struct {
	Acquire        func(context.Context, int64, int) (*AcquireResult, bool, error)
	Fresh          func(context.Context, SelectionCandidate) (SelectionCandidate, bool)
	CanRecheck     func() bool
	Recheck        func(context.Context, SelectionCandidate) (SelectionCandidate, bool)
	CompactAllowed func(SelectionCandidate) bool
	BindSticky     func(context.Context, SelectionCandidate)
}
type SelectionResult struct {
	Candidate      SelectionCandidate
	Attempt        *AttemptLease
	CompactBlocked bool
}

// Select tries candidates in order and releases acquired slots through AttemptLease when rechecks fail.
// The caller owns the returned attempt and executes upstream requests and retries.
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
		attempt := NewAttemptLease(l, nil, acquired.ReleaseFunc)
		fresh, ok := ports.Fresh(ctx, candidate)
		if !ok {
			attempt.Release()
			continue
		}
		if !ports.CanRecheck() {
			attempt.Release()
			break
		}
		fresh, ok = ports.Recheck(ctx, fresh)
		if !ok {
			attempt.Release()
			continue
		}
		if input.RequireCompact && !ports.CompactAllowed(fresh) {
			output.CompactBlocked = true
			attempt.Release()
			continue
		}
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
		if input.SessionHash != "" && !input.PreserveStickyBinding && ports.BindSticky != nil {
			ports.BindSticky(ctx, fresh)
		}
		output.Candidate = fresh
		output.Attempt = attempt
		return output, true, nil
	}
	return output, false, nil
}
