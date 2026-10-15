package provider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// qoder_sessions.go coalesces Qoder session builds and coordinates cached
// sessions with credential generations and shutdown.
// Qoder session builds can outlive the request that first needed their
// credentials. Callers sharing the same provider, full credential digest, and
// generation wait on one asynchronous flight; each caller can leave its own
// wait while the callback remains registered for bounded shutdown.
//
// Changed credential digests use snapshot ordering to reject older identity
// observations; matching digests can replace proxy/TLS inputs without that
// ordering check. Invalidation advances generation so later cache checks and
// new-value installation reject callbacks from the earlier generation. Cache
// values remain shared comparable objects, with zero expiry allowing indefinite
// time-based reuse. Publication
// holds shutdown admission before the state mutex so a stopped holder rejects
// late writes. A reusable cached value can be returned even when its caller
// context is already cancelled, because cache selection precedes that check.

// DefaultQoderSessionBuildTimeout supplies the shared build deadline when a
// holder has no positive configured budget. It is independent of any single
// caller deadline.
const DefaultQoderSessionBuildTimeout = 90 * time.Second

// Qoder session errors distinguish a generation invalidated during exchange
// from holder shutdown, allowing callers to report why a shared build cannot
// supply reusable credentials.
var (
	ErrQoderSessionBuildInvalidated = errors.New("qoder session invalidated during build")
	ErrQoderSessionsStopped         = errors.New("qoder credential sessions are stopped")
)

// QoderSessions shares built comparable sessions across callers while tracking
// provider credential generations.
type QoderSessions[T comparable] struct {
	// Mu protects Sessions and ProviderStates as one provider
	// identity/cache
	// state. External initialization belongs before first use, and direct
	// map
	// inspection or modification uses this same mutex.
	Mu             sync.Mutex                          // cache and identity state lock
	Sessions       map[int64]QoderSessionCacheEntry[T] // cached provider session entries
	ProviderStates map[int64]QoderSessionProviderState // observed credential generations and snapshots
	// SessionBuildGroup coalesces asynchronous callbacks by provider ID,
	// full
	// credential hash, and generation; invalidation starts a distinct key
	// so
	// older callback results can be rejected.
	SessionBuildGroup singleflight.Group
	// SessionBuildTimeout supplies the independent shared-build budget,
	// defaulting to ninety seconds when nonpositive; configure it before
	// concurrent calls begin using the holder.
	SessionBuildTimeout time.Duration
	// activity owns waiter and shared-build registrations until actual
	// completion; shutdown cancels both and rejects cache publication while
	// its
	// stopped flag is held.
	activity operationActivity
}

// QoderSessionCacheEntry associates a built shared value with its full
// credential digest and expiry.
type QoderSessionCacheEntry[T comparable] struct {
	CredentialsHash string // full cached credential JSON digest
	// Session is a shared comparable value; GetSession rejects zero results
	// and
	// consumers coordinate mutations when T contains a pointer to mutable
	// session data.
	Session T
	// ExpiresAt is the explicit cache deadline; zero disables time expiry,
	// while matching or later current time makes the entry unusable for
	// reuse.
	ExpiresAt time.Time
}

// QoderSessionProviderState records accepted credential identity and the
// generation used to validate builds.
type QoderSessionProviderState struct {
	CredentialsHash string // newest accepted credential JSON digest
	// CredentialVersion records the accepted token version when identity is
	// installed; ordering later reads ProviderSnapshot to compare the full
	// candidate observations.
	CredentialVersion int64 // version recorded when identity changes
	// ProviderSnapshot retains the accepted CloneRecord result for future
	// builds; matching credential hashes can replace it with incoming
	// proxy/TLS
	// observations without advancing Generation.
	ProviderSnapshot *Record
	Generation       uint64 // flight and cache invalidation counter
}

// qoderSessionBuildResult carries a shared session value through asynchronous
// singleflight results.
type qoderSessionBuildResult[T comparable] struct {
	// Session carries the shared builder result to flight waiters;
	// GetSession
	// returns that value directly and rejects its zero value before
	// returning
	// success.
	Session T
}

