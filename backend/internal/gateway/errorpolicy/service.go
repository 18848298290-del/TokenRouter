package errorpolicy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Final error matching reads a published priority-sorted snapshot instead of
// loading rules for every response. Administrative writes, startup loads, and
// notification refreshes share one cancellable update gate so a database read
// admitted before a write cannot publish its earlier snapshot after that
// write's refresh.
//
// Operations also register under a lifecycle lock before stopping can wait for
// them. Post-write refresh has a separate service-bound budget, and cache
// errors are observed while the successful database result remains available
// to the caller. Snapshot publication clones mutable rule fields and
// precomputes condition lookups; matching returns another clone. Platform
// eligibility precedes status/keyword combinations, and lowercasing only the
// bounded body prefix is deferred until a keyword decision needs it.

// maxBodyMatchLen bounds case conversion and substring work for upstream
// keyword matching, independently of the complete response body retained for
// presentation or diagnostics.
const maxBodyMatchLen = 8 << 10 // keyword matching prefix limit bytes

// ErrorPassthroughRepository supplies authoritative reads and complete
// error-rule persistence operations.
type ErrorPassthroughRepository interface {
	// List returns authoritative rules for administrative display and snapshot
	// publication. Administrative List clones data even with an error; database
	// publication discards errored reads and sorts successful records for
	// matching.
	List(ctx context.Context) ([]*ErrorPassthroughRule, error)
	// GetByID returns the requested authoritative rule or a missing result
	// according to storage behavior. The PostgreSQL adapter returns nil for
	// not-found; the service clones returned rule data before exposing it.
	GetByID(ctx context.Context, id int64) (*ErrorPassthroughRule, error)
	// Create persists the complete validated rule and returns its stored
	// representation. The service holds the update gate and performs cache
	// refresh afterward; repository errors stop before publication attempts.
	Create(ctx context.Context, rule *ErrorPassthroughRule) (*ErrorPassthroughRule, error)
	// Update replaces the complete validated rule identified by ID and returns
	// the stored representation. The service coordinates subsequent cache
	// publication; storage implementations decide absent-record errors and
	// optional-field clearing.
	Update(ctx context.Context, rule *ErrorPassthroughRule) (*ErrorPassthroughRule, error)
	// Delete removes the authoritative rule identified by ID and reports storage
	// errors. The service refreshes caches after success; missing-record behavior
	// belongs to the storage implementation.
	Delete(ctx context.Context, id int64) error
}

// ErrorPassthroughCache stores rules and coordinates refresh notifications
// between instances.
type ErrorPassthroughCache interface {
	// Get returns a cached rule list and whether cached data is available. False
	// can represent a miss or cache read failure, so the service loads
	// authoritative rules in either case.
	Get(ctx context.Context) ([]*ErrorPassthroughRule, bool)
	// Set publishes the supplied rule list to shared cache storage.
	// Implementations retain independent mutable data; the service observes write
	// errors and still publishes its authoritative local matching snapshot.
	Set(ctx context.Context, rules []*ErrorPassthroughRule) error
	// Invalidate discards cached rule data and reports an error when shared
	// invalidation fails. The service continues its authoritative reload and
	// notification attempts, so failure does not roll back persisted rules.
	Invalidate(ctx context.Context) error
	// NotifyUpdate asks other instances to refresh their rules and reports
	// publication errors. Its result does not acknowledge completed remote
	// reloads; the service observes failure after persistence has succeeded.
	NotifyUpdate(ctx context.Context) error
	// SubscribeUpdates registers the service's synchronous refresh callback using
	// its lifetime context. Implementations track accepted subscriber work for
	// stopping; a callback reloads only while service cancellation permits the
	// operation.
	SubscribeUpdates(ctx context.Context, handler func())
}

