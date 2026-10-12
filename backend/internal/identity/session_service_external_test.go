package identity_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
)

func TestAuthService_ValidateToken_ExpiredReturnsClaimsWithError(t *testing.T) {
	repo := &userRepoStub{}
	service := newAuthService(repo, nil, nil)

	// Create a user and issue an access token.
	user := &identity.User{
		ID:           1,
		Email:        "test@test.com",
		Role:         identity.RoleUser,
		Status:       billing.StatusActive,
		TokenVersion: 1,
	}
	token, err := service.GenerateToken(context.Background(), user)
	require.NoError(t, err)

	// Validate the unexpired token.
	claims, err := service.ValidateToken(token)
	require.NoError(t, err)
	require.NotNil(t, claims)
	require.Equal(t, int64(1), claims.UserID)

	// Issue an already-expired token to exercise the expiration path.
	service.Options.JWT.ExpireHour = -1 // A negative lifetime places expiration in the past.
	rebuildSessionForTest(service)
	expiredToken, err := service.GenerateToken(context.Background(), user)
	require.NoError(t, err)
	service.Options.JWT.ExpireHour = 1 // Restore the normal token lifetime.
	rebuildSessionForTest(service)

	// An expired token returns its claims alongside ErrTokenExpired.
	claims, err = service.ValidateToken(expiredToken)
	require.ErrorIs(t, err, identity.ErrTokenExpired)
	require.NotNil(t, claims, "claims should not be nil when token is expired")
	require.Equal(t, int64(1), claims.UserID)
	require.Equal(t, "test@test.com", claims.Email)
}

func TestAuthService_RefreshToken_ExpiredTokenNoPanic(t *testing.T) {
	user := &identity.User{
		ID:           1,
		Email:        "test@test.com",
		Role:         identity.RoleUser,
		Status:       billing.StatusActive,
		TokenVersion: 1,
	}
	repo := &userRepoStub{user: user}
	service := newAuthService(repo, nil, nil)

	// Issue an expired access token.
	service.Options.JWT.ExpireHour = -1
	rebuildSessionForTest(service)
	expiredToken, err := service.GenerateToken(context.Background(), user)
	require.NoError(t, err)
	service.Options.JWT.ExpireHour = 1
	rebuildSessionForTest(service)

	// Renewal must handle expired-token claims without panicking.
	require.NotPanics(t, func() {
		newToken, err := service.RefreshToken(context.Background(), expiredToken)
		require.NoError(t, err)
		require.NotEmpty(t, newToken)
	})
}

func TestAuthService_GetAccessTokenExpiresIn_FallbackToExpireHour(t *testing.T) {
	service := newAuthService(&userRepoStub{}, nil, nil)
	service.Options.JWT.ExpireHour = 24
	rebuildSessionForTest(service)
	service.Options.JWT.AccessTokenExpireMinutes = 0
	rebuildSessionForTest(service)

	require.Equal(t, 24*3600, service.GetAccessTokenExpiresIn())
}

func TestAuthService_GetAccessTokenExpiresIn_MinutesHasPriority(t *testing.T) {
	service := newAuthService(&userRepoStub{}, nil, nil)
	service.Options.JWT.ExpireHour = 24
	rebuildSessionForTest(service)
	service.Options.JWT.AccessTokenExpireMinutes = 90
	rebuildSessionForTest(service)

	require.Equal(t, 90*60, service.GetAccessTokenExpiresIn())
}

func TestAuthService_GenerateToken_UsesExpireHourWhenMinutesZero(t *testing.T) {
	service := newAuthService(&userRepoStub{}, nil, nil)
	service.Options.JWT.ExpireHour = 24
	rebuildSessionForTest(service)
	service.Options.JWT.AccessTokenExpireMinutes = 0
	rebuildSessionForTest(service)

	user := &identity.User{
		ID:           1,
		Email:        "test@test.com",
		Role:         identity.RoleUser,
		Status:       billing.StatusActive,
		TokenVersion: 1,
	}

	token, err := service.GenerateToken(context.Background(), user)
	require.NoError(t, err)

	claims, err := service.ValidateToken(token)
	require.NoError(t, err)
	require.NotNil(t, claims)
	require.NotNil(t, claims.IssuedAt)
	require.NotNil(t, claims.ExpiresAt)

	require.WithinDuration(t, claims.IssuedAt.Add(24*time.Hour), claims.ExpiresAt.Time, 2*time.Second)
}

func TestAuthService_GenerateToken_UsesMinutesWhenConfigured(t *testing.T) {
	service := newAuthService(&userRepoStub{}, nil, nil)
	service.Options.JWT.ExpireHour = 24
	rebuildSessionForTest(service)
	service.Options.JWT.AccessTokenExpireMinutes = 90
	rebuildSessionForTest(service)

	user := &identity.User{
		ID:           2,
		Email:        "test2@test.com",
		Role:         identity.RoleUser,
		Status:       billing.StatusActive,
		TokenVersion: 1,
	}

	token, err := service.GenerateToken(context.Background(), user)
	require.NoError(t, err)

	claims, err := service.ValidateToken(token)
	require.NoError(t, err)
	require.NotNil(t, claims)
	require.NotNil(t, claims.IssuedAt)
	require.NotNil(t, claims.ExpiresAt)

	require.WithinDuration(t, claims.IssuedAt.Add(90*time.Minute), claims.ExpiresAt.Time, 2*time.Second)
}
