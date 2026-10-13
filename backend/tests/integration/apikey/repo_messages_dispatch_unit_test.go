package apikey_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// Authentication queries select a narrower group record than ordinary key
// reads. SQLite cases set image generation and session isolation separately,
// then reload keys through GetByKeyForAuth to check that each selected control
// reaches the returned group.

// TestAPIKeyRepository_GetByKeyForAuth_PreservesImageGenerationControls_SQLite
// persists a group with image generation enabled and assigns it to a key. The
// authentication read includes a group whose image flag is true.
func TestAPIKeyRepository_GetByKeyForAuth_PreservesImageGenerationControls_SQLite(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "getbykey-auth-images-unit@test.com")

	group, err := client.Group.Create().
		SetName("g-auth-images-unit").
		SetStatus(billing.StatusActive).
		SetRateMultiplier(1).
		SetAllowImageGeneration(true).
		Save(ctx)
	require.NoError(t, err)

	key := &apikey.APIKey{
		UserID:  user.ID,
		Key:     "sk-getbykey-auth-images-unit",
		Name:    "Images Key Unit",
		GroupID: &group.ID,
		Status:  billing.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, key))

	got, err := repo.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.NotNil(t, got.Group)
	require.True(t, got.Group.AllowImageGeneration)
}

// TestAPIKeyRepository_GetByKeyForAuth_PreservesSessionIsolation_SQLite
// persists a group with session isolation enabled and assigns it to a key. The
// authentication read includes a group whose isolation flag is true.
func TestAPIKeyRepository_GetByKeyForAuth_PreservesSessionIsolation_SQLite(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "getbykey-auth-session-isolation-unit@test.com")

	group, err := client.Group.Create().
		SetName("g-auth-session-isolation-unit").
		SetStatus(billing.StatusActive).
		SetRateMultiplier(1).
		SetSessionIsolationEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	key := &apikey.APIKey{
		UserID:  user.ID,
		Key:     "sk-getbykey-auth-session-isolation-unit",
		Name:    "Session Isolation Key Unit",
		GroupID: &group.ID,
		Status:  billing.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, key))

	got, err := repo.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.NotNil(t, got.Group)
	require.True(t, got.Group.SessionIsolationEnabled)
}
