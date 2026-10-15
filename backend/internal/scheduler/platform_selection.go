package scheduler

import (
	"context"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// platform_selection.go selects platform providers using previous-response
// affinity, session policy, ranked candidates, and capacity checks.
// A previous OpenAI response can require the same provider even when session
// affinity is weighted. The selector checks that result against the admitted
// group, requested capability, and transport before accepting its capacity. A
// guardian-parent request can borrow its parent's provider with binding
// preservation, while ordinary hard session affinity can escape for this
// request when shared feedback or confirmed capacity policy requires it.
//
// Load balance ranks the current eligible pool, but a rank does not establish
// physical capacity. Observed load uses an effective denominator, actual
// admission uses Concurrency, and stale compact-disabled snapshots can receive
// current-record retries after ranked Top-K attempts. Subscription priority
// can attempt its own pool, an ordinary pool, and then a busy subscription
// wait choice. Results retain the selected record's invocation association and
// captured feedback settings; metrics independently count every completed
// selection call, including failed ones.

// The scheduling layer labels describe which selection path supplied the
// decision; the probe cap separately bounds charged acquisition and
// database-recheck attempts.
const (
	openAIProviderScheduleLayerPreviousResponse = "previous_response_id" // accepted previous-response hard affinity.
	openAIProviderScheduleLayerGuardianParent   = "guardian_parent"      // accepted guardian-parent provider affinity.
	openAIProviderScheduleLayerSessionSticky    = "session_hash"         // accepted hard session affinity.
	openAIProviderScheduleLayerLoadBalance      = "load_balance"         // ranked pool acquisition or waiting.
	openAIProviderSelectionProbeLimit           = 64                     // maximum charged probes per acquisition or recheck counter.
)

// ErrNoAvailableCompactProviders identifies compact-specific exhaustion so the
// caller can distinguish unavailable compact eligibility from ordinary
// provider exhaustion.
var ErrNoAvailableCompactProviders = errors.New("no available providers support /responses/compact")

// PlatformSelectionInput contains the admitted group's request requirements,
// simulated or stored affinity hints, and captured feedback policy.
type PlatformSelectionInput struct {
	// GroupID is the gateway-admitted group for candidate reads and current
	// membership checks; this selector does not authorize a fallback group.
	GroupID     *int64 // admitted scheduling group ID
	Platform    string // optional forced provider platform
	SessionHash string // request session affinity hash
	// StickyProviderID supplies the session provider association for hard or
	// weighted affinity; positive values bypass GetSticky in the hard session
	// path.
	StickyProviderID int64 // known session provider ID
	// GuardianParentProviderID is an optional same-group affinity hint. Select
	// checks it through the hard session path with PreserveStickyBinding enabled.
	GuardianParentProviderID int64 // guardian parent provider hint
	// StickyPreviousProviderID contributes a weighted previous-response bonus
	// only when PreviousResponseCanMove allows moving that response to another
	// provider.
	StickyPreviousProviderID int64 // known previous-response provider ID
	StickyWeighted           bool  // affinity contributes numeric bonuses
	SubscriptionPriority     bool  // subscription pool preference enabled
	// PreserveStickyBinding suppresses hard-session deletion and TTL refresh and
	// later load-balance rebinding. Feedback escape and guardian-parent reuse set
	// it for the selected request path.
	PreserveStickyBinding bool // suppress session binding mutations
	// RequirePrivacySet is strengthened by GroupRequiresPrivacy in Select before
	// affinity and candidate filtering, so supplied false cannot bypass that
	// resolved group requirement.
	RequirePrivacySet  bool   // require provider privacy qualification
	PreviousResponseID string // upstream previous-response binding ID
	// PreviousResponseCanMove determines whether weighted affinity may replace
	// hard previous-response reuse. When false, Select still tries its hard
	// binding even with StickyWeighted enabled.
	PreviousResponseCanMove bool // previous response migration permitted
	// RequestedModel is the client model used for errors, request compatibility,
	// session identity, and platform-specific canonical cooldown checks.
	RequestedModel string // client-facing model identity R
	// RoutingModel supplies ordinary client routing C or the group-mapped
	// Messages model D. routingModel trims it and uses RequestedModel when the
	// trimmed value is empty.
	RoutingModel      string // provider-layer routing model C/D
	RequiredTransport string // requested upstream transport mode
	// requested OpenAI endpoint capability
	RequiredCapability      provider.OpenAIEndpointCapability
	RequiredImageCapability provider.OpenAIImagesCapability // requested OpenAI image capability
	RequireCompact          bool                            // current compact eligibility required
	// ExcludedIDs is borrowed for membership tests throughout this serial
	// selection; the caller keeps the map stable while affinity and pool
	// callbacks run.
	ExcludedIDs map[int64]struct{} // provider IDs excluded from this attempt
	// AdvancedSchedulerFeedbackConfig and StickyEscapeConfig capture effective
	// request policy. Select replaces both from Effective when Available, while
	// direct selection helpers consume the supplied values.
	AdvancedSchedulerFeedbackConfig policy.FeedbackConfig     // captured request feedback alphas
	StickyEscapeConfig              policy.StickyEscapeConfig // captured request escape thresholds
}

// PlatformDecision reports the selected layer and the observations counted for
// one selector call.
type PlatformDecision struct {
	Layer string // selection path display label
	// StickyPreviousHit and StickySessionHit can both be true in weighted mode
	// when their IDs match the selected provider. Metrics counts each hit
	// independently.
	StickyPreviousHit    bool    // selected previous-response affinity matched
	StickySessionHit     bool    // selected session affinity matched
	CandidateCount       int     // latest ranked pool size
	TopK                 int     // latest ranked Top-K size
	LatencyMs            int64   // milliseconds
	LoadSkew             float64 // observed load standard deviation
	SelectedProviderID   int64   // chosen persisted provider ID
	SelectedProviderType string  // chosen provider authentication type
}

// PlatformMetricsSnapshot contains independent counter samples and derived
// selection ratios for monitoring callers.
type PlatformMetricsSnapshot struct {
	SelectTotal             int64 // all completed selector calls
	StickyPreviousHitTotal  int64 // counted previous-response affinity hits
	StickySessionHitTotal   int64 // counted session affinity hits
	LoadBalanceSelectTotal  int64 // calls reaching load balance
	ProviderSwitchTotal     int64 // reported provider switch count
	SchedulerLatencyMsTotal int64 // milliseconds
	// SchedulerLatencyMsAvg, StickyHitRatio, ProviderSwitchRate, and LoadSkewAvg
	// use the sampled SelectTotal as their denominator. They remain zero when
	// that sampled total is nonpositive, even if other independently sampled
	// counters contain values.
	SchedulerLatencyMsAvg float64 // milliseconds
	// StickyHitRatio adds the two independent affinity-hit counters before
	// dividing by SelectTotal; matching both affinity IDs can make this ratio
	// exceed one.
	StickyHitRatio            float64 // combined hits per select
	ProviderSwitchRate        float64 // reported switches per select
	LoadSkewAvg               float64 // average observed load dispersion
	RuntimeStatsProviderCount int     // caller-supplied feedback provider count
}

// PlatformMetrics holds app-shared atomic counters for selections, affinity
// hits, switches, latency, and dispersion.
type PlatformMetrics struct {
	// selectTotal, stickyPreviousHitTotal, stickySessionHitTotal,
	// loadBalanceSelectTotal, providerSwitchTotal, latencyMsTotal, and
	// loadSkewMilliTotal update independently. Snapshot reads separate atomic
	// samples, so concurrent updates can produce a mixed-time tuple.
	selectTotal            atomic.Int64 // all completed selector calls
	stickyPreviousHitTotal atomic.Int64 // previous-response affinity hit counter
	stickySessionHitTotal  atomic.Int64 // session affinity hit counter
	loadBalanceSelectTotal atomic.Int64 // load-balance layer selection counter
	providerSwitchTotal    atomic.Int64 // reported provider switch counter
	latencyMsTotal         atomic.Int64 // milliseconds
	loadSkewMilliTotal     atomic.Int64 // dispersion summed in thousandths
}

// ProbeBudget tracks serial selection probes and attempted provider IDs when
// limiting is enabled.
type ProbeBudget struct {
	// acquires, rechecks, attempted, and limited belong to one serial selection.
	// limited enables separate acquisition/recheck caps; attempted records IDs
	// only when a limited acquisition consumes a probe.
	acquires  int                // charged physical acquisition attempts
	rechecks  int                // charged database recheck attempts
	attempted map[int64]struct{} // limited acquisition provider IDs
	// limited starts false in NewProbeBudget. Callers enable it for bounded
	// probes; unlimited acquisitions neither increment counters nor record
	// attempted IDs.
	limited bool // probe counters are capped
}

// PlatformCandidateScore associates shared score observations with this
// invocation's credential-free provider metadata.
type PlatformCandidateScore struct {
	// Provider borrows the exact FlowProvider conversion that produced this
	// score; readers keep its metadata stable while ranking and restoration use
	// it. Shared scorer conversions restore by score-pointer identity so same
	// persisted IDs keep distinct target associations.
	// associated invocation metadata pointer
	Provider *FlowProvider
	// LoadInfo and LoadKnown separate storage from observation presence.
	// BuildPlan can allocate an empty LoadInfo with LoadKnown=false; readers use
	// LoadKnown to avoid treating that synthetic record as measured zero. Present
	// cache observations remain borrowed and read-only while selection and retry
	// ordering use them.
	// associated observed load pointer
	LoadInfo *ProviderLoadInfo
	// load observation is present
	LoadKnown bool
	// final weighted score, base weighted sum, combined affinity bonus,
	// previous-response affinity bonus, and session affinity bonus
	Score, BaseScore, StickyBonus, PreviousBonus, SessionStickyBonus float64
	// provider global sort priority
	Priority int
	// observed error-rate EWMA and first-token latency in milliseconds
	ErrorRate, TTFT float64
	// latency sample present and feedback record observed
	HasTTFT, HasFeedback bool
	// per-candidate runtime feedback observation
	Feedback FeedbackSnapshot
	// normalized base scoring factors
	Factors CandidateFactors
}

// PlatformSelectionPorts binds provider reads, request eligibility, binding
// operations, and capacity completion to one adapter invocation.
type PlatformSelectionPorts struct {
	BasicStickyTTL time.Duration // basic binding lifetime duration
	// admitted group model rejection
	CheckPricing func(context.Context, *int64, string) bool
	// Hydrate resolves the exact invocation-associated candidate into execution
	// metadata; ordinary auxiliary selection runs it before attempting SetSticky.
	// execution candidate data completion
	Hydrate func(context.Context, *FlowProvider) (*FlowProvider, error)
	// basic provider binding writer
	SetSticky func(context.Context, *int64, string, int64, time.Duration) error
	// group privacy eligibility check
	PrivacyAllowed func(context.Context, *int64, *FlowProvider) bool
	// shadow protocol qualification check
	ShadowAllowed func(context.Context, *FlowProvider) bool
	// shadow parent health check
	ParentHealthy func(*FlowProvider, func(int64) *FlowProvider) bool
	// cached parent record lookup
	ParentLookup func(context.Context) func(int64) *FlowProvider
	// ReadProviderDB is optional in basic load selection; absent or failed reads
	// can leave nil parent records for ParentHealthy to reject.
	// current parent database reader
	ReadProviderDB func(context.Context, int64) (*FlowProvider, error)
	// NeedsGroupCheck returns true only with a usable group ID, permitting basic
	// selection to dereference it for GroupModelRestricted.
	NeedsGroupCheck func(context.Context, *int64) bool // upstream group gate required
	// provider-layer group model rejection
	GroupModelRestricted func(context.Context, int64, *FlowProvider, string, bool) bool
	// basic request candidate qualification
	BasicEligible func(context.Context, *FlowProvider, string, string, bool, provider.OpenAIEndpointCapability) bool
	// basic request exclusion reason
	BasicFailureReason func(context.Context, *FlowProvider, string, string, bool, provider.OpenAIEndpointCapability) string
	// CompleteAcquired returns the physical release handle to the caller on
	// success and invokes it on execution hydration failure, as required by basic
	// acquisition paths.
	// acquired execution result completion
	CompleteAcquired func(context.Context, *FlowProvider, func()) (*FlowSelection, error)
	// Complete attaches supplied acquisition or wait data after hydration.
	// Acquired callers needing automatic error cleanup use CompleteAcquired; the
	// general Complete callback can return a hydration error without releasing.
	// execution result and wait completion
	Complete func(context.Context, *FlowProvider, bool, func(), *ProviderWaitPlan) (*FlowSelection, error)

	// Available enables effective policy capture and hard-session work;
	// CacheAvailable also gates hard affinity. SnapshotAvailable permits stale
	// compact retries, while RecheckAvailable controls whether canRecheck charges
	// the budget.
	// selector dependencies available, sticky cache available, snapshot retries
	// available, and database rechecks chargeable
	Available, CacheAvailable, SnapshotAvailable, RecheckAvailable bool
	// admitted group effective settings
	Effective func(context.Context, *int64) policy.EffectiveSettings
	// resolved group privacy requirement
	GroupRequiresPrivacy func(context.Context, *int64) bool
	// PreviousResponse can return acquired capacity before final request
	// compatibility checks; rejected group, endpoint, or transport results need
	// their release handle invoked.
	// hard previous-response provider selector
	PreviousResponse func(context.Context, *int64, string, string, map[int64]struct{}, provider.OpenAIEndpointCapability, bool) (*FlowSelection, error)
	// request capability eligibility reason
	RequestCompatible func(context.Context, *FlowProvider, PlatformSelectionInput) (bool, string)
	// upstream transport eligibility check
	TransportCompatible func(*FlowProvider, string) bool
	// provider group metadata present
	HasGroupMetadata func(*FlowProvider) bool
	// admitted group membership check
	MatchesGroup func(*FlowProvider, *int64) bool
	// TryOrder attempts BindSticky after accepted acquisition unless preservation
	// is set. Select's previous-response path also binds a nonempty session after
	// acceptance, using its separate binding rule; failures leave the result
	// usable.
	// advanced request binding writer
	BindSticky func(context.Context, *int64, string, int64) error
	// stale session binding deletion
	DeleteSticky func(context.Context, *int64, string) error
	// stored session provider lookup
	GetSticky func(context.Context, *int64, string) (int64, error)
	// accepted binding expiry refresh
	RefreshSticky func(context.Context, *int64, string, time.Duration) error
	// advanced binding lifetime supplier
	StickyTTL func() time.Duration
	// basic and wait policy supplier
	Options func() FlowOptions
	// current provider metadata reader
	GetSchedulable func(context.Context, int64) (*FlowProvider, error)
	// provider binding invalidation predicate
	ClearSticky func(*FlowProvider, string) bool
	// candidate association is usable
	IsCompatible func(*FlowProvider) bool
	// current provider admission check
	IsSchedulable func(*FlowProvider) bool
	// Recheck can return a distinct same-provider conversion or nil after
	// rejection. Its record must retain physical provider identity so acquisition
	// and cleanup address the selected provider.
	// current database eligibility recheck
	Recheck func(context.Context, *FlowProvider, *int64, string, string, bool, provider.OpenAIEndpointCapability) *FlowProvider
	// Fresh can replace stale metadata with a current same-provider conversion.
	// The returned invocation association identifies that exact target for later
	// recheck and acquisition.
	// fresh snapshot eligibility resolver
	Fresh func(context.Context, *FlowProvider, string, string, bool, provider.OpenAIEndpointCapability) *FlowProvider
	// Grok free-tier candidate filter
	FreeQuota func(context.Context, []FlowProvider) []FlowProvider
	// provider-specific cooldown model resolver
	CanonicalModel func(*FlowProvider, string) string
	// Grok team-model cooldown check
	TeamLimited func(*FlowProvider, string, time.Time) bool
	// provider-model quota cooldown check
	ModelQuotaBlocked func(int64, string, time.Time) bool
	// Acquire supplies idempotent physical cleanup for acquired results; TryOrder
	// may adopt it into a lease, and select-only adapters may succeed with no
	// physical slot.
	// physical provider capacity acquisition
	Acquire func(context.Context, int64, int) (*AcquireResult, error)
	// admitted group candidate source
	ListCandidates func(context.Context, *int64, string) ([]FlowProvider, error)
	// request-model runtime block check
	RuntimeBlocked func(*FlowProvider, string) bool
	// Grok team-model candidate filter
	FilterTeamLimited func([]FlowProvider, string, time.Time) []FlowProvider
	// Grok provider-model quota filter
	FilterModelQuota func([]FlowProvider, string, time.Time) []FlowProvider
	// current compact eligibility flag
	CompactAllowed func(*FlowProvider) bool
	// ChatGPT subscription pool membership
	IsSubscription func(*FlowProvider) bool
	// QuotaHeadroom reads an observation through the ScoreProvider invocation
	// association; absent or expired supported platform data is represented by
	// the callback's neutral factor.
	// platform quota scoring observation
	QuotaHeadroom func(*ScoreProvider, time.Time) float64
	// model-aware exhaustion error builder
	Unavailable func(context.Context, string, string, bool, string, ...[]FlowProvider) error
}

// PlatformSelector orders hard affinity and scored pools using app-shared
// feedback and selection counters.
type PlatformSelector struct {
	// ports captures the invocation lookup for providers and groups; callbacks
	// and selection share its unsynchronized association maps, so the caller
	// serializes this selector's use.
	ports       PlatformSelectionPorts // invocation-bound provider callbacks
	concurrency *ConcurrencyService    // shared provider load service
	stats       *RuntimeStats          // shared request feedback state
	metrics     *PlatformMetrics       // shared atomic selection counters
	diagnostics Diagnostics            // selection diagnostic output callbacks
	// now is required and retained as supplied. Even Select's unavailable-state
	// path samples this clock; construction supplies no clock fallback.
	now func() time.Time // selection timing and scoring clock
}

// PlatformLoadPlan retains ranked candidates, stale compact retries, and their
// physical-probe ordering for one pool.
type PlatformLoadPlan struct {
	// allCandidates contains the full observation pool. For compact requests,
	// candidates contains the enabled scored subset and staleSnapshotCompactRetry
	// retains disabled metadata for later fresh/database qualification; ordinary
	// requests score all candidates.
	allCandidates             []PlatformCandidateScore // entire hard-eligible observation pool
	candidates                []PlatformCandidateScore // pool selected for scoring
	staleSnapshotCompactRetry []PlatformCandidateScore // disabled snapshot compact retry pool
	// selectionOrder starts with sampled ranked Top-K candidates. Snapshot-backed
	// compact requests can append disabled metadata beyond Top-K for
	// current-record retries.
	selectionOrder []PlatformCandidateScore // ranked and stale retry order
	candidateCount int                      // currently ranked candidate count
	topK           int                      // ranked pool Top-K count
	loadSkew       float64                  // known-load pool standard deviation
}

// platformPoolAttempt carries the latest pool order, result, and exhaustion
// details into wait fallback.
type platformPoolAttempt struct {
	result *FlowSelection // accepted pool selection result
	// selectionOrder, candidateCount, topK, and loadSkew describe the latest
	// usable plan from initial or fresh-load attempts. Wait fallback uses this
	// order when no acquisition supplied result.
	selectionOrder []PlatformCandidateScore // latest pool acquisition order
	candidateCount int                      // latest ranked pool size
	topK           int                      // latest ranked Top-K size
	loadSkew       float64                  // latest pool load dispersion
	compactBlocked bool                     // current compact rejection observed
	// noCompactCandidates marks compact-specific exhaustion and can allow
	// subscription fallback to try the regular pool despite this attempt's error.
	noCompactCandidates bool  // pool lacks compact retry choices
	err                 error // pool selection failure cause
}

// PlatformFilterStats counts snapshot-pool exclusions by reason for a
// request's unavailable-provider explanation.
type PlatformFilterStats struct {
	Pool int // initial prefiltered candidate count
	// Reasons is mutated by Exclude without synchronization and retained by value
	// copies of PlatformFilterStats; callers serialize access within one
	// selection.
	Reasons map[string]int // exclusions counted by reason
}

// routingModel returns trimmed provider-layer RoutingModel when present,
// otherwise the supplied RequestedModel. The fallback keeps the client value
// as supplied, so callers requiring normalized routing inputs prepare it
// before selection.
func (r PlatformSelectionInput) routingModel() string {
	if model := strings.TrimSpace(r.RoutingModel); model != "" {
		return model
	}
	return r.RequestedModel
}

// RecordSelect adds one completed selector decision, including failed
// selections, to independent atomic counters. Nil metrics are ignored; both
// affinity flags can contribute hits for one call, and load dispersion is
// rounded to thousandths.
func (m *PlatformMetrics) RecordSelect(decision PlatformDecision) {
	if m == nil {
		return
	}
	m.selectTotal.Add(1)
	m.latencyMsTotal.Add(decision.LatencyMs)
	// Accumulate rounded thousandths so the monitoring mean has quantized
	// dispersion.
	m.loadSkewMilliTotal.Add(int64(math.Round(decision.LoadSkew * 1000)))
	if decision.StickyPreviousHit {
		m.stickyPreviousHitTotal.Add(1)
	}
	if decision.StickySessionHit {
		m.stickySessionHitTotal.Add(1)
	}
	if decision.Layer == openAIProviderScheduleLayerLoadBalance {
		m.loadBalanceSelectTotal.Add(1)
	}
}

// RecordSwitch counts a reported provider switch in this app-owned metrics
// instance. Nil metrics are ignored; switches are reported separately from
// RecordSelect and can occur more than once during a request.
func (m *PlatformMetrics) RecordSwitch() {
	if m == nil {
		return
	}
	m.providerSwitchTotal.Add(1)
}

// Snapshot returns independently sampled counters and ratios using the
// supplied feedback-provider count. Nil metrics produce a zero snapshot.
// Selection totals include failed calls, and reported switches are counted
// separately from selection attempts.
//
// A positive sampled selection total supplies the denominator for latency,
// combined affinity hits, switches, and quantized dispersion. Both affinity
// flags can count for one weighted choice, so StickyHitRatio can exceed one.
// Concurrent updates are not captured as a coherent tuple: callers use these
// values for monitoring rather than request-level accounting.
func (m *PlatformMetrics) Snapshot(providerCount int) PlatformMetricsSnapshot {
	if m == nil {
		return PlatformMetricsSnapshot{}
	}

	// Read the denominator before hit and latency samples. Concurrent later
	// increments can appear in those numerators after this total was sampled.
	selectTotal := m.selectTotal.Load()
	prevHit := m.stickyPreviousHitTotal.Load()
	sessionHit := m.stickySessionHitTotal.Load()
	switchTotal := m.providerSwitchTotal.Load()
	latencyTotal := m.latencyMsTotal.Load()
	loadSkewTotal := m.loadSkewMilliTotal.Load()

	snapshot := PlatformMetricsSnapshot{
		SelectTotal:               selectTotal,
		StickyPreviousHitTotal:    prevHit,
		StickySessionHitTotal:     sessionHit,
		LoadBalanceSelectTotal:    m.loadBalanceSelectTotal.Load(),
		ProviderSwitchTotal:       switchTotal,
		SchedulerLatencyMsTotal:   latencyTotal,
		RuntimeStatsProviderCount: providerCount,
	}
	if selectTotal > 0 {
		snapshot.SchedulerLatencyMsAvg = float64(latencyTotal) / float64(selectTotal)
		snapshot.StickyHitRatio = float64(prevHit+sessionHit) / float64(selectTotal)
		snapshot.ProviderSwitchRate = float64(switchTotal) / float64(selectTotal)
		snapshot.LoadSkewAvg = float64(loadSkewTotal) / 1000 / float64(selectTotal)
	}
	return snapshot
}

// NewProbeBudget constructs an unlimited budget with an allocated attempted-ID
// map. Callers explicitly enable limiting for bounded probes; unlimited
// acquisition and recheck calls do not consume or record probe counts.
func NewProbeBudget() *ProbeBudget {
	return &ProbeBudget{attempted: make(map[int64]struct{})}
}

// enableLimit enables separate acquisition and database-recheck caps on a
// nonnil budget. Prior unlimited probes remain uncounted; the caller
// serializes subsequent budget mutation, and passing nil leaves limiting
// unavailable.
func (b *ProbeBudget) enableLimit() {
	if b != nil {
		b.limited = true
	}
}

// recordAcquire admits a physical probe and records its provider ID when
// limiting is active. Nil budgets reject probes, unlimited budgets accept
// without recording, and a charged limited call increments the counter even
// for a repeatedly tried ID. Callers serialize this mutable budget.
//
// The cap counts actual probes rather than distinct providers. attempted
// supplies the identity set consumed by bounded wait fallback, which prefers
// unattempted candidates within each observed-fullness class before
// considering the already attempted peers in that class.
func (b *ProbeBudget) recordAcquire(providerID int64) bool {
	if b == nil {
		return false
	}
	if !b.limited {
		return true
	}
	// The acquisition cap counts probes, including repeated provider IDs.
	if b.acquires >= openAIProviderSelectionProbeLimit {
		return false
	}
	if b.attempted == nil {
		b.attempted = make(map[int64]struct{})
	}
	b.acquires++
	b.attempted[providerID] = struct{}{}
	return true
}

// recordRecheck admits one charged database recheck under the serial budget.
// Nil budgets reject; unlimited budgets accept without incrementing, and
// limited budgets stop at their independent recheck cap.
func (b *ProbeBudget) recordRecheck() bool {
	if b == nil {
		return false
	}
	if !b.limited {
		return true
	}
	// The database recheck cap is independent of charged acquisitions.
	if b.rechecks >= openAIProviderSelectionProbeLimit {
		return false
	}
	b.rechecks++
	return true
}

// acquireExhausted reports whether an enabled acquisition cap has been
// reached. Nil or unlimited budgets report false, permitting fresh-load
// retries in ordinary selection without imposing the bounded-probe policy.
func (b *ProbeBudget) acquireExhausted() bool {
	return b != nil && b.limited && b.acquires >= openAIProviderSelectionProbeLimit
}

// wasAttempted reports provider membership in the limited acquisition identity
// set. Nil budgets return false; unlimited calls never add IDs, so this result
// distinguishes charged attempts rather than every observed probe.
func (b *ProbeBudget) wasAttempted(providerID int64) bool {
	if b == nil {
		return false
	}
	_, ok := b.attempted[providerID]
	return ok
}

// NewPlatformSelector retains invocation callbacks, shared load and feedback
// services, metrics, diagnostics, and clock. Callers provide the required
// callbacks and nonnil clock; construction supplies neither validation nor
// replacement defaults.
func NewPlatformSelector(ports PlatformSelectionPorts, concurrency *ConcurrencyService, stats *RuntimeStats, metrics *PlatformMetrics, diagnostics Diagnostics, now func() time.Time) *PlatformSelector {
	return &PlatformSelector{ports: ports, concurrency: concurrency, stats: stats, metrics: metrics, diagnostics: diagnostics, now: now}
}

// requestCompatible returns the bound request capability check's boolean
// result for this candidate. Its diagnostic reason is discarded here because
// affinity and fallback callers use acceptance only, while pool filtering
// records reasons directly.
func (s *PlatformSelector) requestCompatible(ctx context.Context, a *FlowProvider, input PlatformSelectionInput) bool {
	ok, _ := s.ports.RequestCompatible(ctx, a, input)
	return ok
}

// canRecheck permits rechecking without charging when database recheck
// dependencies are unavailable. With RecheckAvailable enabled, the caller's
// nonnil budget decides admission through recordRecheck; this does not skip
// the Recheck callback itself.
func (s *PlatformSelector) canRecheck(b *ProbeBudget) bool {
	if !s.ports.RecheckAvailable {
		return true
	}
	return b.recordRecheck()
}

// Exclude increments one exclusion reason, allocating the reason map on first
// use. Callers provide a nonnil receiver and serialize access because copied
// statistics can share this unsynchronized map.
func (s *PlatformFilterStats) Exclude(reason string) {
	if s.Reasons == nil {
		s.Reasons = make(map[string]int, 4)
	}
	s.Reasons[reason]++
}

// Summary formats pool size, lexically sorted reason counts, and optional
// extra diagnostic text. Reason maps remain borrowed, so callers keep them
// stable while formatting; the local strings.Builder accepts all appended
// strings.
func (s PlatformFilterStats) Summary(extra string) string {
	var b strings.Builder
	// strings.Builder appends locally and returns nil write errors. Ignore its
	// count/error pairs while collecting sorted reason text for the final string.
	_, _ = b.WriteString("pool=")
	_, _ = b.WriteString(strconv.Itoa(s.Pool))
	if len(s.Reasons) > 0 {
		reasons := make([]string, 0, len(s.Reasons))
		for reason := range s.Reasons {
			reasons = append(reasons, reason)
		}
		sort.Strings(reasons)
		_, _ = b.WriteString(", filtered:")
		for _, reason := range reasons {
			_, _ = b.WriteString(" ")
			_, _ = b.WriteString(reason)
			_, _ = b.WriteString("=")
			_, _ = b.WriteString(strconv.Itoa(s.Reasons[reason]))
		}
	}
	if extra != "" {
		_, _ = b.WriteString(", ")
		_, _ = b.WriteString(extra)
	}
	return b.String()
}

// Select tries previous-response affinity, guardian-parent affinity, hard
// session affinity, then scored load balance for the admitted request. Callers
// use a constructed nonnil selector and clock. Effective settings capture
// feedback alpha and escape thresholds when dependencies are available;
// weighted and subscription switches remain request inputs.
//
// A previous response that cannot move remains hard-bound even with weighting
// enabled. Returned capacity is released if current group, request, or
// transport checks reject that result. Guardian-parent reuse sets preservation
// for its session attempt, and ordinary sticky escape carries preservation
// into load balance so this request can select another provider while
// retaining affinity.
//
// The returned decision identifies the accepted layer or failed load-balance
// attempt. Deferred metrics record every completed call and elapsed
// milliseconds, including errors; another defer attaches the captured feedback
// settings when the result lacks them. Weighted load choices can match both
// supplied affinity IDs, with each hit counted independently.
func (s *PlatformSelector) Select(
	ctx context.Context,
	req PlatformSelectionInput,
) (selectionResult *FlowSelection, decision PlatformDecision, selectErr error) {
	if s != nil && s.ports.Available {
		effective := s.ports.Effective(ctx, req.GroupID)
		req.AdvancedSchedulerFeedbackConfig = NormalizeFeedbackConfig(effective.Feedback)
		req.StickyEscapeConfig = policy.NormalizeStickyEscape(effective.StickyEscape)
	}
	defer func() {
		// The request resolved its feedback settings before selection. Attach that
		// captured value when the result lacks one, so completion cannot read a
		// later group configuration.
		if selectionResult != nil && selectionResult.AdvancedSchedulerFeedback == nil && s != nil && s.ports.Available {
			feedback := NormalizeFeedbackConfig(req.AdvancedSchedulerFeedbackConfig)
			selectionResult.AdvancedSchedulerFeedback = &feedback
		}
	}()
	if s != nil && s.ports.Available && s.ports.GroupRequiresPrivacy(ctx, req.GroupID) {
		req.RequirePrivacySet = true
	}
	decision = PlatformDecision{}
	start := s.now()
	// Every return path records this invocation, including failed affinity or
	// load selection. Sample elapsed time before passing the final decision to
	// shared monitoring counters.
	defer func() {
		decision.LatencyMs = s.now().Sub(start).Milliseconds()
		s.metrics.RecordSelect(decision)
	}()

	previousResponseID := strings.TrimSpace(req.PreviousResponseID)
	if previousResponseID != "" && (req.Platform == "" || strings.TrimSpace(req.Platform) == capability.PlatformOpenAI) &&
		(!req.StickyWeighted || !req.PreviousResponseCanMove) {
		selection, err := s.ports.PreviousResponse(
			ctx,
			req.GroupID,
			previousResponseID,
			req.routingModel(),
			req.ExcludedIDs,
			req.RequiredCapability,
			req.RequireCompact,
		)
		if err != nil {
			return nil, decision, err
		}
		if selection != nil && selection.Provider != nil {
			compatible, _ := s.ports.RequestCompatible(ctx, selection.Provider, req)
			groupCompatible := s.ports.MatchesGroup(selection.Provider, req.GroupID)
			// Previous-response selection may already own capacity. Return its release
			// handle before discarding a result rejected by the current group,
			// capability, or transport.
			if !groupCompatible || !compatible || !s.ports.TransportCompatible(selection.Provider, req.RequiredTransport) {
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
				selection = nil
			}
		}
		if selection != nil && selection.Provider != nil {
			decision.Layer = openAIProviderScheduleLayerPreviousResponse
			decision.StickyPreviousHit = true
			decision.SelectedProviderID = selection.Provider.ID
			decision.SelectedProviderType = selection.Provider.Type
			if req.SessionHash != "" {
				// The previous-response result passed current request checks. Session
				// binding is best effort after acceptance; cache failure leaves its
				// acquired or waiting result usable.
				_ = s.ports.BindSticky(ctx, req.GroupID, req.SessionHash, selection.Provider.ID)
			}
			return selection, decision, nil
		}
	}

	if req.GuardianParentProviderID > 0 {
		// Use the parent's provider hint under this request's group and capability
		// checks. Preserve binding operations during that attempt so contextual
		// affinity cannot overwrite the stored session choice.
		parentReq := req
		parentReq.StickyProviderID = req.GuardianParentProviderID
		parentReq.PreserveStickyBinding = true
		selection, _, err := s.SelectBySessionHash(ctx, parentReq)
		if err != nil {
			return nil, decision, err
		}
		if selection != nil && selection.Provider != nil {
			decision.Layer = openAIProviderScheduleLayerGuardianParent
			decision.StickySessionHit = true
			decision.SelectedProviderID = selection.Provider.ID
			decision.SelectedProviderType = selection.Provider.Type
			return selection, decision, nil
		}
	}

	if !req.StickyWeighted {
		selection, escapedSticky, err := s.SelectBySessionHash(ctx, req)
		if err != nil {
			return nil, decision, err
		}
		if selection != nil && selection.Provider != nil {
			decision.Layer = openAIProviderScheduleLayerSessionSticky
			decision.StickySessionHit = true
			decision.SelectedProviderID = selection.Provider.ID
			decision.SelectedProviderType = selection.Provider.Type
			return selection, decision, nil
		}
		// Hard-session escape affects this request's later pool choice. Carry
		// preservation into load balance so successful alternative acquisition does
		// not rewrite the existing binding.
		if escapedSticky {
			req.PreserveStickyBinding = true
		}
	}

	selection, candidateCount, topK, loadSkew, err := s.SelectByLoadBalance(ctx, req)
	decision.Layer = openAIProviderScheduleLayerLoadBalance
	decision.CandidateCount = candidateCount
	decision.TopK = topK
	decision.LoadSkew = loadSkew
	if err != nil {
		return nil, decision, err
	}
	if selection != nil && selection.Provider != nil {
		decision.SelectedProviderID = selection.Provider.ID
		decision.SelectedProviderType = selection.Provider.Type
		if req.StickyWeighted {
			if req.StickyPreviousProviderID > 0 && selection.Provider.ID == req.StickyPreviousProviderID {
				decision.StickyPreviousHit = true
			}
			if req.StickyProviderID > 0 && selection.Provider.ID == req.StickyProviderID {
				decision.StickySessionHit = true
			}
		}
	}
	return selection, decision, nil
}

// SelectBySessionHash returns a hard session choice and whether this request
// escaped its binding. Empty session, unavailable dependencies, exclusion,
// failed reads, or failed eligibility can leave no choice.
// PreserveStickyBinding suppresses deletion and expiry refresh; request
// capability rejection alone also retains the stored binding.
//
// Current group, provider status, transport, database, free-quota, team-model,
// and model-quota checks precede capacity. Shared feedback can escape the
// binding before acquisition. Confirmed denied capacity also escapes when
// enabled and a concurrency service exists; an acquisition error can instead
// produce a sticky wait plan. Returned plans use physical Concurrency and
// defer actual bounded queue admission to the waiting layer.
func (s *PlatformSelector) SelectBySessionHash(
	ctx context.Context,
	req PlatformSelectionInput,
) (*FlowSelection, bool, error) {
	sessionHash := strings.TrimSpace(req.SessionHash)
	if sessionHash == "" || s == nil || !s.ports.Available || !s.ports.CacheAvailable {
		return nil, false, nil
	}

	// Current invalidation can clear session affinity, but parent reuse or
	// request escape may need it retained. Centralize best-effort deletion under
	// the preservation flag for these branches.
	clearBinding := func() {
		if !req.PreserveStickyBinding {
			_ = s.ports.DeleteSticky(ctx, req.GroupID, sessionHash)
		}
	}
	providerID := req.StickyProviderID
	if providerID <= 0 {
		var err error
		// An unreadable stored binding behaves as no hard choice. The caller can
		// still use weighted or ordinary candidates for this admitted request.
		providerID, err = s.ports.GetSticky(ctx, req.GroupID, sessionHash)
		if err != nil || providerID <= 0 {
			return nil, false, nil
		}
	}
	if providerID <= 0 {
		return nil, false, nil
	}
	if req.ExcludedIDs != nil {
		if _, excluded := req.ExcludedIDs[providerID]; excluded {
			return nil, false, nil
		}
	}

	// A stale binding may reference an unavailable record. Ignore the read
	// failure after requesting permitted deletion, allowing the next selection
	// layer to run.
	provider, err := s.ports.GetSchedulable(ctx, providerID)
	if err != nil || provider == nil {
		clearBinding()
		return nil, false, nil
	}
	if !s.ports.MatchesGroup(provider, req.GroupID) || s.ports.ClearSticky(provider, req.routingModel()) || req.Platform != "" && provider.Platform != strings.TrimSpace(req.Platform) || !s.ports.IsCompatible(provider) || !s.ports.IsSchedulable(provider) {
		clearBinding()
		return nil, false, nil
	}
	if !s.requestCompatible(ctx, provider, req) {
		return nil, false, nil
	}
	if !s.ports.TransportCompatible(provider, req.RequiredTransport) {
		clearBinding()
		return nil, false, nil
	}
	provider = s.ports.Recheck(ctx, provider, req.GroupID, req.Platform, req.routingModel(), req.RequireCompact, req.RequiredCapability)
	if provider == nil || !s.ports.MatchesGroup(provider, req.GroupID) || !s.ports.TransportCompatible(provider, req.RequiredTransport) {
		clearBinding()
		return nil, false, nil
	}
	// Hard affinity still obeys the request-time free-quota gate. A provider
	// rejected here cannot bypass that gate merely by holding a session binding.
	if provider != nil && len(s.ports.FreeQuota(ctx, []FlowProvider{*provider})) == 0 {
		clearBinding()
		return nil, false, nil
	}
	// Resolve the provider's canonical cooldown model before team and
	// per-provider quota checks. These shared temporary observations can
	// invalidate hard affinity for that model.
	now := s.now()
	upstreamModel := s.ports.CanonicalModel(provider, req.RequestedModel)
	if provider != nil && s.ports.TeamLimited(provider, upstreamModel, now) {
		clearBinding()
		return nil, false, nil
	}
	if provider != nil && s.ports.ModelQuotaBlocked(provider.ID, upstreamModel, now) {
		clearBinding()
		return nil, false, nil
	}
	escapeCfg := policy.NormalizeStickyEscape(req.StickyEscapeConfig)
	if reason, errorRate, ttft, shouldEscape := ShouldEscapeSticky(s.stats, providerID, escapeCfg); shouldEscape {
		s.diagnostics.event("info", "sticky_escape_triggered",
			"provider_id", providerID,
			"reason", reason,
			"error_rate", errorRate,
			"ttft", ttft,
		)
		return nil, true, nil
	}
	result, acquireErr := s.ports.Acquire(ctx, providerID, provider.Concurrency)
	if acquireErr == nil && result != nil && result.Acquired {
		if !req.PreserveStickyBinding {
			// Accepted physical capacity remains usable if expiry refresh fails.
			// Preserved bindings skip the refresh so parent or escaped affinity keeps
			// its stored lifetime.
			_ = s.ports.RefreshSticky(ctx, req.GroupID, sessionHash, s.ports.StickyTTL())
		}
		return &FlowSelection{
			Provider:    provider,
			Acquired:    true,
			ReleaseFunc: result.ReleaseFunc,
			AdvancedSchedulerFeedback: func() *policy.FeedbackConfig {
				feedback := NormalizeFeedbackConfig(req.AdvancedSchedulerFeedbackConfig)
				return &feedback
			}(),
		}, false, nil
	}

	cfg := s.ports.Options()
	// Capacity errors and denied slots can reach waiting. Confirmed denied
	// capacity with escape enabled instead selects another provider; waiting uses
	// physical Concurrency and later queue admission.
	if s.concurrency != nil {
		if escapeCfg.Enabled && acquireErr == nil && result != nil && !result.Acquired {
			errorRate, ttft, _ := s.stats.Snapshot(providerID)
			s.diagnostics.event("info", "sticky_escape_triggered",
				"provider_id", providerID,
				"reason", "concurrency_full",
				"error_rate", errorRate,
				"ttft", ttft,
			)
			return nil, true, nil
		}
		return &FlowSelection{
			Provider: provider,
			WaitPlan: &ProviderWaitPlan{
				ProviderID:     providerID,
				MaxConcurrency: provider.Concurrency,
				Timeout:        cfg.StickySessionWaitTimeout,
				MaxWaiting:     cfg.StickySessionMaxWaiting,
			},
			AdvancedSchedulerFeedback: func() *policy.FeedbackConfig {
				feedback := NormalizeFeedbackConfig(req.AdvancedSchedulerFeedbackConfig)
				return &feedback
			}(),
		}, false, nil
	}
	return nil, false, nil
}

// BuildPlan combines hard-eligible nonnil provider metadata with observed
// loads and shared feedback for one pool. Missing load records receive
// synthetic storage with LoadKnown=false. Actual acquisition remains later;
// fully observed capacity is still eligible for advanced scoring.
//
// Compact requests partition currently enabled metadata from stale disabled
// snapshots. The enabled subset receives shared numeric scores and Top-K
// sampling, while disabled snapshots retain their observation records for
// optional current-record retry. Previous-response bonus is cleared when
// responses cannot move because Select handles that case as hard affinity. The
// resulting order retains each provider's invocation association; an empty
// enabled subset may still have snapshot-backed compact retry choices.
func (s *PlatformSelector) BuildPlan(
	ctx context.Context,
	req PlatformSelectionInput,
	filtered []*FlowProvider,
	loadMap map[int64]*ProviderLoadInfo,
) PlatformLoadPlan {
	allCandidates := make([]PlatformCandidateScore, 0, len(filtered))
	for _, provider := range filtered {
		loadInfo, loadKnown := loadMap[provider.ID]
		// Synthetic zero storage lets stale retry ordering read load fields, while
		// LoadKnown=false retains observation absence for score normalization and
		// physical fullness classification.
		if !loadKnown || loadInfo == nil {
			loadInfo = &ProviderLoadInfo{ProviderID: provider.ID}
			loadKnown = false
		}
		errorRate, ttft, hasTTFT := 0.0, 0.0, false
		if s.stats != nil {
			errorRate, ttft, hasTTFT = s.stats.Snapshot(provider.ID)
		}
		allCandidates = append(allCandidates, PlatformCandidateScore{
			Provider:  provider,
			LoadInfo:  loadInfo,
			LoadKnown: loadKnown,
			ErrorRate: errorRate,
			TTFT:      ttft,
			HasTTFT:   hasTTFT,
		})
	}

	candidates := allCandidates
	staleSnapshotCompactRetry := make([]PlatformCandidateScore, 0, len(allCandidates))
	if req.RequireCompact {
		candidates = make([]PlatformCandidateScore, 0, len(allCandidates))
		for _, candidate := range allCandidates {
			// Snapshot-disabled compact metadata leaves numeric ranking but remains a
			// possible current-record retry. Keeping its association allows
			// administrative reenablement to be discovered later.
			if !s.ports.CompactAllowed(candidate.Provider) {
				staleSnapshotCompactRetry = append(staleSnapshotCompactRetry, candidate)
				continue
			}
			candidates = append(candidates, candidate)
		}
	}

	plan := PlatformLoadPlan{
		allCandidates:             allCandidates,
		candidates:                candidates,
		staleSnapshotCompactRetry: staleSnapshotCompactRetry,
		candidateCount:            len(candidates),
	}
	if len(candidates) == 0 {
		plan.selectionOrder = s.buildOpenAISelectionOrder(req, plan)
		return plan
	}

	providers := make([]*FlowProvider, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Provider != nil {
			providers = append(providers, candidate.Provider)
		}
	}
	previousStickyProviderID := req.StickyPreviousProviderID
	if !req.PreviousResponseCanMove {
		previousStickyProviderID = 0
	}
	effectiveSettings := s.ports.Effective(ctx, req.GroupID)
	plan.candidates, plan.loadSkew = s.scoreCandidates(
		providers,
		loadMap,
		s.stats,
		effectiveSettings.Weights,
		ScoreInput{
			GroupID:                  req.GroupID,
			SessionHash:              req.SessionHash,
			PreviousResponseID:       req.PreviousResponseID,
			RequestedModel:           req.RequestedModel,
			StickyProviderID:         req.StickyProviderID,
			StickyPreviousProviderID: previousStickyProviderID,
			StickyWeighted:           req.StickyWeighted,
			QuotaHeadroomFactor:      s.ports.QuotaHeadroom,
		},
		s.now(),
	)

	plan.topK = min(effectiveSettings.TopK, len(plan.candidates))
	// A nonempty ranked pool retains at least one attempt.
	if plan.topK <= 0 {
		plan.topK = 1
	}

	plan.selectionOrder = s.buildOpenAISelectionOrder(req, plan)
	return plan
}

// buildOpenAISelectionOrder samples the ranked Top-K subset using the
// request's affinity inputs. A missing pool or nonpositive plan Top-K yields
// no ranked attempts. For compact requests, the currently enabled ranking is
// tried before stale disabled metadata.
//
// SnapshotAvailable allows those stale candidates to be appended in
// priority/load/wait/LRU order, even outside ranked Top-K. Fresh and database
// eligibility can then observe administrative reenablement before actual
// admission. Without snapshot support, the disabled retry list is omitted and
// the caller can report compact exhaustion; provider and load references
// remain borrowed from the plan.
func (s *PlatformSelector) buildOpenAISelectionOrder(
	req PlatformSelectionInput,
	plan PlatformLoadPlan,
) []PlatformCandidateScore {
	buildSelectionOrder := func(pool []PlatformCandidateScore) []PlatformCandidateScore {
		if len(pool) == 0 || plan.topK <= 0 {
			return nil
		}
		groupTopK := min(plan.topK, len(pool))
		ranked := platformTopK(pool, groupTopK)
		return s.weightedOrder(ranked, req)
	}

	if req.RequireCompact {
		selectionOrder := make([]PlatformCandidateScore, 0, len(plan.allCandidates))
		selectionOrder = append(selectionOrder, buildSelectionOrder(plan.candidates)...)
		// Append stale disabled metadata after ranked attempts only with
		// snapshot-backed recovery. Rechecks can then discover current compact
		// eligibility beyond the sampled Top-K.
		if len(plan.staleSnapshotCompactRetry) > 0 && s.ports.SnapshotAvailable {
			selectionOrder = append(selectionOrder, sortOpenAICompactRetryCandidates(plan.staleSnapshotCompactRetry)...)
		}
		return selectionOrder
	}

	return buildSelectionOrder(plan.candidates)
}

// SelectByLoadBalance returns an acquired choice or a rechecked wait plan with
// the selected pool's count, Top-K, and load dispersion. Group membership,
// request capabilities, transport, runtime blocks, privacy, and applicable
// Grok gates narrow candidates before scoring. Load failures leave unknown
// observations rather than rejecting those candidates.
//
// A new ordinary budget starts unlimited. With subscription priority,
// subscription acquisition runs first, then regular acquisition and regular
// waiting can be considered. If the regular pool cannot supply a usable
// choice, a busy subscription pool can still provide a wait plan.
// Compact-specific pool exhaustion permits this fallback, while other pool
// errors stop selection according to their branch. Current record checks and
// physical capacity remain separate from ranking.
func (s *PlatformSelector) SelectByLoadBalance(
	ctx context.Context,
	req PlatformSelectionInput,
) (*FlowSelection, int, int, float64, error) {
	budget := NewProbeBudget()
	providers, err := s.ports.ListCandidates(ctx, req.GroupID, req.Platform)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	if len(providers) == 0 {
		return nil, 0, 0, 0, s.ports.Unavailable(ctx, req.RequestedModel, req.routingModel(), false, PlatformFilterStats{}.Summary(""), providers)
	}
	// Request selection applies the local Grok free-quota gate before pool
	// scoring. An empty filtered result is reported with its gate reason so
	// normal ranking cannot revive it.
	providers = s.ports.FreeQuota(ctx, providers)
	if len(providers) == 0 {
		return nil, 0, 0, 0, s.ports.Unavailable(ctx, req.RequestedModel, req.routingModel(), false, PlatformFilterStats{}.Summary("grok_free_quota_soft_gate"))
	}
	// The unrestricted platform pool can contain Grok providers. Apply team and
	// model cooldown filters before per-request ranking so their temporary
	// failures remain candidate exclusions.
	if req.Platform == "" || req.Platform == capability.PlatformGrok {
		now := s.now()
		filtered := s.ports.FilterTeamLimited(providers, req.RequestedModel, now)
		if len(filtered) == 0 && len(providers) > 0 {
			return nil, 0, 0, 0, s.ports.Unavailable(ctx, req.RequestedModel, req.routingModel(), false, PlatformFilterStats{}.Summary("grok_team_model_rate_limit"))
		}
		if filtered != nil {
			providers = filtered
		}
		modelFiltered := s.ports.FilterModelQuota(providers, req.RequestedModel, now)
		if len(modelFiltered) == 0 && len(providers) > 0 {
			return nil, 0, 0, 0, s.ports.Unavailable(ctx, req.RequestedModel, req.routingModel(), false, PlatformFilterStats{}.Summary("grok_model_quota_block"))
		}
		providers = modelFiltered
	}

	filterStats := PlatformFilterStats{Pool: len(providers)}
	filtered := make([]*FlowProvider, 0, len(providers))
	loadReq := make([]ProviderWithConcurrency, 0, len(providers))
	for i := range providers {
		provider := &providers[i]
		if req.ExcludedIDs != nil {
			if _, excluded := req.ExcludedIDs[provider.ID]; excluded {
				filterStats.Exclude("excluded")
				continue
			}
		}
		if !s.ports.MatchesGroup(provider, req.GroupID) {
			filterStats.Exclude("group_mismatch")
			continue
		}
		if !s.ports.IsSchedulable(provider) {
			filterStats.Exclude("not_schedulable")
			continue
		}
		if req.Platform != "" && provider.Platform != strings.TrimSpace(req.Platform) || !s.ports.IsCompatible(provider) {
			filterStats.Exclude("platform_mismatch")
			continue
		}
		if s.ports.RuntimeBlocked(provider, req.routingModel()) {
			filterStats.Exclude("runtime_blocked")
			continue
		}
		if req.RequirePrivacySet && !provider.IsPrivacySet() {
			filterStats.Exclude("privacy_not_set")
			continue
		}
		if compatible, reason := s.ports.RequestCompatible(ctx, provider, req); !compatible {
			filterStats.Exclude(reason)
			continue
		}
		if !s.ports.TransportCompatible(provider, req.RequiredTransport) {
			filterStats.Exclude("transport_incompatible")
			continue
		}
		filtered = append(filtered, provider)
		loadReq = append(loadReq, ProviderWithConcurrency{
			ID:             provider.ID,
			MaxConcurrency: provider.EffectiveLoadFactor(),
		})
	}
	if len(filtered) == 0 {
		return nil, 0, 0, 0, s.ports.Unavailable(ctx, req.RequestedModel, req.routingModel(), false, filterStats.Summary(""), providers)
	}

	loadMap := map[int64]*ProviderLoadInfo{}
	if s.concurrency != nil {
		// Load observation failure leaves the empty map in place. Shared scoring
		// treats unknown load neutrally and later physical acquisition decides
		// capacity.
		if batchLoad, loadErr := s.concurrency.GetProvidersLoadBatch(ctx, loadReq); loadErr == nil {
			loadMap = batchLoad
		}
	}

	if req.SubscriptionPriority {
		subscriptionProviders, regularProviders := s.partitionSubscription(filtered)
		if len(subscriptionProviders) > 0 {
			attempt := s.trySelectByLoadBalancePool(ctx, req, subscriptionProviders, loadMap, budget)
			if attempt.err != nil && (!attempt.noCompactCandidates || len(regularProviders) <= 0) {
				return nil, attempt.candidateCount, attempt.topK, attempt.loadSkew, attempt.err
			}
			if attempt.result != nil {
				return attempt.result, attempt.candidateCount, attempt.topK, attempt.loadSkew, nil
			}
			if len(regularProviders) > 0 {
				regularAttempt := s.trySelectByLoadBalancePool(ctx, req, regularProviders, loadMap, budget)
				if regularAttempt.err != nil && !regularAttempt.noCompactCandidates {
					return nil, regularAttempt.candidateCount, regularAttempt.topK, regularAttempt.loadSkew, regularAttempt.err
				}
				if regularAttempt.result != nil {
					return regularAttempt.result, regularAttempt.candidateCount, regularAttempt.topK, regularAttempt.loadSkew, nil
				}
				var result *FlowSelection
				candidateCount, topK, loadSkew := regularAttempt.candidateCount, regularAttempt.topK, regularAttempt.loadSkew
				fallbackErr := regularAttempt.err
				if regularAttempt.err == nil {
					result, candidateCount, topK, loadSkew, fallbackErr = s.finishLoadBalanceSelectionFallback(ctx, req, regularAttempt, budget, filterStats)
					if fallbackErr == nil && result != nil {
						return result, candidateCount, topK, loadSkew, nil
					}
				}
				// Regular acquisition or waiting failed to produce a usable choice,
				// including compact-specific exhaustion. The earlier busy subscription
				// order can still yield an eligible wait plan before the regular failure
				// is returned.
				subResult, subCandidateCount, subTopK, subLoadSkew, subErr := s.finishLoadBalanceSelectionFallback(ctx, req, attempt, budget, filterStats)
				if subErr == nil && subResult != nil {
					return subResult, subCandidateCount, subTopK, subLoadSkew, nil
				}
				return result, candidateCount, topK, loadSkew, fallbackErr
			}
			return s.finishLoadBalanceSelectionFallback(ctx, req, attempt, budget, filterStats)
		}
	}

	attempt := s.trySelectByLoadBalancePool(ctx, req, filtered, loadMap, budget)
	if attempt.err != nil {
		return nil, attempt.candidateCount, attempt.topK, attempt.loadSkew, attempt.err
	}
	if attempt.result != nil {
		return attempt.result, attempt.candidateCount, attempt.topK, attempt.loadSkew, nil
	}
	return s.finishLoadBalanceSelectionFallback(ctx, req, attempt, budget, filterStats)
}

// trySelectByLoadBalancePool attempts one ranked provider pool and returns its
// order for later waiting. Compact exhaustion is distinguished from an
// ordinary empty order, so subscription orchestration can decide whether
// another pool is worth trying. Acquisition errors stop this attempt.
//
// If the initial order produces no result and the acquisition budget permits
// it, one fresh batch-load read can rebuild and retry the order. A refresh
// error retains the initial fallback metadata. After an error-free retry,
// a nonempty fresh order replaces the fallback order and counters even when
// no provider was acquired, keeping later waiting aligned with that ranking.
func (s *PlatformSelector) trySelectByLoadBalancePool(
	ctx context.Context,
	req PlatformSelectionInput,
	filtered []*FlowProvider,
	loadMap map[int64]*ProviderLoadInfo,
	budget *ProbeBudget,
) platformPoolAttempt {
	plan := s.BuildPlan(ctx, req, filtered, loadMap)
	attempt := platformPoolAttempt{
		selectionOrder: plan.selectionOrder,
		candidateCount: plan.candidateCount,
		topK:           plan.topK,
		loadSkew:       plan.loadSkew,
	}
	if req.RequireCompact && len(plan.candidates) == 0 && len(plan.staleSnapshotCompactRetry) == 0 {
		attempt.noCompactCandidates = true
		attempt.err = ErrNoAvailableCompactProviders
		return attempt
	}
	if req.RequireCompact && len(attempt.selectionOrder) == 0 && !s.ports.SnapshotAvailable {
		attempt.noCompactCandidates = true
		attempt.err = ErrNoAvailableCompactProviders
		return attempt
	}
	if len(attempt.selectionOrder) == 0 {
		attempt.compactBlocked = req.RequireCompact && len(plan.allCandidates) > 0
		return attempt
	}

	result, compactBlocked, acquireErr := s.TryOrder(ctx, req, attempt.selectionOrder, budget)
	attempt.compactBlocked = compactBlocked
	if acquireErr != nil {
		attempt.err = acquireErr
		return attempt
	}
	if result != nil {
		attempt.result = result
		return attempt
	}

	// The initial pool had no acquired result. Retry one fresh load sample while
	// acquisition budget remains; a read failure keeps the initial order
	// available for waiting.
	if s.concurrency != nil && !budget.acquireExhausted() {
		loadReq := buildOpenAIProviderLoadRequest(filtered)
		if freshLoadMap, loadErr := s.concurrency.GetProvidersLoadBatchFresh(ctx, loadReq); loadErr == nil {
			freshPlan := s.BuildPlan(ctx, req, filtered, freshLoadMap)
			if len(freshPlan.selectionOrder) > 0 {
				freshResult, freshCompactBlocked, freshAcquireErr := s.TryOrder(ctx, req, freshPlan.selectionOrder, budget)
				if freshAcquireErr != nil {
					attempt.err = freshAcquireErr
					return attempt
				}
				if freshResult != nil {
					attempt.result = freshResult
					attempt.selectionOrder = freshPlan.selectionOrder
					attempt.candidateCount = freshPlan.candidateCount
					attempt.topK = freshPlan.topK
					attempt.loadSkew = freshPlan.loadSkew
					return attempt
				}
				attempt.compactBlocked = attempt.compactBlocked || freshCompactBlocked
				attempt.selectionOrder = freshPlan.selectionOrder
				attempt.candidateCount = freshPlan.candidateCount
				attempt.topK = freshPlan.topK
				attempt.loadSkew = freshPlan.loadSkew
			}
		}
	}

	return attempt
}

// finishLoadBalanceSelectionFallback returns the first currently eligible wait
// choice from a failed pool attempt. An empty order or exhausted
// current-record checks produces a model-aware unavailable error. Ordinary
// unlimited selection preserves order in one pass.
//
// A limited budget partitions that order into unattempted/not-full,
// attempted/not-full, unattempted/known-full, and attempted/known-full passes.
// Known fullness compares observed physical concurrency with the slot limit.
// Each candidate gets Fresh, request/transport checks, and an admitted
// database recheck before compact qualification. The result describes physical
// capacity and ordinary queue limits; it creates no slot or queue membership
// itself.
func (s *PlatformSelector) finishLoadBalanceSelectionFallback(
	ctx context.Context,
	req PlatformSelectionInput,
	attempt platformPoolAttempt,
	budget *ProbeBudget,
	filterStats PlatformFilterStats,
) (*FlowSelection, int, int, float64, error) {
	candidateCount := attempt.candidateCount
	topK := attempt.topK
	loadSkew := attempt.loadSkew

	if len(attempt.selectionOrder) == 0 {
		return nil, candidateCount, topK, loadSkew, s.ports.Unavailable(ctx, req.RequestedModel, req.routingModel(), attempt.compactBlocked, filterStats.Summary("selection_order_empty"))
	}

	cfg := s.ports.Options()
	compactBlocked := attempt.compactBlocked
	// A bounded fallback searches four disjoint attempted/fullness classes before
	// choosing a wait. Its fullness test and returned plan use physical capacity
	// rather than the score denominator.
	passes := 1
	if budget != nil && budget.limited {
		passes = 4
	}
	// Fresh and Recheck use relaxed compact checks to reach current records. The
	// explicit CompactAllowed check after those reads decides final eligibility
	// for a compact wait.
	for pass := range passes {
		wantAttempted := pass == 1 || pass == 3
		wantKnownFull := pass >= 2
		for _, candidate := range attempt.selectionOrder {
			if candidate.Provider == nil {
				continue
			}
			if budget != nil && budget.limited {
				knownFull := candidate.LoadKnown && candidate.Provider.Concurrency > 0 &&
					candidate.LoadInfo.CurrentConcurrency >= candidate.Provider.Concurrency
				if budget.wasAttempted(candidate.Provider.ID) != wantAttempted || knownFull != wantKnownFull {
					continue
				}
			}
			fresh := s.ports.Fresh(ctx, candidate.Provider, req.Platform, req.routingModel(), false, req.RequiredCapability)
			if fresh == nil || !s.ports.TransportCompatible(fresh, req.RequiredTransport) || !s.requestCompatible(ctx, fresh, req) {
				continue
			}
			if !s.canRecheck(budget) {
				return nil, candidateCount, topK, loadSkew, s.ports.Unavailable(ctx, req.RequestedModel, req.routingModel(), compactBlocked, filterStats.Summary("selection_order_exhausted"))
			}
			fresh = s.ports.Recheck(ctx, fresh, req.GroupID, req.Platform, req.routingModel(), false, req.RequiredCapability)
			if fresh == nil || !s.ports.TransportCompatible(fresh, req.RequiredTransport) || !s.requestCompatible(ctx, fresh, req) {
				continue
			}
			if req.RequireCompact && !s.ports.CompactAllowed(fresh) {
				compactBlocked = true
				continue
			}
			return &FlowSelection{
				Provider: fresh,
				WaitPlan: &ProviderWaitPlan{
					ProviderID:     fresh.ID,
					MaxConcurrency: fresh.Concurrency,
					Timeout:        cfg.FallbackWaitTimeout,
					MaxWaiting:     cfg.FallbackMaxWaiting,
				},
			}, candidateCount, topK, loadSkew, nil
		}
	}

	return nil, candidateCount, topK, loadSkew, s.ports.Unavailable(ctx, req.RequestedModel, req.routingModel(), compactBlocked, filterStats.Summary("selection_order_exhausted"))
}

// sortOpenAICompactRetryCandidates returns stale compact records in a copied
// order for current eligibility retries. Empty input returns nil; each
// nonempty record requires nonnil provider and load metadata. The result
// borrows those records, so callers keep their observations unchanged while
// the comparison runs.
//
// Lower priority wins first, then lower stored LoadRate, lower WaitingCount,
// and finally never-used or earlier exact LastUsedAt. BuildPlan supplies
// zero-valued load records for unknown observations, so those records can
// precede measured positive loads in this retry order. Fresh and Recheck still
// determine current compact eligibility after these records follow numeric
// Top-K attempts. Stable equal comparisons preserve the input order.
func sortOpenAICompactRetryCandidates(pool []PlatformCandidateScore) []PlatformCandidateScore {
	if len(pool) == 0 {
		return nil
	}
	ordered := append([]PlatformCandidateScore(nil), pool...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.Provider.Priority != b.Provider.Priority {
			return a.Provider.Priority < b.Provider.Priority
		}
		if a.LoadInfo.LoadRate != b.LoadInfo.LoadRate {
			return a.LoadInfo.LoadRate < b.LoadInfo.LoadRate
		}
		if a.LoadInfo.WaitingCount != b.LoadInfo.WaitingCount {
			return a.LoadInfo.WaitingCount < b.LoadInfo.WaitingCount
		}
		switch {
		case a.Provider.LastUsedAt == nil && b.Provider.LastUsedAt != nil:
			return true
		case a.Provider.LastUsedAt != nil && b.Provider.LastUsedAt == nil:
			return false
		case a.Provider.LastUsedAt == nil && b.Provider.LastUsedAt == nil:
			return false
		default:
			return a.Provider.LastUsedAt.Before(*b.Provider.LastUsedAt)
		}
	})
	return ordered
}

// buildOpenAIProviderLoadRequest returns an allocated load-query slice for
// nonnil metadata. MaxConcurrency carries EffectiveLoadFactor as the
// observed-load denominator here, while physical acquisition uses the
// provider's distinct Concurrency value.
func buildOpenAIProviderLoadRequest(providers []*FlowProvider) []ProviderWithConcurrency {
	loadReq := make([]ProviderWithConcurrency, 0, len(providers))
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		loadReq = append(loadReq, ProviderWithConcurrency{
			ID:             provider.ID,
			MaxConcurrency: provider.EffectiveLoadFactor(),
		})
	}
	return loadReq
}