// ErrorPassthroughService manages rule persistence, cached snapshots, and matching.
type ErrorPassthroughService struct {
	observe func(string, ...any) // optional service diagnostic observer
	// lifecycleMu protects started and stopped when startup claims admission and
	// registered operations enter. Stopping closes admission before operations.Wait
	// begins, so a new operation cannot appear after shutdown has started waiting.
	lifecycleMu sync.Mutex // startup and operation admission lock
	// started records the first startup claim; stopped records permanent service
	// shutdown. StartContext returns after either flag is set, even if an earlier
	// startup load returned cancellation before subscriber registration completed.
	started, stopped bool
	initOnce         sync.Once // one-time coordinator state initialization
	// updateGate contains the sole update token when idle. Successful acquisition
	// owns one release, serializing storage loads and post-write snapshot publication.
	updateGate chan struct{}
	// runCtx and cancel carry service lifetime cancellation. Post-write refresh
	// derives from runCtx, while beginOperation also links each caller context to it.
	runCtx     context.Context            // shared service operation lifetime context
	cancel     context.CancelFunc         // shared service lifetime cancellation callback
	operations sync.WaitGroup             // admitted storage operation completion count
	stopDone   chan struct{}              // shared shutdown completion signal
	repo       ErrorPassthroughRepository // authoritative error-rule storage adapter
	cache      ErrorPassthroughCache      // optional shared rule cache adapter

	// localCache and localCacheMu publish immutable compiled snapshots for matching.
	// Nil requests lazy refresh; an allocated empty slice represents a loaded empty
	// rule list, and a captured snapshot remains readable after later publication.
	localCache   []*cachedPassthroughRule // current compiled matching snapshot
	localCacheMu sync.RWMutex             // compiled snapshot publication access lock
}

// cachedPassthroughRule contains precomputed case-folded strings and
// status-code lookup data.
type cachedPassthroughRule struct {
	// ErrorPassthroughRule and the compiled lookup fields describe the same cloned
	// rule. Publication treats that entire object as immutable while MatchRule
	// reads it and returns another independent mutable result.
	*ErrorPassthroughRule                  // cloned immutable source rule snapshot
	lowerKeywords         []string         // precomputed lowercase keyword match values
	lowerPlatforms        []string         // precomputed lowercase platform match values
	errorCodeSet          map[int]struct{} // precomputed error status membership set
}

// NewErrorPassthroughService binds authoritative storage, an optional shared
// cache, and the first optional diagnostic observer. Construction performs no
// loading; callers use StartContext or an operation that triggers lazy
// initialization.
func NewErrorPassthroughService(
	repo ErrorPassthroughRepository,
	cache ErrorPassthroughCache,
	observers ...func(string, ...any),
) *ErrorPassthroughService {
	svc := &ErrorPassthroughService{
		repo:  repo,
		cache: cache,
	}

	if len(observers) > 0 {
		svc.observe = observers[0]
	}
	return svc
}

// initialize prepares the update token and detached service lifetime exactly
// once. Directly constructed test services share this same setup; callers
// reach it before registering operations or starting and stopping lifecycle
// work.
func (s *ErrorPassthroughService) initialize() {
	s.initOnce.Do(func() {
		s.updateGate = make(chan struct{}, 1)
		s.updateGate <- struct{}{}
		s.runCtx, s.cancel = context.WithCancel(context.Background())
	})
}

// beginOperation registers storage work and returns a context canceled by
// either caller or service shutdown. The caller supplies a nonnil context and
// invokes the accepted cleanup exactly once after work completes.
//
// Registration increments operations while holding the same lifecycle mutex
// StopContext uses to set stopped, preventing a new task from arriving after
// shutdown begins waiting. A stopped service returns context.Canceled before
// registration. Cleanup calls the AfterFunc stop function and cancels the
// operation before decrementing its count; an already running cancellation
// callback can finish independently. An already canceled runCtx is checked
// immediately so its cancellation does not await callback execution.
func (s *ErrorPassthroughService) beginOperation(ctx context.Context) (context.Context, func(), error) {
	s.initialize()
	s.lifecycleMu.Lock()
	if s.stopped {
		s.lifecycleMu.Unlock()
		return nil, nil, context.Canceled
	}
	// Register work before releasing lifecycle admission, so stopping cannot
	// begin its operation wait before this accepted caller contributes to the
	// shared completion count.
	s.operations.Add(1)
	s.lifecycleMu.Unlock()
	operation, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.runCtx, cancel)
	// Handle service cancellation that already occurred before AfterFunc
	// registration becomes active, keeping a just-admitted operation from
	// continuing solely because the callback has not run.
	if s.runCtx.Err() != nil {
		cancel()
	}
	// Stop the cancellation hook and cancel this operation before releasing its
	// wait count; a callback already running can finish independently of cleanup.
	return operation, func() { stop(); cancel(); s.operations.Done() }, nil
}

