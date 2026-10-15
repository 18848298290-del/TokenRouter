package egress

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"
)

// tls_profile_service.go publishes cached TLS profiles and selects
// fingerprints for requests and token operations.
// Runtime fingerprint selection reads cloned profiles from a locally published
// ID map. Database writes refresh that map and the distributed cache after
// persistence, using an independent three-second context so cancellation of the
// admin request does not cancel the refresh immediately.
//
// A successful read publishes a complete new map, and readers clone values
// before returning them to requests. Cache writes and notifications are best
// effort; reload failure after a write clears local profiles and still attempts
// notification. Start performs its initial load and subscription once, while
// Stop delegates subscriber shutdown when supported. Selection distinguishes
// fixed, random, built-in, and unavailable router choices so callers can choose
// provider fallback deliberately.

// TLSFingerprintProfileRepository persists editable profiles and returns
// records for administration and cache reloads.
type TLSFingerprintProfileRepository interface {
	// List returns persisted fingerprint profiles for administration and runtime
	// cache rebuilding.
	List(ctx context.Context) ([]*TLSFingerprintProfile, error)
	// GetByID returns the stored fingerprint profile for id, or nil when absent.
	// Callers distinguish that missing result from a repository failure before
	// editing or returning an HTTP response.
	GetByID(ctx context.Context, id int64) (*TLSFingerprintProfile, error)
	// Create persists the supplied profile and returns its stored field values.
	Create(ctx context.Context, profile *TLSFingerprintProfile) (*TLSFingerprintProfile, error)
	// Update replaces the stored profile identified by its ID and returns
	// persisted values.
	Update(ctx context.Context, profile *TLSFingerprintProfile) (*TLSFingerprintProfile, error)
	// Delete removes the stored fingerprint profile identified by the supplied
	// database ID.
	Delete(ctx context.Context, id int64) error
}

// TLSFingerprintProfileCache distributes stored profiles and refresh
// notifications between service instances and local readers.
type TLSFingerprintProfileCache interface {
	// Get returns cached profiles and a hit flag for runtime refresh. A miss
	// selects database loading; cache adapters return copied collections so
	// service publication can retain its own snapshot.
	Get(ctx context.Context) ([]*TLSFingerprintProfile, bool)
	// Set writes the supplied profile collection to cache and retains separate
	// local storage. Callers keep input values stable while the adapter serializes
	// and clones their nested fields.
	Set(ctx context.Context, profiles []*TLSFingerprintProfile) error
	// Invalidate removes local and distributed profile values before a subsequent
	// refresh. The local removal can complete even if the distributed deletion
	// returns an error to the service caller.
	Invalidate(ctx context.Context) error
	// NotifyUpdate publishes a profile refresh request for subscriber service
	// instances.
	NotifyUpdate(ctx context.Context) error
	// SubscribeUpdates registers one refresh callback for cache notifications
	// using ctx. The callback can perform repository work synchronously, so
	// service shutdown waits for callback completion when the adapter supports
	// subscription stopping.
	SubscribeUpdates(ctx context.Context, handler func())
}

// TLSFingerprintProfileService publishes cloned cached profiles and coordinates
// administration refresh and subscription lifecycle.
//
// localMu protects localCache replacement and lookup. Each published map owns
// cloned profile collections and remains immutable after replacement, allowing
// a retrieved entry to be cloned after the read lock is released.
type TLSFingerprintProfileService struct {
	diagnostics Diagnostics                     // cache refresh diagnostic sink
	repo        TLSFingerprintProfileRepository // profile administration persistence dependency
	// cache is optional and shared with the application's cache adapter. Stop
	// invokes its optional StopSubscription method before Redis closes; Start
	// installs the subscription at most once.
	cache TLSFingerprintProfileCache // optional distributed profile cache

	localCache map[int64]*TLSFingerprintProfile // published ID-to-profile snapshot map
	localMu    sync.RWMutex                     // local snapshot publication lock
	startOnce  sync.Once                        // one-time initial subscriber registration
}

// NewTLSFingerprintProfileService constructs an idle service with an empty
// local profile map. It retains repo and optional cache, uses the first
// diagnostics option, and leaves cache warming and subscription registration to
// Start.
func NewTLSFingerprintProfileService(
	repo TLSFingerprintProfileRepository,
	cache TLSFingerprintProfileCache,
	diagnostics ...Diagnostics,
) *TLSFingerprintProfileService {
	svc := &TLSFingerprintProfileService{
		diagnostics: diagnosticsOption(diagnostics),
		repo:        repo,
		cache:       cache,
		localCache:  make(map[int64]*TLSFingerprintProfile),
	}

	return svc
}

