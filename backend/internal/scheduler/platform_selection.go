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

const (
	openAIProviderScheduleLayerPreviousResponse = "previous_response_id"
	openAIProviderScheduleLayerGuardianParent   = "guardian_parent"
	openAIProviderScheduleLayerSessionSticky    = "session_hash"
	openAIProviderScheduleLayerLoadBalance      = "load_balance"
	openAIProviderSelectionProbeLimit           = 64
)

var ErrNoAvailableCompactProviders = errors.New("no available providers support /responses/compact")

type PlatformSelectionInput struct {
	GroupID                  *int64
	Platform                 string
	SessionHash              string
	StickyProviderID         int64
	GuardianParentProviderID int64
	StickyPreviousProviderID int64
	StickyWeighted           bool
	SubscriptionPriority     bool
	PreserveStickyBinding    bool
	RequirePrivacySet        bool
	PreviousResponseID       string
	PreviousResponseCanMove  bool
	RequestedModel           string // Client model R, used for restrictions, errors, and session identity.
	RoutingModel             string // Provider-layer model: C for ordinary requests, group-mapped D for Messages.
	RequiredTransport        string
	RequiredCapability       provider.OpenAIEndpointCapability
	RequiredImageCapability  provider.OpenAIImagesCapability
	RequireCompact           bool
	ExcludedIDs              map[int64]struct{}
	// AdvancedSchedulerFeedbackConfig and StickyEscapeConfig capture the effective policy for this request.
	AdvancedSchedulerFeedbackConfig policy.FeedbackConfig
	StickyEscapeConfig              policy.StickyEscapeConfig
}

type PlatformDecision struct {
	Layer                string
	StickyPreviousHit    bool
	StickySessionHit     bool
	CandidateCount       int
	TopK                 int
	LatencyMs            int64
	LoadSkew             float64
	SelectedProviderID   int64
	SelectedProviderType string
}
type PlatformMetricsSnapshot struct {
	SelectTotal               int64
	StickyPreviousHitTotal    int64
	StickySessionHitTotal     int64
	LoadBalanceSelectTotal    int64
	ProviderSwitchTotal       int64
	SchedulerLatencyMsTotal   int64
	SchedulerLatencyMsAvg     float64
	StickyHitRatio            float64
	ProviderSwitchRate        float64
	LoadSkewAvg               float64
	RuntimeStatsProviderCount int
}
type PlatformMetrics struct {
	selectTotal            atomic.Int64
	stickyPreviousHitTotal atomic.Int64
	stickySessionHitTotal  atomic.Int64
	loadBalanceSelectTotal atomic.Int64
	providerSwitchTotal    atomic.Int64
	latencyMsTotal         atomic.Int64
	loadSkewMilliTotal     atomic.Int64
}

type ProbeBudget struct {
	acquires  int
	rechecks  int
	attempted map[int64]struct{}
	limited   bool
}

// PlatformCandidateScore associates shared scoring results with this request's credential-free candidate data.
type PlatformCandidateScore struct {
	Provider                                                         *FlowProvider
	LoadInfo                                                         *ProviderLoadInfo
	LoadKnown                                                        bool
	Score, BaseScore, StickyBonus, PreviousBonus, SessionStickyBonus float64
	Priority                                                         int
	ErrorRate, TTFT                                                  float64
	HasTTFT, HasFeedback                                             bool
	Feedback                                                         FeedbackSnapshot
	Factors                                                          CandidateFactors
}