// acquireUpdate takes the sole update token or returns cancellation from a
// usable operation context. Successful callers release it exactly once after
// their coordinated storage and publication work.
//
// Cancellation can race with a ready token, so admission checks the context
// again after receiving it. If cancellation has arrived, the token is returned
// before reporting that error. This keeps a canceled refresh from retaining
// coordination ownership or executing after another operation expects the gate
// to become available.
func (s *ErrorPassthroughService) acquireUpdate(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.updateGate:
	}
	// Return a token received during the cancellation race before reporting the
	// context error, so another coordinated load or write can continue using the
	// same gate.
	if err := ctx.Err(); err != nil {
		s.releaseUpdate()
		return err
	}
	return nil
}

// releaseUpdate returns the coordinated update token to its channel. Call it
// exactly once after successful acquisition; an unmatched second release can
// block because the idle channel already holds the token.
func (s *ErrorPassthroughService) releaseUpdate() { s.updateGate <- struct{}{} }

// StartContext claims the first startup, loads authoritative rules, and
// registers shared-cache refresh when service lifetime remains active. Callers
// supply a usable context; repeated calls after startup or stopping return
// without retrying that initial claim.
//
// Cancellation or deadline errors during the first load return directly,
// leaving started set. Other loading failures are logged and try cached
// refresh before subscription registration proceeds. The subscriber uses
// runCtx and its callback registers coordinated operations, so stopping
// cancels startup or notification reads and can wait for their shared-resource
// work to finish.
func (s *ErrorPassthroughService) StartContext(ctx context.Context) error {
	s.initialize()
	s.lifecycleMu.Lock()
	if s.started || s.stopped {
		s.lifecycleMu.Unlock()
		return nil
	}
	s.started = true
	s.lifecycleMu.Unlock()
	// Load the database first at startup to replace stale shared data. Ordinary
	// failure tries cached recovery, while cancellation stops before registering
	// a subscriber for this startup.
	if err := s.reloadRulesFromDB(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		s.log("[ErrorPassthroughService] Failed to load rules from DB on startup: %v", err)
		if err := s.refreshLocalCache(ctx); err != nil {
			s.log("[ErrorPassthroughService] Failed to load rules from cache fallback on startup: %v", err)
		}
	}
	operation, done, err := s.beginOperation(ctx)
	if err != nil {
		return err
	}
	defer done()
	if err := operation.Err(); err != nil {
		return err
	}
	if s.cache != nil {
		s.cache.SubscribeUpdates(s.runCtx, func() {
			if err := s.refreshLocalCache(s.runCtx); err != nil && s.runCtx.Err() == nil {
				s.log("[ErrorPassthroughService] Failed to refresh cache on notification: %v", err)
			}
		})
	}
	return nil
}

// Stop waits without a caller deadline for StopContext to finish service
// cleanup. Storage operations and subscriber callbacks must respond to service
// cancellation for this convenience wrapper to return.
func (s *ErrorPassthroughService) Stop() { _ = s.StopContext(context.Background()) }

// StopContext permanently closes operation admission, cancels service work,
// and waits within the caller's usable context. Existing published snapshots
// remain available to cached MatchRule calls; shutdown rejects new registered
// storage operations.
//
// The first stop starts one shared waiter that stops an optional subscription
// and waits for admitted operations before closing stopDone. Later calls wait
// on that same signal. An already completed stop returns success even for an
// expired caller context; otherwise caller cancellation reports unfinished
// cleanup, which continues independently until those operations and callbacks
// return.
func (s *ErrorPassthroughService) StopContext(ctx context.Context) error {
	s.initialize()
	s.lifecycleMu.Lock()
	if !s.stopped {
		s.stopped = true
		s.cancel()
		s.stopDone = make(chan struct{})
		// Run subscription teardown and admitted-operation waiting behind one shared
		// stop signal, letting each caller bound its own wait while cleanup
		// continues after a timeout.
		go func() {
			if stopper, ok := s.cache.(interface {
				// StopSubscription cancels admitted subscription work and waits for its
				// callbacks to finish. The service runs this potentially unbounded cleanup
				// behind stopDone so callers can bound their own shutdown wait.
				StopSubscription()
			}); ok {
				stopper.StopSubscription()
			}
			s.operations.Wait()
			close(s.stopDone)
		}()
	}
	done := s.stopDone
	s.lifecycleMu.Unlock()
	// Prefer an already closed completion signal before consulting caller
	// cancellation, so repeated completed shutdown checks can return success even
	// after that caller's budget has expired.
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("error rules stop: unfinished operations: %w", ctx.Err())
	}
}

