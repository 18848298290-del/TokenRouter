package scheduler

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// TestAdvancedSchedulerCoreUsesRuntimeFeedbackAndNeutralOptionalSignals
// reports repeated failures for one provider and successes for an otherwise
// equal peer. It checks the healthier peer's higher score and absent TTFT
// observations for both providers.
func TestAdvancedSchedulerCoreUsesRuntimeFeedbackAndNeutralOptionalSignals(t *testing.T) {
	providers := []*ScoreProvider{
		{ID: 11, Priority: 1, Platform: capability.PlatformGemini},
		{ID: 12, Priority: 1, Platform: capability.PlatformGemini},
	}
	loadMap := map[int64]*ProviderLoadInfo{
		11: {ProviderID: 11, LoadRate: 20, WaitingCount: 0},
		12: {ProviderID: 12, LoadRate: 20, WaitingCount: 0},
	}
	stats := NewRuntimeStats(time.Now)
	for range 8 {
		stats.Report(11, false, nil)
		stats.Report(12, true, nil)
	}

	candidates, _ := ScoreCandidates(
		providers,
		loadMap,
		stats,
		advancedSchedulerTestWeights(),
		ScoreInput{},
		time.Now(),
	)
	require.Len(t, candidates, 2)
	require.Greater(t, candidates[1].Score, candidates[0].Score)
	require.False(t, candidates[0].HasTTFT)
	require.False(t, candidates[1].HasTTFT)
}

// TestAdvancedSchedulerCoreTreatsMissingErrorRateAsZero compares unknown load
// with an observed neutral load and missing error, latency, and window
// feedback. It requires equal scores, full error-health factors, and zero
// deviation with one known load.
func TestAdvancedSchedulerCoreTreatsMissingErrorRateAsZero(t *testing.T) {
	providers := []*ScoreProvider{
		{ID: 21, Priority: 1, Platform: capability.PlatformGemini},
		{ID: 22, Priority: 1, Platform: capability.PlatformGemini},
	}
	weights := policy.ScoreWeights{
		Load:      1,
		ErrorRate: 1,
		TTFT:      1,
		Reset:     1,
	}

	// Provider 21 has unknown load; provider 22 has load at the neutral value. Neither has
	// error, TTFT, or window feedback, so both receive a 0% error rate and equal scores.
	candidates, skew := ScoreCandidates(
		providers,
		map[int64]*ProviderLoadInfo{22: {ProviderID: 22, LoadRate: 50}},
		nil,
		weights,
		ScoreInput{},
		time.Now(),
	)

	require.Len(t, candidates, 2)
	require.InDelta(t, candidates[0].Score, candidates[1].Score, 0.000001)
	require.Equal(t, 0.0, skew, "只有一个已知负载样本时不能计算出偏斜")
	require.False(t, candidates[0].LoadKnown)
	require.True(t, candidates[1].LoadKnown)
	require.InDelta(t, 1.0, candidates[0].Factors.ErrorRate, 0.000001)
	require.InDelta(t, 1.0, candidates[1].Factors.ErrorRate, 0.000001)
}

