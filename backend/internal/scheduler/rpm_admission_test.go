package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// userRPMCacheStub supplies sequential counter results and records user or
// group increments. Tests configure its borrowed result slices and error
// values before admission calls; atomic call indices select each supplied
// count.
type userRPMCacheStub struct {
	// userGroupCalls and userCalls index userGroupCounts and userCounts
	// respectively. userGroupErr and userErr replace their supplied counts
	// after the invocation is recorded, and all scripted input stays fixed
	// during calls.
	userGroupCalls int32 // user-group increment invocation count
	userCalls      int32 // total user increment count

	userGroupCounts []int // configured group counter result sequence
	userGroupErr    error // configured group increment failure
	userCounts      []int // configured user counter result sequence
	userErr         error // configured user increment failure
}

// rpmOverrideRepoStub records override lookups and returns a configured value
// pointer or failure.
type rpmOverrideRepoStub struct {
	// override is borrowed fixture input returned directly by lookup. Tests
	// keep its pointed-to value stable while admission reads it; nil
	// represents an absent override.
	override *int  // optional group RPM override
	err      error // configured override lookup failure
	calls    int32 // override lookup invocation count
}

// IncrementUserGroupRPM records an increment and returns the corresponding
// configured count or failure. Tests keep calls within the positive int32
// index range; unscripted calls return one to keep group counts below their
// positive test limits.
func (s *userRPMCacheStub) IncrementUserGroupRPM(_ context.Context, _, _ int64) (int, error) {
	idx := int(atomic.AddInt32(&s.userGroupCalls, 1)) - 1
	if s.userGroupErr != nil {
		return 0, s.userGroupErr
	}
	if idx < len(s.userGroupCounts) {
		return s.userGroupCounts[idx], nil
	}
	return 1, nil
}

// IncrementUserRPM records a total-user increment and returns its configured
// count or failure. Tests keep calls within the positive int32 index range;
// unscripted calls return one so scenarios can isolate group admission when
// the user gate is enabled.
func (s *userRPMCacheStub) IncrementUserRPM(_ context.Context, _ int64) (int, error) {
	idx := int(atomic.AddInt32(&s.userCalls, 1)) - 1
	if s.userErr != nil {
		return 0, s.userErr
	}
	if idx < len(s.userCounts) {
		return s.userCounts[idx], nil
	}
	return 1, nil
}

// GetUserGroupRPM returns zero for the fixture's unused group counter read
// path.
func (s *userRPMCacheStub) GetUserGroupRPM(_ context.Context, _, _ int64) (int, error) {
	return 0, nil
}

// GetUserRPM returns zero for the fixture's unused total-user counter read
// path.
func (s *userRPMCacheStub) GetUserRPM(_ context.Context, _ int64) (int, error) {
	return 0, nil
}

// GetRPMOverrideByUserAndGroup counts a lookup and returns the fixture's
// borrowed override pointer. A configured error takes precedence, while nil
// override with nil error represents inheritance from the group limit.
func (s *rpmOverrideRepoStub) GetRPMOverrideByUserAndGroup(_ context.Context, _, _ int64) (*int, error) {
	atomic.AddInt32(&s.calls, 1)
	if s.err != nil {
		return nil, s.err
	}
	return s.override, nil
}

// newRPMAdmissionFixture binds RPM admission to the supplied counter and
// override lookup fixtures. The service retains their interface references, so
// tests configure scripted counts and override values before issuing admission
// calls.
func newRPMAdmissionFixture(t *testing.T, cache UserRPMCache, rateRepo RPMOverrides) *RPMAdmission {
	t.Helper()

	return NewRPMAdmission(cache, rateRepo, Diagnostics{})
}