// List returns cloned authoritative rules with the repository error after
// registering service-bound work. The caller uses a nonnil context; nested
// slices and optional pointers are copied even when repository data
// accompanies an error.
func (s *ErrorPassthroughService) List(ctx context.Context) ([]*ErrorPassthroughRule, error) {
	operation, done, err := s.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rules, err := s.repo.List(operation)
	return cloneRules(rules), err
}

// GetByID returns a cloned authoritative rule with its repository error after
// registering work. A nil repository result remains nil; the copied nested
// fields cannot mutate published or repository-owned rule storage.
func (s *ErrorPassthroughService) GetByID(ctx context.Context, id int64) (*ErrorPassthroughRule, error) {
	operation, done, err := s.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rule, err := s.repo.GetByID(operation, id)
	return cloneRule(rule), err
}

// Create validates and persists a nonnil rule while serializing its cache
// refresh with other service updates. Input and result fields are cloned, and
// validation or repository errors return before refresh publication.
//
// The update token stays held through persistence, cache invalidation,
// authoritative reload, and notification. After the write succeeds, refresh
// uses a separate three-second runCtx budget so caller cancellation does not
// discard those attempts. Cache failures are observed rather than returned as
// a failed database creation; shared instances may still need a later
// successful reload to converge.
func (s *ErrorPassthroughService) Create(ctx context.Context, rule *ErrorPassthroughRule) (*ErrorPassthroughRule, error) {
	rule = cloneRule(rule)
	operation, done, err := s.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if err := s.acquireUpdate(operation); err != nil {
		return nil, err
	}
	defer s.releaseUpdate()
	ctx = operation

	if err := rule.Validate(); err != nil {
		return nil, err
	}

	// Persist the validated rule before shared invalidation, direct reload, and
	// notification. The held update gate spans all those phases; repository
	// failure skips the publication sequence.
	created, err := s.repo.Create(ctx, rule)
	if err != nil {
		return nil, err
	}

	refreshCtx, cancel := s.newCacheRefreshContext()
	defer cancel()
	s.invalidateAndNotify(refreshCtx)

	return cloneRule(created), nil
}

// Update validates and replaces a nonnil rule while coordinating publication
// with other service loads and writes. Its ID identifies the stored rule;
// cloned input and output protect mutable fields from caller sharing.
//
// Persistence completes before shared invalidation, authoritative reload, and
// update notification under the same gate. A separate runCtx-derived
// three-second refresh budget survives caller cancellation while remaining
// stoppable by service shutdown. Repository errors stop the operation, whereas
// post-write cache failures are observed and leave the successful persistence
// result available; no cache failure rolls that write back.
func (s *ErrorPassthroughService) Update(ctx context.Context, rule *ErrorPassthroughRule) (*ErrorPassthroughRule, error) {
	rule = cloneRule(rule)
	operation, done, err := s.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if err := s.acquireUpdate(operation); err != nil {
		return nil, err
	}
	defer s.releaseUpdate()
	ctx = operation

	if err := rule.Validate(); err != nil {
		return nil, err
	}

	// Replace persisted rule data before cache publication under the held update
	// gate, so an older admitted load must finish before this replacement and its
	// authoritative reload.
	updated, err := s.repo.Update(ctx, rule)
	if err != nil {
		return nil, err
	}

	refreshCtx, cancel := s.newCacheRefreshContext()
	defer cancel()
	s.invalidateAndNotify(refreshCtx)

	return cloneRule(updated), nil
}

