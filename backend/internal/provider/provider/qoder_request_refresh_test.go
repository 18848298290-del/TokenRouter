package provider

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// A failed Qoder request can observe newer authentication during coordinated
// refresh or encounter another worker's refresh lock. These cases exercise
// qoder_request_refresh.go with expired credentials, non-authentication mapping
// drift and a later rotated row, checking which result is returned and whether
// a cached session is invalidated.
//
// The serial repository fixture counts attempted writes, copies nested
// map[string]any values and returns shared row pointers for lookup. Its comparison
// and replacement are separate calls without a database transaction or mutex,
// so tests protect the adapter's logical identity decisions rather than atomic
// storage behavior. A competing-lock cache always refuses ownership; a short
// caller deadline verifies that unchanged credentials stay unreturned and cached
// until a real hash change is observed.

// qoderRefreshProviderRepoStub records serial credential writes over its shared provider fixture rows.
type qoderRefreshProviderRepoStub struct {
	qoderRequestProvidersFixture // shared serial provider-row fixture
	// updatedCredentials stores copied map[string]any containers from the latest
	// attempted write. Slices, pointers and other nested mutable values remain shared.
	updatedCredentials map[string]any
	updateCalls        int // attempted credential-write count
}

// qoderRefreshRaceRepoStub substitutes a rotated provider after the first identity lookup.
type qoderRefreshRaceRepoStub struct {
	qoderRefreshProviderRepoStub // embedded credential-write recorder
	// raceProvider is returned directly after the first GetByID call when nonnil.
	// Its pointer remains shared so the fixture can represent a competing rotation.
	raceProvider *providercore.Record
	getByIDCalls int // serial provider-read count
}

// qoderRefreshLockCacheStub forces contention by refusing every refresh lock acquisition.
type qoderRefreshLockCacheStub struct{}

// qoderRequestProvidersFixture returns pointers to matching provider rows in its borrowed slice.
type qoderRequestProvidersFixture struct {
	providers []providercore.Record /* shared fixture provider rows */
}

// cloneQoderRequestCredentials copies the map and nested map[string]any containers.
// Nil input remains nil. Other values, including slices and pointers, are
// assigned directly and remain shared, so callers cannot assume arbitrary nested
// mutable credential values were isolated.
func cloneQoderRequestCredentials(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		if nested, ok := value.(map[string]any); ok {
			dst[key] = cloneQoderRequestCredentials(nested)
			continue
		}
		dst[key] = value
	}
	return dst
}

// UpdateCredentials counts the attempt and copies supported maps into fixture state.
// It updates the matching row when found and records the latest credential map.
// A missing ID still returns nil after counting the attempted write.
func (r *qoderRefreshProviderRepoStub) UpdateCredentials(_ context.Context, id int64, credentials map[string]any) error {
	r.updateCalls++
	r.updatedCredentials = cloneQoderRequestCredentials(credentials)
	for i := range r.providers {
		if r.providers[i].ID == id {
			r.providers[i].Credentials = cloneQoderRequestCredentials(credentials)
			return nil
		}
	}
	return nil
}

// GetByID returns the initial fixture row before substituting a configured rotation.
// Every call increments its serial counter; after the first, a nonnil raceProvider
// is returned directly. Otherwise lookup delegates to the shared row fixture.
func (r *qoderRefreshRaceRepoStub) GetByID(ctx context.Context, id int64) (*providercore.Record, error) {
	r.getByIDCalls++
	if r.getByIDCalls > 1 && r.raceProvider != nil {
		return r.raceProvider, nil
	}
	return r.qoderRequestProvidersFixture.GetByID(ctx, id)
}

// GetAccessToken reports an empty cache miss with no error for every key.
// The contention tests use this result to force provider rereading rather than
// letting a cached token satisfy refresh coordination.
func (qoderRefreshLockCacheStub) GetAccessToken(context.Context, string) (string, error) {
	return "", nil
}

// SetAccessToken accepts the fixture write without retaining any cached access-token value.
func (qoderRefreshLockCacheStub) SetAccessToken(context.Context, string, string, time.Duration) error {
	return nil
}