// TestBillingCacheService_CheckRPM_OverrideTakesPrecedenceOverGroup supplies a
// lower user-group override and increasing group counts. It rejects the third
// request at the override and checks that this rejection occurs before
// total-user counting.
func TestBillingCacheService_CheckRPM_OverrideTakesPrecedenceOverGroup(t *testing.T) {
	override := 2
	// Group counts reach 1, 2, 3 while the user count stays below its limit of
	// 100.
	cache := &userRPMCacheStub{userGroupCounts: []int{1, 2, 3}}
	repo := &rpmOverrideRepoStub{override: &override}
	svc := newRPMAdmissionFixture(t, cache, repo)

	user := &RPMUser{ID: 1, RPMLimit: 100} // Keep the total user limit out of the group override scenario.
	group := &RPMGroup{ID: 10, RPMLimit: 100}

	require.NoError(t, svc.Check(context.Background(), user, group))
	require.NoError(t, svc.Check(context.Background(), user, group))
	require.ErrorIs(t, svc.Check(context.Background(), user, group), ErrGroupRPMExceeded)

	require.EqualValues(t, 3, atomic.LoadInt32(&cache.userGroupCalls), "override 命中分支应走 user-group 计数")
	// The first two requests reach the user check; the third stops at the
	// exceeded group override.
	require.EqualValues(t, 2, atomic.LoadInt32(&cache.userCalls), "override 超限前 user 计数器应被调用")
	require.EqualValues(t, 3, atomic.LoadInt32(&repo.calls))
}

// TestBillingCacheService_CheckRPM_UserLimitIsGlobalHardCap supplies a large
// group override while total-user counts exceed a smaller user cap. The third
// request must return ErrUserRPMExceeded despite the permissive group
// allowance.
func TestBillingCacheService_CheckRPM_UserLimitIsGlobalHardCap(t *testing.T) {
	override := 100 // Above the total user limit.
	// Group counts default to 1; total user counts increase through 1, 2, 3.
	cache := &userRPMCacheStub{userCounts: []int{1, 2, 3}}
	repo := &rpmOverrideRepoStub{override: &override}
	svc := newRPMAdmissionFixture(t, cache, repo)

	user := &RPMUser{ID: 1, RPMLimit: 2} // The total user cap still applies despite the group override of 100.
	group := &RPMGroup{ID: 10, RPMLimit: 100}

	require.NoError(t, svc.Check(context.Background(), user, group))
	require.NoError(t, svc.Check(context.Background(), user, group))
	require.ErrorIs(t, svc.Check(context.Background(), user, group), ErrUserRPMExceeded, "user 全局硬上限应优先于 override")
}

// TestBillingCacheService_CheckRPM_OverrideZeroSkipsGroupButUserStillApplies
// disables group counting with an explicit zero override and increases
// total-user counts. It admits five requests, rejects the sixth, and checks
// that group increments remain unused.
func TestBillingCacheService_CheckRPM_OverrideZeroSkipsGroupButUserStillApplies(t *testing.T) {
	zero := 0
	// Total user counts increase through 1 to 6.
	cache := &userRPMCacheStub{userCounts: []int{1, 2, 3, 4, 5, 6}}
	repo := &rpmOverrideRepoStub{override: &zero}
	svc := newRPMAdmissionFixture(t, cache, repo)

	user := &RPMUser{ID: 1, RPMLimit: 5}
	group := &RPMGroup{ID: 10, RPMLimit: 100}

	// An override of zero skips group counting while the total user limit of
	// five still applies.
	for i := range 5 {
		require.NoError(t, svc.Check(context.Background(), user, group), "request %d should pass", i+1)
	}
	require.ErrorIs(t, svc.Check(context.Background(), user, group), ErrUserRPMExceeded,
		"override=0 跳过分组但 user 全局上限仍应生效")
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.userGroupCalls), "override=0 不应触发分组计数器")
	require.EqualValues(t, 6, atomic.LoadInt32(&cache.userCalls), "user 计数器应被调用")
}