// Stop delegates terminal subscription shutdown when the cache implements
// StopSubscription. Nil receivers or absent caches do nothing; the application
// calls this before closing Redis and keeps refresh callbacks available until
// they return.
func (s *TLSFingerprintProfileService) Stop() {
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

// List returns persisted profiles for administrative queries through the
// repository.
func (s *TLSFingerprintProfileService) List(ctx context.Context) ([]*TLSFingerprintProfile, error) {
	return s.repo.List(ctx)
}

// GetByID returns the persisted profile requested for administration, including
// nil for an absent ID. It delegates repository errors to the caller; runtime
// selection uses GetProfileByID and the local snapshot instead.
func (s *TLSFingerprintProfileService) GetByID(ctx context.Context, id int64) (*TLSFingerprintProfile, error) {
	return s.repo.GetByID(ctx, id)
}

// Create validates the supplied profile, persists it, and returns the stored
// result. A successful database write is followed by best-effort invalidation,
// reload, and notification using an independent three-second context.
//
// Refresh errors are reported through diagnostics and do not reverse
// persistence or replace the returned write result. The refresh helper can
// clear local selection on a failed reload while still notifying other
// instances, so admin success and runtime cache availability are separate
// outcomes.
func (s *TLSFingerprintProfileService) Create(ctx context.Context, profile *TLSFingerprintProfile) (*TLSFingerprintProfile, error) {
	if err := profile.Validate(); err != nil {
		return nil, err
	}

	// Persist before invalidating or rebuilding selection. A repository failure
	// returns before this write path starts cache refresh, while a successful
	// write keeps its result even if the later best-effort refresh fails.
	created, err := s.repo.Create(ctx, profile)
	if err != nil {
		return nil, err
	}

	refreshCtx, cancel := s.newCacheRefreshContext()
	defer cancel()
	s.invalidateAndNotify(refreshCtx)

	return created, nil
}

// Update validates a complete profile value and replaces the record identified
// by its ID. The repository clears optional descriptions and lists supplied as
// absent or empty, then returns stored values after persistence.
//
// Post-write invalidation, reload, and notification share an independent
// three-second context. Their failures are logged and cannot reverse a
// successful replacement. A reload failure clears local selection, so callers
// can receive a successful admin update while subsequent requests use the
// resolver's unavailable-profile fallback.
func (s *TLSFingerprintProfileService) Update(ctx context.Context, profile *TLSFingerprintProfile) (*TLSFingerprintProfile, error) {
	if err := profile.Validate(); err != nil {
		return nil, err
	}

	// Persist before invalidating or rebuilding selection. A repository failure
	// returns before this write path starts cache refresh, while a successful
	// write keeps its result even if the later best-effort refresh fails.
	updated, err := s.repo.Update(ctx, profile)
	if err != nil {
		return nil, err
	}

	refreshCtx, cancel := s.newCacheRefreshContext()
	defer cancel()
	s.invalidateAndNotify(refreshCtx)

	return updated, nil
}

// Delete removes the stored profile and then attempts invalidation, database
// reload, and notification using an independent three-second context. Repository
// failure returns directly before refresh. Successful deletion remains
// successful even if later cache operations fail.
//
// The refresh helper logs failures and clears the current local map when its
// reload fails, then still attempts notification with the same remaining budget.
// Persistence and runtime availability can therefore have different outcomes.
// Runtime callers choose built-in settings or another configured selection
// through the profile resolvers.
func (s *TLSFingerprintProfileService) Delete(ctx context.Context, id int64) error {
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

// GetProfileByID returns a cloned profile from the published local snapshot or
// nil for a missing entry. Nil receivers also return nil; callers choose an
// appropriate built-in or provider fallback for unavailable selections.
func (s *TLSFingerprintProfileService) GetProfileByID(id int64) *TLSFingerprintProfile {
	if s == nil {
		return nil
	}
	s.localMu.RLock()
	p, ok := s.localCache[id]
	s.localMu.RUnlock()

	if ok && p != nil {
		return CloneTLSFingerprintProfile(p)
	}
	return nil
}

// getRandomProfile returns a clone of a randomly selected nonnil local profile.
// Nil receivers, an empty map, or a map containing only nil values return nil
// for the caller's selection fallback.
func (s *TLSFingerprintProfileService) getRandomProfile() *TLSFingerprintProfile {
	if s == nil {
		return nil
	}
	s.localMu.RLock()
	defer s.localMu.RUnlock()

	if len(s.localCache) == 0 {
		return nil
	}

	// Select from usable stored entries before drawing an index, so a nil
	// cache value cannot turn an otherwise available random choice into absence.
	profiles := make([]*TLSFingerprintProfile, 0, len(s.localCache))
	for _, p := range s.localCache {
		if p != nil {
			profiles = append(profiles, p)
		}
	}
	if len(profiles) == 0 {
		return nil
	}

	return CloneTLSFingerprintProfile(profiles[rand.IntN(len(profiles))])
}

// ResolveTLSProfileByID returns nil when fingerprint eligibility is disabled
// and otherwise selects fixed, random, or built-in settings. Positive IDs use
// the local snapshot, -1 requests a random profile, and other IDs select
// built-in defaults.
//
// A missing positive entry or unavailable random choice also uses the built-in
// profile, so the provider-bound selection remains usable when local profiles
// are absent. The returned configured profile is cloned and the built-in value
// has empty lists for transport defaults. Callers determine eligibility before
// invoking this resolver.
func (s *TLSFingerprintProfileService) ResolveTLSProfileByID(enabled bool, id int64) *TLSFingerprintProfile {
	if !enabled {
		return nil
	}
	if id > 0 {
		if p := s.GetProfileByID(id); p != nil {
			return CloneTLSFingerprintProfile(p)
		}
	}
	if id == -1 {
		// Random selection can be unavailable when the snapshot has no usable
		// entries. Fall through to built-in settings in that provider-bound case.
		if p := s.getRandomProfile(); p != nil {
			return CloneTLSFingerprintProfile(p)
		}
	}
	// A provider-bound selection remains enabled when its chosen stored
	// profile is unavailable. Empty lists select the dialer's effective defaults.
	return &TLSFingerprintProfile{Name: "Built-in Default (Node.js 24.x)"}
}

// ResolveRoutableTLSProfileByID selects a router profile and reports whether
// the selection is available. Disabled eligibility returns false; positive IDs
// need a local entry, and -1 needs a usable random entry.
//
// Those unavailable choices return nil and false so request selection can try
// the provider-bound profile. Other IDs return built-in settings and true.
// Configured results are cloned by the local lookup helpers, and the built-in
// profile leaves its lists empty for the transport dialer's effective defaults.
func (s *TLSFingerprintProfileService) ResolveRoutableTLSProfileByID(enabled bool, id int64) (*TLSFingerprintProfile, bool) {
	if !enabled {
		return nil, false
	}
	if id > 0 {
		if p := s.GetProfileByID(id); p != nil {
			return p, true
		}
		return nil, false
	}
	if id == -1 {
		if p := s.getRandomProfile(); p != nil {
			return p, true
		}
		return nil, false
	}
	// Nonpositive choices other than -1 reach this built-in selection. It
	// counts as available so the caller keeps the matched router selection.
	return &TLSFingerprintProfile{Name: "Built-in Default (Node.js 24.x)"}, true
}

// ResolveTokenTLSProfileByID selects token-request settings independently of
// provider fingerprint eligibility. A nil service returns nil and false.
// Positive IDs require a local entry, while -1 requires a usable randomly chosen
// profile; those unavailable choices also return nil and false.
//
// Other IDs select a built-in profile and true, with empty lists interpreted by
// transport defaults. Configured results own cloned lists. Token callers can run
// before provider creation and use the availability flag to decide how to handle
// a missing selected profile rather than silently switching a positive or random
// choice to built-in settings.
func (s *TLSFingerprintProfileService) ResolveTokenTLSProfileByID(id int64) (*TLSFingerprintProfile, bool) {
	if s == nil {
		return nil, false
	}
	if id > 0 {
		if p := s.GetProfileByID(id); p != nil {
			return p, true
		}
		return nil, false
	}
	if id == -1 {
		if p := s.getRandomProfile(); p != nil {
			return p, true
		}
		return nil, false
	}
	// Token calls can precede provider creation. The built-in branch is
	// therefore available without checking a provider-specific fingerprint switch.
	return &TLSFingerprintProfile{Name: "Built-in Default (Node.js 24.x)"}, true
}

// refreshLocalCache publishes cache-hit profile values into the local map, or
// reloads from the repository on a miss. A database failure leaves the current
// map unchanged here and is returned to the invoking refresh path.
func (s *TLSFingerprintProfileService) refreshLocalCache(ctx context.Context) error {
	if s.cache != nil {
		if profiles, ok := s.cache.Get(ctx); ok {
			s.setLocalCache(profiles)
			return nil
		}
	}
	// A cache miss includes Redis failures and unusable payloads. Read the
	// authoritative repository so selection can still refresh when caching is unavailable.
	return s.reloadFromDB(ctx)
}

// reloadFromDB reads stored profiles, attempts distributed publication, and
// installs an owned local snapshot. Repository failure returns before either
// cache update. A distributed-cache write error is logged while local
// publication continues, keeping the successful repository read usable for
// runtime selection.
//
// setLocalCache indexes cloned values by profile ID, skipping nil records and
// replacing duplicate IDs. Remote writes and local replacement are separate
// operations. Competing refreshes can publish in completion order; this helper
// does not compare database versions to prevent an older read from replacing a
// later snapshot.
func (s *TLSFingerprintProfileService) reloadFromDB(ctx context.Context) error {
	profiles, err := s.repo.List(ctx)
	if err != nil {
		return err
	}

	if s.cache != nil {
		// Distributed cache failure should not discard successfully read profiles.
		// Report that failure while still publishing the cloned local selection map.
		if err := s.cache.Set(ctx, profiles); err != nil {
			s.diagnostics.Log("service.tls_fp_profile", "[TLSFPProfileService] Failed to set cache: %v", err)
		}
	}

	s.setLocalCache(profiles)
	return nil
}

// setLocalCache clones nonnil profiles into a complete ID map and publishes it
// under localMu. Later duplicate IDs replace earlier values. Callers keep
// supplied profile values stable while cloning; each published map and its
// entries remain immutable.
//
// Cloning precedes the publication lock, and replacing the whole map lets
// readers retain an old entry after lookup releases its lock. Overlapping
// refreshes replace maps in publication order, with no version comparison; a
// slower read can therefore install older values after another refresh.
func (s *TLSFingerprintProfileService) setLocalCache(profiles []*TLSFingerprintProfile) {
	m := make(map[int64]*TLSFingerprintProfile, len(profiles))
	for _, p := range profiles {
		if p != nil {
			m[p.ID] = CloneTLSFingerprintProfile(p)
		}
	}

	// Construct owned values before taking the publication lock, then replace
	// the whole map so a lookup observes one complete local profile snapshot.
	s.localMu.Lock()
	s.localCache = m
	s.localMu.Unlock()
}

// newCacheRefreshContext creates an independent three-second background context
// for post-write refresh. The caller cancels it after invalidation, reload, and
// notification; request cancellation does not directly cancel this refresh
// context.
func (s *TLSFingerprintProfileService) newCacheRefreshContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 3*time.Second)
}