// DeleteAccessToken accepts the fixture deletion without storing or changing token state.
func (qoderRefreshLockCacheStub) DeleteAccessToken(context.Context, string) error {
	return nil
}

// AcquireRefreshLock reports that another worker owns the key, with no cache error.
// It creates no local ownership. The request refresh path therefore enters its
// stored-credential wait instead of beginning another site exchange.
func (qoderRefreshLockCacheStub) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	return false, nil
}

// ReleaseRefreshLock accepts fixture cleanup without changing the fixed contention behavior.
func (qoderRefreshLockCacheStub) ReleaseRefreshLock(context.Context, string) error {
	return nil
}

// TestQoderGatewayRefreshProviderSessionPersistsCredentialsAndInvalidatesCache
// refreshes an expired identity with an existing cached session.
// It checks one versioned credential write with rotated tokens and removal of the
// provider's old cached session.
func TestQoderGatewayRefreshProviderSessionPersistsCredentialsAndInvalidatesCache(t *testing.T) {
	now := time.Now()
	expiredAt := now.Add(-1 * time.Hour)
	provider := providercore.Record{
		ID:       91,
		Name:     "qoder",
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Status:   billing.StatusActive,
		Credentials: map[string]any{
			"security_oauth_token": "old-token",
			"refresh_token":        "old-refresh",
			"machine_id":           "machine-1",
			"expires_at":           expiredAt.Format(time.RFC3339),
		},
	}
	repo := &qoderRefreshProviderRepoStub{
		qoderRequestProvidersFixture: qoderRequestProvidersFixture{providers: []providercore.Record{provider}},
	}
	tokenSource := &QoderTokenProvider{
		Core: &providercore.QoderSessions[*qoder.SessionContext]{Sessions: map[int64]providercore.QoderSessionCacheEntry[*qoder.SessionContext]{
			provider.ID: {
				CredentialsHash: "old-hash",
				Session: &qoder.SessionContext{
					Identity: &qoder.AuthIdentity{SecurityOauthToken: "old-token"},
					Machine:  &qoder.MachineIdentity{MachineID: "machine-1"},
				},
			},
		}},
	}
	refresher := NewQoderTokenRefresher(QoderRefreshOptions{
		Exchange: qoder.RefreshExchange{
			RefreshSession: func(_ context.Context, refreshToken, securityOauthToken string, machine *qoder.MachineIdentity) (*qoder.AuthIdentity, error) {
				require.Equal(t, "old-refresh", refreshToken)
				require.Equal(t, "old-token", securityOauthToken)
				require.Equal(t, "machine-1", machine.MachineID)
				return &qoder.AuthIdentity{
					SecurityOauthToken: "new-token",
					RefreshToken:       "new-refresh",
					UID:                "user-1",
					AID:                "user-1",
					UserType:           "personal_standard",
				}, nil
			},
		},
	})

	svc := &QoderRequestRefresh{
		Tokens:       tokenSource,
		Store:        repo,
		NewRefresher: func() *QoderTokenRefresher { return refresher },
		Coordinator:  newQoderRequestCoordinator(repo, nil),
	}

	refreshed, err := svc.RefreshProviderSession(context.Background(), &provider)

	require.NoError(t, err)
	require.NotNil(t, refreshed)
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, "new-token", repo.updatedCredentials["security_oauth_token"])
	require.Equal(t, "new-refresh", repo.updatedCredentials["refresh_token"])
	require.NotNil(t, repo.updatedCredentials["_token_version"])
	_, cached := tokenSource.Core.Sessions[provider.ID]
	require.False(t, cached)
}