// TestAdvancedSchedulerCoreTopKUsesStableOrderForMixedKnownAndUnknownLoads
// scores four equal-priority records with zero factor weights and mixed load
// availability. Every input permutation must select the same two lowest
// provider IDs, protecting deterministic tie ordering.
func TestAdvancedSchedulerCoreTopKUsesStableOrderForMixedKnownAndUnknownLoads(t *testing.T) {
	base, _ := ScoreCandidates(
		[]*ScoreProvider{
			{ID: 1, Priority: 5},
			{ID: 2, Priority: 5},
			{ID: 3, Priority: 5},
			{ID: 4, Priority: 5},
		},
		map[int64]*ProviderLoadInfo{
			1: {ProviderID: 1, LoadRate: 99, WaitingCount: 9},
			3: {ProviderID: 3, LoadRate: 1, WaitingCount: 0},
		},
		nil,
		policy.ScoreWeights{},
		ScoreInput{},
		time.Now(),
	)
	require.Len(t, base, 4)
	require.True(t, base[0].LoadKnown)
	require.False(t, base[1].LoadKnown)
	require.True(t, base[2].LoadKnown)
	require.False(t, base[3].LoadKnown)

	// Zero factor weights erase load differences from ranking. Enumerate every
	// candidate ordering so an input-position preference cannot hide behind one
	// arranged fixture.
	permutation := []int{0, 1, 2, 3}
	var verifyPermutations func(int)
	verifyPermutations = func(position int) {
		if position == len(permutation) {
			candidates := make([]CandidateScore, 0, len(permutation))
			for _, index := range permutation {
				candidates = append(candidates, base[index])
			}
			topK := SelectTopK(candidates, 2)
			require.Len(t, topK, 2)
			require.Equal(t, []int64{1, 2}, []int64{topK[0].Provider.ID, topK[1].Provider.ID}, "输入顺序=%v", permutation)
			return
		}
		for index := position; index < len(permutation); index++ {
			permutation[position], permutation[index] = permutation[index], permutation[position]
			verifyPermutations(position + 1)
			permutation[position], permutation[index] = permutation[index], permutation[position]
		}
	}
	verifyPermutations(0)
}

// TestAdvancedSchedulerCoreRanksFirstFailureBelowUnknownProvider reports one
// failure beside an unobserved equal-priority provider. Error-only scoring
// must rank the failed record lower, protecting updates from the zero-error
// baseline.
func TestAdvancedSchedulerCoreRanksFirstFailureBelowUnknownProvider(t *testing.T) {
	failed := &ScoreProvider{ID: 31, Priority: 1, Platform: capability.PlatformGemini}
	unknown := &ScoreProvider{ID: 32, Priority: 1, Platform: capability.PlatformGemini}
	stats := NewRuntimeStats(time.Now)
	stats.Report(failed.ID, false, nil)

	candidates, _ := ScoreCandidates(
		[]*ScoreProvider{failed, unknown},
		nil,
		stats,
		policy.ScoreWeights{ErrorRate: 1},
		ScoreInput{},
		time.Now(),
	)

	require.Len(t, candidates, 2)
	require.Less(t, candidates[0].Score, candidates[1].Score)
	require.Equal(t, unknown.ID, candidates[1].Provider.ID)
}

// TestAdvancedSchedulerCoreUsesWeightedSamplingForStickyCandidate supplies
// precomputed scores and varies session seeds for a two-candidate Top-K. It
// requires both retained IDs and observes either one first; this exercises
// ordering rather than bonus computation.
func TestAdvancedSchedulerCoreUsesWeightedSamplingForStickyCandidate(t *testing.T) {
	candidates := []CandidateScore{
		{Provider: &ScoreProvider{ID: 1, Priority: 1}, LoadInfo: &ProviderLoadInfo{}, Score: 10},
		{Provider: &ScoreProvider{ID: 2, Priority: 1}, LoadInfo: &ProviderLoadInfo{}, Score: 9},
		{Provider: &ScoreProvider{ID: 3, Priority: 1}, LoadInfo: &ProviderLoadInfo{}, Score: 1},
	}

	var observedSticky, observedNonSticky bool
	for index := range 128 {
		order := BuildSelectionOrder(candidates, ScoreInput{
			SessionHash:      fmt.Sprintf("weighted-sticky-%d", index),
			StickyWeighted:   true,
			StickyProviderID: 2,
			TopK:             2,
		})

		require.Len(t, order, 2)
		require.ElementsMatch(t, []int64{1, 2}, []int64{order[0].Provider.ID, order[1].Provider.ID})
		observedSticky = observedSticky || order[0].Provider.ID == 2
		observedNonSticky = observedNonSticky || order[0].Provider.ID == 1
	}
	require.True(t, observedSticky, "粘性加分提供商仍应有机会被抽中")
	require.True(t, observedNonSticky, "粘性加权不能退化为强制置首")
}

