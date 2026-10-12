package scheduler

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

type flowScore struct {
	Provider *FlowProvider
	score    CandidateScore
}

// flowBasic extracts the fields used by basic ordering.
func flowBasic(a *FlowProvider) *BasicProvider {
	if a == nil {
		return nil
	}
	return &BasicProvider{ID: a.ID, Type: a.Type, Priority: a.Priority, LastUsedAt: a.LastUsedAt, SessionWindowEnd: a.SessionWindowEnd}
}

func flowBasics(values []*FlowProvider) ([]*BasicProvider, map[*BasicProvider]*FlowProvider) {
	out := make([]*BasicProvider, len(values))
	m := make(map[*BasicProvider]*FlowProvider, len(values))
	for i, a := range values {
		out[i] = flowBasic(a)
		m[out[i]] = a
	}
	return out, m
}

func flowBasicLoads(values []FlowLoad) ([]BasicCandidate, map[*BasicProvider]FlowLoad) {
	out := make([]BasicCandidate, len(values))
	m := make(map[*BasicProvider]FlowLoad, len(values))
	for i, a := range values {
		v := flowBasic(a.Provider)
		out[i] = BasicCandidate{Provider: v, Load: a.LoadInfo}
		m[v] = a
	}
	return out, m
}

func restoreFlowLoads(values []BasicCandidate, m map[*BasicProvider]FlowLoad) []FlowLoad {
	if values == nil {
		return nil
	}
	out := make([]FlowLoad, len(values))
	for i, a := range values {
		out[i] = m[a.Provider]
	}
	return out
}

func flowFilterByMinPriority(values []FlowLoad) []FlowLoad {
	v, m := flowBasicLoads(values)
	return restoreFlowLoads(FilterByMinPriority(v), m)
}

func flowFilterByMinLoadRate(values []FlowLoad) []FlowLoad {
	v, m := flowBasicLoads(values)
	return restoreFlowLoads(FilterByMinLoadRate(v), m)
}

func flowFilterBySoonestReset(values []FlowLoad, now func() time.Time) []FlowLoad {
	v, m := flowBasicLoads(values)
	return restoreFlowLoads(FilterBySoonestReset(v, now), m)
}

func flowSelectByLRU(values []FlowLoad, preferOAuth bool) *FlowLoad {
	v, _ := flowBasicLoads(values)
	selected := SelectByLRU(v, preferOAuth)
	if selected == nil {
		return nil
	}
	for i := range v {
		if v[i].Provider == selected.Provider {
			return &values[i]
		}
	}
	return nil
}

func flowShuffleWithinSortGroups(values []FlowLoad) {
	v, m := flowBasicLoads(values)
	ShuffleWithinSortGroups(v)
	copy(values, restoreFlowLoads(v, m))
}

func flowSortProvidersByPriorityAndLastUsed(values []*FlowProvider, preferOAuth bool) {
	v, m := flowBasics(values)
	SortProvidersByPriorityAndLastUsed(v, preferOAuth)
	for i, a := range v {
		values[i] = m[a]
	}
}

func flowSortProvidersByPriorityOnly(values []*FlowProvider, preferOAuth bool) {
	v, m := flowBasics(values)
	SortProvidersByPriorityOnly(v, preferOAuth)
	for i, a := range v {
		values[i] = m[a]
	}
}

func flowShuffleWithinPriority(values []*FlowProvider, now func() time.Time) {
	v, m := flowBasics(values)
	ShuffleWithinPriority(v, now)
	for i, a := range v {
		values[i] = m[a]
	}
}

func flowScoreCandidates(values []*FlowProvider, loads map[int64]*ProviderLoadInfo, stats *RuntimeStats, weights policy.ScoreWeights, input ScoreInput, now time.Time) ([]flowScore, float64) {
	projected := make([]*ScoreProvider, len(values))
	source := map[*ScoreProvider]*FlowProvider{}
	for i, a := range values {
		if a != nil {
			projected[i] = &ScoreProvider{ID: a.ID, Name: a.Name, Platform: a.Platform, Priority: a.Priority, SessionWindowEnd: a.SessionWindowEnd}
			source[projected[i]] = a
		}
	}
	scores, skew := ScoreCandidates(projected, loads, stats, weights, input, now)
	out := make([]flowScore, len(scores))
	for i, c := range scores {
		out[i] = flowScore{Provider: source[c.Provider], score: c}
	}
	return out, skew
}

func flowBuildSelectionOrder(values []flowScore, input ScoreInput) []flowScore {
	scores := make([]CandidateScore, len(values))
	source := map[*ScoreProvider]*FlowProvider{}
	for i, v := range values {
		scores[i] = v.score
		source[v.score.Provider] = v.Provider
	}
	order := BuildSelectionOrder(scores, input)
	out := make([]flowScore, len(order))
	for i, c := range order {
		out[i] = flowScore{Provider: source[c.Provider], score: c}
	}
	return out
}
