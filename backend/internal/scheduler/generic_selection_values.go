package scheduler

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// generic_selection_values.go converts flow providers into comparison records
// and restores their associations after ordering.
// Basic and advanced ordering use smaller metadata records while execution
// still needs the originating FlowProvider. Each conversion allocates
// comparison metadata and keeps a lookup keyed by that metadata pointer, so
// records sharing a persisted ID can retain their own association through
// filtering and reordering.
//
// Restoration copies slice entries while retaining provider, load, plan, and
// timestamp references. Sorting wrappers write the restored order back into
// the caller's slice; selection returns its original slice entry. Empty
// conversions allocate empty slices, while restoration of a nil basic result
// returns nil. Callers use each lookup with the conversion that produced it
// and keep borrowed observations stable while comparison or scoring runs.

// flowScore associates one shared scoring value with its original
// request-scoped flow metadata.
type flowScore struct {
	// Provider and score remain associated through score.Provider's conversion
	// pointer. Sorting and sampling copy these values while provider, load, and
	// observation-time references retain their existing ownership.
	Provider *FlowProvider  // borrowed original flow metadata
	score    CandidateScore // associated shared score value
}

// flowBasic creates basic comparison metadata from a flow provider or returns
// nil for nil input. It copies scalar fields while retaining last-use and
// reset time pointers, so callers keep those observations stable during
// ordering.
func flowBasic(a *FlowProvider) *BasicProvider {
	if a == nil {
		return nil
	}
	return &BasicProvider{ID: a.ID, Type: a.Type, Priority: a.Priority, LastUsedAt: a.LastUsedAt, SessionWindowEnd: a.SessionWindowEnd}
}

// flowBasics converts each flow pointer and returns an invocation-local
// pointer lookup for restoration. The resulting slice has new storage,
// including an empty allocation for empty input; nonnil records retain their
// borrowed observation times.
//
// Each allocated BasicProvider pointer identifies its corresponding occurrence
// rather than using the persisted provider ID as the lookup key. Reordering
// these comparison records can therefore recover their original FlowProvider
// association. Nil inputs produce nil comparison records and a nil lookup key;
// callers of the ordering wrappers provide nonnil records before those helpers
// dereference metadata.
func flowBasics(values []*FlowProvider) ([]*BasicProvider, map[*BasicProvider]*FlowProvider) {
	out := make([]*BasicProvider, len(values))
	m := make(map[*BasicProvider]*FlowProvider, len(values))
	for i, a := range values {
		// Assign a new comparison pointer to each occurrence before recording its
		// source. Persisted provider IDs can repeat while this per-conversion
		// pointer keeps restoration unambiguous.
		out[i] = flowBasic(a)
		m[out[i]] = a
	}
	return out, m
}

// flowBasicLoads pairs converted comparison metadata with borrowed load
// observations and an invocation-local restoration lookup. Input records stay
// unchanged, while empty input yields new empty slices and map storage.
//
// The lookup uses each new BasicProvider pointer so filtering preserves the
// matching flow/load pair even when persisted IDs repeat. Provider time
// pointers and load pointers remain borrowed, and nil metadata cannot be
// passed to helpers that dereference it. Restoration uses this same lookup
// after the basic helper chooses or reorders comparison values.
func flowBasicLoads(values []FlowLoad) ([]BasicCandidate, map[*BasicProvider]FlowLoad) {
	out := make([]BasicCandidate, len(values))
	m := make(map[*BasicProvider]FlowLoad, len(values))
	for i, a := range values {
		// Keep the load observation with its originating metadata occurrence. The
		// newly allocated comparison pointer supplies the restoration key used after
		// basic filtering or reordering.
		v := flowBasic(a.Provider)
		out[i] = BasicCandidate{Provider: v, Load: a.LoadInfo}
		m[v] = a
	}
	return out, m
}

// restoreFlowLoads maps basic candidates back to their original flow/load
// values using the supplied conversion lookup. Nil input remains nil, and an
// unmatched comparison pointer produces a zero FlowLoad; callers retain the
// matching lookup for the whole operation.
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

// flowFilterByMinPriority delegates lowest-priority filtering and restores
// original flow/load associations. Result slice storage is new while metadata
// and observations remain borrowed; empty conversion input produces an empty
// result rather than retaining nil input shape.
func flowFilterByMinPriority(values []FlowLoad) []FlowLoad {
	v, m := flowBasicLoads(values)
	return restoreFlowLoads(FilterByMinPriority(v), m)
}

