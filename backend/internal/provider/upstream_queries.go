package provider

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	upstreamUsageTimeout     = 60 * time.Second
	upstreamUsageBatchLimit  = 100
	upstreamUsageConcurrency = 4
)

var ErrUpstreamUsageStopped = errors.New("upstream usage queries are stopped")

// UpstreamUsageReader reads the current provider identity and configuration.
type UpstreamUsageReader interface {
	GetByID(context.Context, int64) (*Record, error)
}

// UpstreamUsageExecution implements platform-specific usage queries.
type UpstreamUsageExecution interface {
	Available() bool
	Supports(string) bool
	BaseURL(*Record) string
	Query(context.Context, *Record, UpstreamUsageQueryConfig) (*UpstreamUsageInfo, error)
}
type UpstreamUsageOptions struct{ Now func() time.Time }

// UpstreamUsageService coalesces queries and owns concurrency limits, metrics, and shutdown waits.
type UpstreamUsageService struct {
	providerRepo UpstreamUsageReader
	execution    UpstreamUsageExecution
	queryFlight  singleflight.Group
	querySlots   chan struct{}
	slotMu       sync.Mutex
	now          func() time.Time
	metricsMu    sync.Mutex
	metrics      map[string]int64
	activity     operationActivity
}

func NewUpstreamUsageService(reader UpstreamUsageReader, execution UpstreamUsageExecution, options UpstreamUsageOptions) *UpstreamUsageService {
	return &UpstreamUsageService{providerRepo: reader, execution: execution, now: options.Now, querySlots: make(chan struct{}, upstreamUsageConcurrency), metrics: make(map[string]int64)}
}

// StopContext rejects new work, cancels shared queries, and waits until completion or the context expires.
func (s *UpstreamUsageService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.activity.stop(ctx, "upstream usage")
}

func (s *UpstreamUsageService) recordMetric(adapter, outcome string) {
	if s == nil {
		return
	}
	adapter = strings.TrimSpace(adapter)
	if adapter == "" {
		adapter = "unknown"
	}
	outcome = strings.TrimSpace(outcome)
	if outcome == "" {
		outcome = "unknown"
	}
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	if s.metrics == nil {
		s.metrics = make(map[string]int64)
	}
	s.metrics[adapter+":"+outcome]++
}

// SnapshotMetrics returns an independent copy of counts by adapter and outcome.
func (s *UpstreamUsageService) SnapshotMetrics() UpstreamUsageMetrics {
	result := UpstreamUsageMetrics{Counts: make(map[string]int64)}
	if s == nil {
		return result
	}
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	maps.Copy(result.Counts, s.metrics)
	return result
}

