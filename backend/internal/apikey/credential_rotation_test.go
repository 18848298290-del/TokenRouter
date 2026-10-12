package apikey

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// rotationRepository supports credential replacement; other calls fail to expose accidental configuration or usage writes.
type rotationRepository struct {
	APIKeyRepository
	key      *APIKey
	err      error
	onRotate func(*APIKey)
	calls    int
}

type rotationCache struct {
	APIKeyCache
	entries   map[string]*APIKeyAuthCacheEntry
	deleted   []string
	published []string
}

func (r *rotationRepository) GetByID(context.Context, int64) (*APIKey, error) {
	if r.key == nil {
		return nil, ErrAPIKeyNotFound
	}
	key := *r.key
	return &key, nil
}

func (r *rotationRepository) GetByKeyForAuth(_ context.Context, credential string) (*APIKey, error) {
	if r.key == nil || r.key.Key != credential {
		return nil, ErrAPIKeyNotFound
	}
	key := *r.key
	return &key, nil
}

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

func (c *rotationCache) GetAuthCache(_ context.Context, key string) (*APIKeyAuthCacheEntry, error) {
	return c.entries[key], nil
}

func (c *rotationCache) SetAuthCache(_ context.Context, key string, entry *APIKeyAuthCacheEntry, _ time.Duration) error {
	c.entries[key] = entry
	return nil
}

func (c *rotationCache) DeleteAuthCache(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	delete(c.entries, key)
	c.deleted = append(c.deleted, key)
	return nil
}

func (c *rotationCache) PublishAuthCacheInvalidation(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.published = append(c.published, key)
	return nil
}

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
	t.Cleanup(svc.Stop)
	_, err := svc.GetByKey(ctx, original.Key)
	require.NoError(t, err)
	svc.authCacheL1.Load().Wait()
	require.Contains(t, cache.entries, AuthCacheKey(original.Key))
	repo.onRotate = func(updated *APIKey) {
		// Cancellation after commit must not skip cleanup; the new credential may also have a negative cache entry.
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

func TestRotateCredentialRejectsUnauthorizedAndFailedWrites(t *testing.T) {
	managedBy := "creative_studio"
	teamID := int64(10)
	writeErr := errors.New("write failed")
	for _, test := range []struct {
		name      string
		key       *APIKey
		writeErr  error
		want      error
		wantCalls int
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
