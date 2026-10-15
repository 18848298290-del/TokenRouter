package egress

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"
)

// tls_router_service.go manages stored TLS routers and matches requests against
// immutable snapshots with precompiled patterns in stored rule order. The first
// enabled matching rule supplies the profile choice and outgoing client
// headers, while missing, disabled, and unmatched routers retain the metadata
// useful for request diagnostics.
//
// Database writes normalize configuration and refresh local and distributed
// snapshots with an independent three-second context. Each publication clones
// router optional values and rules before compiling matching forms, so requests
// can read a prior snapshot after replacement. Missing runtime IDs trigger a
// refresh, cache failures can fall back to the repository, and failed
// post-write reload clears local routers before notifying other instances.
// Startup and subscription registration occur once; the application owns
// subscriber shutdown.

// TLSFingerprintRouterRepository persists ordered router configuration for
// administration and runtime snapshot rebuilding.
type TLSFingerprintRouterRepository interface {
	// List returns stored routers for administration and runtime cache rebuilding.
	List(ctx context.Context) ([]*TLSFingerprintRouter, error)
	// GetByID returns the stored router for id or nil when absent. Administrative
	// callers distinguish that missing result from a repository error before
	// applying updates or returning HTTP responses.
	GetByID(ctx context.Context, id int64) (*TLSFingerprintRouter, error)
	// Create persists the supplied router and returns its stored field values.
	Create(ctx context.Context, router *TLSFingerprintRouter) (*TLSFingerprintRouter, error)
	// Update replaces the stored router identified by ID and returns persisted
	// values.
	Update(ctx context.Context, router *TLSFingerprintRouter) (*TLSFingerprintRouter, error)
	// Delete removes the stored router identified by the supplied database ID.
	Delete(ctx context.Context, id int64) error
}

// TLSFingerprintRouterCache distributes router values and refresh notifications
// across local readers and service instances.
type TLSFingerprintRouterCache interface {
	// Get returns cached routers and a hit flag for runtime refresh. Misses select
	// database loading, and cache adapters return copied collections so the
	// service can publish its own compiled snapshot.
	Get(ctx context.Context) ([]*TLSFingerprintRouter, bool)
	// Set writes routers to cache and retains separately cloned local values. The
	// caller keeps optional fields and rule lists stable while the adapter
	// serializes and clones them.
	Set(ctx context.Context, routers []*TLSFingerprintRouter) error
	// Invalidate clears local and distributed router values before service
	// refresh. Local removal can complete even when distributed deletion returns
	// an error, which the service reports through diagnostics.
	Invalidate(ctx context.Context) error
	// NotifyUpdate publishes a router refresh request for subscriber service
	// instances.
	NotifyUpdate(ctx context.Context) error
	// SubscribeUpdates registers one synchronous refresh callback using ctx for
	// cache notifications. Subscription shutdown waits for a running callback when
	// supported, so application shutdown keeps callback dependencies available
	// until it returns.
	SubscribeUpdates(ctx context.Context, handler func())
}

// TLSFingerprintRouterMatchResult contains the first matching User-Agent rule's settings.
type TLSFingerprintRouterMatchResult struct {
	// Matched gates the rule-derived RuleName, TLSFingerprintProfileID,
	// UpstreamUserAgent, and UpstreamOriginator values. RouterID and RouterName
	// can be present for unmatched requests, preserving available router
	// diagnostics.
	Matched                 bool   // enabled rule match flag
	RouterID                int64  // requested or matched router identity
	RouterName              string // available router display name
	RuleName                string // first matched rule name
	TLSFingerprintProfileID int64  // matched rule profile selection
	UpstreamUserAgent       string // matched outgoing User-Agent text
	UpstreamOriginator      string // matched outgoing originator text
}

