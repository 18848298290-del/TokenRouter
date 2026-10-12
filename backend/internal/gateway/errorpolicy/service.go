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

const maxBodyMatchLen = 8 << 10 // Limit keyword matching to the first 8 KiB.

// ErrorPassthroughRepository stores error-response rules.
type ErrorPassthroughRepository interface {
	// List returns all rules.
	List(ctx context.Context) ([]*ErrorPassthroughRule, error)
	// GetByID returns a rule by ID.
	GetByID(ctx context.Context, id int64) (*ErrorPassthroughRule, error)
	// Create persists a rule.
	Create(ctx context.Context, rule *ErrorPassthroughRule) (*ErrorPassthroughRule, error)
	// Update replaces a rule.
	Update(ctx context.Context, rule *ErrorPassthroughRule) (*ErrorPassthroughRule, error)
	// Delete removes a rule by ID.
	Delete(ctx context.Context, id int64) error
}

// ErrorPassthroughCache stores rules and coordinates refresh notifications between instances.
type ErrorPassthroughCache interface {
	// Get returns cached rules and whether a cached value exists.
	Get(ctx context.Context) ([]*ErrorPassthroughRule, bool)
	// Set publishes the rule list.
	Set(ctx context.Context, rules []*ErrorPassthroughRule) error
	// Invalidate removes the cached value.
	Invalidate(ctx context.Context) error
	// NotifyUpdate asks other instances to refresh.
	NotifyUpdate(ctx context.Context) error
	// SubscribeUpdates registers a refresh handler.
	SubscribeUpdates(ctx context.Context, handler func())
}

// ErrorPassthroughService manages rule persistence, cached snapshots, and matching.
type ErrorPassthroughService struct {
	observe          func(string, ...any)
	lifecycleMu      sync.Mutex
	started, stopped bool
	initOnce         sync.Once
	updateGate       chan struct{}
	runCtx           context.Context
	cancel           context.CancelFunc
	operations       sync.WaitGroup
	stopDone         chan struct{}
	repo             ErrorPassthroughRepository
	cache            ErrorPassthroughCache

	// Published snapshots support matching without repository reads.
	localCache   []*cachedPassthroughRule
	localCacheMu sync.RWMutex
}

// cachedPassthroughRule contains precomputed case-folded strings and status-code lookup data.
type cachedPassthroughRule struct {
	*ErrorPassthroughRule
	lowerKeywords  []string         // Lowercase keywords.
	lowerPlatforms []string         // Lowercase platforms.
	errorCodeSet   map[int]struct{} // Status-code lookup set.
}

// NewErrorPassthroughService binds rule storage, cache, and an optional observer.
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

// initialize prepares coordination state lazily, including for directly constructed test instances.
func (s *ErrorPassthroughService) initialize() {
	s.initOnce.Do(func() {
		s.updateGate = make(chan struct{}, 1)
		s.updateGate <- struct{}{}
		s.runCtx, s.cancel = context.WithCancel(context.Background())
	})
}

// beginOperation serializes registration with stopping and combines caller and service cancellation.
func (s *ErrorPassthroughService) beginOperation(ctx context.Context) (context.Context, func(), error) {
	s.initialize()
	s.lifecycleMu.Lock()
	if s.stopped {
		s.lifecycleMu.Unlock()
		return nil, nil, context.Canceled
	}
	s.operations.Add(1)
	s.lifecycleMu.Unlock()
	operation, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.runCtx, cancel)
	if s.runCtx.Err() != nil {
		cancel()
	}
	return operation, func() { stop(); cancel(); s.operations.Done() }, nil
}

func (s *ErrorPassthroughService) acquireUpdate(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.updateGate:
	}
	if err := ctx.Err(); err != nil {
		s.releaseUpdate()
		return err
	}
	return nil
}
func (s *ErrorPassthroughService) releaseUpdate() { s.updateGate <- struct{}{} }

