package scheduler

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// platformScores associates shared score records with request candidates for ranking and sampling.
func platformScores(values []PlatformCandidateScore) ([]CandidateScore, map[*ScoreProvider]*FlowProvider) {
	out := make([]CandidateScore, len(values))
	source := map[*ScoreProvider]*FlowProvider{}
	for i, v := range values {
		var a *ScoreProvider
		if v.Provider != nil {
			a = &ScoreProvider{ID: v.Provider.ID, Name: v.Provider.Name, Platform: v.Provider.Platform, Priority: v.Provider.Priority, SessionWindowEnd: v.Provider.SessionWindowEnd, ProjectionID: v.Provider.ProjectionID}
			source[a] = v.Provider
		}
		out[i] = CandidateScore{Provider: a, LoadInfo: v.LoadInfo, LoadKnown: v.LoadKnown, Score: v.Score, BaseScore: v.BaseScore, StickyBonus: v.StickyBonus, PreviousBonus: v.PreviousBonus, SessionStickyBonus: v.SessionStickyBonus, Priority: v.Priority, ErrorRate: v.ErrorRate, TTFT: v.TTFT, HasTTFT: v.HasTTFT, HasFeedback: v.HasFeedback, Feedback: v.Feedback, Factors: v.Factors}
	}
	return out, source
}

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

func platformTopK(values []PlatformCandidateScore, k int) []PlatformCandidateScore {
	scores, source := platformScores(values)
	return platformScoresFrom(SelectTopK(scores, k), source)
}

func (s *PlatformSelector) weightedOrder(values []PlatformCandidateScore, req PlatformSelectionInput) []PlatformCandidateScore {
	scores, source := platformScores(values)
	order := BuildWeightedSelectionOrder(scores, ScoreInput{GroupID: req.GroupID, SessionHash: req.SessionHash, PreviousResponseID: req.PreviousResponseID, RequestedModel: req.RequestedModel, StickyProviderID: req.StickyProviderID, StickyPreviousProviderID: req.StickyPreviousProviderID, StickyWeighted: req.StickyWeighted, Now: s.now})
	return platformScoresFrom(order, source)
}

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

// TryOrderBounded applies a 64-attempt budget; shared pool selection receives its budget from the caller.
func (s *PlatformSelector) TryOrderBounded(ctx context.Context, req PlatformSelectionInput, order []PlatformCandidateScore) (*FlowSelection, bool, error) {
	budget := NewProbeBudget()
	budget.enableLimit()
	return s.TryOrder(ctx, req, order, budget)
}

func (s *PlatformSelector) TryOrder(ctx context.Context, req PlatformSelectionInput, order []PlatformCandidateScore, budget *ProbeBudget) (*FlowSelection, bool, error) {
	records := map[uint64]*FlowProvider{}
	project := func(a *FlowProvider) SelectionCandidate {
		if a == nil {
			return SelectionCandidate{}
		}
		records[a.ProjectionID] = a
		return SelectionCandidate{ProjectionID: a.ProjectionID, Snapshot: &provider.ProviderSnapshot{ID: a.ID, Platform: a.Platform, Type: a.Type, Concurrency: a.Concurrency}, Plan: a.Plan}
	}
	candidates := make([]SelectionCandidate, len(order))
	for i, v := range order {
		candidates[i] = project(v.Provider)
		candidates[i].Load = v.LoadInfo
		candidates[i].LoadKnown = v.LoadKnown
	}
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
			_ = s.ports.BindSticky(ctx, req.GroupID, req.SessionHash, c.Snapshot.ID)
		},
	})
	if err != nil || !selected {
		owner.Release()
		return nil, output.CompactBlocked, err
	}
	return &FlowSelection{Provider: records[output.Candidate.ProjectionID], Acquired: true, ReleaseFunc: owner.Release}, output.CompactBlocked, nil
}

// CandidatesSnapshot returns a shallow copy of the candidate slice for diagnostics and tests.
func (p PlatformLoadPlan) CandidatesSnapshot() []PlatformCandidateScore {
	return append([]PlatformCandidateScore(nil), p.candidates...)
}