// TestSelectTopKOpenAICandidates ranks precomputed scores with priority
// differences and a final-score tie. It checks the two selected IDs and the
// complete order when the requested sample covers the pool.
func TestSelectTopKOpenAICandidates(t *testing.T) {
	candidates := []CandidateScore{
		{
			Provider: &ScoreProvider{ID: 11, Priority: 2},
			LoadInfo: &ProviderLoadInfo{LoadRate: 10, WaitingCount: 1},
			Score:    10.0,
		},
		{
			Provider: &ScoreProvider{ID: 12, Priority: 1},
			LoadInfo: &ProviderLoadInfo{LoadRate: 20, WaitingCount: 1},
			Score:    9.5,
		},
		{
			Provider: &ScoreProvider{ID: 13, Priority: 1},
			LoadInfo: &ProviderLoadInfo{LoadRate: 30, WaitingCount: 0},
			Score:    10.0,
		},
		{
			Provider: &ScoreProvider{ID: 14, Priority: 0},
			LoadInfo: &ProviderLoadInfo{LoadRate: 40, WaitingCount: 0},
			Score:    8.0,
		},
	}

	top2 := SelectTopK(candidates, 2)
	require.Len(t, top2, 2)
	require.Equal(t, int64(13), top2[0].Provider.ID)
	require.Equal(t, int64(11), top2[1].Provider.ID)

	topAll := SelectTopK(candidates, 8)
	require.Len(t, topAll, len(candidates))
	require.Equal(t, int64(13), topAll[0].Provider.ID)
	require.Equal(t, int64(11), topAll[1].Provider.ID)
	require.Equal(t, int64(12), topAll[2].Provider.ID)
	require.Equal(t, int64(14), topAll[3].Provider.ID)
}

// TestBuildOpenAIWeightedSelectionOrder_DeterministicBySessionSeed samples the
// same scored pool twice with fixed session, group, and model context. It
// requires identical provider order, protecting repeatability for those
// supplied seed inputs.
func TestBuildOpenAIWeightedSelectionOrder_DeterministicBySessionSeed(t *testing.T) {
	candidates := []CandidateScore{
		{
			Provider: &ScoreProvider{ID: 101},
			LoadInfo: &ProviderLoadInfo{LoadRate: 10, WaitingCount: 0},
			Score:    4.2,
		},
		{
			Provider: &ScoreProvider{ID: 102},
			LoadInfo: &ProviderLoadInfo{LoadRate: 30, WaitingCount: 1},
			Score:    3.5,
		},
		{
			Provider: &ScoreProvider{ID: 103},
			LoadInfo: &ProviderLoadInfo{LoadRate: 50, WaitingCount: 2},
			Score:    2.1,
		},
	}
	req := ScoreInput{
		GroupID:        scoreGroupIDForTest(99),
		SessionHash:    "session_seed_fixed",
		RequestedModel: "gpt-5.1",
	}

	first := BuildWeightedSelectionOrder(candidates, req)
	second := BuildWeightedSelectionOrder(candidates, req)
	require.Len(t, first, len(candidates))
	require.Len(t, second, len(candidates))
	for i := range first {
		require.Equal(t, first[i].Provider.ID, second[i].Provider.ID)
	}
}

// TestDeriveOpenAISelectionSeed_NoAffinityAddsEntropy derives two model-only
// seeds separated by a brief clock delay. It checks nonzero and different
// outputs for those calls, protecting the ordinary no-affinity clock mix.
func TestDeriveOpenAISelectionSeed_NoAffinityAddsEntropy(t *testing.T) {
	req := ScoreInput{
		RequestedModel: "gpt-5.1",
	}
	seed1 := SelectionSeed(req)
	time.Sleep(1 * time.Millisecond)
	seed2 := SelectionSeed(req)
	require.NotZero(t, seed1)
	require.NotZero(t, seed2)
	require.NotEqual(t, seed1, seed2)
}

