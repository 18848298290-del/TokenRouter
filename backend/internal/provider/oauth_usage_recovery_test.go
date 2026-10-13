package provider

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// usageRecoveryRaceRepo models serial recovery comparison against
// administrator-modified provider state.
type usageRecoveryRaceRepo struct {
	OAuthUsageReader // unused embedded usage reads
	// current is a borrowed nonnil administrator record. Tests configure it
	// before recovery and serialize comparison plus the accepted mutation.
	current *Record
	writes  int // accepted recovery write count
}

// ClearUsageErrorIfUnchanged compares current identity, credentials, status,
// exact error, and proxy before clearing. A mismatch returns false and
// performs no fixture write.
//
// Name is excluded, so the renamed case remains eligible. An accepted
// comparison delegates to ClearError, which counts and mutates the borrowed
// current record. Tests serialize these operations because comparison and
// clearing have no shared lock and do not simulate an atomic database
// predicate. Keep current nonnil and stable during each call.
func (r *usageRecoveryRaceRepo) ClearUsageErrorIfUnchanged(ctx context.Context, v UsageRecoveryVersion) (bool, error) {
	c := r.current
	if c.ID != v.ID || c.Platform != v.Platform || c.Type != v.Type || c.Status != v.Status || c.ErrorMessage != v.ErrorMessage || !reflect.DeepEqual(c.Credentials, v.Credentials) || !reflect.DeepEqual(c.ProxyID, v.ProxyID) {
		return false, nil
	}
	return true, r.ClearError(ctx, v.ID)
}

// ClearError counts the fixture write and mutates its borrowed current record.
// Inputs are ignored and success is unconditional; tests provide a nonnil
// record and serialize this unlocked mutation.
func (r *usageRecoveryRaceRepo) ClearError(context.Context, int64) error {
	r.writes++
	r.current.Status = StatusActive
	r.current.ErrorMessage = ""
	return nil
}

// TestUsageRecoveryCannotClearNewAdministratorState varies identity, status,
// error, proxy, and name after observation. It checks accepted
// unchanged/renamed recovery and zero writes with retained state for the
// configured comparison conflicts.
func TestUsageRecoveryCannotClearNewAdministratorState(t *testing.T) {
	for _, change := range []string{"none", "name", "credentials", "status", "error", "proxy"} {
		t.Run(change, func(t *testing.T) {
			observed := &Record{ID: 721, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: StatusError, ErrorMessage: "token refresh failed", Credentials: map[string]any{"refresh_token": "observed"}}
			// Copy the record shell to model a later administrator observation.
			// Nested credentials initially remain shared; the credentials case replaces
			// that map so the earlier record retains its captured values.
			current := *observed
			switch change {
			case "name":
				current.Name = "renamed"
			case "credentials":
				current.Credentials = map[string]any{"refresh_token": "administrator"}
			case "status":
				current.Status = StatusDisabled
			case "error":
				current.ErrorMessage = "administrator forbidden"
			case "proxy":
				id := int64(8)
				current.ProxyID = &id
			}
			beforeStatus, beforeError := current.Status, current.ErrorMessage
			repo := &usageRecoveryRaceRepo{current: &current}
			_, err := RecoverUsageProviderError(context.Background(), observed, repo)
			require.NoError(t, err)
			if change == "none" || change == "name" {
				require.Equal(t, 1, repo.writes)
				require.Equal(t, StatusActive, current.Status)
				return
			}
			require.Zero(t, repo.writes)
			require.Equal(t, beforeStatus, current.Status)
			require.Equal(t, beforeError, current.ErrorMessage)
			require.Equal(t, StatusError, observed.Status)
		})
	}
}