// StopContext cancels registered waiters and shared builds, then waits within
// ctx for actual completion. Nil holders succeed; activity keeps the first
// shutdown result, and later cache publication checks its stopped flag before
// installation.
func (p *QoderSessions[T]) StopContext(ctx context.Context) error {
	if p == nil {
		return nil
	}
	return p.activity.stop(ctx, "qoder credentials")
}

// GetSession returns a current cached session or waits for an asynchronous
// build for the provider's accepted credential identity. Callers supply a Qoder
// COSY record, nonnil context, and builder returning a nonzero comparable
// session. Returned values are shared; callers coordinate mutable session
// access. Credentials need to be JSON-encodable for their hashes to distinguish
// identity; encoding failures share one digest. A cache hit occurs before the
// caller cancellation check.
//
// The record is captured with CloneRecord. When its digest changes,
// provider-state ordering can replace stale incoming identity with the accepted
// snapshot; equal digests accept the incoming snapshot without that comparison.
// Flights include provider ID, the full credential digest, and generation. One
// waiter
// cancelling does not cancel the shared callback, which has its own default
// ninety-second budget and shutdown registration.
//
// Shutdown, caller cancellation, and the result channel compete in the wait
// select without priority when simultaneously ready. Completed callbacks
// recheck context, generation, and shutdown before cache installation;
// a stopped or invalidated generation loses at that publication point. A
// later invalidation can still race with delivery of a previously accepted
// result. Builders keep captured records stable while their flight runs.
// @project-doc docs/interfaces/qoder_upstream.md#qoder_account_contract
func (p *QoderSessions[T]) GetSession(ctx context.Context, provider *Record, build func(context.Context, *Record) (T, time.Time, error)) (T, error) {
	var zero T
	if p == nil {
		return zero, errors.New("qoder token provider is nil")
	}
	if provider == nil {
		return zero, errors.New("provider is nil")
	}
	if provider.Platform != PlatformQoder || provider.Type != ProviderTypeCosy {
		return zero, errors.New("not a qoder cosy provider")
	}

	// Track this waiter independently of its caller cancellation so shared
	// work
	// remains shutdown-owned; the select below still lets this caller leave
	// on
	// cancellation.
	operation, finish, beginErr := p.activity.begin(context.WithoutCancel(ctx), ErrQoderSessionsStopped)
	if beginErr != nil {
		return zero, beginErr
	}
	defer finish()
	providerSnapshot := CloneRecord(provider)
	hash := QoderCredentialsHash(providerSnapshot.Credentials)
	generation, hash, providerSnapshot, session := p.prepareQoderSessionBuild(providerSnapshot, hash)
	if session != zero {
		return session, nil
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}

	// Include full credential identity and generation in the flight key so
	// invalidation can admit a new build while an older exchange is still
	// running.
	flightKey := fmt.Sprintf("%d:%s:%d", providerSnapshot.ID, hash, generation)
	resultCh := p.SessionBuildGroup.DoChan(flightKey, func() (any, error) {
		// Detach the first waiter's cancellation and deadline for
		// shared build
		// ownership; the independent build budget and activity shutdown
		// still
		// cancel that callback.
		buildTimeout := p.SessionBuildTimeout
		if buildTimeout <= 0 {
			buildTimeout = DefaultQoderSessionBuildTimeout
		}
		managed, finishBuild, beginErr := p.activity.begin(context.WithoutCancel(ctx), ErrQoderSessionsStopped)
		if beginErr != nil {
			return zero, beginErr
		}
		defer finishBuild()
		buildCtx, cancel := context.WithTimeout(managed, buildTimeout)
		defer cancel()

		if cached, current := p.cachedQoderSessionForBuild(providerSnapshot.ID, hash, generation); !current {
			return zero, ErrQoderSessionBuildInvalidated
		} else if cached != zero {
			return &qoderSessionBuildResult[T]{Session: cached}, nil
		}

		session, expiresAt, buildErr := build(buildCtx, providerSnapshot)
		if buildErr != nil {
			return zero, buildErr
		}
		if err := buildCtx.Err(); err != nil {
			return zero, err
		}
		// A builder may finish after invalidation or stop; recheck
		// publication
		// ownership before its value can replace the reusable provider
		// cache.
		if !p.storeQoderSessionBuild(providerSnapshot.ID, hash, generation, session, expiresAt) {
			return zero, ErrQoderSessionBuildInvalidated
		}
		return &qoderSessionBuildResult[T]{Session: session}, nil
	})

	var value any
	select {
	case <-operation.Done():
		return zero, ErrQoderSessionsStopped
	case <-ctx.Done():
		return zero, ctx.Err()
	case flightResult := <-resultCh:
		if flightResult.Err != nil {
			// Return the actual shared-build failure, including
			// invalidation, so
			// this caller can report it and a later request can
			// prepare current
			// identity before building again.
			return zero, flightResult.Err
		}
		value = flightResult.Val
	}
	result, ok := value.(*qoderSessionBuildResult[T])
	if !ok || result == nil || result.Session == zero {
		return zero, errors.New("qoder session build returned an invalid result")
	}
	return result.Session, nil
}

