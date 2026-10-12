package apikey

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/creative"
)

// creativeFakeManagedKeyRepo retains one provisioned Key and counts fixture
// creation calls.
type creativeFakeManagedKeyRepo struct {
	key     *APIKey // retained provisioned Key pointer
	getErr  error   // configured lookup failure result
	createN int     // counted fixture creation attempts
}

// TestEnsureCreativeManagedKey provisions a hidden execution Key twice for the
// same user and group. It checks manager metadata, automatic funding, generated
// name and binding, and reuse after one creation.
func TestEnsureCreativeManagedKey(t *testing.T) {
	repo := &creativeFakeManagedKeyRepo{}
	manager := ManagedKeys{Store: repo, Prefix: "sk-", ManagedBy: creative.CreativeManagedBy, NamePrefix: "creative-studio"}
	ctx := context.Background()

	key, err := manager.Ensure(ctx, 7, 12)
	require.NoError(t, err)
	require.NotNil(t, key.ManagedBy)
	require.Equal(t, creative.CreativeManagedBy, *key.ManagedBy)
	require.Equal(t, APIKeyBillingModeAuto, key.BillingMode)
	require.Equal(t, int64(12), *key.GroupID)
	require.Equal(t, "creative-studio:12", key.Name)

	reused, err := manager.Ensure(ctx, 7, 12)
	require.NoError(t, err)
	require.Equal(t, key.ID, reused.ID)
	require.Equal(t, 1, repo.createN)
}

// GetManagedKeyByUserAndGroup returns the configured error or retained Key,
// ignoring lookup identifiers. An empty fixture returns ErrAPIKeyNotFound;
// successful reads share the stored pointer with the test caller.
func (r *creativeFakeManagedKeyRepo) GetManagedKeyByUserAndGroup(ctx context.Context, userID, groupID int64, managedBy string) (*APIKey, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.key != nil {
		return r.key, nil
	}
	return nil, ErrAPIKeyNotFound
}

// CreateManagedKey records the supplied pointer and counts the fixture creation
// attempt. A zero ID receives a synthetic ID derived from that count; the
// retained record shares all data with the caller.
func (r *creativeFakeManagedKeyRepo) CreateManagedKey(ctx context.Context, key *APIKey) error {
	r.createN++
	if key.ID == 0 {
		// Allocate a nonzero fixture ID for later reuse assertions.
		key.ID = 900 + int64(r.createN)
	}
	r.key = key
	return nil
}
