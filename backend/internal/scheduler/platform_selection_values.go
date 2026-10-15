package scheduler

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// platform_selection_values.go carries flow-record associations through
// scoring, acquisition, and provider rechecks.
// Platform scoring borrows request-scoped flow records while the shared scorer
// operates on smaller ScoreProvider values. Newly allocated score pointers
// retain an invocation-local lookup to the exact originating flow record,
// including separate observations for one persisted provider ID.
//
// The ordering helpers restore borrowed flow pointers after ranking or
// weighted sampling. TryOrder additionally retains each adapter ProjectionID
// while fresh and database callbacks produce new metadata associations. A
// nested request lease adopts acquired attempts before eligibility rechecks
// can fail, and returning a successful result transfers cleanup through that
// lease. Snapshot copies duplicate slice entries while keeping their nested
// provider, load, and feedback-time references shared.

// platformScores converts platform score entries and returns a lookup to their
// originating flow records. It allocates new candidate storage and score
// metadata while retaining load and feedback-time pointers; empty input yields
// allocated empty conversion storage.
//
// A new ScoreProvider pointer identifies each nonnil flow occurrence, and its
// ProjectionID carries the adapter association needed by quota callbacks. The
// pointer lookup survives ordering by the shared scorer so repeated persisted
// IDs do not overwrite one another's source record. Nil flow records keep nil
// score metadata and are handled by the consumer's eligibility rules.
func platformScores(values []PlatformCandidateScore) ([]CandidateScore, map[*ScoreProvider]*FlowProvider) {
	out := make([]CandidateScore, len(values))
	source := map[*ScoreProvider]*FlowProvider{}
	for i, v := range values {
		var a *ScoreProvider
		if v.Provider != nil {
			// Allocate a separate scoring identity for this occurrence. The shared
			// ranking keeps that pointer, allowing restoration to retain its original
			// request record even when persisted IDs repeat.
			a = &ScoreProvider{ID: v.Provider.ID, Name: v.Provider.Name, Platform: v.Provider.Platform, Priority: v.Provider.Priority, SessionWindowEnd: v.Provider.SessionWindowEnd, ProjectionID: v.Provider.ProjectionID}
			source[a] = v.Provider
		}
		out[i] = CandidateScore{Provider: a, LoadInfo: v.LoadInfo, LoadKnown: v.LoadKnown, Score: v.Score, BaseScore: v.BaseScore, StickyBonus: v.StickyBonus, PreviousBonus: v.PreviousBonus, SessionStickyBonus: v.SessionStickyBonus, Priority: v.Priority, ErrorRate: v.ErrorRate, TTFT: v.TTFT, HasTTFT: v.HasTTFT, HasFeedback: v.HasFeedback, Feedback: v.Feedback, Factors: v.Factors}
	}
	return out, source
}

// platformScoresFrom restores platform score values through the supplied
// conversion lookup. Nil input stays nil, while an unmatched score-provider
// pointer produces nil flow metadata; callers use the lookup from the same
// conversion and keep nested observations immutable.
func platformScoresFrom(values []CandidateScore, source map[*ScoreProvider]*FlowProvider) []PlatformCandidateScore {
	if values == nil {
		return nil
	}
	out := make([]PlatformCandidateScore, len(values))
	for i, v := range values {
		out[i] = PlatformCandidateScore{Provider: source[v.Provider], LoadInfo: v.LoadInfo, LoadKnown: v.LoadKnown, Score: v.Score, BaseScore: v.BaseScore, StickyBonus: v.StickyBonus, PreviousBonus: v.PreviousBonus, SessionStickyBonus: v.SessionStickyBonus, Priority: v.Priority, ErrorRate: v.ErrorRate, TTFT: v.TTFT, HasTTFT: v.HasTTFT, HasFeedback: v.HasFeedback, Feedback: v.Feedback, Factors: v.Factors}
	}
	return out
}