// invalidateAndNotify attempts distributed invalidation, database reload, and
// cross-instance notification in that order. Its caller provides the refresh
// context and treats these operations as best effort after persistence.
// Failures are logged rather than returned.
//
// A reload failure replaces the local map with an empty map so unavailable
// profiles select the resolver's defined fallback. Notification is attempted
// even after invalidation or reload failure. Each operation uses the same
// supplied context, so an earlier exhausted budget can also affect the later
// attempts.
func (s *TLSFingerprintProfileService) invalidateAndNotify(ctx context.Context) {
	if s.cache != nil {
		if err := s.cache.Invalidate(ctx); err != nil {
			s.diagnostics.Log("service.tls_fp_profile", "[TLSFPProfileService] Failed to invalidate cache: %v", err)
		}
	}

	if err := s.reloadFromDB(ctx); err != nil {
		s.diagnostics.Log("service.tls_fp_profile", "[TLSFPProfileService] Failed to refresh local cache: %v", err)
		// A failed post-write reload cannot confirm the old profile contents.
		// Publish absence so subsequent selection uses its documented unavailable-profile path.
		s.localMu.Lock()
		s.localCache = make(map[int64]*TLSFingerprintProfile)
		s.localMu.Unlock()
	}

	if s.cache != nil {
		if err := s.cache.NotifyUpdate(ctx); err != nil {
			s.diagnostics.Log("service.tls_fp_profile", "[TLSFPProfileService] Failed to notify cache update: %v", err)
		}
	}
}

