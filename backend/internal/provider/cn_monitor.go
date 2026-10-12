package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	CNUsageMonitorSnapshotVersion = 1
	cnUsageMonitorLeaderLockKey   = "cn:usage:monitor:leader"
	cnUsageMonitorReasonPrefix    = "cn_usage_monitor:"
)

var ErrCNMonitorStopped = errors.New("CN usage monitor is stopped")

// CNMonitorStore supplies monitor reads, snapshot CAS, and identity-conditional health writes.
type CNMonitorStore interface {
	GetByID(context.Context, int64) (*Record, error)
	ListByPlatform(context.Context, string) ([]Record, error)
	UpdateCNUsageMonitorSnapshotCAS(context.Context, int64, time.Time, *CNUsageMonitorSnapshot, string) (bool, error)
	SetCNUsageDecisionCAS(context.Context, int64, time.Time, time.Time, string, bool) (bool, error)
}
type CNMonitorQueries interface {
	QueryProvider(context.Context, int64) (*UpstreamUsageQueryResult, error)
}
type CNMonitorLeader interface {
	TryAcquireLeaderLock(context.Context, string, string, time.Duration) (bool, error)
	ReleaseLeaderLock(context.Context, string, string) error
}
type CNMonitorOptions struct {
	Enabled                              bool
	Interval, ProbeTimeout, RoundTimeout time.Duration
	Concurrency                          int
	BalanceThreshold                     float64
	HostPolicy                           egress.MonitorHostPolicy
	Now                                  func() time.Time
	InstanceID                           string
	Leader                               CNMonitorLeader
	Advisory                             func(context.Context, string) (func(), bool)
	Warn, Debug                          func(string, ...any)
}

// CNUsageMonitor owns periodic polling, identity rechecks, and health decisions.
type CNUsageMonitor struct {
	providerRepo                         CNMonitorStore
	usageService                         CNMonitorQueries
	options                              CNMonitorOptions
	interval, probeTimeout, roundTimeout time.Duration
	concurrency                          int
	mu                                   sync.Mutex
	started, stopped                     bool
	cancel                               context.CancelFunc
	activity                             operationActivity
}

func NewCNUsageMonitor(store CNMonitorStore, queries CNMonitorQueries, options CNMonitorOptions) *CNUsageMonitor {
	if options.Interval <= 0 {
		options.Interval = 10 * time.Minute
	}
	if options.ProbeTimeout <= 0 {
		options.ProbeTimeout = 20 * time.Second
	}
	if options.RoundTimeout <= 0 {
		options.RoundTimeout = 5 * time.Minute
	}
	if options.Concurrency <= 0 {
		options.Concurrency = 4
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Warn == nil {
		options.Warn = func(string, ...any) {}
	}
	if options.Debug == nil {
		options.Debug = func(string, ...any) {}
	}
	options.HostPolicy = options.HostPolicy.Clone()
	return &CNUsageMonitor{providerRepo: store, usageService: queries, options: options, interval: options.Interval, probeTimeout: options.ProbeTimeout, roundTimeout: options.RoundTimeout, concurrency: options.Concurrency}
}

func (s *CNUsageMonitor) StartContext(parent context.Context) error {
	if s == nil || s.providerRepo == nil || s.usageService == nil || !s.options.Enabled {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped {
		return nil
	}
	loopParent, cancel := context.WithCancel(parent)
	ctx, finish, err := s.activity.begin(loopParent, ErrCNMonitorStopped)
	if err != nil {
		cancel()
		return err
	}
	s.started = true
	s.cancel = cancel
	go func() {
		defer cancel()
		defer finish()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.RunOnce(ctx)
			}
		}
	}()
	return nil
}

func (s *CNUsageMonitor) Stop() { _ = s.StopContext(context.Background()) }
func (s *CNUsageMonitor) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	return s.activity.stop(ctx, "CN usage monitor")
}

// acquireLease skips contention, falls back from Redis errors to the database, and permits execution if neither backend exists.
func (s *CNUsageMonitor) acquireLease(ctx context.Context) (func(), bool) {
	return AcquireSingletonLease(ctx, s.options.Leader, s.options.Advisory, cnUsageMonitorLeaderLockKey, s.options.InstanceID, s.roundTimeout+30*time.Second)
}

func (s *CNUsageMonitor) RunOnce(parents ...context.Context) {
	parent := context.Background()
	if len(parents) > 0 && parents[0] != nil {
		parent = parents[0]
	}
	if s == nil {
		return
	}
	if s.providerRepo == nil || s.usageService == nil {
		return
	}
	parent, finish, err := s.activity.begin(parent, ErrCNMonitorStopped)
	if err != nil {
		return
	}
	defer finish()
	roundCtx, cancel := context.WithTimeout(parent, s.roundTimeout)
	defer cancel()
	release, acquired := s.acquireLease(roundCtx)
	if !acquired {
		return
	}
	defer release()

	providers := s.monitorCandidates(roundCtx)
	group, groupCtx := errgroup.WithContext(roundCtx)
	group.SetLimit(s.concurrency)
	for i := range providers {
		providerID := providers[i].ID
		group.Go(func() error {
			s.probeOne(groupCtx, providerID)
			return nil
		})
	}
	_ = group.Wait()
}

