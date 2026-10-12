package billing_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// userGroupRateRepoStubForGroupRate captures administrator group override
// operations and supplies configured results.
type userGroupRateRepoStubForGroupRate struct {
	// getByGroupIDData and getByGroupIDErr supply read outcomes, with the error
	// taking precedence. Returned slices retain the fixture's storage for
	// response assertions.
	getByGroupIDData map[int64][]billing.UserGroupRateEntry // group read response slices
	getByGroupIDErr  error                                  // injected group read error

	// deletedGroupIDs records each attempted group deletion before
	// deleteByGroupErr is returned. Failed operations therefore still identify
	// which group the administrator requested.
	deletedGroupIDs  []int64 // captured group deletion sequence
	deleteByGroupErr error   // injected group deletion error

	// syncedGroupID, syncedEntries and syncGroupErr describe a rate replacement
	// attempt. The stub records the supplied group and entries before returning
	// its configured error, so failed attempts remain observable to assertions.
	syncedGroupID int64 // captured rate replacement group
	// syncedEntries retains the caller's slice after rate synchronization. Later
	// caller mutations are visible in this capture, so assertions run before
	// changing it.
	syncedEntries []billing.GroupRateMultiplierInput // captured rate replacement entries
	syncGroupErr  error                              // injected rate synchronization error

	// rpmSyncedGroupID and rpmSyncedEntries retain the RPM replacement request
	// before rpmSyncErr is returned. These fields remain separate from the
	// captured billing-rate request.
	rpmSyncedGroupID int64 // captured RPM replacement group
	// rpmSyncedEntries retains the caller's replacement slice for assertions. The
	// stub does not copy entry pointers before returning its configured result.
	rpmSyncedEntries []billing.GroupRPMOverrideInput // captured RPM replacement entries
	rpmSyncErr       error                           // injected RPM synchronization error
}

// TestAdminService_GetGroupRateMultipliers supplies populated, absent and
// failing group reads, plus an unavailable repository. It checks returned
// override rows, nil results and propagation of the configured repository
// error.
func TestAdminService_GetGroupRateMultipliers(t *testing.T) {
	t.Run("returns entries for group", func(t *testing.T) {
		aliceRate, bobRate := 1.5, 0.8
		repo := &userGroupRateRepoStubForGroupRate{
			getByGroupIDData: map[int64][]billing.UserGroupRateEntry{
				10: {
					{UserID: 1, UserName: "alice", UserEmail: "alice@test.com", RateMultiplier: &aliceRate},
					{UserID: 2, UserName: "bob", UserEmail: "bob@test.com", RateMultiplier: &bobRate},
				},
			},
		}
		svc := billing.NewGroupRateAdmin(repo, nil)

		entries, err := svc.GetGroupRateMultipliers(context.Background(), 10)
		require.NoError(t, err)
		require.Len(t, entries, 2)
		require.Equal(t, int64(1), entries[0].UserID)
		require.Equal(t, "alice", entries[0].UserName)
		require.NotNil(t, entries[0].RateMultiplier)
		require.Equal(t, 1.5, *entries[0].RateMultiplier)
		require.Equal(t, int64(2), entries[1].UserID)
		require.NotNil(t, entries[1].RateMultiplier)
		require.Equal(t, 0.8, *entries[1].RateMultiplier)
	})

	t.Run("returns nil when repo is nil", func(t *testing.T) {
		svc := billing.NewGroupRateAdmin(nil, nil)

		entries, err := svc.GetGroupRateMultipliers(context.Background(), 10)
		require.NoError(t, err)
		require.Nil(t, entries)
	})

	t.Run("returns empty slice for group with no entries", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{
			getByGroupIDData: map[int64][]billing.UserGroupRateEntry{},
		}
		svc := billing.NewGroupRateAdmin(repo, nil)

		entries, err := svc.GetGroupRateMultipliers(context.Background(), 99)
		require.NoError(t, err)
		require.Nil(t, entries)
	})

	t.Run("propagates repo error", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{
			getByGroupIDErr: errors.New("db error"),
		}
		svc := billing.NewGroupRateAdmin(repo, nil)

		_, err := svc.GetGroupRateMultipliers(context.Background(), 10)
		require.Error(t, err)
		require.Contains(t, err.Error(), "db error")
	})
}

