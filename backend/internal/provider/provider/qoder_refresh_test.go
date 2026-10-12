package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

type qoderRefreshHTTPUpstreamStub struct {
	proxyURL            string
	providerID          int64
	providerConcurrency int
}

func TestQoderTokenRefresherNeedsRefreshWhenExpiresAtWithinWindow(t *testing.T) {
	refresher := NewQoderTokenRefresher(QoderRefreshOptions{})
	expiresAt := time.Now().Add(time.Minute).Format(time.RFC3339)
	provider := &providercore.Record{
		ID:       1,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"refresh_token": "refresh-1",
			"expires_at":    expiresAt,
		},
	}

	require.True(t, refresher.CanRefresh(provider))
	require.True(t, refresher.NeedsRefresh(provider, time.Hour))
}

func TestQoderTokenRefresherDoesNotRefreshQuotaRateLimitedWithoutExpiresAt(t *testing.T) {
	refresher := NewQoderTokenRefresher(QoderRefreshOptions{})
	resetAt := time.Now().Add(time.Minute)
	provider := &providercore.Record{
		ID:               1,
		Platform:         capability.PlatformQoder,
		Type:             capability.ProviderTypeCosy,
		RateLimitResetAt: &resetAt,
		Credentials: map[string]any{
			"refresh_token": "refresh-1",
		},
	}

	require.False(t, refresher.NeedsRefresh(provider, time.Hour))
}

func TestQoderTokenRefresherDoesNotRefreshWithoutRefreshToken(t *testing.T) {
	refresher := NewQoderTokenRefresher(QoderRefreshOptions{})
	resetAt := time.Now().Add(time.Minute)
	provider := &providercore.Record{
		ID:               1,
		Platform:         capability.PlatformQoder,
		Type:             capability.ProviderTypeCosy,
		RateLimitResetAt: &resetAt,
	}

	require.False(t, refresher.NeedsRefresh(provider, time.Hour))
}

func TestQoderTokenRefresherRefreshMergesCredentials(t *testing.T) {
	refresher := NewQoderTokenRefresher(QoderRefreshOptions{})
	refresher.options.Exchange.RefreshSession = func(_ context.Context, refreshToken, securityOauthToken string, machine *qoder.MachineIdentity) (*qoder.AuthIdentity, error) {
		require.Equal(t, "old-refresh", refreshToken)
		require.Equal(t, "old-token", securityOauthToken)
		require.Equal(t, "machine-1", machine.MachineID)
		return &qoder.AuthIdentity{
			Name:               "Refreshed User",
			UID:                "user-1",
			AID:                "user-1",
			OrganizationID:     "org-1",
			OrganizationName:   "Org 1",
			UserType:           "personal_pro",
			SecurityOauthToken: "new-token",
			RefreshToken:       "new-refresh",
		}, nil
	}
	provider := &providercore.Record{
		ID:       1,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"security_oauth_token": "old-token",
			"refresh_token":        "old-refresh",
			"machine_id":           "machine-1",
			"custom":               "keep",
		},
	}

	credentials, err := refresher.Refresh(context.Background(), provider)

	require.NoError(t, err)
	require.Equal(t, "new-token", credentials["security_oauth_token"])
	require.Equal(t, "new-refresh", credentials["refresh_token"])
	require.Equal(t, "machine-1", credentials["machine_id"])
	require.Equal(t, "org-1", credentials["organization_id"])
	require.Equal(t, "keep", credentials["custom"])
}

func TestQoderTokenRefresherRefreshPreservesOldRefreshTokenWhenMissing(t *testing.T) {
	refresher := NewQoderTokenRefresher(QoderRefreshOptions{})
	refresher.options.Exchange.RefreshSession = func(context.Context, string, string, *qoder.MachineIdentity) (*qoder.AuthIdentity, error) {
		return &qoder.AuthIdentity{
			UID:                "user-1",
			AID:                "user-1",
			SecurityOauthToken: "new-token",
		}, nil
	}
	provider := &providercore.Record{
		ID:       1,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"security_oauth_token": "old-token",
			"refresh_token":        "old-refresh",
			"machine_id":           "machine-1",
		},
	}

	credentials, err := refresher.Refresh(context.Background(), provider)

	require.NoError(t, err)
	require.Equal(t, "new-token", credentials["security_oauth_token"])
	require.Equal(t, "old-refresh", credentials["refresh_token"])
}