func (s *ErrorPassthroughService) StartContext(ctx context.Context) error {
	s.initialize()
	s.lifecycleMu.Lock()
	if s.started || s.stopped {
		s.lifecycleMu.Unlock()
		return nil
	}
	s.started = true
	s.lifecycleMu.Unlock()
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

// Stop cancels startup and subscription refreshes, then waits for operations using shared resources.
func (s *ErrorPassthroughService) Stop() { _ = s.StopContext(context.Background()) }

func (s *ErrorPassthroughService) StopContext(ctx context.Context) error {
	s.initialize()
	s.lifecycleMu.Lock()
	if !s.stopped {
		s.stopped = true
		s.cancel()
		s.stopDone = make(chan struct{})
		go func() {
			if stopper, ok := s.cache.(interface{ StopSubscription() }); ok {
				stopper.StopSubscription()
			}
			s.operations.Wait()
			close(s.stopDone)
		}()
	}
	done := s.stopDone
	s.lifecycleMu.Unlock()
	// A completed stop returns success even if this caller context has since expired.
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

// List returns independent copies of all persisted rules.
func (s *ErrorPassthroughService) List(ctx context.Context) ([]*ErrorPassthroughRule, error) {
	operation, done, err := s.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rules, err := s.repo.List(operation)
	return cloneRules(rules), err
}

// GetByID returns an independent copy of the requested rule.
func (s *ErrorPassthroughService) GetByID(ctx context.Context, id int64) (*ErrorPassthroughRule, error) {
	operation, done, err := s.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rule, err := s.repo.GetByID(operation, id)
	return cloneRule(rule), err
}

// Create validates and persists a rule, then refreshes caches.
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

	created, err := s.repo.Create(ctx, rule)
	if err != nil {
		return nil, err
	}

	refreshCtx, cancel := s.newCacheRefreshContext()
	defer cancel()
	s.invalidateAndNotify(refreshCtx)

	return cloneRule(created), nil
}

// Update validates and replaces a rule, then refreshes caches.
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

	updated, err := s.repo.Update(ctx, rule)
	if err != nil {
		return nil, err
	}

	refreshCtx, cancel := s.newCacheRefreshContext()
	defer cancel()
	s.invalidateAndNotify(refreshCtx)

	return cloneRule(updated), nil
}

// Delete removes a rule and refreshes caches.
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

	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}

	refreshCtx, cancel := s.newCacheRefreshContext()
	defer cancel()
	s.invalidateAndNotify(refreshCtx)

	return nil
}

// MatchRule returns a copy of the first enabled matching rule in priority order,
// or nil when no rule matches.
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

// getCachedRules returns the priority-ordered local snapshot, refreshing an uninitialized cache.
func (s *ErrorPassthroughService) getCachedRules() []*cachedPassthroughRule {
	s.localCacheMu.RLock()
	rules := s.localCache
	s.localCacheMu.RUnlock()

	if rules != nil {
		return rules
	}

	ctx := context.Background()
	if err := s.refreshLocalCache(ctx); err != nil {
		s.log("[ErrorPassthroughService] Failed to refresh cache: %v", err)
		return nil
	}

	s.localCacheMu.RLock()
	defer s.localCacheMu.RUnlock()
	return s.localCache
}

// refreshLocalCache serializes cache refresh with rule writes.
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

func (s *ErrorPassthroughService) refreshLocalCacheLocked(ctx context.Context) error {
	// Prefer the shared cache before reading the repository.
	if s.cache != nil {
		if rules, ok := s.cache.Get(ctx); ok {
			s.setLocalCache(rules)
			return nil
		}
	}

	return s.reloadRulesFromDBLocked(ctx)
}

// reloadRulesFromDB reads current rules directly from the repository and publishes a sorted snapshot.
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

func (s *ErrorPassthroughService) reloadRulesFromDBLocked(ctx context.Context) error {
	rules, err := s.repo.List(ctx)
	if err != nil {
		return err
	}

	if s.cache != nil {
		if err := s.cache.Set(ctx, rules); err != nil {
			s.log("[ErrorPassthroughService] Failed to set cache: %v", err)
		}
	}

	// setLocalCache sorts the published rules by priority.
	s.setLocalCache(rules)

	return nil
}

// setLocalCache clones and compiles rules before publishing the local snapshot.
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
	s.localCache = cached
	s.localCacheMu.Unlock()
}

// clearLocalCache invalidates the local snapshot after a failed refresh.
func (s *ErrorPassthroughService) clearLocalCache() {
	s.localCacheMu.Lock()
	s.localCache = nil
	s.localCacheMu.Unlock()
}

