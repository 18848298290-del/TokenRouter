package apikey

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// rotationRepository stores a replaceable credential and exposes a post-write
// test callback.
type rotationRepository struct {
	APIKeyRepository         // unused methods require setup
	key              *APIKey // retained credential storage record
	err              error   // configured replacement write error
	// onRotate runs after the fixture changes its secret and timestamps.
	// Tests install the callback before replacement to model cancellation after persistence.
	onRotate func(*APIKey) // optional successful replacement callback
	calls    int           // counted replacement write attempts
}

// rotationCache retains entry pointers and records hashed deletion and
// publication attempts.
type rotationCache struct {
	APIKeyCache                                  // unused cache methods require setup
	entries     map[string]*APIKeyAuthCacheEntry // retained authentication entry pointers
	deleted     []string                         // recorded deleted cache identities
	published   []string                         // recorded published cache identities
}

// GetByID returns a shallow copy of the retained Key, or ErrAPIKeyNotFound when
// absent. The fixture ignores ID and context, and nested fields remain shared
// with its stored record.
func (r *rotationRepository) GetByID(context.Context, int64) (*APIKey, error) {
	if r.key == nil {
		return nil, ErrAPIKeyNotFound
	}
	key := *r.key
	return &key, nil
}

// GetByKeyForAuth returns a shallow copy when the credential equals the
// retained secret. Missing or differently named credentials return
// ErrAPIKeyNotFound, and nested fields remain shared for the configured
// fixture.
func (r *rotationRepository) GetByKeyForAuth(_ context.Context, credential string) (*APIKey, error) {
	if r.key == nil || r.key.Key != credential {
		return nil, ErrAPIKeyNotFound
	}
	key := *r.key
	return &key, nil
}

// RotateCredential counts the attempt, checks the old secret, and changes the
// retained credential and write timestamp. A configured error stops mutation; a
// successful replacement invokes onRotate when supplied so tests can cancel the
// caller after persistence.
func (r *rotationRepository) RotateCredential(_ context.Context, key *APIKey, oldKey string) error {
	r.calls++
	if r.err != nil {
		return r.err
	}
	if r.key.Key != oldKey {
		return ErrAPIKeyRotationConflict
	}
	r.key.Key = key.Key
	key.UpdatedAt = time.Now()
	r.key.UpdatedAt = key.UpdatedAt
	if r.onRotate != nil {
		r.onRotate(key)
	}
	return nil
}

// GetAuthCache returns the stored entry pointer for the supplied cache
// identity, or nil with no error when absent. Entries remain shared, so tests
// keep snapshot data unchanged while service authentication reconstructs a view.
func (c *rotationCache) GetAuthCache(_ context.Context, key string) (*APIKeyAuthCacheEntry, error) {
	return c.entries[key], nil
}

// SetAuthCache retains the supplied entry pointer in the fixture map. Callers
// initialize entries before writes; the fixture ignores TTL and context and
// therefore supplies no expiration or cancellation behavior.
func (c *rotationCache) SetAuthCache(_ context.Context, key string, entry *APIKeyAuthCacheEntry, _ time.Duration) error {
	c.entries[key] = entry
	return nil
}

// DeleteAuthCache removes and records key when ctx remains usable at entry. A
// canceled context returns its error before mutation, allowing the rotation
// test to observe the detached cleanup budget.
func (c *rotationCache) DeleteAuthCache(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	delete(c.entries, key)
	c.deleted = append(c.deleted, key)
	return nil
}

// PublishAuthCacheInvalidation records the hashed identity when the context is
// usable at entry. Cancellation returns its error before recording, and the
// fixture sends no real Pub/Sub message.
func (c *rotationCache) PublishAuthCacheInvalidation(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.published = append(c.published, key)
	return nil
}