// PlatformSelectionPorts supplies eligibility checks; PlatformSelector orders scoring, bindings, slot acquisition, and waiting.
type PlatformSelectionPorts struct {
	BasicStickyTTL       time.Duration
	CheckPricing         func(context.Context, *int64, string) bool
	Hydrate              func(context.Context, *FlowProvider) (*FlowProvider, error)
	SetSticky            func(context.Context, *int64, string, int64, time.Duration) error
	PrivacyAllowed       func(context.Context, *int64, *FlowProvider) bool
	ShadowAllowed        func(context.Context, *FlowProvider) bool
	ParentHealthy        func(*FlowProvider, func(int64) *FlowProvider) bool
	ParentLookup         func(context.Context) func(int64) *FlowProvider
	ReadProviderDB       func(context.Context, int64) (*FlowProvider, error)
	NeedsGroupCheck      func(context.Context, *int64) bool
	GroupModelRestricted func(context.Context, int64, *FlowProvider, string, bool) bool
	BasicEligible        func(context.Context, *FlowProvider, string, string, bool, provider.OpenAIEndpointCapability) bool
	BasicFailureReason   func(context.Context, *FlowProvider, string, string, bool, provider.OpenAIEndpointCapability) string
	CompleteAcquired     func(context.Context, *FlowProvider, func()) (*FlowSelection, error)
	Complete             func(context.Context, *FlowProvider, bool, func(), *ProviderWaitPlan) (*FlowSelection, error)

	Available, CacheAvailable, SnapshotAvailable, RecheckAvailable bool
	Effective                                                      func(context.Context, *int64) policy.EffectiveSettings
	GroupRequiresPrivacy                                           func(context.Context, *int64) bool
	PreviousResponse                                               func(context.Context, *int64, string, string, map[int64]struct{}, provider.OpenAIEndpointCapability, bool) (*FlowSelection, error)
	RequestCompatible                                              func(context.Context, *FlowProvider, PlatformSelectionInput) (bool, string)
	TransportCompatible                                            func(*FlowProvider, string) bool
	HasGroupMetadata                                               func(*FlowProvider) bool
	MatchesGroup                                                   func(*FlowProvider, *int64) bool
	BindSticky                                                     func(context.Context, *int64, string, int64) error
	DeleteSticky                                                   func(context.Context, *int64, string) error
	GetSticky                                                      func(context.Context, *int64, string) (int64, error)
	RefreshSticky                                                  func(context.Context, *int64, string, time.Duration) error
	StickyTTL                                                      func() time.Duration
	Options                                                        func() FlowOptions
	GetSchedulable                                                 func(context.Context, int64) (*FlowProvider, error)
	ClearSticky                                                    func(*FlowProvider, string) bool
	IsCompatible                                                   func(*FlowProvider) bool
	IsSchedulable                                                  func(*FlowProvider) bool
	Recheck                                                        func(context.Context, *FlowProvider, *int64, string, string, bool, provider.OpenAIEndpointCapability) *FlowProvider
	Fresh                                                          func(context.Context, *FlowProvider, string, string, bool, provider.OpenAIEndpointCapability) *FlowProvider
	FreeQuota                                                      func(context.Context, []FlowProvider) []FlowProvider
	CanonicalModel                                                 func(*FlowProvider, string) string
	TeamLimited                                                    func(*FlowProvider, string, time.Time) bool
	ModelQuotaBlocked                                              func(int64, string, time.Time) bool
	Acquire                                                        func(context.Context, int64, int) (*AcquireResult, error)
	ListCandidates                                                 func(context.Context, *int64, string) ([]FlowProvider, error)
	RuntimeBlocked                                                 func(*FlowProvider, string) bool
	FilterTeamLimited                                              func([]FlowProvider, string, time.Time) []FlowProvider
	FilterModelQuota                                               func([]FlowProvider, string, time.Time) []FlowProvider
	CompactAllowed                                                 func(*FlowProvider) bool
	IsSubscription                                                 func(*FlowProvider) bool
	QuotaHeadroom                                                  func(*ScoreProvider, time.Time) float64
	Unavailable                                                    func(context.Context, string, string, bool, string, ...[]FlowProvider) error
}

// PlatformSelector shares feedback and counters while using each request's inputs and callbacks.
type PlatformSelector struct {
	ports       PlatformSelectionPorts
	concurrency *ConcurrencyService
	stats       *RuntimeStats
	metrics     *PlatformMetrics
	diagnostics Diagnostics
	now         func() time.Time
}

type PlatformLoadPlan struct {
	allCandidates             []PlatformCandidateScore
	candidates                []PlatformCandidateScore
	staleSnapshotCompactRetry []PlatformCandidateScore
	selectionOrder            []PlatformCandidateScore
	candidateCount            int
	topK                      int
	loadSkew                  float64
}
type platformPoolAttempt struct {
	result              *FlowSelection
	selectionOrder      []PlatformCandidateScore
	candidateCount      int
	topK                int
	loadSkew            float64
	compactBlocked      bool
	noCompactCandidates bool
	err                 error
}
type PlatformFilterStats struct {
	Pool    int
	Reasons map[string]int
}

func (r PlatformSelectionInput) routingModel() string {
	if model := strings.TrimSpace(r.RoutingModel); model != "" {
		return model
	}
	return r.RequestedModel
}

