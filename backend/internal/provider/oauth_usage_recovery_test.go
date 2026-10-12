package provider

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// usageRecoveryRaceRepo models identity checks that reject recovery after an administrator changes the record.
type usageRecoveryRaceRepo struct {
	OAuthUsageReader
	current *Record
	writes  int
}

// ClearUsageErrorIfUnchanged returns false when the current identity or error differs from the observation.
func (r *usageRecoveryRaceRepo) ClearUsageErrorIfUnchanged(ctx context.Context, v UsageRecoveryVersion) (bool, error) {
	c := r.current
	if c.ID != v.ID || c.Platform != v.Platform || c.Type != v.Type || c.Status != v.Status || c.ErrorMessage != v.ErrorMessage || !reflect.DeepEqual(c.Credentials, v.Credentials) || !reflect.DeepEqual(c.ProxyID, v.ProxyID) {
		return false, nil
	}
	return true, r.ClearError(ctx, v.ID)
}

func (r *usageRecoveryRaceRepo) ClearError(context.Context, int64) error {
	r.writes++
	r.current.Status = StatusActive
	r.current.ErrorMessage = ""
	return nil
}

func TestUsageRecoveryCannotClearNewAdministratorState(t *testing.T) {
	for _, change := range []string{"none", "name", "credentials", "status", "error", "proxy"} {
		t.Run(change, func(t *testing.T) {
			observed := &Record{ID: 721, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: StatusError, ErrorMessage: "token refresh failed", Credentials: map[string]any{"refresh_token": "observed"}}
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