// TestBuildOpenAIWeightedSelectionOrder_HandlesInvalidScores samples a pool
// containing NaN, infinity, and a negative score. It checks complete length
// and distinct provider membership so repaired individual weights still
// produce every supplied record once.
func TestBuildOpenAIWeightedSelectionOrder_HandlesInvalidScores(t *testing.T) {
	candidates := []CandidateScore{
		{
			Provider: &ScoreProvider{ID: 901},
			LoadInfo: &ProviderLoadInfo{LoadRate: 5, WaitingCount: 0},
			Score:    math.NaN(),
		},
		{
			Provider: &ScoreProvider{ID: 902},
			LoadInfo: &ProviderLoadInfo{LoadRate: 5, WaitingCount: 0},
			Score:    math.Inf(1),
		},
		{
			Provider: &ScoreProvider{ID: 903},
			LoadInfo: &ProviderLoadInfo{LoadRate: 5, WaitingCount: 0},
			Score:    -1,
		},
	}
	req := ScoreInput{
		SessionHash: "seed_invalid_scores",
	}

	order := BuildWeightedSelectionOrder(candidates, req)
	require.Len(t, order, len(candidates))
	seen := map[int64]struct{}{}
	for _, item := range order {
		seen[item.Provider.ID] = struct{}{}
	}
	require.Len(t, seen, len(candidates))
}

// TestOpenAISelectionRNG_SeedZeroStillWorks constructs the generator with zero
// input seed and draws several values. It requires different integer outputs
// and fractional range checks, protecting the constructor's usable default
// state.
func TestOpenAISelectionRNG_SeedZeroStillWorks(t *testing.T) {
	rng := NewSelectionRNG(0)
	v1 := rng.NextUint64()
	v2 := rng.NextUint64()
	require.NotEqual(t, v1, v2)
	require.GreaterOrEqual(t, rng.NextFloat64(), 0.0)
	require.Less(t, rng.NextFloat64(), 1.0)
}

// BenchmarkOpenAIProviderSchedulerSelectTopK compares heap Top-K with a
// full-sort helper on fixed synthetic pool sizes. Its timed loops report
// allocations and require a nonempty result, providing a local algorithm
// workload rather than production latency evidence.
func BenchmarkOpenAIProviderSchedulerSelectTopK(b *testing.B) {
	cases := []struct {
		name string // benchmark pool scenario name
		size int    // synthetic candidate pool size
		topK int    // requested retained sample size
	}{
		{name: "n_16_k_3", size: 16, topK: 3},
		{name: "n_64_k_3", size: 64, topK: 3},
		{name: "n_256_k_5", size: 256, topK: 5},
	}

	for _, tc := range cases {
		candidates := buildOpenAISchedulerBenchmarkCandidates(tc.size)
		b.Run(tc.name+"/heap_topk", func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				result := SelectTopK(candidates, tc.topK)
				if len(result) == 0 {
					b.Fatal("unexpected empty result")
				}
			}
		})
		b.Run(tc.name+"/full_sort", func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				result := selectTopKOpenAICandidatesBySortBenchmark(candidates, tc.topK)
				if len(result) == 0 {
					b.Fatal("unexpected empty result")
				}
			}
		})
	}
}

// TestOpenAIProviderCandidateHeap_PushPopAndInvalidType adds and removes one
// CandidateScore through the heap adapter and then supplies the wrong dynamic
// type. It checks size, restored ID, and panic rejection of that invalid
// insertion.
func TestOpenAIProviderCandidateHeap_PushPopAndInvalidType(t *testing.T) {
	h := candidateHeap{}
	h.Push(CandidateScore{
		Provider: &ScoreProvider{ID: 7001},
		LoadInfo: &ProviderLoadInfo{LoadRate: 0, WaitingCount: 0},
		Score:    1.0,
	})
	require.Equal(t, 1, h.Len())
	popped, ok := h.Pop().(CandidateScore)
	require.True(t, ok)
	require.Equal(t, int64(7001), popped.Provider.ID)
	require.Equal(t, 0, h.Len())

	require.Panics(t, func() {
		h.Push("bad_element_type")
	})
}

// advancedSchedulerTestWeights returns equal basic factor weights for isolated
// scoring assertions.
func advancedSchedulerTestWeights() policy.ScoreWeights {
	return policy.ScoreWeights{
		Priority:  1,
		Load:      1,
		Queue:     1,
		ErrorRate: 1,
		TTFT:      1,
	}
}