// Delete removes an authoritative rule and coordinates the subsequent cache
// refresh with service updates. The caller supplies a usable context, and a
// repository error returns before invalidation or notification begins.
//
// After success, the held update token spans shared invalidation, direct
// database reload, and notification under a separate three-second service
// budget. Request cancellation therefore does not discard refresh attempts
// after persistence, while shutdown still cancels them. Cache failures are
// logged and the successful deletion returns nil; failed shared invalidation
// or reload can require later recovery.
func (s *ErrorPassthroughService) Delete(ctx context.Context, id int64) error {
	operation, done, err := s.beginOperation(ctx)
	if err != nil {
		return err
	}
	defer done()
	if err := s.acquireUpdate(operation); err != nil {
		return err
	}
	defer s.releaseUpdate()
	ctx = operation

	// Delete authoritative data before invalidating and reloading its snapshots.
	// Repository failure returns before those publication attempts, while success
	// keeps the update token through their completion.
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}

	refreshCtx, cancel := s.newCacheRefreshContext()
	defer cancel()
	s.invalidateAndNotify(refreshCtx)

	return nil
}

// MatchRule returns a cloned first enabled match from its captured
// priority-sorted snapshot, or nil when none qualifies. Missing local state
// triggers a coordinated lazy load; an existing snapshot can still be matched
// after stopping.
//
// Platform membership qualifies each rule before its configured status and
// keyword categories. Any mode accepts one configured category, all requires
// each, and unconfigured categories do not become additional requirements.
// Body lowercase conversion is shared across this match call and restricted to
// the first eight KiB. A concurrent publication does not mutate the captured
// snapshot, and returned rule fields are independently copied.
// @project-doc docs/interfaces/gateway_error_policy.md#error_passthrough_rule_matching
func (s *ErrorPassthroughService) MatchRule(platform string, statusCode int, body []byte) *ErrorPassthroughRule {
	rules := s.getCachedRules()
	if len(rules) == 0 {
		return nil
	}

	lowerPlatform := strings.ToLower(platform)
	var bodyLower string // Computed lazily for keyword matching.
	var bodyLowerDone bool

	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		if !s.platformMatchesCached(rule, lowerPlatform) {
			continue
		}
		if s.ruleMatchesOptimized(rule, statusCode, body, &bodyLower, &bodyLowerDone) {
			return cloneRule(rule.ErrorPassthroughRule)
		}
	}

	return nil
}

// getCachedRules returns the immutable local slice captured under its read
// lock, refreshing nil state first. Refresh failure is observed and returns
// nil; allocated empty snapshots are reused and callers leave returned
// compiled rules unchanged.
func (s *ErrorPassthroughService) getCachedRules() []*cachedPassthroughRule {
	s.localCacheMu.RLock()
	rules := s.localCache
	s.localCacheMu.RUnlock()

	if rules != nil {
		return rules
	}

	ctx := context.Background()
	// A failed load supplies no complete matching snapshot. Return nil for this
	// unmatched decision while a later uncached call can attempt loading again.
	if err := s.refreshLocalCache(ctx); err != nil {
		s.log("[ErrorPassthroughService] Failed to refresh cache: %v", err)
		return nil
	}

	s.localCacheMu.RLock()
	defer s.localCacheMu.RUnlock()
	return s.localCache
}

// refreshLocalCache registers service-bound work and acquires the update gate
// before preferring shared cache or reloading storage. Cleanup releases the
// token and operation once; callers provide a usable context for cancellation.
func (s *ErrorPassthroughService) refreshLocalCache(ctx context.Context) error {
	operation, done, err := s.beginOperation(ctx)
	if err != nil {
		return err
	}
	defer done()
	if err := s.acquireUpdate(operation); err != nil {
		return err
	}
	defer s.releaseUpdate()
	return s.refreshLocalCacheLocked(operation)
}

// refreshLocalCacheLocked prefers available shared rules and otherwise
// delegates authoritative database reload. The caller owns the update gate and
// operation lifetime; cached nil lists count as available data and compile to
// an empty local snapshot.
func (s *ErrorPassthroughService) refreshLocalCacheLocked(ctx context.Context) error {
	// Publish available shared data before attempting a database read; a false
	// cache result includes misses and read failures, both of which need
	// authoritative reload.
	if s.cache != nil {
		if rules, ok := s.cache.Get(ctx); ok {
			s.setLocalCache(rules)
			return nil
		}
	}

	return s.reloadRulesFromDBLocked(ctx)
}