// QoderCredentialSnapshotOlder reports whether an incoming snapshot should lose
// to the current credential observation. Nil comparisons return false. Unequal
// positive token versions determine order directly; a positive incoming version
// beats current zero regardless of update time.
//
// An incoming zero version facing positive current version loses unless both
// timestamps exist and incoming UpdatedAt is later. Remaining comparisons use
// strictly earlier nonzero timestamps. These rules let an unversioned later
// database observation replace a versioned one, while incomplete dates can keep
// a versioned identity ahead of an older scheduler record.
func QoderCredentialSnapshotOlder(incoming, current *Record) bool {
	if incoming == nil || current == nil {
		return false
	}
	incomingVersion := incoming.GetCredentialAsInt64("_token_version")
	currentVersion := current.GetCredentialAsInt64("_token_version")
	if incomingVersion > 0 && currentVersion > 0 && incomingVersion != currentVersion {
		return incomingVersion < currentVersion
	}
	if incomingVersion > 0 && currentVersion == 0 {
		return false
	}
	if incomingVersion == 0 && currentVersion > 0 {
		return incoming.UpdatedAt.IsZero() || current.UpdatedAt.IsZero() || !incoming.UpdatedAt.After(current.UpdatedAt)
	}
	return !incoming.UpdatedAt.IsZero() && !current.UpdatedAt.IsZero() && incoming.UpdatedAt.Before(current.UpdatedAt)
}