// cachedTLSFingerprintRouter couples an owned router snapshot with ordered
// precompiled matching rules.
type cachedTLSFingerprintRouter struct {
	// TLSFingerprintRouter and rules are produced together by
	// newCachedTLSFingerprintRouter. They remain immutable after local publication
	// so concurrent matching can read shared snapshots after the lookup lock is
	// released.
	*TLSFingerprintRouter                                  // owned router configuration snapshot
	rules                 []cachedTLSFingerprintRouterRule // ordered compiled matching rules
}

// cachedTLSFingerprintRouterRule retains one normalized rule and its
// precomputed match representation.
type cachedTLSFingerprintRouterRule struct {
	// pattern supplies literal match modes and is lowercased for insensitive
	// rules. regex supplies regex mode with an optional insensitive prefix; failed
	// compilation leaves regex nil and the rule unable to match.
	TLSFingerprintRouterRule                // owned normalized rule values
	pattern                  string         // precomputed literal match pattern
	regex                    *regexp.Regexp // optional compiled regex matcher
}

// TLSFingerprintRouterService manages User-Agent rules that select TLS profiles.
//
// localMu protects localCache replacement and ID lookup. Cached router values
// are cloned and compiled before publication, then remain immutable while
// readers retain a shared internal pointer.
type TLSFingerprintRouterService struct {
	diagnostics Diagnostics                    // router refresh diagnostic sink
	repo        TLSFingerprintRouterRepository // router configuration persistence dependency
	// cache is optional and shared with the application's adapter. Stop delegates
	// its optional StopSubscription method before Redis closes; startup
	// registration remains guarded by startOnce.
	cache TLSFingerprintRouterCache // optional distributed router cache

	localCache map[int64]*cachedTLSFingerprintRouter // published compiled-router snapshot map
	localMu    sync.RWMutex                          // local snapshot publication lock
	startOnce  sync.Once                             // one-time startup subscription registration
}

// NewTLSFingerprintRouterService constructs an idle router service with an
// empty local ID map. It retains the repository and optional cache, selects the
// first diagnostics option, and leaves warming and subscription registration to
// Start.
func NewTLSFingerprintRouterService(
	repo TLSFingerprintRouterRepository,
	cache TLSFingerprintRouterCache,
	diagnostics ...Diagnostics,
) *TLSFingerprintRouterService {
	svc := &TLSFingerprintRouterService{
		diagnostics: diagnosticsOption(diagnostics),
		repo:        repo,
		cache:       cache,
		localCache:  make(map[int64]*cachedTLSFingerprintRouter),
	}

	return svc
}

// Stop delegates terminal subscriber shutdown when the cache implements
// StopSubscription. Nil receivers and absent caches do nothing; the application
// keeps refresh dependencies available until callbacks return and invokes this
// before closing Redis.
func (s *TLSFingerprintRouterService) Stop() {
	if s == nil || s.cache == nil {
		return
	}
	if stopper, ok := s.cache.(interface {
		// StopSubscription cancels the retained subscriber and waits for completion.
		// Call it outside subscription callbacks before closing Redis, because
		// synchronous callback work needs to return for cleanup to finish.
		StopSubscription()
	}); ok {
		stopper.StopSubscription()
	}
}

// List returns persisted router configuration for administrative queries
// through the repository.
func (s *TLSFingerprintRouterService) List(ctx context.Context) ([]*TLSFingerprintRouter, error) {
	return s.repo.List(ctx)
}

// GetByID returns stored router configuration for administration, including nil
// for an absent ID. It passes repository errors to the caller; runtime matching
// instead reads the compiled local snapshot.
func (s *TLSFingerprintRouterService) GetByID(ctx context.Context, id int64) (*TLSFingerprintRouter, error) {
	return s.repo.GetByID(ctx, id)
}