// newCacheRefreshContext gives post-write cache updates three seconds, independent of request cancellation.
func (s *ErrorPassthroughService) newCacheRefreshContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(s.runCtx, 3*time.Second)
}

// invalidateAndNotify invalidates the shared cache, reloads local rules, and notifies other instances.
func (s *ErrorPassthroughService) invalidateAndNotify(ctx context.Context) {
	// Invalidate first so subsequent refreshes obtain the persisted rules.
	if s.cache != nil {
		if err := s.cache.Invalidate(ctx); err != nil {
			s.log("[ErrorPassthroughService] Failed to invalidate cache: %v", err)
		}
	}

	if err := s.reloadRulesFromDBLocked(ctx); err != nil {
		s.log("[ErrorPassthroughService] Failed to refresh local cache: %v", err)
		// Discard stale local rules when reloading fails.
		s.clearLocalCache()
	}

	if s.cache != nil {
		if err := s.cache.NotifyUpdate(ctx); err != nil {
			s.log("[ErrorPassthroughService] Failed to notify cache update: %v", err)
		}
	}
}

// ensureBodyLower lowercases the first 8 KiB once, when keyword matching first needs it.
func ensureBodyLower(body []byte, bodyLower *string, done *bool) string {
	if *done {
		return *bodyLower
	}
	b := body
	if len(b) > maxBodyMatchLen {
		b = b[:maxBodyMatchLen]
	}
	*bodyLower = strings.ToLower(string(b))
	*done = true
	return *bodyLower
}

// platformMatchesCached checks the precomputed lowercase platform filter; an empty filter matches all.
func (s *ErrorPassthroughService) platformMatchesCached(rule *cachedPassthroughRule, lowerPlatform string) bool {
	if len(rule.lowerPlatforms) == 0 {
		return true
	}
	return slices.Contains(rule.lowerPlatforms, lowerPlatform)
}

// ruleMatchesOptimized short-circuits configured conditions and converts the body only when needed.
func (s *ErrorPassthroughService) ruleMatchesOptimized(rule *cachedPassthroughRule, statusCode int, body []byte, bodyLower *string, bodyLowerDone *bool) bool {
	hasErrorCodes := len(rule.errorCodeSet) > 0
	hasKeywords := len(rule.lowerKeywords) > 0

	if !hasErrorCodes && !hasKeywords {
		return false
	}

	codeMatch := !hasErrorCodes || s.containsIntSet(rule.errorCodeSet, statusCode)

	if rule.MatchMode == MatchModeAll {
		// All configured categories must match.
		if hasErrorCodes && !codeMatch {
			return false
		}
		if hasKeywords {
			return s.containsAnyKeywordCached(ensureBodyLower(body, bodyLower, bodyLowerDone), rule.lowerKeywords)
		}
		return codeMatch
	}

	// Either configured category may match.
	if hasErrorCodes && hasKeywords {
		if codeMatch {
			return true
		}
		return s.containsAnyKeywordCached(ensureBodyLower(body, bodyLower, bodyLowerDone), rule.lowerKeywords)
	}
	// Evaluate the single configured category.
	if hasKeywords {
		return s.containsAnyKeywordCached(ensureBodyLower(body, bodyLower, bodyLowerDone), rule.lowerKeywords)
	}
	return codeMatch
}

func (s *ErrorPassthroughService) containsIntSet(set map[int]struct{}, val int) bool {
	_, ok := set[val]
	return ok
}

// containsAnyKeywordCached checks case-folded keywords against the case-folded body.
func (s *ErrorPassthroughService) containsAnyKeywordCached(bodyLower string, lowerKeywords []string) bool {
	for _, kw := range lowerKeywords {
		if strings.Contains(bodyLower, kw) {
			return true
		}
	}
	return false
}

// cloneRule isolates mutable input and result fields from published snapshots.
func cloneRule(rule *ErrorPassthroughRule) *ErrorPassthroughRule {
	if rule == nil {
		return nil
	}
	copy := *rule
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

// CloneRules copies rules for Redis storage, retaining the distinction between nil and empty slices.
func CloneRules(rules []*ErrorPassthroughRule) []*ErrorPassthroughRule { return cloneRules(rules) }

func (s *ErrorPassthroughService) log(message string, args ...any) {
	if s.observe != nil {
		s.observe(message, args...)
	}
}