// prepareQoderSessionBuild selects provider identity and returns its
// generation, effective hash/snapshot, and reusable session or zero. Callers
// pass a nonnil captured record. It initializes maps and serializes identity
// selection with cache inspection under Mu.
//
// A changed hash loses when its snapshot is older; otherwise it advances
// generation and evicts cache before accepting the new identity. A matching
// hash refreshes proxy/TLS snapshot inputs without generation change. Cache
// reuse requires matching hash, nonzero value, and unexpired deadline; rejected
// entries are removed, and returned snapshots/values stay shared with holder
// state.
func (p *QoderSessions[T]) prepareQoderSessionBuild(provider *Record, hash string) (uint64, string, *Record, T) {
	var zero T
	p.Mu.Lock()
	defer p.Mu.Unlock()
	providerID := provider.ID
	if p.Sessions == nil {
		p.Sessions = make(map[int64]QoderSessionCacheEntry[T])
	}
	if p.ProviderStates == nil {
		p.ProviderStates = make(map[int64]QoderSessionProviderState)
	}

	incomingVersion := provider.GetCredentialAsInt64("_token_version")
	state := p.ProviderStates[providerID]
	if state.CredentialsHash == "" {
		state.CredentialsHash = hash
		state.CredentialVersion = incomingVersion
		state.ProviderSnapshot = provider
	} else if state.CredentialsHash != hash {
		current := state.ProviderSnapshot
		if QoderCredentialSnapshotOlder(provider, current) {
			// The accepted snapshot wins this ordering comparison;
			// replace the
			// incoming build inputs so a stale scheduler record
			// joins the
			// current credential flight instead of restoring its
			// older identity.
			hash = state.CredentialsHash
			provider = current
		} else {
			// Advance the flight generation before evicting the
			// cache so an
			// already-running exchange cannot publish against the
			// newly
			// accepted credential identity.
			state.Generation++
			state.CredentialsHash = hash
			state.CredentialVersion = incomingVersion
			state.ProviderSnapshot = provider
			delete(p.Sessions, providerID)
		}
	} else {
		// The hash covers credentials, so matching identity can still
		// supply
		// changed proxy/TLS inputs for a later rebuild without
		// invalidating
		// the reusable session generation here.
		state.ProviderSnapshot = provider
	}
	p.ProviderStates[providerID] = state
	if entry, ok := p.Sessions[providerID]; ok && entry.CredentialsHash == hash && entry.Session != zero && !qoderSessionCacheEntryExpired(entry, time.Now()) {
		return state.Generation, hash, provider, entry.Session
	}
	// Evict the unusable cached value before returning zero so this
	// accepted
	// identity can start a fresh build instead of reusing failed cache
	// validation.
	delete(p.Sessions, providerID)
	return state.Generation, hash, provider, zero
}

// cachedQoderSessionForBuild rechecks the captured generation/hash and reports
// whether that build identity is still current. False tells the callback to
// return invalidation for missing or changed state.
//
// The comparison and cache inspection hold Mu together. A current generation
// with an absent, expired, or invalid cached value removes that entry and
// returns zero with true, permitting a build for the same identity. Returning
// a shared cached session avoids that exchange and can race with a later
// invalidation before delivery. Callbacks building a new value perform their
// separate generation check again at installation.
func (p *QoderSessions[T]) cachedQoderSessionForBuild(providerID int64, hash string, generation uint64) (T, bool) {
	var zero T
	p.Mu.Lock()
	defer p.Mu.Unlock()
	state, ok := p.ProviderStates[providerID]
	if !ok || state.Generation != generation || state.CredentialsHash != hash {
		return zero, false
	}
	entry, ok := p.Sessions[providerID]
	if !ok || entry.CredentialsHash != hash || entry.Session == zero || qoderSessionCacheEntryExpired(entry, time.Now()) {
		// Remove the invalid cached entry while keeping identity
		// current; zero
		// with true lets this callback rebuild for that same
		// generation.
		delete(p.Sessions, providerID)
		return zero, true
	}
	return entry.Session, true
}

// storeQoderSessionBuild installs the supplied session/expiry only while
// shutdown admission and the captured generation/hash still match. False
// reports stopped, missing, or invalidated state. It accepts the supplied
// comparable value without independently rejecting zero or expired timestamps.
//
// The activity mutex is acquired before Mu and held through cache installation,
// preventing Stop from winning between its stopped check and publication.
// Provider identity and generation are then compared under Mu so an older
// callback cannot overwrite accepted invalidation or credential changes.
// GetSession performs its separate builder-result validation for the waiting
// caller.
func (p *QoderSessions[T]) storeQoderSessionBuild(providerID int64, hash string, generation uint64, session T, expiresAt time.Time) bool {
	// Hold shutdown admission while checking identity and publishing cache
	// state; Stop cannot mark the holder stopped between that check and
	// installation.
	p.activity.mu.Lock()
	defer p.activity.mu.Unlock()
	if p.activity.stopped {
		return false
	}
	p.Mu.Lock()
	defer p.Mu.Unlock()
	state, ok := p.ProviderStates[providerID]
	if !ok || state.Generation != generation || state.CredentialsHash != hash {
		return false
	}
	p.Sessions[providerID] = QoderSessionCacheEntry[T]{CredentialsHash: hash, Session: session, ExpiresAt: expiresAt}
	return true
}