// TestBillingCacheService_CheckRPM_OverrideZeroAndUserZeroIsFullyUnlimited
// combines an explicit zero group override with a zero user limit. Fifty
// admission calls must succeed while both counter invocation totals remain
// zero.
func TestBillingCacheService_CheckRPM_OverrideZeroAndUserZeroIsFullyUnlimited(t *testing.T) {
	zero := 0
	cache := &userRPMCacheStub{}
	repo := &rpmOverrideRepoStub{override: &zero}
	svc := newRPMAdmissionFixture(t, cache, repo)

	user := &RPMUser{ID: 1, RPMLimit: 0} // Total user counting is also disabled.
	group := &RPMGroup{ID: 10, RPMLimit: 100}

	for range 50 {
		require.NoError(t, svc.Check(context.Background(), user, group))
	}
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.userGroupCalls), "override=0 不触发分组计数")
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.userCalls), "user.RPMLimit=0 也不触发用户计数")
}

// TestBillingCacheService_CheckRPM_NilOverrideFallsThroughToGroup returns no
// override and supplies counts at then above the group limit. It checks the
// second request's group rejection and that only the first request reaches
// total-user counting.
func TestBillingCacheService_CheckRPM_NilOverrideFallsThroughToGroup(t *testing.T) {
	// Group counts are 5 then 6; total user counts remain 1.
	cache := &userRPMCacheStub{userGroupCounts: []int{5, 6}}
	repo := &rpmOverrideRepoStub{override: nil}
	svc := newRPMAdmissionFixture(t, cache, repo)

	user := &RPMUser{ID: 1, RPMLimit: 999} // The group limit is reached first.
	group := &RPMGroup{ID: 10, RPMLimit: 5}

	require.NoError(t, svc.Check(context.Background(), user, group))                      // Group count 5 and user count 1 both pass.
	require.ErrorIs(t, svc.Check(context.Background(), user, group), ErrGroupRPMExceeded) // Group count 6 exceeds 5.

	require.EqualValues(t, 2, atomic.LoadInt32(&cache.userGroupCalls))
	// The first request reaches total user counting; the second stops at the
	// group rejection.
	require.EqualValues(t, 1, atomic.LoadInt32(&cache.userCalls), "group 未超时 user 也应检查；group 超时直接返回")
}

// TestBillingCacheService_CheckRPM_OverrideLookupErrorFallsThroughToGroup
// fails override lookup while a configured group count remains below its
// limit. It requires successful admission and verifies that lookup and group
// increment still run.
func TestBillingCacheService_CheckRPM_OverrideLookupErrorFallsThroughToGroup(t *testing.T) {
	cache := &userRPMCacheStub{userGroupCounts: []int{3}}
	repo := &rpmOverrideRepoStub{err: errors.New("db down")}
	svc := newRPMAdmissionFixture(t, cache, repo)

	user := &RPMUser{ID: 1, RPMLimit: 0}
	group := &RPMGroup{ID: 10, RPMLimit: 10}

	// A failed override lookup still applies the group's configured limit.
	require.NoError(t, svc.Check(context.Background(), user, group))
	require.EqualValues(t, 1, atomic.LoadInt32(&cache.userGroupCalls))
	require.EqualValues(t, 1, atomic.LoadInt32(&repo.calls))
}

// TestBillingCacheService_CheckRPM_UserLevelFallbackWhenGroupUnlimited
// disables the group limit and supplies increasing total-user counts. It
// requires the third request's user rejection and confirms that group counting
// is skipped.
func TestBillingCacheService_CheckRPM_UserLevelFallbackWhenGroupUnlimited(t *testing.T) {
	cache := &userRPMCacheStub{userCounts: []int{1, 2, 3}}
	repo := &rpmOverrideRepoStub{override: nil}
	svc := newRPMAdmissionFixture(t, cache, repo)

	user := &RPMUser{ID: 1, RPMLimit: 2}
	group := &RPMGroup{ID: 10, RPMLimit: 0} // Group counting is disabled.

	require.NoError(t, svc.Check(context.Background(), user, group))
	require.NoError(t, svc.Check(context.Background(), user, group))
	require.ErrorIs(t, svc.Check(context.Background(), user, group), ErrUserRPMExceeded)

	require.EqualValues(t, 0, atomic.LoadInt32(&cache.userGroupCalls), "group 未设限时不应 INCR user-group 键")
	require.EqualValues(t, 3, atomic.LoadInt32(&cache.userCalls))
}

