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

type qoderRefreshProviderRepoStub struct {
	qoderRequestProvidersFixture
	updatedCredentials map[string]any
	updateCalls        int
}

type qoderRefreshRaceRepoStub struct {
	qoderRefreshProviderRepoStub
	raceProvider *providercore.Record
	getByIDCalls int
}

type qoderRefreshLockCacheStub struct{}

// qoderRequestProvidersFixture supplies provider records for refresh reads.
type qoderRequestProvidersFixture struct{ providers []providercore.Record }

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

func (r *qoderRefreshRaceRepoStub) GetByID(ctx context.Context, id int64) (*providercore.Record, error) {
	r.getByIDCalls++
	if r.getByIDCalls > 1 && r.raceProvider != nil {
		return r.raceProvider, nil
	}
	return r.qoderRequestProvidersFixture.GetByID(ctx, id)
}

func (qoderRefreshLockCacheStub) GetAccessToken(context.Context, string) (string, error) {
	return "", nil
}

func (qoderRefreshLockCacheStub) SetAccessToken(context.Context, string, string, time.Duration) error {
	return nil
}

func (qoderRefreshLockCacheStub) DeleteAccessToken(context.Context, string) error {
	return nil
}

func (qoderRefreshLockCacheStub) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	return false, nil
}

func (qoderRefreshLockCacheStub) ReleaseRefreshLock(context.Context, string) error {
	return nil
}

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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	refreshed, err := svc.RefreshProviderSession(ctx, &provider)

	require.Nil(t, refreshed)
	require.ErrorIs(t, err, providercore.ErrQoderRefreshInProgress)
	_, cached := tokenSource.Core.Sessions[provider.ID]
	require.True(t, cached)
	require.Equal(t, "old-token", repo.providers[0].GetCredential("security_oauth_token"))
}

// UpdateOAuthCredentialsIfUnchanged models a credential write guarded by the observed identity.
func (r *qoderRefreshProviderRepoStub) UpdateOAuthCredentialsIfUnchanged(ctx context.Context, version providercore.CredentialVersion, credentials map[string]any) (bool, error) {
	current, err := r.GetByID(ctx, version.ID)
	if err != nil {
		return false, err
	}
	if current.Platform != version.Platform || current.Type != version.Type || current.Status != version.Status || !reflect.DeepEqual(querycache.ShallowMap(current.Credentials), version.Credentials) || !reflect.DeepEqual(current.ProxyID, version.ProxyID) {
		return false, nil
	}
	err = r.UpdateCredentials(ctx, version.ID, credentials)
	return err == nil, err
}

func (r qoderRequestProvidersFixture) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	for i := range r.providers {
		if r.providers[i].ID == id {
			return &r.providers[i], nil
		}
	}
	return nil, errors.New("provider not found")
}

// newQoderRequestCoordinator supplies the clock and platform policy for refresh tests.
func newQoderRequestCoordinator(repo providercore.RefreshRepository, cache providercore.AccessTokenCache) *providercore.OAuthRefreshAPI {
	return providercore.NewOAuthRefreshAPI(repo, cache, providercore.RefreshOptions{Now: time.Now, Warn: slog.Warn, Info: slog.Info, Error: slog.Error, Platform: providercore.ProviderRefreshPlatformPolicy()})
}