func (m *PlatformMetrics) RecordSelect(decision PlatformDecision) {
	if m == nil {
		return
	}
	m.selectTotal.Add(1)
	m.latencyMsTotal.Add(decision.LatencyMs)
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

func (m *PlatformMetrics) RecordSwitch() {
	if m == nil {
		return
	}
	m.providerSwitchTotal.Add(1)
}

func (m *PlatformMetrics) Snapshot(providerCount int) PlatformMetricsSnapshot {
	if m == nil {
		return PlatformMetricsSnapshot{}
	}

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

func NewProbeBudget() *ProbeBudget {
	return &ProbeBudget{attempted: make(map[int64]struct{})}
}

func (b *ProbeBudget) enableLimit() {
	if b != nil {
		b.limited = true
	}
}

func (b *ProbeBudget) recordAcquire(providerID int64) bool {
	if b == nil {
		return false
	}
	if !b.limited {
		return true
	}
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

func (b *ProbeBudget) recordRecheck() bool {
	if b == nil {
		return false
	}
	if !b.limited {
		return true
	}
	if b.rechecks >= openAIProviderSelectionProbeLimit {
		return false
	}
	b.rechecks++
	return true
}

func (b *ProbeBudget) acquireExhausted() bool {
	return b != nil && b.limited && b.acquires >= openAIProviderSelectionProbeLimit
}

func (b *ProbeBudget) wasAttempted(providerID int64) bool {
	if b == nil {
		return false
	}
	_, ok := b.attempted[providerID]
	return ok
}

func NewPlatformSelector(ports PlatformSelectionPorts, concurrency *ConcurrencyService, stats *RuntimeStats, metrics *PlatformMetrics, diagnostics Diagnostics, now func() time.Time) *PlatformSelector {
	return &PlatformSelector{ports: ports, concurrency: concurrency, stats: stats, metrics: metrics, diagnostics: diagnostics, now: now}
}

func (s *PlatformSelector) requestCompatible(ctx context.Context, a *FlowProvider, input PlatformSelectionInput) bool {
	ok, _ := s.ports.RequestCompatible(ctx, a, input)
	return ok
}

func (s *PlatformSelector) canRecheck(b *ProbeBudget) bool {
	if !s.ports.RecheckAvailable {
		return true
	}
	return b.recordRecheck()
}

func (s *PlatformFilterStats) Exclude(reason string) {
	if s.Reasons == nil {
		s.Reasons = make(map[string]int, 4)
	}
	s.Reasons[reason]++
}

func (s PlatformFilterStats) Summary(extra string) string {
	var b strings.Builder
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
		// Return the captured feedback settings so completion uses the policy selected for this request.
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
				_ = s.ports.BindSticky(ctx, req.GroupID, req.SessionHash, selection.Provider.ID)
			}
			return selection, decision, nil
		}
	}

	if req.GuardianParentProviderID > 0 {
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

func (s *PlatformSelector) SelectBySessionHash(
	ctx context.Context,
	req PlatformSelectionInput,
) (*FlowSelection, bool, error) {
	sessionHash := strings.TrimSpace(req.SessionHash)
	if sessionHash == "" || s == nil || !s.ports.Available || !s.ports.CacheAvailable {
		return nil, false, nil
	}

	clearBinding := func() {
		if !req.PreserveStickyBinding {
			_ = s.ports.DeleteSticky(ctx, req.GroupID, sessionHash)
		}
	}
	providerID := req.StickyProviderID
	if providerID <= 0 {
		var err error
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
	// Sticky selection rejects exhausted free OAuth providers; admin queries and import probes run separately.
	if provider != nil && len(s.ports.FreeQuota(ctx, []FlowProvider{*provider})) == 0 {
		clearBinding()
		return nil, false, nil
	}
	// During team/model cooldown, related providers in the team's 429 window are ineligible for stickiness.
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
	// WaitPlan uses Concurrency because it waits for physical Redis slots; EffectiveLoadFactor is a scoring denominator.
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

func (s *PlatformSelector) BuildPlan(
	ctx context.Context,
	req PlatformSelectionInput,
	filtered []*FlowProvider,
	loadMap map[int64]*ProviderLoadInfo,
) PlatformLoadPlan {
	allCandidates := make([]PlatformCandidateScore, 0, len(filtered))
	for _, provider := range filtered {
		loadInfo, loadKnown := loadMap[provider.ID]
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
	if plan.topK <= 0 {
		plan.topK = 1
	}

	plan.selectionOrder = s.buildOpenAISelectionOrder(req, plan)
	return plan
}

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
		if len(plan.staleSnapshotCompactRetry) > 0 && s.ports.SnapshotAvailable {
			selectionOrder = append(selectionOrder, sortOpenAICompactRetryCandidates(plan.staleSnapshotCompactRetry)...)
		}
		return selectionOrder
	}

	return buildSelectionOrder(plan.candidates)
}

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
	// Check Grok's local free-tier allowance here; admin probes run separately.
	providers = s.ports.FreeQuota(ctx, providers)
	if len(providers) == 0 {
		return nil, 0, 0, 0, s.ports.Unavailable(ctx, req.RequestedModel, req.routingModel(), false, PlatformFilterStats{}.Summary("grok_free_quota_soft_gate"))
	}
	// Providers in a rate-limited team skip the affected model during its cooldown.
	if req.Platform == "" || req.Platform == capability.PlatformGrok {
		now := s.now()
		filtered := s.ports.FilterTeamLimited(providers, req.RequestedModel, now)
		if len(filtered) == 0 && len(providers) > 0 {
			return nil, 0, 0, 0, s.ports.Unavailable(ctx, req.RequestedModel, req.routingModel(), false, PlatformFilterStats{}.Summary("grok_team_model_rate_limit"))
		}
		if filtered != nil {
			providers = filtered
		}
		// Apply free-quota blocks per provider and model so other models remain eligible.
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
		// Apply the current group's privacy requirement before scoring candidates.
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
				// If the ordinary pool can neither acquire nor queue a request, including when compact is unsupported,
				// try the subscription pool's wait plan. Busy subscription providers may still accept queued work.
				// Discarding that plan would fail requests that can wait successfully with subscription priority enabled.
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
	// WaitPlan uses Concurrency because it waits for physical Redis slots; EffectiveLoadFactor is a scoring denominator.
	passes := 1
	if budget != nil && budget.limited {
		passes = 4
	}
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