// Create normalizes the supplied router in place, validates it, persists it,
// and returns the stored result. After persistence, refresh uses an independent
// three-second context for invalidation, database reload, and notification.
//
// The normalization modifies names and rule values before validation, so
// callers retain those edits even when validation rejects the router. Refresh
// errors are logged and can clear local availability after a failed reload;
// they do not reverse the stored write or change its returned success.
func (s *TLSFingerprintRouterService) Create(ctx context.Context, router *TLSFingerprintRouter) (*TLSFingerprintRouter, error) {
	normalizeTLSFingerprintRouter(router)
	if err := router.Validate(); err != nil {
		return nil, err
	}

	// Persist before invalidating or rebuilding selection. A repository failure
	// returns before this write path starts cache refresh, while a successful
	// write keeps its result even if the later best-effort refresh fails.
	created, err := s.repo.Create(ctx, router)
	if err != nil {
		return nil, err
	}

	refreshCtx, cancel := s.newCacheRefreshContext()
	defer cancel()
	s.invalidateAndNotify(refreshCtx)

	return created, nil
}

// Update normalizes and validates a complete router value before replacing the
// stored record identified by ID. Normalization edits the supplied names and
// rule values even when validation later fails, and persistence clears absent
// optional configuration according to repository rules.
//
// After replacement, invalidation, reload, and notification share an
// independent three-second context. These refresh failures are logged and
// cannot reverse the stored replacement; failed reload clears local matching so
// later runtime lookup takes its missing-router refresh path. The returned
// value reflects the repository write outcome.
func (s *TLSFingerprintRouterService) Update(ctx context.Context, router *TLSFingerprintRouter) (*TLSFingerprintRouter, error) {
	normalizeTLSFingerprintRouter(router)
	if err := router.Validate(); err != nil {
		return nil, err
	}

	// Persist before invalidating or rebuilding selection. A repository failure
	// returns before this write path starts cache refresh, while a successful
	// write keeps its result even if the later best-effort refresh fails.
	updated, err := s.repo.Update(ctx, router)
	if err != nil {
		return nil, err
	}

	refreshCtx, cancel := s.newCacheRefreshContext()
	defer cancel()
	s.invalidateAndNotify(refreshCtx)

	return updated, nil
}

// Delete removes the stored router and then attempts invalidation, database
// reload, and notification using an independent three-second context. Repository
// failure returns directly before refresh. Successful deletion remains
// successful even if later cache operations fail.
//
// The refresh helper logs failures and clears the current local map when its
// reload fails, then still attempts notification with the same remaining budget.
// Persistence and runtime availability can therefore have different outcomes.
// Later runtime lookup can refresh the emptied map before returning its
// missing-router result.
func (s *TLSFingerprintRouterService) Delete(ctx context.Context, id int64) error {
	// Persist before invalidating or rebuilding selection. A repository failure
	// returns before this write path starts cache refresh, while a successful
	// write keeps its result even if the later best-effort refresh fails.
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}

	refreshCtx, cancel := s.newCacheRefreshContext()
	defer cancel()
	s.invalidateAndNotify(refreshCtx)

	return nil
}

// MatchUserAgent returns the first enabled rule matching the trimmed User-Agent
// in an enabled router. Nil services and nonpositive IDs return an empty
// result; missing or disabled routers retain the requested RouterID.
//
// A usable router retains its name even for blank or unmatched User-Agents,
// allowing diagnostics to identify the attempted configuration. Rules use the
// immutable compiled snapshot in stored order, so one earlier match wins even
// if later rules also match. Profile availability is resolved separately after
// this method returns the rule's configured selection.
// @project-doc docs/operations/upstream_transport_security.md#upstream_tls_routing
func (s *TLSFingerprintRouterService) MatchUserAgent(routerID int64, userAgent string) TLSFingerprintRouterMatchResult {
	if s == nil || routerID <= 0 {
		return TLSFingerprintRouterMatchResult{}
	}
	router := s.getCachedRouter(routerID)
	if router == nil || !router.Enabled {
		return TLSFingerprintRouterMatchResult{RouterID: routerID}
	}
	ua := strings.TrimSpace(userAgent)
	if ua == "" {
		return TLSFingerprintRouterMatchResult{RouterID: routerID, RouterName: router.Name}
	}
	for _, rule := range router.rules {
		if !rule.Enabled {
			continue
		}
		if tlsRouterRuleMatches(rule, ua) {
			return TLSFingerprintRouterMatchResult{
				Matched:                 true,
				RouterID:                router.ID,
				RouterName:              router.Name,
				RuleName:                rule.Name,
				TLSFingerprintProfileID: rule.TLSFingerprintProfileID,
				UpstreamUserAgent:       rule.UpstreamUserAgent,
				UpstreamOriginator:      rule.UpstreamOriginator,
			}
		}
	}
	return TLSFingerprintRouterMatchResult{RouterID: router.ID, RouterName: router.Name}
}