// scoreGroupIDForTest returns a pointer to a separate copy of the supplied
// group ID.
func scoreGroupIDForTest(value int64) *int64 { return &value }

// buildOpenAISchedulerBenchmarkCandidates creates deterministic records for
// local ranking benchmarks. Nonpositive size returns nil, while positive size
// allocates provider and load records with varied priority and precomputed
// scores; its error-rate arithmetic performs integer division and therefore
// yields zero.
func buildOpenAISchedulerBenchmarkCandidates(size int) []CandidateScore {
	if size <= 0 {
		return nil
	}
	candidates := make([]CandidateScore, 0, size)
	// Cycle scalar observations to populate repeatable ranking workloads. These
	// fields are synthetic input; the error fraction remains zero because integer
	// division precedes its float conversion.
	for i := range size {
		providerID := int64(10_000 + i)
		candidates = append(candidates, CandidateScore{
			Provider: &ScoreProvider{
				ID:       providerID,
				Priority: i % 7,
			},
			LoadInfo: &ProviderLoadInfo{
				ProviderID:   providerID,
				LoadRate:     (i * 17) % 100,
				WaitingCount: (i * 11) % 13,
			},
			Score:     float64((i*29)%1000) / 100,
			ErrorRate: float64((i * 5) % 100 / 100),
			TTFT:      float64(30 + (i*3)%500),
			HasTTFT:   i%3 != 0,
		})
	}
	return candidates
}

// selectTopKOpenAICandidatesBySortBenchmark copies and fully sorts candidate
// values before truncating the sample. Empty input returns nil, nonpositive k
// selects one, and metadata/load pointers remain borrowed from the benchmark
// fixture.
func selectTopKOpenAICandidatesBySortBenchmark(candidates []CandidateScore, topK int) []CandidateScore {
	if len(candidates) == 0 {
		return nil
	}
	if topK <= 0 {
		topK = 1
	}
	ranked := append([]CandidateScore(nil), candidates...)
	sort.Slice(ranked, func(i, j int) bool {
		return CandidateBetter(ranked[i], ranked[j])
	})
	if topK > len(ranked) {
		topK = len(ranked)
	}
	// Truncation drops lower-ranked copies while retaining the caller's complete
	// candidate pool for later benchmark iterations.
	return ranked[:topK]
}

// TestAdvancedSchedulerRuntimeStatsUsesIndependentEWMAFactors reports error
// outcomes and positive latencies with separate coefficients. It checks the
// resulting error fraction and latency average, protecting independent error
// and TTFT blends.
func TestAdvancedSchedulerRuntimeStatsUsesIndependentEWMAFactors(t *testing.T) {
	stats := NewRuntimeStats(time.Now)
	stats.Report(31, false, nil, policy.FeedbackConfig{ErrorRateAlpha: 0.8, TtftAlpha: 0.4})
	feedback := stats.FeedbackSnapshot(31)
	require.InDelta(t, 0.8, feedback.ErrorRate, 0.000001)

	firstTTFT := 100
	stats.Report(31, true, &firstTTFT, policy.FeedbackConfig{ErrorRateAlpha: 0.5, TtftAlpha: 0.25})
	secondTTFT := 200
	stats.Report(31, true, &secondTTFT, policy.FeedbackConfig{ErrorRateAlpha: 0.5, TtftAlpha: 0.25})
	feedback = stats.FeedbackSnapshot(31)
	require.InDelta(t, 0.2, feedback.ErrorRate, 0.000001)
	require.InDelta(t, 125, feedback.TTFT, 0.000001)
}