// reloadRulesFromDB registers service-bound work and obtains the update token
// before authoritative loading and publication. Callers supply a nonnil
// context; coordinated cleanup releases both ownership registrations after the
// locked implementation returns.
func (s *ErrorPassthroughService) reloadRulesFromDB(ctx context.Context) error {
	operation, done, err := s.beginOperation(ctx)
	if err != nil {
		return err
	}
	defer done()
	if err := s.acquireUpdate(operation); err != nil {
		return err
	}
	defer s.releaseUpdate()
	return s.reloadRulesFromDBLocked(operation)
}

// reloadRulesFromDBLocked reads authoritative rules and publishes shared then
// local snapshots while its caller holds the update gate. Repository failure
// returns before either publication, and every listed rule pointer must be
// usable.
//
// A shared-cache write failure is observed but does not discard successful
// database data: setLocalCache still publishes its cloned, compiled priority
// order. Keeping these steps inside the caller's coordinated update prevents
// an older service load from publishing after a completed administrative
// refresh. Cross-instance notification and shared storage recovery remain
// separate operations.
func (s *ErrorPassthroughService) reloadRulesFromDBLocked(ctx context.Context) error {
	rules, err := s.repo.List(ctx)
	if err != nil {
		return err
	}

	if s.cache != nil {
		// Attempt shared publication from authoritative data but continue local
		// compilation on a cache-write error; successful database reads remain
		// usable by this instance under the same update token.
		if err := s.cache.Set(ctx, rules); err != nil {
			s.log("[ErrorPassthroughService] Failed to set cache: %v", err)
		}
	}

	// Keep authoritative data available locally even after a shared write fails;
	// compilation sorts and clones the snapshot before readers can observe its
	// replacement.
	s.setLocalCache(rules)

	return nil
}

// setLocalCache clones and compiles a list of nonnil rules before publishing
// its matching snapshot. Nil or empty lists become an allocated empty
// snapshot, distinguishing a loaded empty configuration from uninitialized
// local state.
//
// Lowercase keyword and platform arrays plus a status membership set all
// derive from the same cloned rule. Sorting by priority occurs before taking
// the publication lock, and equal priorities have no secondary key. The new
// slice replaces localCache in one locked assignment; previous readers retain
// their immutable captured objects, and later input mutation cannot alter
// those published nested fields.
func (s *ErrorPassthroughService) setLocalCache(rules []*ErrorPassthroughRule) {
	cached := make([]*cachedPassthroughRule, len(rules))
	for i, r := range rules {
		r = cloneRule(r)
		cr := &cachedPassthroughRule{ErrorPassthroughRule: r}
		if len(r.Keywords) > 0 {
			cr.lowerKeywords = make([]string, len(r.Keywords))
			for j, kw := range r.Keywords {
				cr.lowerKeywords[j] = strings.ToLower(kw)
			}
		}
		if len(r.Platforms) > 0 {
			cr.lowerPlatforms = make([]string, len(r.Platforms))
			for j, p := range r.Platforms {
				cr.lowerPlatforms[j] = strings.ToLower(p)
			}
		}
		if len(r.ErrorCodes) > 0 {
			cr.errorCodeSet = make(map[int]struct{}, len(r.ErrorCodes))
			for _, code := range r.ErrorCodes {
				cr.errorCodeSet[code] = struct{}{}
			}
		}
		cached[i] = cr
	}

	sort.Slice(cached, func(i, j int) bool {
		return cached[i].Priority < cached[j].Priority
	})

	s.localCacheMu.Lock()
	// Publish the completed compiled set in one locked assignment, leaving old
	// captured snapshots available to current matchers while new readers use this
	// replacement.
	s.localCache = cached
	s.localCacheMu.Unlock()
}

// clearLocalCache sets the local snapshot to nil under its publication lock so
// later matching attempts lazy reload. Existing readers keep their captured
// data; clearing alone supplies no shared Redis invalidation guarantee.
func (s *ErrorPassthroughService) clearLocalCache() {
	s.localCacheMu.Lock()
	// Force the next uncached match to attempt loading after publication failed;
	// readers already holding a snapshot finish against that captured immutable
	// slice.
	s.localCache = nil
	s.localCacheMu.Unlock()
}