// GetRuntimeRouter returns a cloned runtime router, refreshing through cache or
// repository on an ID miss. Nil services, nonpositive IDs, failed refresh, or
// continued absence return nil; optional values and rules in successful results
// have separate storage.
func (s *TLSFingerprintRouterService) GetRuntimeRouter(routerID int64) *TLSFingerprintRouter {
	if s == nil || routerID <= 0 {
		return nil
	}
	cached := s.getCachedRouter(routerID)
	if cached == nil || cached.TLSFingerprintRouter == nil {
		return nil
	}
	return CloneTLSFingerprintRouter(cached.TLSFingerprintRouter)
}

// getCachedRouter returns an immutable shared compiled router from the local
// map, refreshing on a miss. Refresh uses a background context; failures are
// logged and return nil, and callers leave returned configuration and match
// representations unchanged.
func (s *TLSFingerprintRouterService) getCachedRouter(id int64) *cachedTLSFingerprintRouter {
	s.localMu.RLock()
	router := s.localCache[id]
	s.localMu.RUnlock()
	if router != nil {
		return router
	}
	// A missing ID may reflect a newly written router on another instance.
	// Refresh before declaring absence, then perform another lookup in the published map.
	if err := s.refreshLocalCache(context.Background()); err != nil {
		s.diagnostics.Log("service.tls_fp_router", "[TLSFPRouterService] Failed to refresh cache: %v", err)
		return nil
	}
	s.localMu.RLock()
	defer s.localMu.RUnlock()
	return s.localCache[id]
}

// refreshLocalCache publishes a cache-hit router collection or reloads from
// persistence on a miss. Database failure leaves the current map unchanged in
// this helper and returns to the refresh caller for handling.
func (s *TLSFingerprintRouterService) refreshLocalCache(ctx context.Context) error {
	if s.cache != nil {
		if routers, ok := s.cache.Get(ctx); ok {
			s.setLocalCache(routers)
			return nil
		}
	}
	// Missing or unusable distributed values select an authoritative database
	// read, keeping router refresh available when the cache adapter cannot supply a hit.
	return s.reloadFromDB(ctx)
}

// reloadFromDB reads stored routers, attempts distributed publication, and
// installs an owned compiled local snapshot. Repository failure returns before
// either cache update. A distributed-cache write error is logged while local
// publication continues, keeping the successful repository read usable for
// runtime matching.
//
// setLocalCache clones optional values and ordered rules before compiling their
// match forms. Remote writes and local replacement are separate operations.
// Competing refreshes can publish in completion order; this helper does not
// compare database versions to prevent an older read from replacing a later
// snapshot.
func (s *TLSFingerprintRouterService) reloadFromDB(ctx context.Context) error {
	routers, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	if s.cache != nil {
		// Keep successful repository reads usable locally when the distributed
		// write fails, while reporting that cache failure for later diagnosis.
		if err := s.cache.Set(ctx, routers); err != nil {
			s.diagnostics.Log("service.tls_fp_router", "[TLSFPRouterService] Failed to set cache: %v", err)
		}
	}
	s.setLocalCache(routers)
	return nil
}