// scoreCandidates delegates platform candidate scoring while retaining
// conversion pointers for restoration. The shared scorer omits nil metadata
// and supplies known-load deviation; returned platform entries borrow their
// original flow records and supplied observations.
func (s *PlatformSelector) scoreCandidates(values []*FlowProvider, loads map[int64]*ProviderLoadInfo, stats *RuntimeStats, weights policy.ScoreWeights, input ScoreInput, now time.Time) ([]PlatformCandidateScore, float64) {
	seed := make([]PlatformCandidateScore, len(values))
	for i, a := range values {
		seed[i] = PlatformCandidateScore{Provider: a}
	}
	projected, source := platformScores(seed)
	providers := make([]*ScoreProvider, len(projected))
	for i, v := range projected {
		providers[i] = v.Provider
	}
	scores, skew := ScoreCandidates(providers, loads, stats, weights, input, now)
	return platformScoresFrom(scores, source), skew
}

// platformTopK selects ranked platform entries through the shared Top-K helper
// and restores their flow associations. Nonpositive k inherits selection of
// one, empty ranking returns nil, and nested metadata and load pointers remain
// borrowed.
func platformTopK(values []PlatformCandidateScore, k int) []PlatformCandidateScore {
	scores, source := platformScores(values)
	return platformScoresFrom(SelectTopK(scores, k), source)
}

// weightedOrder returns a sampled permutation using this request's affinity,
// model, group, and selector clock as seed context. It restores original flow
// associations from the conversion lookup and retains shared observation
// references.
func (s *PlatformSelector) weightedOrder(values []PlatformCandidateScore, req PlatformSelectionInput) []PlatformCandidateScore {
	scores, source := platformScores(values)
	order := BuildWeightedSelectionOrder(scores, ScoreInput{GroupID: req.GroupID, SessionHash: req.SessionHash, PreviousResponseID: req.PreviousResponseID, RequestedModel: req.RequestedModel, StickyProviderID: req.StickyProviderID, StickyPreviousProviderID: req.StickyPreviousProviderID, StickyWeighted: req.StickyWeighted, Now: s.now})
	return platformScoresFrom(order, source)
}

// partitionSubscription separates subscription metadata from regular
// candidates while retaining input order within each pool. It uses the
// configured subscription predicate for nonnil providers, includes nil records
// in the regular pool, and returns newly allocated pointer slices.
func (s *PlatformSelector) partitionSubscription(providers []*FlowProvider) ([]*FlowProvider, []*FlowProvider) {
	subscriptions := make([]*FlowProvider, 0, len(providers))
	regular := make([]*FlowProvider, 0, len(providers))
	for _, a := range providers {
		if a != nil && s.ports.IsSubscription(a) {
			subscriptions = append(subscriptions, a)
		} else {
			regular = append(regular, a)
		}
	}
	return subscriptions, regular
}

// TryOrderBounded enables separate sixty-four-call acquisition and database
// recheck budgets before delegating to TryOrder. Counts track actual limited
// probes rather than unique providers; ordinary shared-pool selection receives
// its caller's budget.
func (s *PlatformSelector) TryOrderBounded(ctx context.Context, req PlatformSelectionInput, order []PlatformCandidateScore) (*FlowSelection, bool, error) {
	budget := NewProbeBudget()
	budget.enableLimit()
	return s.TryOrder(ctx, req, order, budget)
}

