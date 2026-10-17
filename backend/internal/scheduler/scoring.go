package scheduler

import (
	"container/heap"
	"hash/fnv"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// scoring.go records provider feedback and ranks eligible candidates for
// weighted Top-K selection.
// Advanced selection ranks eligible metadata using priority, load, queue
// pressure, provider feedback, reset windows, and optional quota observations.
// Missing load and optional signals use neutral factors, while an unobserved
// error rate starts at zero and receives full health preference. The same
// factor calculations and normalization ranges support administrative
// explanations.
//
// Provider feedback is shared across groups, with each report carrying the
// request's effective error and TTFT blending coefficients. Individual atomic
// updates allow concurrent reporting, but a snapshot can combine observations
// from different instants. Ranked Top-K candidates are sampled without
// replacement using shifted positive weights and a request-derived generator
// seed. Metadata and load pointers remain borrowed; ranking copies slice
// entries so selection order can change independently of the input slice.

// Default blending coefficients govern error and TTFT feedback when a report
// supplies no usable override.
const (
	// DefaultErrorRateAlpha supplies the ordinary error-sample blending
	// coefficient.
	DefaultErrorRateAlpha = 0.2
	// DefaultTTFTAlpha supplies the ordinary nonnegative-TTFT sample blending
	// coefficient.
	DefaultTTFTAlpha = 0.2
)

// RuntimeStats retains provider feedback shared by advanced groups and
// independently counted switch observations. Atomic fields permit concurrent
// reports; consumers receive independent value samples rather than a
// transactionally consistent set of metrics.
type RuntimeStats struct {
	// now is called directly when Report records observation times. Callers
	// supply a clock safe for concurrent use because provider reports can
	// sample this shared callback simultaneously.
	now func() time.Time // optional feedback observation clock
	// providers retains shared statistic pointers by provider ID. loadOrCreate
	// initializes their baseline before publication, and later reports change
	// each record's atomic measurements independently.
	providers     sync.Map     // provider-indexed atomic feedback records
	providerCount atomic.Int64 // published provider statistic count
	switchCount   atomic.Int64 // reported provider switch count
}

// FeedbackConfig selects independent error and TTFT blending coefficients for
// provider outcome reports.
type FeedbackConfig = policy.FeedbackConfig

// advancedProviderRuntimeStat stores one provider's atomic EWMA values and
// independent observation metadata.
type advancedProviderRuntimeStat struct {
	// errorRateEWMABits starts at zero and ttftEWMABits starts with NaN until
	// a nonnegative latency arrives. errorSamples, ttftSamples,
	// lastObservedUnixNano, and lastTTFTObservedNanos are updated independently
	// after their corresponding value updates.
	errorRateEWMABits     atomic.Uint64 // float64 error fraction encoding
	ttftEWMABits          atomic.Uint64 // float64 TTFT millisecond encoding
	errorSamples          atomic.Int64  // recorded outcome sample count
	ttftSamples           atomic.Int64  // nonnegative TTFT sample count
	lastObservedUnixNano  atomic.Int64  // Unix nanoseconds
	lastTTFTObservedNanos atomic.Int64  // Unix nanoseconds
}

// FeedbackSnapshot contains sampled provider feedback and separately read
// observation counts and timestamps.
type FeedbackSnapshot struct {
	// HasFeedback reports that a statistic record was found. During concurrent
	// first publication it can be true before the first sample count or
	// timestamp becomes visible.
	HasFeedback  bool    // stored feedback record presence
	ErrorRate    float64 // sampled error fraction value
	ErrorSamples int64   // sampled outcome observation count
	// TTFT, HasTTFT, and TTFTSamples distinguish a recorded latency value from
	// missing latency. LastObservedAt and LastTTFTAt are separate UTC copies
	// when their recorded Unix-nanosecond values are positive.
	TTFT           float64    // milliseconds
	HasTTFT        bool       // populated TTFT observation flag
	TTFTSamples    int64      // sampled nonnegative latency count
	LastObservedAt *time.Time // optional sampled outcome timestamp
	LastTTFTAt     *time.Time // optional sampled latency timestamp
}

// CandidateScore associates eligible metadata and borrowed load observations
// with computed ranking and feedback values.
type CandidateScore struct {
	// Provider remains borrowed from the eligible input pool. Ranking and
	// sampling reorder candidate values while callers keep this metadata
	// stable during scoring, ranking, and sampling.
	Provider *ScoreProvider // borrowed eligible provider metadata
	// LoadInfo retains a supplied observation or a new record containing only
	// ProviderID when data is missing. LoadKnown determines whether its fields
	// contribute measured load and queue factors.
	LoadInfo  *ProviderLoadInfo // borrowed or neutral load data
	LoadKnown bool              // confirmed load observation presence
	// Score equals BaseScore plus StickyBonus. StickyBonus combines
	// PreviousBonus and SessionStickyBonus when affinity weighting is enabled;
	// each bonus starts at zero when its provider ID does not match.
	Score              float64          // base score plus affinity
	BaseScore          float64          // weighted normalized factor sum
	StickyBonus        float64          // combined enabled affinity bonuses
	PreviousBonus      float64          // previous-response affinity contribution
	SessionStickyBonus float64          // session affinity contribution value
	Priority           int              // numeric provider priority observation
	ErrorRate          float64          // provider error fraction observation
	TTFT               float64          // milliseconds
	HasTTFT            bool             // populated latency observation flag
	HasFeedback        bool             // stored feedback record presence
	Feedback           FeedbackSnapshot // independently sampled feedback details
	Factors            CandidateFactors // computed normalized scoring preferences
}

// CandidateFactors records individual preference factors used for scoring and
// administrative explanations.
type CandidateFactors struct {
	Priority      float64 // normalized numeric priority preference
	Load          float64 // normalized provider load preference
	Queue         float64 // normalized waiting count preference
	ErrorRate     float64 // normalized error health preference
	TTFT          float64 // normalized observed latency preference
	Reset         float64 // normalized active reset preference
	QuotaHeadroom float64 // normalized quota headroom preference
}

// ScoreRanges records the eligible pool extrema used to normalize comparable
// scheduling observations.
type ScoreRanges struct {
	// MinPriority and MaxPriority span every eligible candidate's priority.
	// Equal endpoints give full priority preference; a span representable as
	// int favors smaller priorities when ScoreCandidatesWithRanges normalizes
	// the differences.
	MinPriority int // smallest numeric provider priority
	MaxPriority int // largest numeric provider priority
	MaxWaiting  int // queue denominator lower-bounded by one
	// MinTTFT, MaxTTFT, and HasTTFTSample describe positive latency samples
	// together. Missing or equal ranges leave the latency factor neutral,
	// including candidates without a latency sample.
	MinTTFT       float64 // milliseconds
	MaxTTFT       float64 // milliseconds
	HasTTFTSample bool    // positive latency range presence
	// MinResetRemaining, MaxResetRemaining, and HasResetSample describe future
	// window ends when reset weight is positive. An equal observed range gives
	// active-window candidates full reset preference; missing windows remain
	// neutral.
	MinResetRemaining float64 // seconds
	MaxResetRemaining float64 // seconds
	HasResetSample    bool    // future reset range presence
}

// candidateHeap retains Top-K peers with the least-preferred candidate at its
// root.
type candidateHeap []CandidateScore

// ScoreInput supplies request affinity, seed context, and optional platform
// quota observations.
type ScoreInput struct {
	// Now supplies entropy for selection seeds when affinity is absent or the
	// hash mix is zero. Nil selects time.Now; scoring's observation instant is
	// passed separately.
	Now func() time.Time // optional selection seed clock
	// GroupID, SessionHash, PreviousResponseID, and RequestedModel supply the
	// sampling seed context. SelectionSeed trims text and skips empty values,
	// while a nil GroupID contributes no decimal group text.
	GroupID            *int64 // optional seed namespace identifier
	SessionHash        string // session affinity seed text
	PreviousResponseID string // previous-response seed text
	RequestedModel     string // requested model seed text
	// StickyProviderID and StickyPreviousProviderID contribute bonuses when
	// StickyWeighted is enabled and the IDs are positive. The same matched
	// provider can receive both affinity contributions in its final score.
	StickyProviderID         int64 // session-affinity provider identifier
	StickyPreviousProviderID int64 // previous-response affinity provider identifier
	StickyWeighted           bool  // enable provider affinity bonuses
	// TopK limits the ranked sample in BuildSelectionOrder. Nonpositive values
	// select one, while values above the pool size retain every candidate.
	TopK int // requested candidate sample size
	// QuotaHeadroomFactor supplies platform quota preference. The scorer calls
	// it synchronously for each candidate when quota weight is positive, using
	// borrowed metadata and the supplied observation time. Nil uses one-half;
	// callers return finite values because NaN passes through Clamp01.
	// optional platform quota factor
	QuotaHeadroomFactor func(*ScoreProvider, time.Time) float64
}

// SelectionRNG stores a per-selection xorshift generator whose calls mutate
// its state.
type SelectionRNG struct {
	// state is initialized by NewSelectionRNG with a nonzero seed. Callers
	// serialize access to one generator; a zero-value generator's
	// uninitialized state produces zeros.
	state uint64 // current pseudorandom generator state
}

// ScoreSnapshot contains baseline and hypothetical affinity display values for
// administrative provider lists.
type ScoreSnapshot struct {
	BaseScore   float64 // ordinary candidate score value
	StickyScore float64 // hypothetical affinity score value
	// StickyScoreInfinity marks hard affinity when weighted stickiness is
	// disabled. ProvidersView.vue displays positive infinity from this flag
	// while StickyScore retains its zero value.
	StickyScoreInfinity   bool // hard-affinity display infinity flag
	StickyWeightedEnabled bool // effective weighted affinity switch
}

// ScoreProvider contains credential-free provider metadata borrowed for
// ranking and quota-factor callbacks.
type ScoreProvider struct {
	Name string // configured provider display name
	// ProjectionID associates metadata with execution records for
	// PlatformSelector and DiagnosticService quota callbacks. Those adapters
	// assign it within one invocation; pointer-keyed callbacks can leave it
	// zero.
	ProjectionID uint64 // request execution-record association
	ID           int64  // persistent provider record identifier
	Platform     string // upstream provider platform identifier
	Priority     int    // global numeric provider priority
	// SessionWindowEnd is a borrowed time pointer consulted when reset scoring
	// is enabled. A missing or expired value leaves that provider's reset
	// factor neutral.
	SessionWindowEnd *time.Time // optional active reset observation
}

// ScoreGroup represents the currently unused group argument accepted by score
// snapshot construction.
type ScoreGroup struct{}

// NormalizeFeedbackConfig returns usable independent EWMA coefficients through
// the shared policy rules. Invalid or absent error and TTFT coefficients
// select their defaults separately so one valid factor can remain effective.
func NormalizeFeedbackConfig(value FeedbackConfig) FeedbackConfig {
	return policy.NormalizeFeedback(value)
}

// NewRuntimeStats constructs empty provider feedback using an optional
// observation clock. Nil clock uses time.Now, and the returned instance is
// shared by the advanced selectors whose reports update each provider's
// observations.
func NewRuntimeStats(now func() time.Time) *RuntimeStats {
	return &RuntimeStats{now: now}
}

// ReportSwitch increments this runtime's provider-switch observation when the
// receiver exists. A nil receiver skips the counter, allowing the owning
// selector to invoke optional runtime feedback consistently alongside its
// separate selection metrics.
func (s *RuntimeStats) ReportSwitch() {
	if s != nil {
		s.switchCount.Add(1)
	}
}

// loadOrCreate returns a shared provider statistic or publishes its
// initialized baseline. Concurrent callers use LoadOrStore so one published
// pointer determines subsequent reports and the provider count advances for
// that publication.
//
// Error rate begins at zero while latency begins at a NaN sentinel, allowing
// the first positive latency to install its value directly. If an existing
// entry has an unexpected type, the method returns its fresh local statistic
// without replacing that entry or counting another provider. Normal reports
// supply positive IDs before reaching this helper.
func (s *RuntimeStats) loadOrCreate(providerID int64) *advancedProviderRuntimeStat {
	if value, ok := s.providers.Load(providerID); ok {
		stat, _ := value.(*advancedProviderRuntimeStat)
		if stat != nil {
			return stat
		}
	}

	stat := &advancedProviderRuntimeStat{}
	// A provider with no errors starts fully healthy, while latency has no
	// sample yet. Initialize both baselines before LoadOrStore exposes this
	// record to competing reports.
	stat.errorRateEWMABits.Store(math.Float64bits(0))
	stat.ttftEWMABits.Store(math.Float64bits(math.NaN()))
	actual, loaded := s.providers.LoadOrStore(providerID, stat)
	if !loaded {
		s.providerCount.Add(1)
		return stat
	}
	existing, _ := actual.(*advancedProviderRuntimeStat)
	if existing != nil {
		return existing
	}
	// An unexpected stored type supplies no usable shared record. Return the
	// initialized local statistic here while retaining the existing map entry
	// and its publication count.
	return stat
}

// updateAdvancedSchedulerEWMA blends one sample into an atomically encoded
// float value. Callers provide a nonnil initialized target, finite sample, and
// usable blending coefficient; the calculation keeps the old value's remaining
// contribution beside the new sample.
//
// A concurrent report can publish between reading the old bits and attempting
// replacement. CompareAndSwap determines which calculated update wins, and a
// losing caller reads the new current bits before recalculating its own
// sample. This retry applies both reports through successive blends, while
// sample counts and timestamps remain separate work in Report.
func updateAdvancedSchedulerEWMA(target *atomic.Uint64, sample float64, alpha float64) {
	// A competing report can replace the observed value between read and
	// update. Retry the blend from its newly published bits so this sample
	// does not overwrite another update.
	for {
		oldBits := target.Load()
		oldValue := math.Float64frombits(oldBits)
		newValue := alpha*sample + (1-alpha)*oldValue
		if target.CompareAndSwap(oldBits, math.Float64bits(newValue)) {
			return
		}
	}
}

// Report records one provider outcome and an optional nonnegative TTFT
// latency. Nil receiver or nonpositive provider ID skips the report, and the
// first optional feedback configuration supplies independently normalized
// blending factors; absent configuration uses the defaults.
//
// Error updates blend from the zero baseline and count every reported outcome.
// Latency uses a NaN sentinel so its first recorded sample is installed
// directly before later samples are blended. Each successful value update
// precedes its count and timestamp writes, allowing concurrent snapshots to
// observe intermediate combinations. Report applies the request's chosen
// coefficients to shared provider state at this observation point.
func (s *RuntimeStats) Report(providerID int64, success bool, ttftMs *int, feedback ...FeedbackConfig) {
	if s == nil || providerID <= 0 {
		return
	}
	feedbackConfig := FeedbackConfig{
		ErrorRateAlpha: DefaultErrorRateAlpha,
		TtftAlpha:      DefaultTTFTAlpha,
	}
	if len(feedback) > 0 {
		feedbackConfig = NormalizeFeedbackConfig(feedback[0])
	}
	stat := s.loadOrCreate(providerID)

	errorSample := 1.0
	if success {
		errorSample = 0.0
	}
	// Outcome metadata follows its value update, and the timestamp is stored
	// as Unix nanoseconds for later UTC reconstruction. Separate atomic writes
	// let snapshots see the new value before this count or timestamp.
	updateAdvancedSchedulerEWMA(&stat.errorRateEWMABits, errorSample, feedbackConfig.ErrorRateAlpha)
	stat.errorSamples.Add(1)
	stat.lastObservedUnixNano.Store(s.nowTime().UnixNano())

	if ttftMs != nil && *ttftMs >= 0 {
		ttft := float64(*ttftMs)
		ttftBits := math.Float64bits(ttft)
		// Install the first recorded latency directly because averaging from
		// NaN would retain an unusable value. Later competing samples blend
		// from the successful current value.
		for {
			oldBits := stat.ttftEWMABits.Load()
			oldValue := math.Float64frombits(oldBits)
			if math.IsNaN(oldValue) {
				if stat.ttftEWMABits.CompareAndSwap(oldBits, ttftBits) {
					stat.ttftSamples.Add(1)
					stat.lastTTFTObservedNanos.Store(s.nowTime().UnixNano())
					break
				}
				continue
			}
			newValue := feedbackConfig.TtftAlpha*ttft + (1-feedbackConfig.TtftAlpha)*oldValue
			if stat.ttftEWMABits.CompareAndSwap(oldBits, math.Float64bits(newValue)) {
				stat.ttftSamples.Add(1)
				stat.lastTTFTObservedNanos.Store(s.nowTime().UnixNano())
				break
			}
		}
	}
}

// FeedbackSnapshot samples a provider's shared EWMA values, counts, and UTC
// observation times. Missing receiver, invalid ID, or absent statistic returns
// an empty value; time pointers are new copies and recorded latency is
// distinguished from its uninitialized NaN sentinel. Individual fields are
// read independently during concurrent reports.
func (s *RuntimeStats) FeedbackSnapshot(providerID int64) FeedbackSnapshot {
	if s == nil || providerID <= 0 {
		return FeedbackSnapshot{}
	}
	value, ok := s.providers.Load(providerID)
	if !ok {
		return FeedbackSnapshot{}
	}
	stat, _ := value.(*advancedProviderRuntimeStat)
	if stat == nil {
		return FeedbackSnapshot{}
	}

	Snapshot := FeedbackSnapshot{
		HasFeedback:  true,
		ErrorRate:    Clamp01(math.Float64frombits(stat.errorRateEWMABits.Load())),
		ErrorSamples: stat.errorSamples.Load(),
	}
	if observedAt := stat.lastObservedUnixNano.Load(); observedAt > 0 {
		value := time.Unix(0, observedAt).UTC()
		Snapshot.LastObservedAt = &value
	}
	if ttftValue := math.Float64frombits(stat.ttftEWMABits.Load()); !math.IsNaN(ttftValue) {
		Snapshot.TTFT = ttftValue
		Snapshot.HasTTFT = true
		Snapshot.TTFTSamples = stat.ttftSamples.Load()
		if observedAt := stat.lastTTFTObservedNanos.Load(); observedAt > 0 {
			value := time.Unix(0, observedAt).UTC()
			Snapshot.LastTTFTAt = &value
		}
	}
	return Snapshot
}

// Snapshot returns sampled error fraction, latency, and latency presence for
// one provider. Missing state produces zero values with absent latency; the
// two atomic value reads can reflect different reporting instants during
// concurrent updates.
func (s *RuntimeStats) Snapshot(providerID int64) (errorRate float64, ttft float64, hasTTFT bool) {
	if s == nil || providerID <= 0 {
		return 0, 0, false
	}
	value, ok := s.providers.Load(providerID)
	if !ok {
		return 0, 0, false
	}
	stat, _ := value.(*advancedProviderRuntimeStat)
	if stat == nil {
		return 0, 0, false
	}
	errorRate = Clamp01(math.Float64frombits(stat.errorRateEWMABits.Load()))
	ttftValue := math.Float64frombits(stat.ttftEWMABits.Load())
	if math.IsNaN(ttftValue) {
		return errorRate, 0, false
	}
	return errorRate, ttftValue, true
}

// Size reports how many provider statistic records this runtime has published.
// A nil receiver returns zero, and this independent atomic count can become
// visible separately from a record's first reported feedback values.
func (s *RuntimeStats) Size() int {
	if s == nil {
		return 0
	}
	return int(s.providerCount.Load())
}

// Len returns the number of candidates currently retained by the Top-K heap.
func (h candidateHeap) Len() int {
	return len(h)
}

// Less orders heap entries with CandidateBetter reversed so the
// least-preferred record stays at the root.
func (h candidateHeap) Less(i, j int) bool {
	return CandidateBetter(h[j], h[i])
}

// Swap exchanges retained candidate values during container/heap maintenance.
// It changes the heap slice in place, so callers supply indices inside the
// current storage while referenced provider and observation objects retain
// their ownership.
func (h candidateHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

// Push appends a CandidateScore supplied by container/heap to the retained
// slice. Other dynamic types panic, requiring heap callers to keep every
// inserted value in that declared element form.
func (h *candidateHeap) Push(x any) {
	candidate, ok := x.(CandidateScore)
	if !ok {
		panic("candidateHeap: invalid element type")
	}
	*h = append(*h, candidate)
}

// Pop removes and returns the final retained candidate for container/heap.
// Callers use it with a nonempty heap after the heap package has arranged the
// removable element's position.
func (h *candidateHeap) Pop() any {
	old := *h
	n := len(old)
	last := old[n-1]
	*h = old[:n-1]
	return last
}

// CandidateBetter compares final score, then numeric provider priority, then
// provider ID. A missing left provider loses and a missing right provider
// permits a nonnil left winner. Callers supply finite scores so comparisons
// establish the ordering used by ranking.
//
// Load and queue information already contributes to final score, so equal
// scores use one shared deterministic priority-and-ID order. This tie rule
// keeps Top-K independent of the candidate input order when provider
// identities are distinct. Comparisons retain metadata pointers and leave
// score values untouched for later weighted sampling.
func CandidateBetter(left, right CandidateScore) bool {
	if left.Provider == nil {
		return false
	}
	if right.Provider == nil {
		return true
	}
	if left.Score != right.Score {
		return left.Score > right.Score
	}
	if left.Provider.Priority != right.Provider.Priority {
		return left.Provider.Priority < right.Provider.Priority
	}
	return left.Provider.ID < right.Provider.ID
}

// SelectTopK returns the preferred ranked candidates in new slice storage
// while borrowing their referenced metadata. Empty input returns nil and
// nonpositive k selects one; a k covering the pool copies and ranks every
// input candidate.
//
// Smaller samples use a heap whose root is the worst retained peer, letting a
// better candidate replace that peer without sorting the complete pool.
// CandidateBetter supplies the same score, priority, and ID ordering for
// retention and the final ranked result. Input entries remain untouched, while
// provider, load, and feedback-time pointers keep their existing ownership.
func SelectTopK(candidates []CandidateScore, topK int) []CandidateScore {
	if len(candidates) == 0 {
		return nil
	}
	if topK <= 0 {
		topK = 1
	}
	if topK >= len(candidates) {
		ranked := append([]CandidateScore(nil), candidates...)
		SortCandidates(ranked)
		return ranked
	}

	best := make(candidateHeap, 0, topK)
	for _, candidate := range candidates {
		if len(best) < topK {
			heap.Push(&best, candidate)
			continue
		}
		if CandidateBetter(candidate, best[0]) {
			best[0] = candidate
			heap.Fix(&best, 0)
		}
	}

	ranked := make([]CandidateScore, len(best))
	copy(ranked, best)
	SortCandidates(ranked)
	return ranked
}

// SortCandidates orders candidate values in place using CandidateBetter's
// score, priority, and ID comparisons. Equal comparisons retain their current
// order, and referenced provider, load, and feedback-time values remain
// borrowed.
func SortCandidates(candidates []CandidateScore) {
	for i := 1; i < len(candidates); i++ {
		for j := i; j > 0 && CandidateBetter(candidates[j], candidates[j-1]); j-- {
			candidates[j], candidates[j-1] = candidates[j-1], candidates[j]
		}
	}
}

// ScoreCandidates returns candidate values and known-load population deviation
// in percentage points. Supply valid weights, finite quota observations, and
// priorities whose span fits int; keep provider and load records stable during
// scoring. Returned candidates borrow those records, and unknown loads are
// omitted from the deviation calculation.
// @project-doc docs/architecture/provider_scheduling_and_cache.md#advanced_scheduler_selection
func ScoreCandidates(
	providers []*ScoreProvider,
	loadMap map[int64]*ProviderLoadInfo,
	stats *RuntimeStats,
	weights policy.ScoreWeights,
	input ScoreInput,
	now time.Time,
) ([]CandidateScore, float64) {
	candidates, skew, _ := ScoreCandidatesWithRanges(providers, loadMap, stats, weights, input, now)
	return candidates, skew
}

// ScoreCandidatesWithRanges computes factors and score components for eligible
// nonnil provider metadata, returning pool normalization ranges and known-load
// population deviation in percentage points. Callers supply valid weights and
// finite quota observations, and keep providers, loadMap, and their referenced
// records stable during scoring. Priority differences are calculated as int
// before conversion, so callers use a priority span representable in int.
// Returned candidate values borrow the existing provider and load records.
//
// Missing load, latency, active reset, or quota data uses a neutral one-half
// factor, while an unobserved error rate supplies full health preference.
// Equal priorities use full preference; equal positive latency observations
// remain neutral, and equal active reset windows use full reset preference.
// Enabled matching affinities add their separate bonuses after the base factor
// sum, making those additions visible in diagnostics before Top-K sampling.
func ScoreCandidatesWithRanges(
	providers []*ScoreProvider,
	loadMap map[int64]*ProviderLoadInfo,
	stats *RuntimeStats,
	weights policy.ScoreWeights,
	input ScoreInput,
	now time.Time,
) ([]CandidateScore, float64, ScoreRanges) {
	candidates := make([]CandidateScore, 0, len(providers))
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		loadInfo, loadKnown := loadMap[provider.ID]
		// An absent or nil load entry supplies no measured preference. Retain
		// the provider identity in a neutral record and let LoadKnown
		// distinguish it from an observed zero load.
		if !loadKnown || loadInfo == nil {
			loadInfo = &ProviderLoadInfo{ProviderID: provider.ID}
			loadKnown = false
		}
		feedback := FeedbackSnapshot{}
		if stats != nil {
			feedback = stats.FeedbackSnapshot(provider.ID)
		}
		candidates = append(candidates, CandidateScore{
			Provider:    provider,
			LoadInfo:    loadInfo,
			LoadKnown:   loadKnown,
			Priority:    provider.Priority,
			ErrorRate:   feedback.ErrorRate,
			TTFT:        feedback.TTFT,
			HasTTFT:     feedback.HasTTFT,
			HasFeedback: feedback.HasFeedback,
			Feedback:    feedback,
		})
	}
	if len(candidates) == 0 {
		return nil, 0, ScoreRanges{}
	}

	minPriority, maxPriority := candidates[0].Priority, candidates[0].Priority
	// Keep queue normalization defined when all observed waiting counts are
	// zero.
	maxWaiting := 1
	loadRateSum := 0.0
	loadRateSumSquares := 0.0
	knownLoadCount := 0
	minTTFT, maxTTFT := 0.0, 0.0
	hasTTFTSample := false
	for i := range candidates {
		candidate := &candidates[i]
		if candidate.Priority < minPriority {
			minPriority = candidate.Priority
		}
		if candidate.Priority > maxPriority {
			maxPriority = candidate.Priority
		}
		if candidate.LoadKnown && candidate.LoadInfo.WaitingCount > maxWaiting {
			maxWaiting = candidate.LoadInfo.WaitingCount
		}
		if candidate.HasTTFT && candidate.TTFT > 0 {
			if !hasTTFTSample {
				minTTFT, maxTTFT, hasTTFTSample = candidate.TTFT, candidate.TTFT, true
			} else {
				if candidate.TTFT < minTTFT {
					minTTFT = candidate.TTFT
				}
				if candidate.TTFT > maxTTFT {
					maxTTFT = candidate.TTFT
				}
			}
		}
		if candidate.LoadKnown {
			loadRate := float64(candidate.LoadInfo.LoadRate)
			loadRateSum += loadRate
			loadRateSumSquares += loadRate * loadRate
			knownLoadCount++
		}
	}

	minResetRemaining, maxResetRemaining := 0.0, 0.0
	hasResetSample := false
	if weights.Reset > 0 {
		for _, candidate := range candidates {
			end := candidate.Provider.SessionWindowEnd
			if end == nil || !now.Before(*end) {
				continue
			}
			// Express active-window remaining time in seconds for the
			// normalization range.
			remaining := end.Sub(now).Seconds()
			if !hasResetSample {
				minResetRemaining, maxResetRemaining, hasResetSample = remaining, remaining, true
				continue
			}
			if remaining < minResetRemaining {
				minResetRemaining = remaining
			}
			if remaining > maxResetRemaining {
				maxResetRemaining = remaining
			}
		}
	}

	ranges := ScoreRanges{
		MinPriority:       minPriority,
		MaxPriority:       maxPriority,
		MaxWaiting:        maxWaiting,
		MinTTFT:           minTTFT,
		MaxTTFT:           maxTTFT,
		HasTTFTSample:     hasTTFTSample,
		MinResetRemaining: minResetRemaining,
		MaxResetRemaining: maxResetRemaining,
		HasResetSample:    hasResetSample,
	}

	// Quota is an optional adapter observation. Keep candidates available with
	// a neutral factor when no callback can provide platform headroom for this
	// pool.
	quotaFactor := input.QuotaHeadroomFactor
	if quotaFactor == nil {
		quotaFactor = func(*ScoreProvider, time.Time) float64 { return 0.5 }
	}
	for i := range candidates {
		item := &candidates[i]
		priorityFactor := 1.0
		if maxPriority > minPriority {
			priorityFactor = 1 - float64(item.Priority-minPriority)/float64(maxPriority-minPriority)
		}
		// Missing load observations supply no measured capacity or queue
		// preference. Keep both factors neutral until LoadKnown identifies
		// data that can distinguish this provider.
		loadFactor := 0.5
		queueFactor := 0.5
		if item.LoadKnown {
			// Convert load percentages to fractions before applying the
			// saturation clamp.
			loadFactor = 1 - Clamp01(float64(item.LoadInfo.LoadRate)/100.0)
			queueFactor = 1 - Clamp01(float64(item.LoadInfo.WaitingCount)/float64(maxWaiting))
		}
		// Missing error feedback uses the zero-error baseline so an unobserved
		// provider does not receive a health penalty merely because it has
		// served no reported request.
		errorFactor := 1.0
		if item.HasFeedback {
			errorFactor = 1 - Clamp01(item.ErrorRate)
		}
		// Latency can distinguish candidates only with an observed nonempty
		// range. Missing or equal samples retain the neutral factor instead of
		// inventing a latency ranking.
		ttftFactor := 0.5
		if item.HasTTFT && hasTTFTSample && maxTTFT > minTTFT {
			ttftFactor = 1 - Clamp01((item.TTFT-minTTFT)/(maxTTFT-minTTFT))
		}
		// Only future windows participate in reset preference. Missing windows
		// remain neutral, while equal active ends receive the full reset
		// factor when this signal is enabled.
		resetFactor := 0.5
		if weights.Reset > 0 && hasResetSample {
			if end := item.Provider.SessionWindowEnd; end != nil && now.Before(*end) {
				if maxResetRemaining > minResetRemaining {
					// Compare remaining seconds with the same active-window
					// pool range.
					resetFactor = 1 - Clamp01((end.Sub(now).Seconds()-minResetRemaining)/(maxResetRemaining-minResetRemaining))
				} else {
					resetFactor = 1
				}
			}
		}
		quotaHeadroomFactor := 0.5
		if weights.QuotaHeadroom > 0 {
			quotaHeadroomFactor = Clamp01(quotaFactor(item.Provider, now))
		}
		item.Factors = CandidateFactors{
			Priority:      priorityFactor,
			Load:          loadFactor,
			Queue:         queueFactor,
			ErrorRate:     errorFactor,
			TTFT:          ttftFactor,
			Reset:         resetFactor,
			QuotaHeadroom: quotaHeadroomFactor,
		}
		item.BaseScore = weights.Priority*priorityFactor +
			weights.Load*loadFactor +
			weights.Queue*queueFactor +
			weights.ErrorRate*errorFactor +
			weights.TTFT*ttftFactor +
			weights.Reset*resetFactor +
			weights.QuotaHeadroom*quotaHeadroomFactor
		item.Score = item.BaseScore
		if input.StickyWeighted {
			if input.StickyPreviousProviderID > 0 && item.Provider.ID == input.StickyPreviousProviderID {
				item.PreviousBonus = weights.Previous
				item.StickyBonus += item.PreviousBonus
			}
			if input.StickyProviderID > 0 && item.Provider.ID == input.StickyProviderID {
				item.SessionStickyBonus = weights.SessionSticky
				item.StickyBonus += item.SessionStickyBonus
			}
		}
		item.Score += item.StickyBonus
	}

	return candidates, LoadSkewByMoments(loadRateSum, loadRateSumSquares, knownLoadCount), ranges
}

// NewSelectionRNG initializes one mutable selection generator from the
// supplied seed. A zero seed chooses the fixed nonzero default so the
// recurrence can advance; callers serialize use of the returned generator.
func NewSelectionRNG(seed uint64) SelectionRNG {
	if seed == 0 {
		seed = 0x9e3779b97f4a7c15
	}
	return SelectionRNG{state: seed}
}

// NextUint64 advances the generator's xorshift state and returns its scrambled
// output. Callers initialize the state through NewSelectionRNG and serialize
// calls that share this mutable generator.
func (r *SelectionRNG) NextUint64() uint64 {
	// Apply the xorshift64* transition before scrambling its returned output.
	x := r.state
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	r.state = x
	return x * 2685821657736338717
}

// NextFloat64 advances the generator and returns a fractional value from zero
// inclusive to one exclusive. It retains the upper fifty-three output bits,
// and callers follow the generator's initialization and serialization rules.
func (r *SelectionRNG) NextFloat64() float64 {
	// Retain fifty-three bits for an exactly representable float64 fraction.
	return float64(r.NextUint64()>>11) / (1 << 53)
}

// SelectionSeed mixes trimmed nonempty affinity and model text with optional
// group context for sampling. A nonzero hash with session or previous-response
// affinity is repeatable for identical supplied inputs. Absent affinity mixes
// the selected clock so ordinary requests can produce different orders.
//
// Each retained text value ends with a zero byte to distinguish concatenated
// value fragments, while empty fields contribute no bytes. The optional group
// ID is appended in decimal form. A zero mixed hash receives another
// clock-and-constant mix; NewSelectionRNG still handles any zero seed supplied
// to it. The returned seed controls pseudorandom ordering for this sampling
// invocation.
func SelectionSeed(input ScoreInput) uint64 {
	now := input.Now
	if now == nil {
		now = time.Now
	}
	hasher := fnv.New64a()
	writeValue := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		// End every retained text value with a separator so differently split
		// fragments such as ab/c and a/bc contribute different byte sequences.
		// FNV Write returns a nil error, so both writes can discard their
		// results while adding the complete text and delimiter to the seed.
		_, _ = hasher.Write([]byte(trimmed))
		_, _ = hasher.Write([]byte{0})
	}
	writeValue(input.SessionHash)
	writeValue(input.PreviousResponseID)
	writeValue(input.RequestedModel)
	if input.GroupID != nil {
		// Add the group namespace after text fields so it influences sampling
		// within that group. FNV Write returns nil error for this decimal text.
		_, _ = hasher.Write([]byte(strconv.FormatInt(*input.GroupID, 10)))
	}
	seed := hasher.Sum64()
	// Without affinity text, nanosecond clock readings can vary sampling for
	// otherwise identical request inputs. Mix that clock value into the hash
	// before checking the resulting seed.
	if strings.TrimSpace(input.SessionHash) == "" && strings.TrimSpace(input.PreviousResponseID) == "" {
		seed ^= uint64(now().UnixNano())
	}
	// A zero mixed seed cannot advance the raw xorshift recurrence. Try the
	// clock-and-constant mix here; NewSelectionRNG handles any zero that remains
	// after this mix.
	if seed == 0 {
		seed = uint64(now().UnixNano()) ^ 0x9e3779b97f4a7c15
	}
	return seed
}