func (s *CNUsageMonitor) monitorCandidates(ctx context.Context) []Record {
	result := make([]Record, 0)
	for _, platform := range []string{PlatformKimi, PlatformZhipu, PlatformDeepseek} {
		if ctx.Err() != nil {
			return result
		}
		providers, err := s.providerRepo.ListByPlatform(ctx, platform)
		if err != nil {
			s.options.Warn("cn_usage_monitor_list_failed", "platform", platform, "error", err)
			continue
		}
		for i := range providers {
			provider := providers[i]
			if provider.Type != ProviderTypeAPIKey || provider.Status != StatusActive {
				continue
			}
			queryConfig, err := EffectiveUpstreamUsageConfig(&provider)
			if err != nil || !queryConfig.Enabled || CNUpstreamUsageAdapterName(&provider) == "" {
				continue
			}
			result = append(result, provider)
		}
	}
	return result
}

func (s *CNUsageMonitor) probeOne(parent context.Context, providerID int64) {
	if parent.Err() != nil {
		return
	}
	provider, err := s.providerRepo.GetByID(parent, providerID)
	if err != nil || provider == nil {
		return
	}
	queryConfig, err := EffectiveUpstreamUsageConfig(provider)
	if err != nil || !queryConfig.Enabled {
		return
	}
	queryConfig.Adapter = CNUpstreamUsageAdapterName(provider)
	if queryConfig.Adapter == "" {
		return
	}
	identityHash := cnMonitorFingerprint(provider, queryConfig)
	if err := s.validateMonitorHost(provider, queryConfig); err != nil {
		s.persistAttempt(parent, provider, queryConfig, identityHash, nil, err)
		return
	}

	probeCtx, cancel := context.WithTimeout(parent, s.probeTimeout)
	result, queryErr := s.usageService.QueryProvider(probeCtx, providerID)
	cancel()
	if queryErr == nil && result != nil {
		s.applyBalanceDecision(parent, providerID, identityHash, result)
	}
	current, err := s.providerRepo.GetByID(parent, providerID)
	if err != nil || current == nil {
		return
	}
	s.persistAttempt(parent, current, queryConfig, identityHash, result, queryErr)
}

func (s *CNUsageMonitor) persistAttempt(
	ctx context.Context,
	provider *Record,
	queryConfig UpstreamUsageQueryConfig,
	identityHash string,
	result *UpstreamUsageQueryResult,
	queryErr error,
) {
	if provider == nil || ctx.Err() != nil {
		return
	}
	currentConfig, err := EffectiveUpstreamUsageConfig(provider)
	if err != nil {
		return
	}
	currentConfig.Adapter = CNUpstreamUsageAdapterName(provider)
	if currentConfig != queryConfig || cnMonitorFingerprint(provider, currentConfig) != identityHash {
		return
	}
	now := s.options.Now().UTC()
	snapshot := CNUsageMonitorSnapshotFromExtra(provider.Extra)
	if snapshot == nil || snapshot.Version != CNUsageMonitorSnapshotVersion || snapshot.IdentityHash != identityHash {
		snapshot = &CNUsageMonitorSnapshot{
			Version:      CNUsageMonitorSnapshotVersion,
			Adapter:      queryConfig.Adapter,
			IdentityHash: identityHash,
		}
	}
	snapshot.LastAttemptAt = now
	if queryErr != nil || result == nil {
		code := apperror.Reason(queryErr)
		if code == "" {
			code = "CN_USAGE_MONITOR_QUERY_FAILED"
		}
		snapshot.LastError = &CNUsageMonitorError{Code: code, ObservedAt: now}
	} else {
		observedAt := result.ObservedAt.UTC()
		snapshot.Adapter = result.Adapter
		snapshot.Provider = result.Provider
		snapshot.Mode = result.Mode
		snapshot.Unit = result.Unit
		snapshot.Balance = result.Balance
		snapshot.Balances = result.Balances
		snapshot.Available = result.Available
		snapshot.Limits = result.Limits
		snapshot.Subscription = result.Subscription
		snapshot.ExpiresAt = result.ExpiresAt
		snapshot.ObservedAt = &observedAt
		snapshot.LastError = nil
	}
	written, err := s.providerRepo.UpdateCNUsageMonitorSnapshotCAS(
		ctx,
		provider.ID,
		provider.UpdatedAt,
		snapshot,
		"",
	)
	if err != nil {
		s.options.Warn("cn_usage_monitor_snapshot_failed", "provider_id", provider.ID, "error", err)
	} else if !written {
		s.options.Debug("cn_usage_monitor_snapshot_stale", "provider_id", provider.ID)
	}
}