// TryOrder acquires and rechecks the supplied candidate order under a nested
// completion-based request owner. Callers provide a usable probe budget and
// per-record ProjectionIDs from the same adapter invocation, with nonnil load
// records when LoadKnown is true.
//
// Every converted candidate is retained by its ProjectionID, and fresh or
// database callbacks register their separately associated records before
// returning metadata to Lease.Select. That selector owns capacity rollback and
// changed-limit reacquisition. Failure or no selection releases the nested
// owner; success returns its Release callback so execution or parent cleanup
// can finish all adopted attempts. Binding failures are ignored by this
// adapter callback and do not replace a selected result.
func (s *PlatformSelector) TryOrder(ctx context.Context, req PlatformSelectionInput, order []PlatformCandidateScore, budget *ProbeBudget) (*FlowSelection, bool, error) {
	records := map[uint64]*FlowProvider{}
	project := func(a *FlowProvider) SelectionCandidate {
		if a == nil {
			return SelectionCandidate{}
		}
		// ProjectionIDs come from the adapter's conversion scope and identify
		// successive reads separately. Keep each record before returning metadata so
		// rechecks can resolve the corresponding execution target.
		records[a.ProjectionID] = a
		return SelectionCandidate{ProjectionID: a.ProjectionID, Snapshot: &provider.ProviderSnapshot{ID: a.ID, Platform: a.Platform, Type: a.Type, Concurrency: a.Concurrency}, Plan: a.Plan}
	}
	candidates := make([]SelectionCandidate, len(order))
	for i, v := range order {
		candidates[i] = project(v.Provider)
		candidates[i].Load = v.LoadInfo
		candidates[i].LoadKnown = v.LoadKnown
	}
	// Adopt all selection attempts under one completion owner before acquiring
	// slots. An existing request lease can then release that owner if later
	// preparation or request cleanup ends execution.
	owner := NewLease(ctx, ReleaseOnCompletion)
	if parent := RequestLease(ctx); parent != nil {
		parent.Own(owner.Release)
	}
	output, selected, err := owner.Select(ctx, SelectionInput{Candidates: candidates, RequireCompact: req.RequireCompact, SessionHash: req.SessionHash, PreserveStickyBinding: req.PreserveStickyBinding}, AttemptSelectionPorts{
		Acquire: func(ctx context.Context, id int64, limit int) (*AcquireResult, bool, error) {
			if s.concurrency != nil && limit > 0 && !budget.recordAcquire(id) {
				return nil, false, nil
			}
			v, err := s.ports.Acquire(ctx, id, limit)
			return v, true, err
		},
		Fresh: func(ctx context.Context, c SelectionCandidate) (SelectionCandidate, bool) {
			a := s.ports.Fresh(ctx, records[c.ProjectionID], req.Platform, req.routingModel(), false, req.RequiredCapability)
			if a == nil || !s.ports.TransportCompatible(a, req.RequiredTransport) || !s.requestCompatible(ctx, a, req) {
				return SelectionCandidate{}, false
			}
			return project(a), true
		},
		CanRecheck: func() bool { return s.canRecheck(budget) },
		Recheck: func(ctx context.Context, c SelectionCandidate) (SelectionCandidate, bool) {
			a := s.ports.Recheck(ctx, records[c.ProjectionID], req.GroupID, req.Platform, req.routingModel(), false, req.RequiredCapability)
			if a == nil || !s.ports.TransportCompatible(a, req.RequiredTransport) || !s.requestCompatible(ctx, a, req) {
				return SelectionCandidate{}, false
			}
			return project(a), true
		},
		CompactAllowed: func(c SelectionCandidate) bool { return s.ports.CompactAllowed(records[c.ProjectionID]) },
		BindSticky: func(ctx context.Context, c SelectionCandidate) {
			// Binding publication is best effort after accepted metadata and capacity.
			// The selected attempt remains usable when cache affinity cannot be written
			// for this request.
			_ = s.ports.BindSticky(ctx, req.GroupID, req.SessionHash, c.Snapshot.ID)
		},
	})
	if err != nil || !selected {
		// Selection failure leaves no execution caller to return adopted resources.
		// Release the nested owner before returning the error or empty result so
		// acquired attempts finish cleanup.
		owner.Release()
		return nil, output.CompactBlocked, err
	}
	return &FlowSelection{Provider: records[output.Candidate.ProjectionID], Acquired: true, ReleaseFunc: owner.Release}, output.CompactBlocked, nil
}

// CandidatesSnapshot copies candidate slice entries for diagnostics or tests,
// collapsing empty input to nil. Provider, load, plan, and feedback-time
// pointers remain shared, so callers may reorder copied entries while leaving
// the referenced objects unchanged.
func (p PlatformLoadPlan) CandidatesSnapshot() []PlatformCandidateScore {
	return append([]PlatformCandidateScore(nil), p.candidates...)
}