// newCacheRefreshContext returns a three-second budget derived from
// initialized service runCtx. The caller cancels it after post-write
// publication; it survives request cancellation while service shutdown can
// still end the refresh.
func (s *ErrorPassthroughService) newCacheRefreshContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(s.runCtx, 3*time.Second)
}

// invalidateAndNotify attempts shared invalidation, authoritative reload, and
// update notification after persistence. Its caller holds the update gate and
// supplies the bounded service refresh context; individual cache failures are
// observed rather than returned.
//
// Invalidation precedes reload so a successful shared deletion prevents later
// cache reads from selecting the earlier blob. Reload failure clears the local
// snapshot to request lazy recovery, while notification still runs so other
// instances can refresh. Shared deletion can fail independently, so clearing
// local state does not prove every later cache read has new rules; this
// sequence supplies attempts, not transactional cross-instance publication.
func (s *ErrorPassthroughService) invalidateAndNotify(ctx context.Context) {
	// Attempt shared invalidation before reload so acknowledged deletion removes
	// the earlier blob; failures are recorded while direct database publication
	// still proceeds under the held gate.
	if s.cache != nil {
		if err := s.cache.Invalidate(ctx); err != nil {
			s.log("[ErrorPassthroughService] Failed to invalidate cache: %v", err)
		}
	}

	if err := s.reloadRulesFromDBLocked(ctx); err != nil {
		s.log("[ErrorPassthroughService] Failed to refresh local cache: %v", err)
		// Drop the local snapshot when authoritative reload fails, prompting another
		// lazy load; shared Redis state can still remain stale if its earlier
		// deletion also failed.
		s.clearLocalCache()
	}

	if s.cache != nil {
		if err := s.cache.NotifyUpdate(ctx); err != nil {
			s.log("[ErrorPassthroughService] Failed to notify cache update: %v", err)
		}
	}
}

// ensureBodyLower returns the cached lowercase body or computes its first
// eight-KiB prefix once. Callers provide usable bodyLower and done pointers
// shared for this match call; done already true trusts their stored value.
func ensureBodyLower(body []byte, bodyLower *string, done *bool) string {
	if *done {
		return *bodyLower
	}
	b := body
	// Bound conversion work for the keyword body prefix.
	if len(b) > maxBodyMatchLen {
		b = b[:maxBodyMatchLen]
	}
	*bodyLower = strings.ToLower(string(b))
	*done = true
	return *bodyLower
}

// platformMatchesCached reports whether a pre-folded platform satisfies the
// compiled filter. Empty filters accept every platform; nonempty filters use
// exact membership, and callers supply case-normalized values without implicit
// whitespace trimming here.
func (s *ErrorPassthroughService) platformMatchesCached(rule *cachedPassthroughRule, lowerPlatform string) bool {
	if len(rule.lowerPlatforms) == 0 {
		return true
	}
	return slices.Contains(rule.lowerPlatforms, lowerPlatform)
}

// ruleMatchesOptimized decides configured status and keyword categories using
// one lazy lowercase-body cache. It requires a usable compiled rule and cache
// pointers; a rule with neither category never matches.
//
// All mode rejects a failed configured status before needing body text and
// requires configured keywords when present. Every other mode follows any
// behavior, accepting a matching status before keyword conversion when both
// categories exist. A single configured category decides alone. Missing
// categories therefore do not authorize an otherwise empty rule, and
// previously converted body text is reused across later checks.
func (s *ErrorPassthroughService) ruleMatchesOptimized(rule *cachedPassthroughRule, statusCode int, body []byte, bodyLower *string, bodyLowerDone *bool) bool {
	hasErrorCodes := len(rule.errorCodeSet) > 0
	hasKeywords := len(rule.lowerKeywords) > 0

	if !hasErrorCodes && !hasKeywords {
		return false
	}

	codeMatch := !hasErrorCodes || s.containsIntSet(rule.errorCodeSet, statusCode)

	if rule.MatchMode == MatchModeAll {
		// Reject a failed status category before lowercasing this rule's body,
		// because all-mode keyword success cannot compensate for a configured status
		// condition that already failed.
		if hasErrorCodes && !codeMatch {
			return false
		}
		if hasKeywords {
			return s.containsAnyKeywordCached(ensureBodyLower(body, bodyLower, bodyLowerDone), rule.lowerKeywords)
		}
		return codeMatch
	}

	// Let status success settle an any-mode decision before body conversion when
	// both categories exist; keywords are inspected only if that earlier
	// condition did not match.
	if hasErrorCodes && hasKeywords {
		if codeMatch {
			return true
		}
		return s.containsAnyKeywordCached(ensureBodyLower(body, bodyLower, bodyLowerDone), rule.lowerKeywords)
	}
	// Evaluate the remaining configured category alone, because absent status or
	// keyword lists should not become extra requirements or authorize an entirely
	// condition-free rule.
	if hasKeywords {
		return s.containsAnyKeywordCached(ensureBodyLower(body, bodyLower, bodyLowerDone), rule.lowerKeywords)
	}
	return codeMatch
}

