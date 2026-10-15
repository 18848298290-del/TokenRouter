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

// upstream_queries.go coalesces upstream usage queries while enforcing
// identity checks, execution capacity, and shutdown tracking.
// An upstream usage observation belongs to the credentials and query settings
// that produced it. A preflight provider read chooses the adapter and
// fingerprint used to join shared work. The shared operation rereads identity
// before and after execution, then each successful waiter receives copied
// mutable display values so edits stay local to that result.
//
// Shared queries use a system-clock deadline covering the initial read, capacity
// wait, upstream call, and final reread. Caller waits and shared operations
// register separately for shutdown; abandoning a wait leaves the shared context
// available until its budget or service cancellation. A four-slot pool bounds
// shared execution across single and batch requests, while metrics count
// completed queryProvider attempts once rather than every waiter. The first stop
// result remains fixed when work outlives its shutdown budget.

// Upstream usage limits govern the full query budget, raw batch length before
// deduplication, and shared execution capacity across single and batch calls.
const (
	upstreamUsageTimeout     = 60 * time.Second // complete shared operation budget
	upstreamUsageBatchLimit  = 100              // maximum supplied batch ID count
	upstreamUsageConcurrency = 4                // shared slots and batch workers
)

// ErrUpstreamUsageStopped marks rejected query registration after the service
// permanently closes its activity tracker to new callers and shared operations.
var ErrUpstreamUsageStopped = errors.New("upstream usage queries are stopped")

// UpstreamUsageReader supplies provider records for query eligibility and
// identity validation.
type UpstreamUsageReader interface {
	// GetByID returns current provider data or a repository error. The returned
	// snapshot and referenced credentials, proxy metadata, and Extra values stay
	// stable while the shared query uses them for identity checks.
	GetByID(context.Context, int64) (*Record, error)
}

// UpstreamUsageExecution selects adapters and performs provider-specific
// normalized upstream usage queries.
type UpstreamUsageExecution interface {
	// Available reports whether the configured usage-query execution can be used.
	Available() bool
	// Supports reports whether the supplied usage adapter name has an
	// implementation.
	Supports(string) bool
	// BaseURL resolves the query root used in the preflight flight fingerprint.
	// Callers supply a stable provider record; the execution implementation
	// configures this resolver before serving queries.
	BaseURL(*Record) string
	// Query returns normalized usage for the supplied provider and query choices.
	// It uses the shared operation context for upstream work, and its returned
	// mutable observation stays stable while flight waiters copy the result.
	Query(context.Context, *Record, UpstreamUsageQueryConfig) (*UpstreamUsageInfo, error)
}

// UpstreamUsageOptions supplies the optional clock for successful observation
// timestamps.
type UpstreamUsageOptions struct {
	// Now optionally supplies ObservedAt after successful identity validation. Nil
	// uses the system clock, while query deadlines always use system time.
	Now func() time.Time
}

// UpstreamUsageService owns shared provider queries, execution slots, result
// copies, and shutdown.
type UpstreamUsageService struct {
	// providerRepo and execution remain usable through registered shared work.
	// Their owner supports concurrent calls and keeps dependencies open until
	// actual query completion, including work remaining after a stop timeout.
	providerRepo UpstreamUsageReader    // provider identity read interface
	execution    UpstreamUsageExecution // configured adapter execution interface
	// queryFlight joins in-flight requests by provider ID and input fingerprint.
	// Completed calls leave that sharing group; successful waiters copy the stored
	// result.
	queryFlight singleflight.Group
	// querySlots holds occupied shared-query capacity until its release callback.
	// slotMu protects lazy initialization and channel capture so all acquisitions
	// return capacity to the same pool they entered.
	querySlots chan struct{} // occupied shared execution slots
	slotMu     sync.Mutex    // execution pool initialization lock
	// now is an optional observation clock, retained from options. Concurrent
	// successful queries may call it, while their timeout calculations use
	// time.Now.
	now func() time.Time
	// metricsMu protects metrics increments and snapshots. Each queryProvider call
	// records one adapter/outcome count; SnapshotMetrics copies those scalar counts
	// while this lock is held.
	metricsMu sync.Mutex       // metric accumulation state lock
	metrics   map[string]int64 // adapter and outcome counts
	// activity registers caller waits and independently running flight callbacks.
	// The first stop rejects future registration, cancels both kinds, and retains
	// its wait result.
	activity operationActivity
}