// TestQoderGatewayRefreshProviderSessionRequiresInjectedRefreshAPI supplies storage
// and a refresher factory but no coordinator. It checks the configuration error
// and absence of a returned refreshed provider record.
func TestQoderGatewayRefreshProviderSessionRequiresInjectedRefreshAPI(t *testing.T) {
	svc := &QoderRequestRefresh{
		Store: &qoderRefreshProviderRepoStub{},
		NewRefresher: func() *QoderTokenRefresher {
			return NewQoderTokenRefresher(QoderRefreshOptions{})
		},
	}

	refreshed, err := svc.RefreshProviderSession(context.Background(), &providercore.Record{ID: 1})

	require.Nil(t, refreshed)
	require.EqualError(t, err, "qoder refresh API is not configured")
}

// TestQoderGatewayRefreshProviderSessionIgnoresNonAuthCredentialDrift changes model
// mapping while failed authentication remains identical. It checks one refresh
// write rotates tokens and retains the reread mapping in merged credentials.
func TestQoderGatewayRefreshProviderSessionIgnoresNonAuthCredentialDrift(t *testing.T) {
	now := time.Now()
	failedProvider := providercore.Record{
		ID:       91,
		Name:     "qoder",
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Status:   billing.StatusActive,
		Credentials: map[string]any{
			"security_oauth_token": "old-token",
			"refresh_token":        "old-refresh",
			"machine_id":           "machine-1",
			"uid":                  "user-1",
			"expires_at":           now.Add(1 * time.Hour).Format(time.RFC3339),
		},
	}
	freshProvider := failedProvider
	freshProvider.Credentials = cloneQoderRequestCredentials(failedProvider.Credentials)
	freshProvider.Credentials["model_mapping"] = map[string]any{"qwen3.7-plus": "qmodel"}
	repo := &qoderRefreshProviderRepoStub{
		qoderRequestProvidersFixture: qoderRequestProvidersFixture{providers: []providercore.Record{freshProvider}},
	}
	refresher := NewQoderTokenRefresher(QoderRefreshOptions{
		Exchange: qoder.RefreshExchange{
			RefreshSession: func(_ context.Context, refreshToken, _ string, _ *qoder.MachineIdentity) (*qoder.AuthIdentity, error) {
				require.Equal(t, "old-refresh", refreshToken)
				return &qoder.AuthIdentity{
					UID:                "user-1",
					AID:                "user-1",
					SecurityOauthToken: "new-token",
					RefreshToken:       "new-refresh",
				}, nil
			},
		},
	})

	svc := &QoderRequestRefresh{
		Tokens:       NewQoderTokenProvider(qoder.SessionBuilder{}),
		Store:        repo,
		NewRefresher: func() *QoderTokenRefresher { return refresher },
		Coordinator:  newQoderRequestCoordinator(repo, nil),
	}

	refreshed, err := svc.RefreshProviderSession(context.Background(), &failedProvider)

	require.NoError(t, err)
	require.NotNil(t, refreshed)
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, "new-token", repo.updatedCredentials["security_oauth_token"])
	require.Equal(t, "new-refresh", repo.updatedCredentials["refresh_token"])
	require.Equal(t, map[string]any{"qwen3.7-plus": "qmodel"}, repo.updatedCredentials["model_mapping"])
}