// TestAdminService_ClearGroupRateMultipliers records a group deletion and
// exercises an unavailable or failing repository. It checks the requested
// group ID, successful nil-repository behavior and the reported deletion
// error.
func TestAdminService_ClearGroupRateMultipliers(t *testing.T) {
	t.Run("deletes by group ID", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{}
		svc := billing.NewGroupRateAdmin(repo, nil)

		err := svc.ClearGroupRateMultipliers(context.Background(), 42)
		require.NoError(t, err)
		require.Equal(t, []int64{42}, repo.deletedGroupIDs)
	})

	t.Run("returns nil when repo is nil", func(t *testing.T) {
		svc := billing.NewGroupRateAdmin(nil, nil)

		err := svc.ClearGroupRateMultipliers(context.Background(), 42)
		require.NoError(t, err)
	})

	t.Run("propagates repo error", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{
			deleteByGroupErr: errors.New("delete failed"),
		}
		svc := billing.NewGroupRateAdmin(repo, nil)

		err := svc.ClearGroupRateMultipliers(context.Background(), 42)
		require.Error(t, err)
		require.Contains(t, err.Error(), "delete failed")
	})
}

// TestAdminService_BatchSetGroupRateMultipliers supplies replacement rates and
// a configured synchronization error. It checks the captured group and
// entries, the error result and successful behavior when no repository is
// bound.
func TestAdminService_BatchSetGroupRateMultipliers(t *testing.T) {
	t.Run("syncs entries to repo", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{}
		svc := billing.NewGroupRateAdmin(repo, nil)

		entries := []billing.GroupRateMultiplierInput{
			{UserID: 1, RateMultiplier: 1.5},
			{UserID: 2, RateMultiplier: 0.8},
		}
		err := svc.BatchSetGroupRateMultipliers(context.Background(), 10, entries)
		require.NoError(t, err)
		require.Equal(t, int64(10), repo.syncedGroupID)
		require.Equal(t, entries, repo.syncedEntries)
	})

	t.Run("returns nil when repo is nil", func(t *testing.T) {
		svc := billing.NewGroupRateAdmin(nil, nil)

		err := svc.BatchSetGroupRateMultipliers(context.Background(), 10, nil)
		require.NoError(t, err)
	})

	t.Run("propagates repo error", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{
			syncGroupErr: errors.New("sync failed"),
		}
		svc := billing.NewGroupRateAdmin(repo, nil)

		err := svc.BatchSetGroupRateMultipliers(context.Background(), 10, []billing.GroupRateMultiplierInput{
			{UserID: 1, RateMultiplier: 1.0},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "sync failed")
	})
}

// TestAdminService_BatchSetGroupRPMOverrides submits a valid RPM override and
// then a negative value. It checks the captured replacement request and
// rejects the negative value as a bad request before synchronization.
func TestAdminService_BatchSetGroupRPMOverrides(t *testing.T) {
	t.Run("syncs entries to repo", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{}
		svc := billing.NewGroupRateAdmin(repo, nil)
		override := 20
		entries := []billing.GroupRPMOverrideInput{{UserID: 2, RPMOverride: &override}}

		err := svc.BatchSetGroupRPMOverrides(context.Background(), 10, entries)
		require.NoError(t, err)
		require.Equal(t, int64(10), repo.rpmSyncedGroupID)
		require.Equal(t, entries, repo.rpmSyncedEntries)
	})

	t.Run("rejects negative override as bad request", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{}
		svc := billing.NewGroupRateAdmin(repo, nil)
		negative := -1

		err := svc.BatchSetGroupRPMOverrides(context.Background(), 10, []billing.GroupRPMOverrideInput{
			{UserID: 2, RPMOverride: &negative},
		})
		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
		require.Zero(t, repo.rpmSyncedGroupID)
	})
}