func TestQoderTokenRefresherRefreshDropsStaleExpiresAt(t *testing.T) {
	refresher := NewQoderTokenRefresher(QoderRefreshOptions{})
	refresher.options.Exchange.RefreshSession = func(context.Context, string, string, *qoder.MachineIdentity) (*qoder.AuthIdentity, error) {
		return &qoder.AuthIdentity{
			UID:                "user-1",
			AID:                "user-1",
			SecurityOauthToken: "new-token",
			RefreshToken:       "new-refresh",
		}, nil
	}
	provider := &providercore.Record{
		ID:       1,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"security_oauth_token": "old-token",
			"refresh_token":        "old-refresh",
			"machine_id":           "machine-1",
			"expires_at":           time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
	}

	credentials, err := refresher.Refresh(context.Background(), provider)

	require.NoError(t, err)
	require.Equal(t, "new-token", credentials["security_oauth_token"])
	require.Equal(t, "new-refresh", credentials["refresh_token"])
	require.NotContains(t, credentials, "expires_at")
}

func TestQoderTokenRefresherRefreshRejectsEmptySecurityOauthToken(t *testing.T) {
	refresher := NewQoderTokenRefresher(QoderRefreshOptions{})
	refresher.options.Exchange.RefreshSession = func(context.Context, string, string, *qoder.MachineIdentity) (*qoder.AuthIdentity, error) {
		return &qoder.AuthIdentity{
			UID:          "user-1",
			AID:          "user-1",
			RefreshToken: "new-refresh",
		}, nil
	}
	provider := &providercore.Record{
		ID:       1,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"security_oauth_token": "old-token",
			"refresh_token":        "old-refresh",
			"machine_id":           "machine-1",
		},
	}

	credentials, err := refresher.Refresh(context.Background(), provider)

	require.Nil(t, credentials)
	require.ErrorContains(t, err, "empty security_oauth_token")
}

func TestQoderTokenRefresherRefreshRequiresMachineID(t *testing.T) {
	refresher := NewQoderTokenRefresher(QoderRefreshOptions{})
	provider := &providercore.Record{
		ID:       1,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"refresh_token": "old-refresh",
		},
	}

	_, err := refresher.Refresh(context.Background(), provider)

	require.ErrorContains(t, err, "machine_id")
}

func TestQoderTokenRefresherRoutesCN20RefreshAndPersistsExpiry(t *testing.T) {
	refresher := NewQoderTokenRefresher(QoderRefreshOptions{})
	expiresAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	refresher.options.Exchange.RefreshCN20 = func(_ context.Context, refreshToken string, machine *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		require.Equal(t, "cn-refresh", refreshToken)
		require.Equal(t, "machine-1", machine.MachineID)
		require.Empty(t, machine.MachineToken)
		require.Empty(t, machine.MachineType)
		return &qoder.AuthIdentity{
			UID:                "uid-1",
			AID:                "aid-1",
			SecurityOauthToken: "new-cosy-token",
			RefreshToken:       "rotated-cn-refresh",
			UserType:           "personal_standard",
		}, expiresAt, nil
	}
	provider := &providercore.Record{
		ID:       20,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"site":                 "cn",
			"refresh_mode":         qoder.RefreshModeQoderCN20,
			"security_oauth_token": "old-cosy-token",
			"refresh_token":        "cn-refresh",
			"machine_id":           "machine-1",
			"machine_token":        "legacy-machine-token",
			"machine_type":         "legacy-machine-type",
			"uid":                  "uid-1",
		},
	}

	credentials, err := refresher.Refresh(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "new-cosy-token", credentials["security_oauth_token"])
	require.Equal(t, "rotated-cn-refresh", credentials["refresh_token"])
	require.Equal(t, "cn", credentials["site"])
	require.Equal(t, qoder.RefreshModeQoderCN20, credentials["refresh_mode"])
	require.Equal(t, expiresAt.Format(time.RFC3339), credentials["expires_at"])
	require.NotContains(t, credentials, "machine_token")
	require.NotContains(t, credentials, "machine_type")
}