// BuildWeightedSelectionOrder returns a sampled permutation of the supplied
// pool without replacing its input slice entries. It copies candidate values
// and assigns each a weight equal to its score minus the pool minimum plus
// one. Referenced metadata and observations remain borrowed in the result.
//
// Invalid or nonpositive individual weights become one, and each chosen peer
// is removed before the next draw so its record appears once. SelectionSeed
// initializes the generator; a fixed returned seed and candidate pool produce
// the same permutation. Callers keep the summed weight finite for meaningful
// probabilities. Aggregate overflow enters the positive-total branch and can
// choose the first infinite accumulated prefix.
func BuildWeightedSelectionOrder(candidates []CandidateScore, input ScoreInput) []CandidateScore {
	if len(candidates) <= 1 {
		return append([]CandidateScore(nil), candidates...)
	}

	pool := append([]CandidateScore(nil), candidates...)
	weights := make([]float64, len(pool))
	minScore := pool[0].Score
	for i := 1; i < len(pool); i++ {
		if pool[i].Score < minScore {
			minScore = pool[i].Score
		}
	}
	for i := range pool {
		// Shift relative scores to positive individual weights. Repair
		// unusable weights so malformed score input still produces a complete
		// candidate permutation.
		weight := (pool[i].Score - minScore) + 1.0
		if math.IsNaN(weight) || math.IsInf(weight, 0) || weight <= 0 {
			weight = 1.0
		}
		weights[i] = weight
	}

	order := make([]CandidateScore, 0, len(pool))
	rng := NewSelectionRNG(SelectionSeed(input))
	for len(pool) > 0 {
		total := 0.0
		for _, weight := range weights {
			total += weight
		}
		selectedIdx := 0
		// The draw and cumulative comparisons use the unvalidated total. When
		// it overflows, a positive random fraction selects an infinite prefix;
		// a zero fraction leaves the default index after producing NaN.
		if total > 0 {
			random := rng.NextFloat64() * total
			accumulated := 0.0
			for i, weight := range weights {
				accumulated += weight
				if random <= accumulated {
					selectedIdx = i
					break
				}
			}
		} else {
			// An unusable nonpositive total cannot supply a weighted draw.
			// Choose an index from generator output so the remaining
			// permutation can still advance.
			selectedIdx = int(rng.NextUint64() % uint64(len(pool)))
		}
		order = append(order, pool[selectedIdx])
		pool = append(pool[:selectedIdx], pool[selectedIdx+1:]...)
		weights = append(weights[:selectedIdx], weights[selectedIdx+1:]...)
	}
	return order
}