// QueryProvider fetches current upstream usage. Caller cancellation stops waiting while shared work continues.
func (s *UpstreamUsageService) QueryProvider(ctx context.Context, providerID int64) (*UpstreamUsageQueryResult, error) {
	if s == nil || s.providerRepo == nil || s.execution == nil || !s.execution.Available() {
		return nil, ErrUpstreamUsageUnavailable
	}
	if providerID <= 0 {
		return nil, ErrUpstreamUsageProviderInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, finish, err := s.activity.begin(ctx, ErrUpstreamUsageStopped)
	if err != nil {
		return nil, err
	}
	defer finish()
	queryDeadline := time.Now().Add(upstreamUsageTimeout)
	preflightCtx, cancelPreflight := context.WithDeadline(ctx, queryDeadline)
	defer cancelPreflight()
	// Read identity before choosing the singleflight key so credential, proxy,
	// and query configuration changes start a separate shared query.
	provider, err := s.loadQueryProvider(preflightCtx, providerID)
	if err != nil {
		return nil, err
	}
	queryConfig, err := EffectiveUpstreamUsageConfig(provider)
	if err != nil {
		return nil, err
	}
	if !queryConfig.Enabled {
		return nil, ErrUpstreamUsageDisabled
	}
	// CN platforms select adapters by platform and provider mode, preventing a
	// manual choice of a generic site adapter from overriding their usage protocol.
	if provider.IsCNProvider() {
		queryConfig.Adapter = CNUpstreamUsageAdapterName(provider)
		if queryConfig.Adapter == "" {
			return nil, ErrUpstreamUsageUnsupported
		}
	}
	if !s.execution.Supports(queryConfig.Adapter) {
		return nil, ErrUpstreamUsageUnsupported
	}
	fingerprint := UpstreamUsageContextFingerprint(provider, queryConfig, s.execution.BaseURL(provider))
	key := fmt.Sprintf("%d:%s", providerID, fingerprint)
	resultCh := s.queryFlight.DoChan(key, func() (any, error) {
		// Shared work keeps the first caller's values and uses service cancellation.
		// The deadline includes the initial identity read in the 60-second budget.
		workCtx, finish, err := s.activity.begin(context.WithoutCancel(ctx), ErrUpstreamUsageStopped)
		if err != nil {
			return nil, err
		}
		defer finish()
		opCtx, cancel := context.WithDeadline(workCtx, queryDeadline)
		defer cancel()
		return s.queryProvider(opCtx, providerID, provider, queryConfig)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			return nil, result.Err
		}
		queryResult, ok := result.Val.(*UpstreamUsageQueryResult)
		if !ok || queryResult == nil {
			return nil, ErrUpstreamUsageInvalidResponse
		}
		return CloneUpstreamUsageResult(queryResult), nil
	}
}

// QueryBatch queries providers independently and returns per-provider failures in its second result.
func (s *UpstreamUsageService) QueryBatch(ctx context.Context, providerIDs []int64) (map[int64]*UpstreamUsageQueryResult, map[int64]error, error) {
	if s == nil || s.providerRepo == nil || s.execution == nil || !s.execution.Available() {
		return nil, nil, ErrUpstreamUsageUnavailable
	}
	if len(providerIDs) == 0 {
		return nil, nil, ErrUpstreamUsageBatchInvalid
	}
	if len(providerIDs) > upstreamUsageBatchLimit {
		return nil, nil, ErrUpstreamUsageBatchTooLarge
	}
	unique := make([]int64, 0, len(providerIDs))
	seen := make(map[int64]struct{}, len(providerIDs))
	errorsByID := make(map[int64]error)
	for _, id := range providerIDs {
		if id <= 0 {
			errorsByID[id] = ErrUpstreamUsageProviderInvalid
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	results := make(map[int64]*UpstreamUsageQueryResult, len(unique))
	if len(unique) == 0 {
		return results, errorsByID, nil
	}
	var mu sync.Mutex
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(upstreamUsageConcurrency)
	for _, id := range unique {
		group.Go(func() error {
			result, err := s.QueryProvider(groupCtx, id)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errorsByID[id] = err
			} else {
				results[id] = result
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, nil, err
	}
	return results, errorsByID, nil
}

func (s *UpstreamUsageService) queryProvider(ctx context.Context, providerID int64, expected *Record, expectedConfig UpstreamUsageQueryConfig) (result *UpstreamUsageQueryResult, err error) {
	defer func() {
		outcome := "success"
		if err != nil {
			outcome = apperror.Reason(err)
			if outcome == "" {
				outcome = "error"
			}
		}
		s.recordMetric(expectedConfig.Adapter, outcome)
	}()
	release, err := s.acquireQuerySlot(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	provider, err := s.loadQueryProvider(ctx, providerID)
	if err != nil {
		if errors.Is(err, ErrUpstreamUsageProviderInvalid) || errors.Is(err, ErrUpstreamUsageProviderDisabled) {
			// A changed type, status, or deleted record invalidates the preflight identity.
			return nil, ErrUpstreamUsageIdentityChanged
		}
		return nil, err
	}
	if !SameUpstreamUsageIdentity(expected, provider, expectedConfig) {
		return nil, ErrUpstreamUsageIdentityChanged
	}
	if !s.execution.Supports(expectedConfig.Adapter) {
		return nil, ErrUpstreamUsageUnsupported
	}
	usage, err := s.execution.Query(ctx, provider, expectedConfig)
	if err != nil {
		return nil, err
	}
	if err := ValidateNormalizedUsage(usage); err != nil {
		return nil, ErrUpstreamUsageInvalidResponse.WithCause(err)
	}
	current, err := s.providerRepo.GetByID(ctx, providerID)
	if err != nil {
		if errors.Is(err, ErrProviderNotFound) {
			// Deletion during the query invalidates its identity; discard the result
			// rather than attribute it to a provider that no longer exists.
			return nil, ErrUpstreamUsageIdentityChanged
		}
		return nil, upstreamUsageRepositoryError(ctx, err)
	}
	if !SameUpstreamUsageIdentity(provider, current, expectedConfig) {
		return nil, ErrUpstreamUsageIdentityChanged
	}
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}
	return &UpstreamUsageQueryResult{
		ProviderID:   providerID,
		Adapter:      expectedConfig.Adapter,
		ObservedAt:   now,
		Provider:     usage.Provider,
		Mode:         usage.Mode,
		Unit:         usage.Unit,
		Balance:      usage.Balance,
		Balances:     usage.Balances,
		Available:    usage.Available,
		Limits:       usage.Limits,
		Subscription: usage.Subscription,
		ExpiresAt:    usage.ExpiresAt,
		Usage:        usage,
	}, nil
}

func (s *UpstreamUsageService) loadQueryProvider(ctx context.Context, providerID int64) (*Record, error) {
	provider, err := s.providerRepo.GetByID(ctx, providerID)
	if err != nil {
		return nil, upstreamUsageRepositoryError(ctx, err)
	}
	if provider == nil || provider.Type != ProviderTypeAPIKey {
		return nil, ErrUpstreamUsageProviderInvalid
	}
	if provider.Status != "" && provider.Status != StatusActive {
		return nil, ErrUpstreamUsageProviderDisabled
	}
	return provider, nil
}

func upstreamUsageRepositoryError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return ErrUpstreamUsageTimeout
	}
	if errors.Is(err, ErrProviderNotFound) {
		return ErrUpstreamUsageProviderInvalid
	}
	return ErrUpstreamUsageRequestFailed
}

func (s *UpstreamUsageService) acquireQuerySlot(ctx context.Context) (func(), error) {
	s.slotMu.Lock()
	if s.querySlots == nil {
		s.querySlots = make(chan struct{}, upstreamUsageConcurrency)
	}
	slots := s.querySlots
	s.slotMu.Unlock()
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, UpstreamUsageContextError(ctx)
	}
}