// setLocalCache clones and compiles nonnil routers into a complete replacement
// ID map before publication. Later duplicate IDs replace earlier entries.
// Callers keep source values stable during construction; the published
// configurations and match forms are immutable.
//
// The publication lock covers map replacement, keeping each reader on a complete
// router snapshot while older readers retain their retrieved pointers.
// Compilation happens first so configuration and compiled rule order stay
// associated. Overlapping refreshes publish in completion order, with no
// database-version comparison, so a slower read can install older configuration
// later.
func (s *TLSFingerprintRouterService) setLocalCache(routers []*TLSFingerprintRouter) {
	next := make(map[int64]*cachedTLSFingerprintRouter, len(routers))
	for _, router := range routers {
		if router == nil {
			continue
		}
		next[router.ID] = newCachedTLSFingerprintRouter(router)
	}
	// Compilation finishes before publication, so a reader receives router
	// configuration and match forms from one complete replacement map.
	s.localMu.Lock()
	s.localCache = next
	s.localMu.Unlock()
}

// newCacheRefreshContext allocates an independent three-second background
// context for post-write refresh. Its caller cancels it after cache operations;
// cancellation of the administrative request does not directly cancel this
// context.
func (s *TLSFingerprintRouterService) newCacheRefreshContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 3*time.Second)
}

// invalidateAndNotify attempts cache invalidation, database reload, and refresh
// publication after persistence. Its caller supplies one context for the
// ordered sequence, and operation failures are logged instead of returned.
//
// A failed reload replaces the currently published local router map with an
// empty map. Concurrent refreshes can publish another snapshot afterward. Notification is still
// attempted after earlier failure, but an exhausted context budget can affect
// that attempt too. Other instances decide their refresh outcome through their
// own cache or repository reads.
func (s *TLSFingerprintRouterService) invalidateAndNotify(ctx context.Context) {
	if s.cache != nil {
		if err := s.cache.Invalidate(ctx); err != nil {
			s.diagnostics.Log("service.tls_fp_router", "[TLSFPRouterService] Failed to invalidate cache: %v", err)
		}
	}
	if err := s.reloadFromDB(ctx); err != nil {
		s.diagnostics.Log("service.tls_fp_router", "[TLSFPRouterService] Failed to refresh local cache: %v", err)
		// Post-write reload failure leaves the old rules unconfirmed. Publish an
		// empty map so runtime lookup follows its missing-router refresh path.
		s.localMu.Lock()
		s.localCache = make(map[int64]*cachedTLSFingerprintRouter)
		s.localMu.Unlock()
	}
	if s.cache != nil {
		if err := s.cache.NotifyUpdate(ctx); err != nil {
			s.diagnostics.Log("service.tls_fp_router", "[TLSFPRouterService] Failed to notify cache update: %v", err)
		}
	}
}

// normalizeTLSFingerprintRouter trims editable names, dedicated User-Agents,
// and rule values in place. Unknown match types become contains, nil Rules
// becomes a present empty slice, and nil router input does nothing.
func normalizeTLSFingerprintRouter(router *TLSFingerprintRouter) {
	if router == nil {
		return
	}
	router.Name = strings.TrimSpace(router.Name)
	router.ChatGPTOAuthTokenUserAgent = strings.TrimSpace(router.ChatGPTOAuthTokenUserAgent)
	router.CodexInviteResetUserAgent = strings.TrimSpace(router.CodexInviteResetUserAgent)
	for i := range router.Rules {
		router.Rules[i].Name = strings.TrimSpace(router.Rules[i].Name)
		router.Rules[i].Pattern = strings.TrimSpace(router.Rules[i].Pattern)
		router.Rules[i].MatchType = NormalizeTLSRouterMatchType(router.Rules[i].MatchType)
		router.Rules[i].UpstreamUserAgent = strings.TrimSpace(router.Rules[i].UpstreamUserAgent)
		router.Rules[i].UpstreamOriginator = strings.TrimSpace(router.Rules[i].UpstreamOriginator)
	}
	if router.Rules == nil {
		router.Rules = []TLSFingerprintRouterRule{}
	}
}