// BuildSelectionOrder ranks a Top-K pool and delegates weighted permutation to
// BuildWeightedSelectionOrder. Empty input returns nil, nonpositive Top-K
// selects one, and returned candidate values keep the borrowed references
// described by the ranking and sampling helpers.
func BuildSelectionOrder(candidates []CandidateScore, input ScoreInput) []CandidateScore {
	if len(candidates) == 0 {
		return nil
	}
	topK := input.TopK
	if topK <= 0 {
		topK = 1
	}
	ranked := SelectTopK(candidates, topK)
	return BuildWeightedSelectionOrder(ranked, input)
}

// BuildScoreSnapshot returns baseline and hypothetical affinity values keyed
// by provider ID. Empty eligible input returns nil, and group currently
// supplies no scoring input; caller weights and quota observations follow the
// production scorer's validity rules.
//
// Weighted affinity adds the session bonus to each baseline and adds
// previous-response bonus for OpenAI providers. Disabled weighting marks the
// hard-affinity infinity flag and leaves the numeric sticky value at zero.
// These are display scenarios, not actual request affinity selections, because
// this call supplies no bound provider IDs to the scorer.
func BuildScoreSnapshot(
	providers []*ScoreProvider,
	loadMap map[int64]*ProviderLoadInfo,
	stats *RuntimeStats,
	group *ScoreGroup,
	weights policy.ScoreWeights,
	stickyWeightedEnabled bool,
	quotaHeadroomFactor func(*ScoreProvider, time.Time) float64,
	now time.Time,
) map[int64]ScoreSnapshot {
	candidates, _ := ScoreCandidates(providers, loadMap, stats, weights, ScoreInput{
		QuotaHeadroomFactor: quotaHeadroomFactor,
	}, now)
	if len(candidates) == 0 {
		return nil
	}
	result := make(map[int64]ScoreSnapshot, len(candidates))
	for _, candidate := range candidates {
		score := ScoreSnapshot{
			BaseScore:             candidate.Score,
			StickyWeightedEnabled: stickyWeightedEnabled,
			StickyScoreInfinity:   !stickyWeightedEnabled,
		}
		if stickyWeightedEnabled {
			// Model hypothetical session affinity for every provider. Add
			// previous-response affinity for OpenAI metadata because that
			// display scenario has the additional binding signal.
			platform := candidate.Provider.Platform
			score.StickyScore = candidate.Score + weights.SessionSticky
			if platform == capability.PlatformOpenAI {
				score.StickyScore += weights.Previous
			}
		}
		result[candidate.Provider.ID] = score
	}
	return result
}