// containsIntSet reports whether val belongs to the configured status-code
// membership set.
func (s *ErrorPassthroughService) containsIntSet(set map[int]struct{}, val int) bool {
	_, ok := set[val]
	return ok
}

// containsAnyKeywordCached reports whether one supplied keyword occurs in the
// pre-folded body. Empty lists return false; a configured empty keyword
// matches, and callers fold text before using this substring helper.
func (s *ErrorPassthroughService) containsAnyKeywordCached(bodyLower string, lowerKeywords []string) bool {
	for _, kw := range lowerKeywords {
		if strings.Contains(bodyLower, kw) {
			return true
		}
	}
	return false
}

// cloneRule copies mutable fields of one rule, preserving nil input and
// nil-versus-empty condition slices. Scalar values are retained by value, and
// response code, message, and description pointers receive separate pointed
// storage.
//
// Each nonnil condition slice is copied, with an allocated empty slice
// restored when append would collapse that state into nil. This preserves rule
// JSON and cache representation while keeping callers from mutating published
// nested fields through shared storage. The returned rule can be used as an
// input or result snapshot independently of its supplied mutable fields.
func cloneRule(rule *ErrorPassthroughRule) *ErrorPassthroughRule {
	if rule == nil {
		return nil
	}
	copy := *rule
	// Copy condition arrays and optional pointed values together before
	// publication or return, preserving their nil/empty representation while
	// separating every mutable field from source ownership.
	copy.ErrorCodes = append([]int(nil), rule.ErrorCodes...)
	if rule.ErrorCodes != nil && copy.ErrorCodes == nil {
		copy.ErrorCodes = []int{}
	}
	copy.Keywords = append([]string(nil), rule.Keywords...)
	if rule.Keywords != nil && copy.Keywords == nil {
		copy.Keywords = []string{}
	}
	copy.Platforms = append([]string(nil), rule.Platforms...)
	if rule.Platforms != nil && copy.Platforms == nil {
		copy.Platforms = []string{}
	}
	if rule.ResponseCode != nil {
		v := *rule.ResponseCode
		copy.ResponseCode = &v
	}
	if rule.CustomMessage != nil {
		v := *rule.CustomMessage
		copy.CustomMessage = &v
	}
	if rule.Description != nil {
		v := *rule.Description
		copy.Description = &v
	}
	return &copy
}

// cloneRules returns a new rule slice with cloneRule applied to each element.
// Nil lists and individual nil entries remain nil; each nonnil rule receives
// separately copied mutable fields for result or publication ownership.
func cloneRules(rules []*ErrorPassthroughRule) []*ErrorPassthroughRule {
	if rules == nil {
		return nil
	}
	out := make([]*ErrorPassthroughRule, len(rules))
	for i, r := range rules {
		out[i] = cloneRule(r)
	}
	return out
}

// CloneRules exposes rule-list cloning for shared-cache adapters and preserves
// nil versus empty collection state. Mutable rule slices and optional pointers
// receive separate storage, while nil rules remain nil for the caller to
// handle.
func CloneRules(rules []*ErrorPassthroughRule) []*ErrorPassthroughRule { return cloneRules(rules) }

// log synchronously emits a service diagnostic when an observer is configured.
// The optional callback receives the original message and arguments; an
// unconfigured observer leaves the diagnostic unreported by this helper.
func (s *ErrorPassthroughService) log(message string, args ...any) {
	if s.observe != nil {
		s.observe(message, args...)
	}
}
