package apikey

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// reauthenticationKeys counts direct authentication reads and supplies current
// Key data.
type reauthenticationKeys struct {
	APIKeyRepository         // unused methods require setup
	key              *APIKey // configured current credential view
	err              error   // configured direct lookup error
	reads            int     // counted direct authentication reads
}

// reauthenticationGroups returns active groups for the connection's selected
// target ID.
type reauthenticationGroups struct {
	GroupRepository // unused group methods require setup
}

// staleReauthenticationCache supplies a retained positive entry despite current
// repository rejection.
type staleReauthenticationCache struct {
	APIKeyCache                       // unused cache methods require setup
	entry       *APIKeyAuthCacheEntry // retained positive cache entry
}

// GetByKeyForAuth counts one storage read and returns a copied configured Key
// with its error. CopyAPIKey retains its documented nested references, and the
// fixture ignores the requested credential and context.
func (r *reauthenticationKeys) GetByKeyForAuth(context.Context, string) (*APIKey, error) {
	r.reads++
	return CopyAPIKey(r.key), r.err
}

// GetByIDLite returns a newly allocated active group for the supplied
// identifier.
func (reauthenticationGroups) GetByIDLite(_ context.Context, id int64) (*routing.Group, error) {
	return &routing.Group{ID: id, Status: "active"}, nil
}

// TestReauthenticate compares a previously admitted personal view with active,
// revoked, changed and restricted current data. It checks rejection or fresh
// admission, one repository read, and unchanged prior status and quota fields.
func TestReauthenticate(t *testing.T) {
	for _, name := range []string{"active", "deleted", "lookup_error", "replacement_key", "disabled", "inactive_user", "missing_user", "expired", "quota", "billing_changed", "ip_denied", "team_removed"} {
		t.Run(name, func(t *testing.T) {
			groupID := int64(3)
			previous := &APIKey{ID: 1, UserID: 2, Key: "sk-test", Status: "active", GroupID: &groupID, User: &identity.User{ID: 2, Status: "active"}}
			current := CopyAPIKey(previous)
			repo := &reauthenticationKeys{key: current}
			switch name {
			case "deleted":
				repo.key = nil
				repo.err = ErrAPIKeyNotFound
			case "lookup_error":
				repo.err = errors.New("database unavailable")
			case "replacement_key":
				current.ID = 9
			case "disabled":
				current.Status = "disabled"
			case "inactive_user":
				current.User.Status = "disabled"
			case "missing_user":
				current.User = nil
			case "expired":
				expired := time.Now().Add(-time.Hour)
				current.ExpiresAt = &expired
			case "quota":
				current.Quota = 1
				current.QuotaUsed = 1
			case "billing_changed":
				current.BillingMode = APIKeyBillingModeBalance
			case "ip_denied":
				current.IPWhitelist = []string{"192.0.2.1"}
			case "team_removed":
				id := int64(4)
				previous.TeamID = &id
				current.TeamID = &id
			}
			service := &APIKeyService{apiKeyRepo: repo, groupRepo: reauthenticationGroups{}}
			got, err := service.Reauthenticate(context.Background(), previous, AuthenticationInput{ClientIP: "127.0.0.1", CheckMemberLimits: true})
			if name == "active" {
				require.NoError(t, err)
				require.Equal(t, previous.ID, got.ID)
				require.NotSame(t, previous, got)
			} else {
				require.Error(t, err)
				require.Nil(t, got)
			}
			require.Equal(t, 1, repo.reads)
			require.Equal(t, "active", previous.Status)
			require.Equal(t, "active", previous.User.Status)
			require.Zero(t, previous.QuotaUsed)
		})
	}
}

// GetAuthCache returns the retained entry pointer for every credential lookup.
// It ignores context and key text; tests keep published snapshot data unchanged
// while authentication reconstructs its request view.
func (c staleReauthenticationCache) GetAuthCache(context.Context, string) (*APIKeyAuthCacheEntry, error) {
	return c.entry, nil
}

// TestReauthenticateBypassesPositiveCache supplies a positive cache entry for a
// credential rejected by storage. It checks an initial cache load succeeds
// while later reauthentication performs a direct read and rejects the Key.
func TestReauthenticateBypassesPositiveCache(t *testing.T) {
	ctx := context.Background()
	previous := &APIKey{ID: 1, UserID: 2, Key: "sk-cached", Status: "active", User: &identity.User{ID: 2, Status: "active"}}
	repo := &reauthenticationKeys{err: ErrAPIKeyNotFound}
	service := &APIKeyService{apiKeyRepo: repo, authCfg: KeyNewAPIKeyAuthCacheConfig(&Options{APIKeyAuth: APIKeyAuthCacheConfig{L2TTLSeconds: 60}})}
	service.cache = staleReauthenticationCache{entry: &APIKeyAuthCacheEntry{Snapshot: service.KeySnapshotFromAPIKey(ctx, previous)}}
	cached, err := service.GetByKey(ctx, previous.Key)
	require.NoError(t, err)
	require.Equal(t, previous.ID, cached.ID)
	require.Zero(t, repo.reads)
	_, err = service.Reauthenticate(ctx, previous, AuthenticationInput{})
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
	require.Equal(t, 1, repo.reads)
}