func (s *CNUsageMonitor) applyBalanceDecision(
	ctx context.Context,
	providerID int64,
	identityHash string,
	result *UpstreamUsageQueryResult,
) {
	if result == nil || result.Mode != "balance" || ctx.Err() != nil {
		return
	}
	provider, err := s.providerRepo.GetByID(ctx, providerID)
	if err != nil || provider == nil {
		return
	}
	queryConfig, err := EffectiveUpstreamUsageConfig(provider)
	if err != nil {
		return
	}
	queryConfig.Adapter = CNUpstreamUsageAdapterName(provider)
	if cnMonitorFingerprint(provider, queryConfig) != identityHash {
		return
	}
	threshold := s.options.BalanceThreshold
	low, known := CNUsageBalanceBelowThreshold(result, threshold)
	if !known {
		return
	}
	reason := CNUsageMonitorReason(identityHash)
	if low {
		if !provider.IsSchedulable() {
			return
		}
		until := s.options.Now().Add(2 * s.interval)
		if _, err := s.providerRepo.SetCNUsageDecisionCAS(ctx, provider.ID, provider.UpdatedAt, until, reason, false); err != nil {
			s.options.Warn("cn_usage_monitor_pause_failed", "provider_id", provider.ID, "error", err)
		}
		return
	}
	if provider.TempUnschedulableUntil != nil && provider.TempUnschedulableReason == reason {
		if _, err := s.providerRepo.SetCNUsageDecisionCAS(ctx, provider.ID, provider.UpdatedAt, time.Time{}, reason, true); err != nil {
			s.options.Warn("cn_usage_monitor_resume_failed", "provider_id", provider.ID, "error", err)
		}
	}
}

func CNUsageBalanceBelowThreshold(result *UpstreamUsageQueryResult, threshold float64) (bool, bool) {
	if result == nil || result.Mode != "balance" {
		return false, false
	}
	if result.Available != nil && !*result.Available {
		return true, true
	}
	if len(result.Balances) > 0 {
		for _, balance := range result.Balances {
			if balance.Remaining >= threshold {
				return false, true
			}
		}
		return true, true
	}
	if result.Balance == nil || result.Balance.Remaining == nil {
		return false, false
	}
	return *result.Balance.Remaining < threshold, true
}

func CNUsageMonitorReason(identityHash string) string {
	return cnUsageMonitorReasonPrefix + identityHash + ": 余额低于监控阈值"
}

func CNUsageMonitorSnapshotFromExtra(extra map[string]any) *CNUsageMonitorSnapshot {
	if len(extra) == 0 || extra[CNUsageMonitorSnapshotExtraKey] == nil {
		return nil
	}
	payload, err := json.Marshal(extra[CNUsageMonitorSnapshotExtraKey])
	if err != nil {
		return nil
	}
	var snapshot CNUsageMonitorSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return nil
	}
	return &snapshot
}

// ValidCNUsageMonitorSnapshot returns a snapshot only when its query identity matches the current provider.
func ValidCNUsageMonitorSnapshot(provider *Record) *CNUsageMonitorSnapshot {
	if provider == nil || !provider.IsCNProvider() {
		return nil
	}
	queryConfig, err := EffectiveUpstreamUsageConfig(provider)
	if err != nil {
		return nil
	}
	queryConfig.Adapter = CNUpstreamUsageAdapterName(provider)
	if queryConfig.Adapter == "" {
		return nil
	}
	snapshot := CNUsageMonitorSnapshotFromExtra(provider.Extra)
	if snapshot == nil || snapshot.Version != CNUsageMonitorSnapshotVersion ||
		snapshot.Adapter != queryConfig.Adapter ||
		snapshot.IdentityHash != cnMonitorFingerprint(provider, queryConfig) {
		return nil
	}
	return snapshot
}

func CNUsageOfficialHost(platform, host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	switch platform {
	case PlatformKimi:
		return host == "api.moonshot.cn" || host == "api.kimi.com"
	case PlatformZhipu:
		return host == "open.bigmodel.cn" || host == "api.z.ai"
	case PlatformDeepseek:
		return host == "api.deepseek.com"
	default:
		return false
	}
}

func cnMonitorFingerprint(value *Record, config UpstreamUsageQueryConfig) string {
	return UpstreamUsageContextFingerprint(value, config, value.OpenAIBaseURL(value.ConfiguredAPIProtocol() == APIProtocolAdaptive))
}

func (s *CNUsageMonitor) validateMonitorHost(value *Record, config UpstreamUsageQueryConfig) error {
	base := strings.TrimSpace(config.BaseURL)
	if base == "" {
		base = value.OpenAIBaseURL(value.ConfiguredAPIProtocol() == APIProtocolAdaptive)
	}
	err := s.options.HostPolicy.Validate(base, func(host string) bool { return CNUsageOfficialHost(value.Platform, host) })
	if errors.Is(err, egress.ErrMonitorURLInvalid) {
		return ErrUpstreamUsageConfigInvalid
	}
	return err
}