// NewUpstreamUsageService retains the reader and execution implementation with a
// four-slot query pool and empty metrics. Construction starts no queries. Now is
// optional for ObservedAt; operation deadlines use system time, and supplied
// dependencies support concurrent query calls.
func NewUpstreamUsageService(reader UpstreamUsageReader, execution UpstreamUsageExecution, options UpstreamUsageOptions) *UpstreamUsageService {
	return &UpstreamUsageService{providerRepo: reader, execution: execution, now: options.Now, querySlots: make(chan struct{}, upstreamUsageConcurrency), metrics: make(map[string]int64)}
}

// StopContext closes registration, cancels caller waits and shared operations,
// and waits within ctx for completion. A nil service succeeds. The first
// completed stop result is retained, including unfinished-work timeout;
// concurrent later stops can return their own context error while that result is
// still pending.
func (s *UpstreamUsageService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.activity.stop(ctx, "upstream usage")
}

// recordMetric increments the process-local adapter/outcome counter under its
// mutex. It trims both labels and uses unknown for either empty label; a nil
// service skips recording, and a nil map is initialized on first use.
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

// SnapshotMetrics returns a separately allocated map of adapter/outcome counts.
// A nil service returns an empty map; counts are copied under the metrics lock,
// so caller edits remain independent of later shared-query observations.
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

// QueryProvider returns copied usage for an eligible API Key provider with
// enabled supported query configuration. Service availability and positive ID
// checks precede context admission. Callers supply a nonnil context; preflight
// repository failures are translated to stable usage errors.
//
// The initial read selects an adapter and input fingerprint for in-flight
// sharing. CN platforms select their platform/mode adapter. Shared work keeps
// the initiating caller's context values with cancellation detached, then uses
// service cancellation and a sixty-second system deadline that includes
// preflight time. The execution implementation cooperates with that context; an
// unresponsive dependency can outlive its deadline.
//
// During shared waiting, caller cancellation returns ctx.Err while the shared
// operation remains tracked. Ready completion can race with that cancellation.
// Returned values must contain a nonnil query result; successful waiters copy
// its nested display data through CloneUpstreamUsageResult before returning. New
// identities are checked again by the shared query before the observation is
// accepted.
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
	// Capture the query inputs before joining a flight so its key derives from the
	// current record, normalized choices, and resolved root. Fingerprint encoding
	// exceptions are defined by UpstreamUsageContextFingerprint.
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
	// CN usage protocols are chosen by platform and provider mode. Override the
	// generic configured adapter so the native platform request format reaches
	// execution.
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
		// Register the flight separately from its initiating waiter so waiter
		// cancellation leaves shared work tracked. Reuse the preflight deadline,
		// preventing its read time from being added to a fresh execution budget.
		workCtx, finish, err := s.activity.begin(context.WithoutCancel(ctx), ErrUpstreamUsageStopped)
		if err != nil {
			return nil, err
		}
		defer finish()
		opCtx, cancel := context.WithDeadline(workCtx, queryDeadline)
		defer cancel()
		return s.queryProvider(opCtx, providerID, provider, queryConfig)
	})
	// DoChan buffers the shared result, allowing this cancelled waiter to leave
	// while completion can still deliver to other waiters. Each successful waiter
	// receives its own display copy.
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