// TestRotateCredentialPreservesKeyAndInvalidatesCredentials rotates a
// quota-exhausted composite record and cancels its caller immediately after the
// write. It checks retained configuration and usage, both cache invalidations,
// old-secret rejection and new-secret lookup.
func TestRotateCredentialPreservesKeyAndInvalidatesCredentials(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Now()
	key := &APIKey{
		ID: 7, UserID: 3, Key: "sk-old-credential", Name: "原地轮换",
		Status: StatusAPIKeyQuotaExhausted, Quota: 100, QuotaUsed: 100,
		RateLimit5h: 50, Usage5h: 17, Window5hStart: &now, ExpiresAt: &now,
		CreatedAt: now, TeamOwnerDisabled: true, LastUsedAt: &now,
		IsComposite:     true,
		CompositeGroups: []APIKeyCompositeGroup{{GroupID: 9, Prefix: "GPT", NormalizedPrefix: "gpt"}},
		ModelMapping:    map[string]string{"model-a": "model-b"},
		IPWhitelist:     []string{"127.0.0.1"}, BillingMode: APIKeyBillingModeBalance,
		User: &User{ID: 3},
	}
	original := *key
	repo := &rotationRepository{key: key}
	cache := &rotationCache{entries: make(map[string]*APIKeyAuthCacheEntry)}
	options := &Options{APIKeyAuth: APIKeyAuthCacheConfig{L1Size: 64, L1TTLSeconds: 60, L2TTLSeconds: 60, NegativeTTLSeconds: 60}}
	options.Default.APIKeyPrefix = "tr-"
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, cache, options)
	svc.initAuthCaches()
	// Stop after the test so allocated positive and negative local caches
	// are closed after the fixture's admitted authentication operations have finished.
	t.Cleanup(svc.Stop)
	_, err := svc.GetByKey(ctx, original.Key)
	require.NoError(t, err)
	svc.authCacheL1.Load().Wait()
	require.Contains(t, cache.entries, AuthCacheKey(original.Key))
	repo.onRotate = func(updated *APIKey) {
		// Insert and flush a negative entry for the generated credential before
		// canceling the caller, so cleanup must remove both positive and negative state.
		svc.KeySetAuthCacheL1(AuthCacheKey(updated.Key), &APIKeyAuthCacheEntry{NotFound: true})
		svc.authNegativeCacheL1.Load().Wait()
		cancel()
	}

	updated, err := svc.RotateCredential(ctx, key.ID, key.UserID)
	require.NoError(t, err)
	require.NotEqual(t, original.Key, updated.Key)
	require.Regexp(t, `^tr-[0-9a-f]{64}$`, updated.Key)
	expected := original
	expected.Key = updated.Key
	expected.UpdatedAt = updated.UpdatedAt
	require.Equal(t, expected, *updated)
	require.Equal(t, expected, *repo.key)
	expectedInvalidations := []string{AuthCacheKey(original.Key), AuthCacheKey(updated.Key)}
	require.Equal(t, expectedInvalidations, cache.deleted)
	require.Equal(t, expectedInvalidations, cache.published)
	_, err = svc.GetByKey(context.Background(), original.Key)
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
	got, err := svc.GetByKey(context.Background(), updated.Key)
	require.NoError(t, err)
	require.Equal(t, original.ID, got.ID)
}

// TestRotateCredentialRejectsUnauthorizedAndFailedWrites exercises missing,
// foreign, managed, team-disabled and failed replacement cases. It checks the
// returned error, allowed write attempts, retained old credential, and absence
// of cache invalidation on failure.
func TestRotateCredentialRejectsUnauthorizedAndFailedWrites(t *testing.T) {
	managedBy := "creative_studio"
	teamID := int64(10)
	writeErr := errors.New("write failed")
	for _, test := range []struct {
		name      string  // rotation rejection scenario label
		key       *APIKey // scenario credential storage record
		writeErr  error   // scenario replacement write failure
		want      error   // expected returned error identity
		wantCalls int     // expected replacement attempt count
	}{
		{name: "missing", want: ErrAPIKeyNotFound},
		{name: "other_owner", key: &APIKey{UserID: 8}, want: ErrAPIKeyNotFound},
		{name: "managed", key: &APIKey{UserID: 3, ManagedBy: &managedBy}, want: ErrAPIKeyNotFound},
		{name: "team_disabled", key: &APIKey{UserID: 3, TeamID: &teamID, Status: StatusAPIKeyActive}, want: ErrTeamFeatureDisabled},
		{name: "write_failure", key: &APIKey{UserID: 3, Key: "old"}, writeErr: writeErr, want: writeErr, wantCalls: 1},
		{name: "concurrent_rotation", key: &APIKey{UserID: 3, Key: "old"}, writeErr: ErrAPIKeyRotationConflict, want: ErrAPIKeyRotationConflict, wantCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &rotationRepository{key: test.key, err: test.writeErr}
			cache := &rotationCache{}
			svc := NewAPIKeyService(repo, nil, nil, nil, nil, cache, &Options{})
			key, err := svc.RotateCredential(context.Background(), 7, 3)
			require.Nil(t, key)
			require.ErrorIs(t, err, test.want)
			require.Equal(t, test.wantCalls, repo.calls)
			require.Empty(t, cache.deleted)
			require.Empty(t, cache.published)
			if test.writeErr != nil {
				require.Equal(t, "old", repo.key.Key)
			}
		})
	}
}