// nowTime returns the injected feedback clock or time.Now when none was
// supplied. Report samples it separately for outcome and positive-latency
// timestamps, so concurrent observations can reflect independent clock
// readings.
func (s *RuntimeStats) nowTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Clamp01 restricts comparable values to the interval zero through one. NaN
// passes through because neither comparison matches, so callers that need
// finite scores validate their observations before using this helper.
func Clamp01(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
}

// LoadSkewByMoments returns population standard deviation from the sum and sum
// of squares for one set of load observations. Callers pass moments from the
// same set and its sample count; zero or one sample returns zero because no
// between-provider spread is reported.
//
// Variance subtracts two floating-point terms, so rounding can make an
// equal-load pool slightly negative. Negative results are clamped before
// taking the square root, allowing that case to report zero deviation. The
// result uses the same units as the supplied observations.
func LoadSkewByMoments(sum float64, sumSquares float64, count int) float64 {
	if count <= 1 {
		return 0
	}
	mean := sum / float64(count)
	variance := sumSquares/float64(count) - mean*mean
	// Floating-point moment subtraction can yield a small negative variance.
	// Clamp that result before the square root so known-load diagnostics can
	// represent an equal-load pool.
	if variance < 0 {
		variance = 0
	}
	return math.Sqrt(variance)
}