// TestAdvancedSchedulerRuntimeFeedbackSnapshot_RecordsSamplesAndObservedTime
// reports one positive latency and another failed outcome. It checks sample
// counts, populated observation times, and expected feedback values for their
// different reporting histories.
func TestAdvancedSchedulerRuntimeFeedbackSnapshot_RecordsSamplesAndObservedTime(t *testing.T) {
	stats := NewRuntimeStats(time.Now)
	ttft := 240
	stats.Report(401, true, &ttft)
	stats.Report(401, false, nil)

	snapshot := stats.FeedbackSnapshot(401)
	require.True(t, snapshot.HasFeedback)
	require.EqualValues(t, 2, snapshot.ErrorSamples)
	require.NotNil(t, snapshot.LastObservedAt)
	require.True(t, snapshot.HasTTFT)
	require.EqualValues(t, 1, snapshot.TTFTSamples)
	require.NotNil(t, snapshot.LastTTFTAt)
	require.InDelta(t, 0.2, snapshot.ErrorRate, 0.000001)
	require.InDelta(t, 240, snapshot.TTFT, 0.000001)
}

// TestOpenAIProviderRuntimeStats_ReportAndSnapshot reports success followed by
// two latency-bearing failures for one provider. It checks the expected error
// and latency EWMAs, positive latency presence, and one retained provider
// statistic.
func TestOpenAIProviderRuntimeStats_ReportAndSnapshot(t *testing.T) {
	stats := NewRuntimeStats(time.Now)
	stats.Report(1001, true, nil)
	firstTTFT := 100
	stats.Report(1001, false, &firstTTFT)
	secondTTFT := 200
	stats.Report(1001, false, &secondTTFT)

	errorRate, ttft, hasTTFT := stats.Snapshot(1001)
	require.True(t, hasTTFT)
	require.InDelta(t, 0.36, errorRate, 1e-9)
	require.InDelta(t, 120.0, ttft, 1e-9)
	require.Equal(t, 1, stats.Size())
}

// TestOpenAIProviderRuntimeStats_ReportConcurrent dispatches concurrent
// reports across four provider IDs and joins all workers. It checks retained
// provider count, bounded error fractions, and positive observed latency,
// protecting concurrent update results without asserting a coherent snapshot
// tuple.
func TestOpenAIProviderRuntimeStats_ReportConcurrent(t *testing.T) {
	stats := NewRuntimeStats(time.Now)

	// Use several workers on the same small provider set so reports contend on
	// shared atomic records rather than creating an independent record per
	// worker.
	const (
		providerCount = 4
		workers       = 16
		iterations    = 800
	)
	var wg sync.WaitGroup
	wg.Add(workers)
	for worker := range workers {
		// Worker reports can compete on the same provider's atomic values. Join
		// every worker before inspecting the final samples so assertions observe
		// completed update work.
		go func() {
			defer wg.Done()
			for i := range iterations {
				providerID := int64(i%providerCount + 1)
				success := (i+worker)%3 != 0
				ttft := 80 + (i+worker)%40
				stats.Report(providerID, success, &ttft)
			}
		}()
	}
	wg.Wait()

	require.Equal(t, providerCount, stats.Size())
	for providerID := int64(1); providerID <= providerCount; providerID++ {
		errorRate, ttft, hasTTFT := stats.Snapshot(providerID)
		require.GreaterOrEqual(t, errorRate, 0.0)
		require.LessOrEqual(t, errorRate, 1.0)
		require.True(t, hasTTFT)
		require.Greater(t, ttft, 0.0)
	}
}

// TestClamp01_AllBranches supplies finite values below zero, inside the unit
// interval, and above one. It checks both endpoint clamps and the retained
// middle value for those inputs.
func TestClamp01_AllBranches(t *testing.T) {
	require.Equal(t, 0.0, Clamp01(-0.2))
	require.Equal(t, 1.0, Clamp01(1.3))
	require.Equal(t, 0.5, Clamp01(0.5))
}

// TestCalcLoadSkewByMoments_Branches supplies one-sample, deliberately
// negative-variance, and ordinary moments. It checks zero recovery for the
// first cases and a nonnegative result for the multi-sample calculation.
func TestCalcLoadSkewByMoments_Branches(t *testing.T) {
	require.Equal(t, 0.0, LoadSkewByMoments(1, 1, 1))
	// A negative sumSquares/count - mean^2 is clamped to zero before taking the square root.
	require.Equal(t, 0.0, LoadSkewByMoments(1, 0, 2))
	require.GreaterOrEqual(t, LoadSkewByMoments(6, 20, 3), 0.0)
}