// TestBillingCacheService_CheckRPM_NoLimitsConfiguredIsNoop supplies zero
// group and user limits with no override value. Repeated admissions must
// succeed and leave both increment invocation totals at zero.
func TestBillingCacheService_CheckRPM_NoLimitsConfiguredIsNoop(t *testing.T) {
	cache := &userRPMCacheStub{}
	repo := &rpmOverrideRepoStub{override: nil}
	svc := newRPMAdmissionFixture(t, cache, repo)

	user := &RPMUser{ID: 1, RPMLimit: 0}
	group := &RPMGroup{ID: 10, RPMLimit: 0}

	for range 10 {
		require.NoError(t, svc.Check(context.Background(), user, group))
	}
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.userGroupCalls))
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.userCalls))
}

// TestBillingCacheService_CheckRPM_RedisErrorFailOpen fails the enabled group
// counter while total-user counting is disabled. It requires successful
// admission and one group increment attempt, protecting that gate's error
// recovery path.
func TestBillingCacheService_CheckRPM_RedisErrorFailOpen(t *testing.T) {
	cache := &userRPMCacheStub{userGroupErr: errors.New("redis unavailable")}
	repo := &rpmOverrideRepoStub{override: nil}
	svc := newRPMAdmissionFixture(t, cache, repo)

	user := &RPMUser{ID: 1, RPMLimit: 0}
	group := &RPMGroup{ID: 10, RPMLimit: 5}

	// The request is admitted when Redis counting fails.
	require.NoError(t, svc.Check(context.Background(), user, group))
	require.EqualValues(t, 1, atomic.LoadInt32(&cache.userGroupCalls))
}

// TestBillingCacheService_CheckRPM_NoGroupUsesUserOnly omits the group and
// increases total-user counts across three requests. It rejects at the user
// cap and checks that no group override lookup is attempted.
func TestBillingCacheService_CheckRPM_NoGroupUsesUserOnly(t *testing.T) {
	cache := &userRPMCacheStub{userCounts: []int{1, 2, 3}}
	repo := &rpmOverrideRepoStub{}
	svc := newRPMAdmissionFixture(t, cache, repo)

	user := &RPMUser{ID: 1, RPMLimit: 2}

	// A request without a group checks the total user limit directly.
	require.NoError(t, svc.Check(context.Background(), user, nil))
	require.NoError(t, svc.Check(context.Background(), user, nil))
	require.ErrorIs(t, svc.Check(context.Background(), user, nil), ErrUserRPMExceeded)

	require.EqualValues(t, 0, atomic.LoadInt32(&repo.calls), "无 group 时不应查询 rpm_override")
	require.EqualValues(t, 3, atomic.LoadInt32(&cache.userCalls))
}

// TestBillingCacheService_CheckRPM_NilUserIsNoop supplies no user alongside an
// enabled group limit. It requires successful admission with no override
// lookup or user/group increment calls, protecting the missing-user bypass.
func TestBillingCacheService_CheckRPM_NilUserIsNoop(t *testing.T) {
	cache := &userRPMCacheStub{}
	repo := &rpmOverrideRepoStub{}
	svc := newRPMAdmissionFixture(t, cache, repo)

	require.NoError(t, svc.Check(context.Background(), nil, &RPMGroup{ID: 1, RPMLimit: 10}))
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.userGroupCalls))
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.userCalls))
	require.EqualValues(t, 0, atomic.LoadInt32(&repo.calls))
}