// Start loads profiles and registers the optional refresh subscriber once for
// the service lifetime. Initial database failure tries the cache path, which
// can itself retry the database on a miss. Load failures are logged.
//
// The subscription callback refreshes with a background context when
// notifications arrive, and callback failure leaves the current local snapshot
// available. startOnce records registration even after failed warming, so later
// Start calls do not retry startup. The application stops the subscriber before
// closing its cache resources.
func (s *TLSFingerprintProfileService) Start() {
	s.startOnce.Do(func() {
		ctx := context.Background()
		if err := s.reloadFromDB(ctx); err != nil {
			s.diagnostics.Log("service.tls_fp_profile", "[TLSFPProfileService] Failed to load profiles from DB on startup: %v", err)
			// Initial database failure may still have a usable cached collection.
			// Try that refresh path before leaving startup with an empty local map.
			if fallbackErr := s.refreshLocalCache(ctx); fallbackErr != nil {
				s.diagnostics.Log("service.tls_fp_profile", "[TLSFPProfileService] Failed to load profiles from cache fallback on startup: %v", fallbackErr)
			}
		}

		if s.cache != nil {
			s.cache.SubscribeUpdates(ctx, func() {
				if err := s.refreshLocalCache(context.Background()); err != nil {
					s.diagnostics.Log("service.tls_fp_profile", "[TLSFPProfileService] Failed to refresh cache on notification: %v", err)
				}
			})
		}
	})
}