// TestQoderGatewayRefreshProviderSessionRecoversRotatedRefreshTokenRace returns a
// rotated row after the first read and an invalid_grant exchange failure.
// It checks recovery returns the newer token with no credential overwrite.
func TestQoderGatewayRefreshProviderSessionRecoversRotatedRefreshTokenRace(t *testing.T) {
	now := time.Now()
	expiredAt := now.Add(-1 * time.Hour)
	provider := providercore.Record{
		ID:       92,
		Name:     "qoder",
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Status:   billing.StatusActive,
		Credentials: map[string]any{
			"security_oauth_token": "old-token",
			"refresh_token":        "old-refresh",
			"machine_id":           "machine-1",
			"expires_at":           expiredAt.Format(time.RFC3339),
		},
	}
	racedProvider := provider
	racedProvider.Credentials = map[string]any{
		"security_oauth_token": "new-token",
		"refresh_token":        "new-refresh",
		"machine_id":           "machine-1",
		"expires_at":           now.Add(1 * time.Hour).Format(time.RFC3339),
	}
	repo := &qoderRefreshRaceRepoStub{
		qoderRefreshProviderRepoStub: qoderRefreshProviderRepoStub{
			qoderRequestProvidersFixture: qoderRequestProvidersFixture{providers: []providercore.Record{provider}},
		},
		raceProvider: &racedProvider,
	}
	refresher := NewQoderTokenRefresher(QoderRefreshOptions{
		Exchange: qoder.RefreshExchange{
			RefreshSession: func(_ context.Context, refreshToken, _ string, _ *qoder.MachineIdentity) (*qoder.AuthIdentity, error) {
				require.Equal(t, "old-refresh", refreshToken)
				return nil, errors.New("invalid_grant: refresh token has already been used")
			},
		},
	})

	svc := &QoderRequestRefresh{
		Tokens:       NewQoderTokenProvider(qoder.SessionBuilder{}),
		Store:        repo,
		NewRefresher: func() *QoderTokenRefresher { return refresher },
		Coordinator:  newQoderRequestCoordinator(repo, nil),
	}

	refreshed, err := svc.RefreshProviderSession(context.Background(), &provider)

	require.NoError(t, err)
	require.NotNil(t, refreshed)
	require.Equal(t, "new-refresh", refreshed.GetCredential("refresh_token"))
	require.Equal(t, 0, repo.updateCalls)
	require.GreaterOrEqual(t, repo.getByIDCalls, 2)
}

// TestQoderGatewayRefreshProviderSessionWaitsForLockHolderRotation forces a held
// refresh lock then supplies changed authentication on reread. It checks the
// rotated tokens are returned and the old session cache is invalidated.
func TestQoderGatewayRefreshProviderSessionWaitsForLockHolderRotation(t *testing.T) {
	now := time.Now()
	provider := providercore.Record{
		ID:       94,
		Name:     "qoder",
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"security_oauth_token": "old-token",
			"refresh_token":        "old-refresh",
			"machine_id":           "machine-1",
			"uid":                  "user-1",
			"expires_at":           now.Add(1 * time.Hour).Format(time.RFC3339),
		},
	}
	rotatedProvider := provider
	rotatedProvider.Credentials = map[string]any{
		"security_oauth_token": "new-token",
		"refresh_token":        "new-refresh",
		"machine_id":           "machine-1",
		"uid":                  "user-1",
		"expires_at":           now.Add(2 * time.Hour).Format(time.RFC3339),
	}
	repo := &qoderRefreshRaceRepoStub{
		qoderRefreshProviderRepoStub: qoderRefreshProviderRepoStub{
			qoderRequestProvidersFixture: qoderRequestProvidersFixture{providers: []providercore.Record{provider}},
		},
		raceProvider: &rotatedProvider,
	}
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	tokenSource.Core.Sessions[provider.ID] = providercore.QoderSessionCacheEntry[*qoder.SessionContext]{
		CredentialsHash: providercore.QoderCredentialsHash(provider.Credentials),
		Session:         &qoder.SessionContext{},
	}
	svc := &QoderRequestRefresh{
		Tokens: tokenSource,
		Store:  repo,
		NewRefresher: func() *QoderTokenRefresher {
			return NewQoderTokenRefresher(QoderRefreshOptions{})
		},
		Coordinator: newQoderRequestCoordinator(repo, qoderRefreshLockCacheStub{}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	refreshed, err := svc.RefreshProviderSession(ctx, &provider)

	require.NoError(t, err)
	require.NotNil(t, refreshed)
	require.Equal(t, "new-token", refreshed.GetCredential("security_oauth_token"))
	require.Equal(t, "new-refresh", refreshed.GetCredential("refresh_token"))
	require.GreaterOrEqual(t, repo.getByIDCalls, 2)
	_, cached := tokenSource.Core.Sessions[provider.ID]
	require.False(t, cached)
}

// TestQoderGatewayRefreshProviderSessionLockHeldReturnsRefreshInProgressWithoutStaleProvider
// forces lock contention with unchanged stored credentials and a short deadline.
// It checks ErrQoderRefreshInProgress, no returned provider, and retention of both
// the old cached session and stored token.
func TestQoderGatewayRefreshProviderSessionLockHeldReturnsRefreshInProgressWithoutStaleProvider(t *testing.T) {
	now := time.Now()
	provider := providercore.Record{
		ID:       95,
		Name:     "qoder",
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"security_oauth_token": "old-token",
			"refresh_token":        "old-refresh",
			"machine_id":           "machine-1",
			"uid":                  "user-1",
			"expires_at":           now.Add(1 * time.Hour).Format(time.RFC3339),
		},
	}
	repo := &qoderRefreshProviderRepoStub{
		qoderRequestProvidersFixture: qoderRequestProvidersFixture{providers: []providercore.Record{provider}},
	}
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	tokenSource.Core.Sessions[provider.ID] = providercore.QoderSessionCacheEntry[*qoder.SessionContext]{
		CredentialsHash: providercore.QoderCredentialsHash(provider.Credentials),
		Session:         &qoder.SessionContext{},
	}
	svc := &QoderRequestRefresh{
		Tokens: tokenSource,
		Store:  repo,
		NewRefresher: func() *QoderTokenRefresher {
			return NewQoderTokenRefresher(QoderRefreshOptions{})
		},
		Coordinator: newQoderRequestCoordinator(repo, qoderRefreshLockCacheStub{}),
	}
	// End the caller budget before the next polling interval so an unchanged
	// lock-holder identity produces in-progress rather than being returned as refreshed.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	refreshed, err := svc.RefreshProviderSession(ctx, &provider)

	require.Nil(t, refreshed)
	require.ErrorIs(t, err, providercore.ErrQoderRefreshInProgress)
	_, cached := tokenSource.Core.Sessions[provider.ID]
	require.True(t, cached)
	require.Equal(t, "old-token", repo.providers[0].GetCredential("security_oauth_token"))
}