// qoderSessionCacheEntryExpired reports whether a nonzero explicit deadline has
// arrived, including equality with now. Zero expiry keeps the entry eligible
// for time-based reuse until identity, generation, or other cache validation
// rejects it.
func qoderSessionCacheEntryExpired[T comparable](entry QoderSessionCacheEntry[T], now time.Time) bool {
	return !entry.ExpiresAt.IsZero() && !now.Before(entry.ExpiresAt)
}

// Invalidate advances a provider's generation and evicts its cached session
// under Mu, leaving any accepted credential snapshot available for rebuilding.
// Nil holders are ignored; future preparation can still reuse the accepted
// identity snapshot.
//
// Generation advancement and eviction share the mutex used by build checks and
// installation. A callback already running for the earlier generation loses
// its subsequent publication comparison, even when its exchange succeeds.
// Completed values that were delivered before invalidation remain with their
// consumers; this method removes the holder's reusable cache entry.
func (p *QoderSessions[T]) Invalidate(providerID int64) {
	if p == nil {
		return
	}
	p.Mu.Lock()
	if p.ProviderStates == nil {
		p.ProviderStates = make(map[int64]QoderSessionProviderState)
	}
	state := p.ProviderStates[providerID]
	// Advance generation and remove the provider cache under the same mutex
	// so
	// existing callbacks fail their later identity/generation check before
	// publication.
	state.Generation++
	p.ProviderStates[providerID] = state
	delete(p.Sessions, providerID)
	p.Mu.Unlock()
}

// InvalidateProvider captures a record with CloneRecord and invalidates only an
// accepted credential observation. Nil inputs are ignored; a snapshot ordered
// older than current state leaves the cache and generation untouched.
//
// Accepted inputs replace credential hash/version/snapshot, advance generation,
// and evict cache together under Mu. Builders already running for the previous
// generation fail their later check. Callers keep the source stable during
// capture; supported mutable containers are copied by CloneRecord, while opaque
// unsupported values can remain shared with its source.
func (p *QoderSessions[T]) InvalidateProvider(provider *Record) {
	if p == nil || provider == nil {
		return
	}
	snapshot := CloneRecord(provider)
	hash := QoderCredentialsHash(snapshot.Credentials)

	p.Mu.Lock()
	if p.Sessions == nil {
		p.Sessions = make(map[int64]QoderSessionCacheEntry[T])
	}
	if p.ProviderStates == nil {
		p.ProviderStates = make(map[int64]QoderSessionProviderState)
	}
	state := p.ProviderStates[snapshot.ID]
	incomingVersion := snapshot.GetCredentialAsInt64("_token_version")
	if current := state.ProviderSnapshot; current != nil {
		if QoderCredentialSnapshotOlder(snapshot, current) {
			// Reject the older observed snapshot before
			// invalidation, keeping
			// the accepted session and generation usable for
			// requests already
			// observing the newer credentials.
			p.Mu.Unlock()
			return
		}
	}
	// Publish the accepted captured identity together with generation
	// advancement and cache removal so earlier callbacks lose their
	// subsequent
	// publication comparison.
	state.CredentialsHash = hash
	state.CredentialVersion = incomingVersion
	state.ProviderSnapshot = snapshot
	state.Generation++
	p.ProviderStates[snapshot.ID] = state
	delete(p.Sessions, snapshot.ID)
	p.Mu.Unlock()
}

// QoderCredentialsHash returns a hexadecimal SHA-256 digest of the credentials'
// JSON bytes. Marshal errors are discarded; the standard marshaler returns nil
// bytes on failure, so all those failures share the empty-byte digest. Callers
// supply serializable credentials when the digest must distinguish identity.
func QoderCredentialsHash(credentials map[string]any) string {
	// Hash the marshaler's returned bytes even on error; unsupported
	// credential
	// values therefore lack a separately reported identity-validation
	// failure
	// here.
	body, _ := json.Marshal(credentials)
	sum := sha256.Sum256(body)
	return fmt.Sprintf("%x", sum[:])
}
