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

const DefaultQoderSessionBuildTimeout = 90 * time.Second

var (
	ErrQoderSessionBuildInvalidated = errors.New("qoder session invalidated during build")
	ErrQoderSessionsStopped         = errors.New("qoder credential sessions are stopped")
)

type QoderSessions[T comparable] struct {
	Mu                  sync.Mutex
	Sessions            map[int64]QoderSessionCacheEntry[T]
	ProviderStates      map[int64]QoderSessionProviderState
	SessionBuildGroup   singleflight.Group
	SessionBuildTimeout time.Duration
	activity            operationActivity
}

type QoderSessionCacheEntry[T comparable] struct {
	CredentialsHash string
	Session         T
	ExpiresAt       time.Time
}

// QoderSessionProviderState tracks the newest observed credentials and the snapshot used for session builds.
type QoderSessionProviderState struct {
	CredentialsHash   string
	CredentialVersion int64
	ProviderSnapshot  *Record
	Generation        uint64
}

// qoderSessionBuildResult holds a session shared by singleflight callers.
type qoderSessionBuildResult[T comparable] struct {
	Session T
}

// StopContext cancels shared builds and waits for active work. Stopping rejects new builds and late cache writes.
func (p *QoderSessions[T]) StopContext(ctx context.Context) error {
	if p == nil {
		return nil
	}
	return p.activity.stop(ctx, "qoder credentials")
}

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

	// Key flights by credentials and generation so invalidation or credential changes bypass older exchanges.
	flightKey := fmt.Sprintf("%d:%s:%d", providerSnapshot.ID, hash, generation)
	resultCh := p.SessionBuildGroup.DoChan(flightKey, func() (any, error) {
		// Shared builds outlive individual waiters; each caller handles its own cancellation below.
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
			// Return invalidation errors so a later request can rebuild with current credentials.
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

// QoderCredentialSnapshotOlder compares token versions first, using update times when versions cannot establish order.
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

// prepareQoderSessionBuild replaces older scheduler credentials with the newest observed snapshot.
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
			// Refresh versions increase monotonically; older scheduler snapshots join or build with the observed
			// credentials.
			hash = state.CredentialsHash
			provider = current
		} else {
			state.Generation++
			state.CredentialsHash = hash
			state.CredentialVersion = incomingVersion
			state.ProviderSnapshot = provider
			delete(p.Sessions, providerID)
		}
	} else {
		// With matching credentials, retain the supplied proxy and TLS settings for later rebuilds.
		state.ProviderSnapshot = provider
	}
	p.ProviderStates[providerID] = state
	if entry, ok := p.Sessions[providerID]; ok && entry.CredentialsHash == hash && entry.Session != zero && !qoderSessionCacheEntryExpired(entry, time.Now()) {
		return state.Generation, hash, provider, entry.Session
	}
	delete(p.Sessions, providerID)
	return state.Generation, hash, provider, zero
}

// cachedQoderSessionForBuild rechecks the generation and cache after entering the singleflight callback.
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
		delete(p.Sessions, providerID)
		return zero, true
	}
	return entry.Session, true
}

// storeQoderSessionBuild accepts the current credential generation, preventing older exchanges from overwriting it.
func (p *QoderSessions[T]) storeQoderSessionBuild(providerID int64, hash string, generation uint64, session T, expiresAt time.Time) bool {
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

// qoderSessionCacheEntryExpired reports whether an explicitly dated session has expired.
func qoderSessionCacheEntryExpired[T comparable](entry QoderSessionCacheEntry[T], now time.Time) bool {
	return !entry.ExpiresAt.IsZero() && !now.Before(entry.ExpiresAt)
}

func (p *QoderSessions[T]) Invalidate(providerID int64) {
	if p == nil {
		return
	}
	p.Mu.Lock()
	if p.ProviderStates == nil {
		p.ProviderStates = make(map[int64]QoderSessionProviderState)
	}
	state := p.ProviderStates[providerID]
	state.Generation++
	p.ProviderStates[providerID] = state
	delete(p.Sessions, providerID)
	p.Mu.Unlock()
}

// InvalidateProvider invalidates the session and records credentials from a database or refresh snapshot.
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
			// Ignore older snapshots so they cannot evict a newer session or advance its generation.
			p.Mu.Unlock()
			return
		}
	}
	state.CredentialsHash = hash
	state.CredentialVersion = incomingVersion
	state.ProviderSnapshot = snapshot
	state.Generation++
	p.ProviderStates[snapshot.ID] = state
	delete(p.Sessions, snapshot.ID)
	p.Mu.Unlock()
}

func QoderCredentialsHash(credentials map[string]any) string {
	body, _ := json.Marshal(credentials)
	sum := sha256.Sum256(body)
	return fmt.Sprintf("%x", sum[:])
}