// GetByUserID panics because these administrator cases expect group-scoped
// reads. Calling it records no fixture state; the panic identifies an
// unexpected request for an individual user's override map.
func (s *userGroupRateRepoStubForGroupRate) GetByUserID(_ context.Context, _ int64) (map[int64]float64, error) {
	panic("unexpected GetByUserID call")
}

// GetByUserAndGroup panics when an administrator case tries runtime multiplier
// resolution. These tests exercise group configuration operations, and the
// panic identifies a single user/group lookup outside that fixture path.
func (s *userGroupRateRepoStubForGroupRate) GetByUserAndGroup(_ context.Context, _, _ int64) (*float64, error) {
	panic("unexpected GetByUserAndGroup call")
}

// GetRPMOverrideByUserAndGroup panics when an administrator case reads an
// individual RPM value. Group replacement tests use the group-scoped methods,
// so this call identifies an unexpected runtime-style lookup.
func (s *userGroupRateRepoStubForGroupRate) GetRPMOverrideByUserAndGroup(_ context.Context, _, _ int64) (*int, error) {
	panic("unexpected GetRPMOverrideByUserAndGroup call")
}

// GetByGroupID returns the configured group slice or read error. A configured
// error takes precedence over fixture data, and returned slices are shared
// with the stub for these read-only administrator cases.
func (s *userGroupRateRepoStubForGroupRate) GetByGroupID(_ context.Context, groupID int64) ([]billing.UserGroupRateEntry, error) {
	if s.getByGroupIDErr != nil {
		return nil, s.getByGroupIDErr
	}
	return s.getByGroupIDData[groupID], nil
}

// SyncUserGroupRates panics when a group administration case requests a
// user-scoped mutation. The fixture captures replacements by group, so this
// method identifies an unexpected operation before any fixture state changes.
func (s *userGroupRateRepoStubForGroupRate) SyncUserGroupRates(_ context.Context, _ int64, _ map[int64]*float64) error {
	panic("unexpected SyncUserGroupRates call")
}

// SyncGroupRateMultipliers captures the group ID and supplied entries, then
// returns its configured error. The slice remains shared with the caller, and
// recording occurs even when the stub reports failure.
func (s *userGroupRateRepoStubForGroupRate) SyncGroupRateMultipliers(_ context.Context, groupID int64, entries []billing.GroupRateMultiplierInput) error {
	s.syncedGroupID = groupID
	s.syncedEntries = entries
	return s.syncGroupErr
}

// SyncGroupRPMOverrides captures the RPM replacement group and entries, then
// returns its configured error. The supplied slice remains shared, allowing
// assertions to inspect exactly the request made by GroupRateAdmin.
func (s *userGroupRateRepoStubForGroupRate) SyncGroupRPMOverrides(_ context.Context, groupID int64, entries []billing.GroupRPMOverrideInput) error {
	s.rpmSyncedGroupID = groupID
	s.rpmSyncedEntries = entries
	return s.rpmSyncErr
}

// ClearGroupRPMOverrides panics because this fixture exercises RPM batch
// replacement rather than standalone clearing. No recording field is updated;
// an unexpected clear request is identified by this method's panic.
func (s *userGroupRateRepoStubForGroupRate) ClearGroupRPMOverrides(_ context.Context, _ int64) error {
	panic("unexpected ClearGroupRPMOverrides call")
}

// DeleteByGroupID appends the requested group ID before returning its
// configured error. Repeated calls therefore remain visible in the captured
// sequence, including calls for which the stub reports failure.
func (s *userGroupRateRepoStubForGroupRate) DeleteByGroupID(_ context.Context, groupID int64) error {
	s.deletedGroupIDs = append(s.deletedGroupIDs, groupID)
	return s.deleteByGroupErr
}

// DeleteByUserID panics when a group administration case attempts user-wide
// deletion. These tests capture group deletion through DeleteByGroupID, so the
// panic identifies a request outside their configured mutation path.
func (s *userGroupRateRepoStubForGroupRate) DeleteByUserID(_ context.Context, _ int64) error {
	panic("unexpected DeleteByUserID call")
}