// flowFilterByMinLoadRate delegates lowest-load filtering through basic
// comparison metadata and restores matched flow values. Callers supply usable
// load pointers; resulting slice entries retain the original provider and
// observation ownership.
func flowFilterByMinLoadRate(values []FlowLoad) []FlowLoad {
	v, m := flowBasicLoads(values)
	return restoreFlowLoads(FilterByMinLoadRate(v), m)
}

// flowFilterBySoonestReset delegates active-window preference using the
// supplied clock and restores the matched flow/load values. Missing active
// windows retain the converted pool, and the returned records still borrow
// provider and timestamp observations.
func flowFilterBySoonestReset(values []FlowLoad, now func() time.Time) []FlowLoad {
	v, m := flowBasicLoads(values)
	return restoreFlowLoads(FilterBySoonestReset(v, now), m)
}

// flowSelectByLRU returns the selected entry from the original flow/load
// slice, or nil when basic selection yields no candidate. Its matching
// comparison pointer locates that entry, so later entry mutation is visible
// through the returned pointer.
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

// flowShuffleWithinSortGroups randomizes equal basic groups and writes their
// restored order into values. Callers sort the original flow/load slice first
// and keep borrowed provider/load observations stable; no provider or load
// record is copied.
func flowShuffleWithinSortGroups(values []FlowLoad) {
	v, m := flowBasicLoads(values)
	ShuffleWithinSortGroups(v)
	copy(values, restoreFlowLoads(v, m))
}

// flowSortProvidersByPriorityAndLastUsed delegates priority, recency, and
// optional OAuth ordering before restoring each original flow pointer. It
// mutates the supplied slice order and inherits the basic helper's
// whole-second shuffle grouping.
func flowSortProvidersByPriorityAndLastUsed(values []*FlowProvider, preferOAuth bool) {
	v, m := flowBasics(values)
	SortProvidersByPriorityAndLastUsed(v, preferOAuth)
	for i, a := range v {
		values[i] = m[a]
	}
}

// flowSortProvidersByPriorityOnly orders the caller's flow pointers through
// basic priority comparisons and optional OAuth preference. It restores those
// pointers into the same slice, while provider metadata and time observations
// remain borrowed.
func flowSortProvidersByPriorityOnly(values []*FlowProvider, preferOAuth bool) {
	v, m := flowBasics(values)
	SortProvidersByPriorityOnly(v, preferOAuth)
	for i, a := range v {
		values[i] = m[a]
	}
}

// flowShuffleWithinPriority delegates clock-seeded shuffling within adjacent
// equal priorities and restores original flow pointers in place. Callers sort
// values by priority first, since this wrapper preserves the basic helper's
// adjacency requirement.
func flowShuffleWithinPriority(values []*FlowProvider, now func() time.Time) {
	v, m := flowBasics(values)
	ShuffleWithinPriority(v, now)
	for i, a := range v {
		values[i] = m[a]
	}
}

// flowScoreCandidates scores eligible metadata while retaining each
// originating flow provider in a local pointer lookup. It returns computed
// flow scores and known-load deviation, allocating result slice storage and
// borrowing input observation pointers.
//
// Each nonnil flow record gets a separate ScoreProvider, so repeated persisted
// IDs retain their occurrence's association through the scoring result. Nil
// records are omitted by the shared scorer. An empty scoring result becomes an
// allocated empty flow slice, and later ordering uses these same
// score-provider pointers to restore the corresponding flow values.
func flowScoreCandidates(values []*FlowProvider, loads map[int64]*ProviderLoadInfo, stats *RuntimeStats, weights policy.ScoreWeights, input ScoreInput, now time.Time) ([]flowScore, float64) {
	projected := make([]*ScoreProvider, len(values))
	source := map[*ScoreProvider]*FlowProvider{}
	for i, a := range values {
		if a != nil {
			// Each scored occurrence needs its own pointer association because the
			// shared scorer returns metadata references. Recovering by persisted ID
			// alone would merge repeated input records.
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

// flowBuildSelectionOrder ranks and samples flow scores through the shared
// Top-K algorithm. It copies score values and restores the original flow
// metadata after sampling; callers pass score/provider associations produced
// by the same conversion.
//
// The local lookup uses score.Provider pointers rather than persisted IDs,
// retaining distinct converted occurrences while result order changes. Reusing
// one score pointer for different flow records makes its last lookup
// assignment win, so each scored occurrence keeps its own association. Empty
// input returns an allocated empty flow slice, while borrowed metadata and
// observation pointers remain shared.
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