// UpdateOAuthCredentialsIfUnchanged compares fixture identity before a replacement write.
// It checks platform, kind, status, normalized credential-map shape and proxy.
// A mismatch returns false; a match calls UpdateCredentials and reports its
// result. These serial calls provide no atomic database or concurrent-write guarantee.
func (r *qoderRefreshProviderRepoStub) UpdateOAuthCredentialsIfUnchanged(ctx context.Context, version providercore.CredentialVersion, credentials map[string]any) (bool, error) {
	current, err := r.GetByID(ctx, version.ID)
	if err != nil {
		return false, err
	}
	// Check the observed authentication before invoking the separate fixture write;
	// comparison failure leaves its counter and rows untouched, but these calls are
	// not protected against an interleaving writer by this serial test substitute.
	if current.Platform != version.Platform || current.Type != version.Type || current.Status != version.Status || !reflect.DeepEqual(querycache.ShallowMap(current.Credentials), version.Credentials) || !reflect.DeepEqual(current.ProxyID, version.ProxyID) {
		return false, nil
	}
	err = r.UpdateCredentials(ctx, version.ID, credentials)
	return err == nil, err
}

// GetByID returns a pointer to the matching row in the fixture slice.
// Missing IDs return a provider-not-found error. Returned rows and their mutable
// credential contents remain shared with the fixture instead of being cloned.
func (r qoderRequestProvidersFixture) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	for i := range r.providers {
		if r.providers[i].ID == id {
			return &r.providers[i], nil
		}
	}
	return nil, errors.New("provider not found")
}

// newQoderRequestCoordinator binds fixture storage, cache and production platform policy.
// It supplies the real clock and application logging callbacks. The returned
// coordinator owns refresh locks and rereads while the repository fixture owns
// its simplified comparison and replacement behavior.
func newQoderRequestCoordinator(repo providercore.RefreshRepository, cache providercore.AccessTokenCache) *providercore.OAuthRefreshAPI {
	return providercore.NewOAuthRefreshAPI(repo, cache, providercore.RefreshOptions{Now: time.Now, Warn: slog.Warn, Info: slog.Info, Error: slog.Error, Platform: providercore.ProviderRefreshPlatformPolicy()})
}