func TestQoderTokenRefresherRoutesCNManualCosyRefresh(t *testing.T) {
	refresher := NewQoderTokenRefresher(QoderRefreshOptions{})
	refresher.options.Exchange.RefreshCNCosy = func(_ context.Context, refreshToken, securityToken, userID, organizationID string, _ *qoder.MachineIdentity) (*qoder.AuthIdentity, error) {
		require.Equal(t, "cosy-refresh", refreshToken)
		require.Equal(t, "old-token", securityToken)
		require.Equal(t, "uid-1", userID)
		require.Equal(t, "org-1", organizationID)
		return &qoder.AuthIdentity{
			UID:                "uid-1",
			AID:                "uid-1",
			OrganizationID:     "org-1",
			SecurityOauthToken: "new-token",
			RefreshToken:       "new-cosy-refresh",
		}, nil
	}
	provider := &providercore.Record{
		ID:       21,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"site":                 "cn",
			"refresh_mode":         qoder.RefreshModeCosy,
			"security_oauth_token": "old-token",
			"refresh_token":        "cosy-refresh",
			"machine_id":           "machine-1",
			"uid":                  "uid-1",
			"organization_id":      "org-1",
		},
	}

	credentials, err := refresher.Refresh(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "new-token", credentials["security_oauth_token"])
	require.Equal(t, "new-cosy-refresh", credentials["refresh_token"])
	require.Equal(t, qoder.RefreshModeCosy, credentials["refresh_mode"])
	require.NotContains(t, credentials, "expires_at")
}

func TestQoderTokenRefresherRefreshWrapsError(t *testing.T) {
	refresher := NewQoderTokenRefresher(QoderRefreshOptions{})
	refresher.options.Exchange.RefreshSession = func(context.Context, string, string, *qoder.MachineIdentity) (*qoder.AuthIdentity, error) {
		return nil, errors.New("invalid_grant")
	}
	provider := &providercore.Record{
		ID:       1,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"refresh_token": "old-refresh",
			"machine_id":    "machine-1",
		},
	}

	_, err := refresher.Refresh(context.Background(), provider)

	require.ErrorContains(t, err, "invalid_grant")
}

func TestQoderTokenRefresherUsesProviderDoer(t *testing.T) {
	upstream := &qoderRefreshHTTPUpstreamStub{}
	profiles := &egressprovider.TLSProfiles{}
	refresher := NewQoderTokenRefresher(QoderRefreshOptions{Transport: upstream, Profiles: profiles})
	require.Same(t, upstream, refresher.options.Transport)
	require.Same(t, profiles, refresher.options.Profiles)
	proxyID := int64(9)
	provider := &providercore.Record{
		ID:          108,
		Platform:    capability.PlatformQoder,
		Type:        capability.ProviderTypeCosy,
		Concurrency: 4,
		ProxyID:     &proxyID,
		Proxy:       &egress.Proxy{Protocol: "http", Host: "proxy.example.com", Port: 8080},
		Credentials: map[string]any{
			"security_oauth_token": "old-token",
			"refresh_token":        "old-refresh",
			"machine_id":           "machine-1",
		},
	}

	credentials, err := refresher.Refresh(context.Background(), provider)

	require.NoError(t, err)
	require.Equal(t, "new-token", credentials["security_oauth_token"])
	require.Equal(t, "http://proxy.example.com:8080", upstream.proxyURL)
	require.Equal(t, int64(108), upstream.providerID)
	require.Equal(t, 4, upstream.providerConcurrency)
}

func (s *qoderRefreshHTTPUpstreamStub) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	return s.DoWithTLS(req, proxyURL, providerID, providerConcurrency, nil)
}

func (s *qoderRefreshHTTPUpstreamStub) DoWithTLS(req *http.Request, proxyURL string, providerID int64, providerConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	s.proxyURL = proxyURL
	s.providerID = providerID
	s.providerConcurrency = providerConcurrency
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(`{
			"id":"user-1",
			"name":"User",
			"userType":"personal_standard",
			"securityOauthToken":"new-token",
			"refreshToken":"new-refresh"
		}`)),
		Request: req,
	}, nil
}