// newCachedTLSFingerprintRouter clones router values and compiles ordered rule
// representations for runtime matching. Literal patterns are trimmed and
// optionally lowercased; insensitive regex patterns gain a mode prefix before
// compilation.
//
// The input snapshot remains independent of the returned configuration and rule
// slice. Stored data may bypass service validation, so regex compilation
// failure leaves a nil matcher and that rule cannot match later requests. The
// cached router is published as immutable data, allowing callers to share the
// pointer while another refresh installs a replacement.
func newCachedTLSFingerprintRouter(router *TLSFingerprintRouter) *cachedTLSFingerprintRouter {
	router = CloneTLSFingerprintRouter(router)
	cached := &cachedTLSFingerprintRouter{TLSFingerprintRouter: router}
	for _, rule := range router.Rules {
		rule.MatchType = NormalizeTLSRouterMatchType(rule.MatchType)
		pattern := strings.TrimSpace(rule.Pattern)
		cachedRule := cachedTLSFingerprintRouterRule{
			TLSFingerprintRouterRule: rule,
			pattern:                  pattern,
		}
		if rule.MatchType == TLSRouterMatchRegex {
			// Regex mode keeps case handling in the compiled expression, while
			// literal modes normalize their compared text below for the same rule flag.
			if !rule.CaseSensitive {
				pattern = "(?i)" + pattern
			}
			// Stored rules can predate or bypass write validation. Retain a nil
			// matcher on compilation failure so the unusable regex cannot match a request.
			cachedRule.regex, _ = regexp.Compile(pattern)
		} else if !rule.CaseSensitive {
			cachedRule.pattern = strings.ToLower(pattern)
		}
		cached.rules = append(cached.rules, cachedRule)
	}
	return cached
}

// tlsRouterRuleMatches evaluates a cached rule against the supplied User-Agent
// using its normalized match mode. Literal insensitive modes lowercase the
// value; regex mode uses its compiled expression and returns false when
// compilation was unavailable.
func tlsRouterRuleMatches(rule cachedTLSFingerprintRouterRule, userAgent string) bool {
	value := userAgent
	pattern := rule.pattern
	if !rule.CaseSensitive && rule.MatchType != TLSRouterMatchRegex {
		value = strings.ToLower(value)
	}
	switch NormalizeTLSRouterMatchType(rule.MatchType) {
	case TLSRouterMatchPrefix:
		return strings.HasPrefix(value, pattern)
	case TLSRouterMatchExact:
		return value == pattern
	case TLSRouterMatchRegex:
		return rule.regex != nil && rule.regex.MatchString(userAgent)
	default:
		return strings.Contains(value, pattern)
	}
}

// Start warms router configuration and registers an optional refresh callback
// once for the instance lifetime. Startup read failure tries cache refresh,
// which may retry the database when distributed values are unavailable.
// Failures are logged.
//
// Notifications invoke background refresh synchronously in the adapter
// callback, leaving the prior local map available when that read fails.
// startOnce is consumed even after unsuccessful warming, so later starts do not
// repeat registration. The application stops the retained subscriber before
// closing Redis and repository dependencies.
func (s *TLSFingerprintRouterService) Start() {
	s.startOnce.Do(func() {
		ctx := context.Background()
		if err := s.reloadFromDB(ctx); err != nil {
			s.diagnostics.Log("service.tls_fp_router", "[TLSFPRouterService] Failed to load routers from DB on startup: %v", err)
			// Existing cached router values may remain usable during database
			// failure. Try that refresh path before startup leaves local matching empty.
			if fallbackErr := s.refreshLocalCache(ctx); fallbackErr != nil {
				s.diagnostics.Log("service.tls_fp_router", "[TLSFPRouterService] Failed to load routers from cache fallback on startup: %v", fallbackErr)
			}
		}

		if s.cache != nil {
			s.cache.SubscribeUpdates(ctx, func() {
				if err := s.refreshLocalCache(context.Background()); err != nil {
					s.diagnostics.Log("service.tls_fp_router", "[TLSFPRouterService] Failed to refresh cache on notification: %v", err)
				}
			})
		}
	})
}
