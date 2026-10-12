package apikey

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/requestcontext"
)

// requestLimitCacheStub records lease releases and embeds unused cache operations.
type requestLimitCacheStub struct {
	APIKeyCache
	released   int
	reserveErr error
	refreshErr error
	refreshed  int
}

func (s *requestLimitCacheStub) ReserveRequest(context.Context, int64, string, int, int) (time.Duration, error) {
	return time.Second, s.reserveErr
}

func (s *requestLimitCacheStub) RefreshRequest(context.Context, int64, string) error {
	s.refreshed++
	return s.refreshErr
}

func (s *requestLimitCacheStub) ReleaseRequest(ctx context.Context, _ int64, _ string) error {
	if ctx.Err() == nil {
		s.released++
	}
	return ctx.Err()
}

// TestRequestLimitsValidation covers absent limits, clearing limits, and database integer bounds.
func TestRequestLimitsValidation(t *testing.T) {
	for _, n := range []int{0, 1, 2147483647} {
		require.NoError(t, ValidateRequestLimits(&n, &n))
	}
	require.NoError(t, ValidateRequestLimits(nil, nil))
	for _, n := range []int{-1, 2147483648} {
		require.Error(t, KeyValidateCreateAPIKeyRequest(CreateAPIKeyRequest{ConcurrencyLimit: n}))
		require.Error(t, KeyValidateUpdateAPIKeyRequest(UpdateAPIKeyRequest{RPMLimit: &n}))
	}
}

// TestRequestLimitsReleaseAfterCancellation checks that release survives cancellation and runs once.
func TestRequestLimitsReleaseAfterCancellation(t *testing.T) {
	cache := &requestLimitCacheStub{}
	service := &APIKeyService{cache: cache}
	ctx, cancel := context.WithCancel(context.Background())
	admitted, release, _, err := service.AcquireRequest(ctx, &APIKey{ID: 1, ConcurrencyLimit: 1})
	require.NoError(t, err)
	cancel()
	release()
	release()
	require.ErrorIs(t, admitted.Err(), context.Canceled)
	require.Equal(t, 1, cache.released)
}

// TestRequestLimitsUnavailable rejects limited keys without storage while admitting unlimited keys.
func TestRequestLimitsUnavailable(t *testing.T) {
	service := &APIKeyService{}
	_, release, _, err := service.AcquireRequest(context.Background(), &APIKey{ConcurrencyLimit: 1})
	require.ErrorIs(t, err, ErrKeyLimiterUnavailable)
	require.Nil(t, release)
	_, release, _, err = service.AcquireRequest(context.Background(), &APIKey{})
	require.NoError(t, err)
	release()
}

// TestRequestLimitSnapshotRoundTrip retains request limits and rejects snapshots predating them.
func TestRequestLimitSnapshotRoundTrip(t *testing.T) {
	service := &APIKeyService{}
	_, used, err := service.KeyApplyAuthCacheEntry("old", &APIKeyAuthCacheEntry{Snapshot: &APIKeyAuthSnapshot{Version: 46}})
	require.NoError(t, err)
	require.False(t, used)
	snapshot := service.KeySnapshotFromAPIKey(context.Background(), &APIKey{
		ID: 1, UserID: 2, ConcurrencyLimit: 3, RPMLimit: 20, User: &User{ID: 2},
	})
	key, used, err := service.KeyApplyAuthCacheEntry("current", &APIKeyAuthCacheEntry{Snapshot: snapshot})
	require.NoError(t, err)
	require.True(t, used)
	require.Equal(t, 3, key.ConcurrencyLimit)
	require.Equal(t, 20, key.RPMLimit)
}

// TestRequestLeaseRenewalFailure checks cancellation on renewal failure and cleanup after release.
func TestRequestLeaseRenewalFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := &requestLimitCacheStub{refreshErr: ErrKeyLimiterUnavailable}
		service := &APIKeyService{cache: cache}
		ctx, release, _, err := service.AcquireRequest(context.Background(), &APIKey{ID: 1, ConcurrencyLimit: 1})
		require.NoError(t, err)
		detached := requestcontext.Detach(ctx)
		synctest.Wait()
		time.Sleep(31 * time.Second)
		synctest.Wait()
		require.ErrorIs(t, ctx.Err(), context.Canceled)
		require.ErrorIs(t, detached.Err(), context.Canceled)
		late := requestcontext.Detach(ctx)
		require.ErrorIs(t, late.Err(), context.Canceled)
		require.Equal(t, 1, cache.refreshed)
		release()
		require.Equal(t, 1, cache.released)
		service.Stop()
		_, _, _, err = service.AcquireRequest(context.Background(), &APIKey{ID: 1, ConcurrencyLimit: 1})
		require.ErrorIs(t, err, ErrKeyLimiterUnavailable)
	})
}

// TestRequestLeaseSurvivesClientCancellation keeps a concurrency slot while processing continues after disconnect.
func TestRequestLeaseSurvivesClientCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := &requestLimitCacheStub{}
		service := &APIKeyService{cache: cache}
		ctx, cancel := context.WithCancel(context.Background())
		admitted, release, _, err := service.AcquireRequest(ctx, &APIKey{ID: 1, ConcurrencyLimit: 1})
		require.NoError(t, err)
		detached := requestcontext.Detach(admitted)
		cancel()
		synctest.Wait()
		require.NoError(t, detached.Err())
		time.Sleep(31 * time.Second)
		synctest.Wait()
		require.Equal(t, 1, cache.refreshed)
		require.Zero(t, cache.released)
		release()
		time.Sleep(time.Minute)
		synctest.Wait()
		require.Equal(t, 1, cache.refreshed)
		require.Equal(t, 1, cache.released)
	})
}

// TestRequestReservationUnknownOutcome cleans up a slot that may have been reserved before response loss.
func TestRequestReservationUnknownOutcome(t *testing.T) {
	cache := &requestLimitCacheStub{reserveErr: ErrKeyLimiterUnavailable}
	service := &APIKeyService{cache: cache}
	_, release, _, err := service.AcquireRequest(context.Background(), &APIKey{ID: 1, ConcurrencyLimit: 1})
	require.ErrorIs(t, err, ErrKeyLimiterUnavailable)
	require.Nil(t, release)
	require.Equal(t, 1, cache.released)
	service.Stop()
}