// QueryBatch returns successful results and individual failures keyed by
// provider ID. Unavailable execution, empty input, or more than one hundred
// supplied IDs fails before dispatch; the length limit applies before
// deduplication. Nonpositive IDs receive provider-invalid entries, and each
// distinct positive ID is queried once.
//
// Four workers run per batch and use the service's shared execution pool. Each
// worker stores its result or error under the same map lock, then returns nil to
// the group so one provider failure does not cancel other queries. Caller
// cancellation can therefore appear in the per-ID error map with a nil overall
// error. An all-invalid batch returns allocated empty results and its validation
// errors without starting workers.
func (s *UpstreamUsageService) QueryBatch(ctx context.Context, providerIDs []int64) (map[int64]*UpstreamUsageQueryResult, map[int64]error, error) {
	if s == nil || s.providerRepo == nil || s.execution == nil || !s.execution.Available() {
		return nil, nil, ErrUpstreamUsageUnavailable
	}
	if len(providerIDs) == 0 {
		return nil, nil, ErrUpstreamUsageBatchInvalid
	}
	// Apply the raw input ceiling before deduplication so repeated and invalid IDs
	// still count toward admission rather than expanding accepted batch input.
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
	// All workers write their success and failure maps under one mutex. Return nil
	// from each worker after recording failure so independent provider queries
	// continue.
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

// queryProvider executes one shared observation and records its adapter outcome
// even when slot admission or identity validation fails. The expected record and
// query choices remain stable during this operation, and the caller owns its
// bounded context.
//
// After taking a shared slot, the method reloads eligibility and compares
// identity before executing the adapter. A missing record or newly ineligible
// type or status invalidates the preflight identity. A successful normalized
// reply is validated and followed by another record read; changed identity
// discards that reply. The returned outer display fields initially share
// normalized Usage storage, leaving waiter copying to QueryProvider. ObservedAt
// is sampled only after the final identity check.
func (s *UpstreamUsageService) queryProvider(ctx context.Context, providerID int64, expected *Record, expectedConfig UpstreamUsageQueryConfig) (result *UpstreamUsageQueryResult, err error) {
	// Record one outcome for the shared execution attempt after its final result is
	// known. Preflight rejection and additional waiters do not create counts here.
	defer func() {
		outcome := "success"
		if err != nil {
			outcome = apperror.Reason(err)
			// Unstructured execution errors have no application reason key. Count them
			// under error so the shared attempt still has an outcome in the metric
			// snapshot.
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

	// Recheck identity after capacity admission, because the slot wait may span
	// administrator edits. Execute only after the prepared inputs still match this
	// reread.
	provider, err := s.loadQueryProvider(ctx, providerID)
	if err != nil {
		if errors.Is(err, ErrUpstreamUsageProviderInvalid) || errors.Is(err, ErrUpstreamUsageProviderDisabled) {
			// A record that disappeared or became ineligible after preflight cannot own
			// this observation. Report identity change so its earlier query inputs are
			// rejected together.
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
	// Reread after validating the upstream reply so an observation produced during
	// credential or configuration replacement is discarded before constructing the
	// shared display result.
	current, err := s.providerRepo.GetByID(ctx, providerID)
	if err != nil {
		if errors.Is(err, ErrProviderNotFound) {
			// Deletion during execution removes the owner of this completed reply.
			// Convert that not-found outcome to identity change so callers discard the
			// earlier observation.
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

// loadQueryProvider reads an API Key record and checks its stored status. Nil
// records or other types return provider-invalid; blank and active statuses are
// accepted, and other statuses return provider-disabled. Repository errors use
// the service's stable error mapping.
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

// upstreamUsageRepositoryError maps storage failure to the service error
// vocabulary. Deadline expiry in either ctx or err takes precedence over
// not-found, which becomes provider-invalid; other failures, including ordinary
// cancellation, become request-failed.
func upstreamUsageRepositoryError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return ErrUpstreamUsageTimeout
	}
	if errors.Is(err, ErrProviderNotFound) {
		return ErrUpstreamUsageProviderInvalid
	}
	// Collapse storage failures outside the deadline and not-found cases to one
	// query-failure key. This path returns stable administrator errors instead of
	// backend error strings.
	return ErrUpstreamUsageRequestFailed
}

// acquireQuerySlot returns a release callback after occupying the service's
// shared pool. Call release exactly once after the shared operation finishes.
// Cancellation while waiting maps through UpstreamUsageContextError; a ready
// slot can compete with cancellation in the select.
func (s *UpstreamUsageService) acquireQuerySlot(ctx context.Context) (func(), error) {
	// Capture the initialized slot channel under its mutex before admission. The
	// returned callback releases that captured pool, retaining ownership of the
	// capacity used by this shared operation.
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
