package billing_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	sqlitetest "github.com/TokenFlux/TokenRouter/internal/testutil/sqlite"
)

// Grant timeline and window tests use in-memory records to expose issued
// dates, reset flags and self-revoke rereads. Two scenarios carry real Ent
// transactions while fixture writes remain in memory; their assertions
// establish context reuse independently of PostgreSQL locking or rollback.

// subscriptionSelectionGroupFixture rejects group-name reads outside these
// repository selection scenarios.
type subscriptionSelectionGroupFixture struct{}

// resetQuotaUserSubRepoStub records selected reset flags and mutates one
// retained grant for administrator reset tests. Promoted unsupported methods
// use the embedded panic fixture.
type resetQuotaUserSubRepoStub struct {
	// SubscriptionRepositoryNoop supplies promoted methods that panic. These
	// scenarios use only the explicitly overridden reads and reset operations on
	// this fixture.
	billingtestkit.SubscriptionRepositoryNoop // unsupported-operation panic fixture

	// sub is configured before calls and mutated directly on successful reset.
	// GetByID returns a shallow copy whose optional pointers and plan remain
	// shared.
	sub *billing.UserSubscription // retained reset target grant

	// resetDailyCalled, resetWeeklyCalled and resetMonthlyCalled record selection
	// before resetDailyErr, resetWeeklyErr and resetMonthlyErr are checked.
	// Selected failures stop counter writes while preserving those attempted
	// flags.
	resetDailyCalled   bool  // recorded daily reset selection
	resetWeeklyCalled  bool  // recorded weekly reset selection
	resetMonthlyCalled bool  // recorded monthly reset selection
	resetDailyErr      error // programmed daily reset failure
	resetWeeklyErr     error // programmed weekly reset failure
	resetMonthlyErr    error // programmed monthly reset failure
}

// transactionTrackingUserSubRepo records mutation contexts while directly
// changing retained in-memory grant rows. Its write observations establish
// context binding, with no SQL grant persistence or transaction rollback of
// those maps.
type transactionTrackingUserSubRepo struct {
	// SubscriptionRepository is a preinitialized shared backing fixture.
	// Overrides mutate its retained rows and index directly, while promoted reads
	// follow its shallow-copy policies.
	*billingtestkit.SubscriptionRepository // shared initialized grant repository
	// writeContexts appends before each fixture mutation attempt, including
	// missing-ID failures. Access and retained map updates are unsynchronized;
	// tests inspect them after synchronous service calls.
	writeContexts []context.Context // recorded mutation caller contexts
}

// subscriptionContextTransactions joins the test's real Ent transaction and
// overrides locking with an identity assertion. The backing grant repository
// remains in-memory, so these fixtures test context reuse without performing
// database grant locks.
type subscriptionContextTransactions struct {
	// SubscriptionMutations has no base client but joins a transaction carried in
	// context. LockSubscription is replaced locally with an assertion, while
	// inherited Within retains caller completion ownership.
	*billingpostgres.SubscriptionMutations            // shared transaction selection adapter
	t                                      *testing.T // fixture assertion test owner
	// tx is created by the scenario and checked by LockSubscription. Test cleanup
	// attempts its rollback; grant fixture map mutations are independent of that
	// transaction.
	tx *dbent.Tx // expected caller-owned Ent transaction
}

// dailyResetTrackingUserSubRepo records window-maintenance calls and chosen
// daily anchors without storing grant counters. Unsupported repository
// operations reach the embedded panic fixture.
type dailyResetTrackingUserSubRepo struct {
	// SubscriptionRepositoryNoop supplies promoted methods that panic.
	// Maintenance scenarios use this fixture's call-tracking overrides and avoid
	// unrelated grant reads or writes.
	billingtestkit.SubscriptionRepositoryNoop // unsupported-operation panic fixture

	// resetDailyCalled, resetWeeklyCalled, resetMonthlyCalled and activateCalled
	// record synchronous fixture calls; lastDailyStart accompanies daily reset
	// and lastActivation accompanies activation. Tests read these unsynchronized
	// observations after maintenance returns.
	resetDailyCalled   bool                                 // recorded daily reset invocation
	resetWeeklyCalled  bool                                 // recorded weekly reset invocation
	resetMonthlyCalled bool                                 // recorded monthly reset invocation
	activateCalled     bool                                 // recorded window activation invocation
	lastActivation     billing.SubscriptionWindowActivation // latest supplied activation flags
	lastDailyStart     time.Time                            // latest daily replacement anchor
}

// revokeSubscriptionRepoStub removes matching in-memory grants and rebuilds
// its lookup index for revoke scenarios.
type revokeSubscriptionRepoStub struct {
	// SubscriptionRepository is the preinitialized retained grant store. This
	// wrapper's Delete physically removes its map entry, and promoted reads/index
	// helpers share that backing state.
	*billingtestkit.SubscriptionRepository // shared initialized grant repository
}

// resettingRevokeSubscriptionRepoStub overrides monthly reset to update the
// retained grant before self-revoke rereads quota.
type resettingRevokeSubscriptionRepoStub struct {
	// revokeSubscriptionRepoStub shares the retained grant map and deletion
	// behavior. The monthly override changes that same stored row so self-revoke
	// can reread its reset counter.
	*revokeSubscriptionRepoStub // shared revoke fixture backing store
}

// TestAssignSubscription_SamePlanCreatesPendingChain supplies a future-ending
// grant of the same plan. A new issuance must start at that expiry, remain
// pending, retain its calendar-day duration and perform one create.
func TestAssignSubscription_SamePlanCreatesPendingChain(t *testing.T) {
	subRepo := billingtestkit.NewSubscriptionRepository()
	now := time.Now().UTC()
	limit := 10.0
	existing := &billing.UserSubscription{
		ID:        10,
		UserID:    1001,
		PlanID:    1,
		StartsAt:  now.Add(-24 * time.Hour),
		ExpiresAt: now.Add(29 * 24 * time.Hour),
		Status:    billing.SubscriptionStatusActive,
		CreatedAt: now.Add(-24 * time.Hour),
	}
	subRepo.Seed(existing)

	svc := newSubscriptionServiceForTest(subRepo)
	created, queued, err := svc.AssignOrExtendSubscription(context.Background(), &billing.AssignSubscriptionInput{
		UserID:        1001,
		PlanID:        1,
		ValidityDays:  30,
		DailyLimitUSD: &limit,
		Notes:         "renew",
	})
	require.NoError(t, err)
	require.True(t, queued)
	require.Equal(t, billing.SubscriptionStatusPending, created.Status)
	require.Equal(t, existing.ExpiresAt, created.StartsAt)
	require.Equal(t, existing.ExpiresAt.AddDate(0, 0, 30), created.ExpiresAt)
	require.Equal(t, 1, subRepo.CreateCalls)
}

// TestAssignSubscription_DifferentPlanStartsImmediately seeds an active grant
// for a different plan. Issuance must start near current time as active and
// use its own requested calendar-day duration rather than that existing
// expiry.
func TestAssignSubscription_DifferentPlanStartsImmediately(t *testing.T) {
	subRepo := billingtestkit.NewSubscriptionRepository()
	now := time.Now().UTC()
	limit := 10.0
	subRepo.Seed(&billing.UserSubscription{
		ID:        11,
		UserID:    1001,
		PlanID:    1,
		StartsAt:  now.Add(-24 * time.Hour),
		ExpiresAt: now.Add(29 * 24 * time.Hour),
		Status:    billing.SubscriptionStatusActive,
		CreatedAt: now.Add(-24 * time.Hour),
	})

	svc := newSubscriptionServiceForTest(subRepo)
	created, queued, err := svc.AssignOrExtendSubscription(context.Background(), &billing.AssignSubscriptionInput{
		UserID:        1001,
		PlanID:        2,
		ValidityDays:  7,
		DailyLimitUSD: &limit,
	})
	require.NoError(t, err)
	require.False(t, queued)
	require.Equal(t, billing.SubscriptionStatusActive, created.Status)
	// Allow two seconds for independent current-time observations around
	// synchronous fixture issuance; the lifetime relation is checked separately
	// from this clock comparison.
	require.WithinDuration(t, time.Now().UTC(), created.StartsAt, 2*time.Second)
	require.Equal(t, created.StartsAt.AddDate(0, 0, 7), created.ExpiresAt)
}

// TestAssignSubscription_ReusesExistingSourceOrderSubscription supplies an
// already recorded positive source-order identity with proposed new terms. The
// returned grant ID must remain the existing one, with no new create and no
// queued flag.
func TestAssignSubscription_ReusesExistingSourceOrderSubscription(t *testing.T) {
	subRepo := billingtestkit.NewSubscriptionRepository()
	now := time.Now().UTC()
	sourceOrderID := int64(7788)
	limit := 18.0
	existing := &billing.UserSubscription{
		ID:            21,
		UserID:        42,
		PlanID:        7,
		StartsAt:      now.Add(-2 * time.Hour),
		ExpiresAt:     now.Add(7 * 24 * time.Hour),
		Status:        billing.SubscriptionStatusActive,
		SourceOrderID: &sourceOrderID,
		CreatedAt:     now.Add(-2 * time.Hour),
	}
	subRepo.Seed(existing)

	svc := newSubscriptionServiceForTest(subRepo)
	created, queued, err := svc.AssignOrExtendSubscription(context.Background(), &billing.AssignSubscriptionInput{
		UserID:              42,
		PlanID:              7,
		ValidityDays:        30,
		DailyLimitUSD:       &limit,
		SourceOrderID:       &sourceOrderID,
		UseProvidedTemplate: true,
		Notes:               "retry same order",
	})
	require.NoError(t, err)
	require.False(t, queued)
	require.Equal(t, existing.ID, created.ID)
	require.Equal(t, 0, subRepo.CreateCalls)
}

// TestBulkAssignSubscription_ReportsQueuedAndActive gives one recipient a
// future-ending matching plan and another no prior grant. Both attempts
// succeed as created, with queued/active status map entries and zero
// failure/reuse counts.
func TestBulkAssignSubscription_ReportsQueuedAndActive(t *testing.T) {
	subRepo := billingtestkit.NewSubscriptionRepository()
	now := time.Now().UTC()
	limit := 10.0
	subRepo.Seed(&billing.UserSubscription{
		ID:        12,
		UserID:    1,
		PlanID:    9,
		StartsAt:  now.Add(-24 * time.Hour),
		ExpiresAt: now.Add(6 * 24 * time.Hour),
		Status:    billing.SubscriptionStatusActive,
		CreatedAt: now.Add(-24 * time.Hour),
	})

	svc := newSubscriptionServiceForTest(subRepo)
	result, err := svc.BulkAssignSubscription(context.Background(), &billing.BulkAssignSubscriptionInput{
		UserIDs:       []int64{1, 2},
		PlanID:        9,
		ValidityDays:  7,
		DailyLimitUSD: &limit,
	})
	require.NoError(t, err)
	require.Equal(t, 2, result.SuccessCount)
	require.Equal(t, 2, result.CreatedCount)
	require.Equal(t, 0, result.ReusedCount)
	require.Equal(t, 0, result.FailedCount)
	require.Equal(t, "queued", result.Statuses[1])
	require.Equal(t, "active", result.Statuses[2])
}

// TestShiftLaterChain_ShiftsOnlyLaterSubscriptions supplies an anchor, an
// overlapping sibling and one starting at anchor expiry. A positive shift must
// leave the overlap untouched while moving both later endpoints and retaining
// its pending status.
func TestShiftLaterChain_ShiftsOnlyLaterSubscriptions(t *testing.T) {
	subRepo := billingtestkit.NewSubscriptionRepository()
	now := time.Now().UTC()
	anchor := &billing.UserSubscription{
		ID:        31,
		UserID:    5,
		PlanID:    9,
		StartsAt:  now,
		ExpiresAt: now.Add(7 * 24 * time.Hour),
		Status:    billing.SubscriptionStatusActive,
		CreatedAt: now,
	}
	overlap := &billing.UserSubscription{
		ID:        32,
		UserID:    5,
		PlanID:    9,
		StartsAt:  now.Add(24 * time.Hour),
		ExpiresAt: now.Add(8 * 24 * time.Hour),
		Status:    billing.SubscriptionStatusPending,
		CreatedAt: now.Add(time.Minute),
	}
	later := &billing.UserSubscription{
		ID:        33,
		UserID:    5,
		PlanID:    9,
		StartsAt:  anchor.ExpiresAt,
		ExpiresAt: anchor.ExpiresAt.Add(7 * 24 * time.Hour),
		Status:    billing.SubscriptionStatusPending,
		CreatedAt: now.Add(2 * time.Minute),
	}
	subRepo.Seed(anchor)
	subRepo.Seed(overlap)
	subRepo.Seed(later)

	svc := newSubscriptionServiceForTest(subRepo)
	err := svc.ShiftLaterChain(context.Background(), []billing.UserSubscription{*anchor, *overlap, *later}, anchor, 48*time.Hour)
	require.NoError(t, err)

	unchangedOverlap, err := subRepo.GetByID(context.Background(), overlap.ID)
	require.NoError(t, err)
	require.Equal(t, overlap.StartsAt, unchangedOverlap.StartsAt)
	require.Equal(t, overlap.ExpiresAt, unchangedOverlap.ExpiresAt)

	shiftedLater, err := subRepo.GetByID(context.Background(), later.ID)
	require.NoError(t, err)
	require.Equal(t, later.StartsAt.Add(48*time.Hour), shiftedLater.StartsAt)
	require.Equal(t, later.ExpiresAt.Add(48*time.Hour), shiftedLater.ExpiresAt)
	require.Equal(t, billing.SubscriptionStatusPending, shiftedLater.Status)
}

// TestRevokeChainDelta_ActiveSubscriptionOnlyReleasesRemainingWindow evaluates
// fixed active, future and elapsed lifetimes. The returned shifts must remove
// remaining active time, the entire queued duration and zero for the elapsed
// grant.
func TestRevokeChainDelta_ActiveSubscriptionOnlyReleasesRemainingWindow(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	active := &billing.UserSubscription{
		StartsAt:  now.AddDate(0, 0, -10),
		ExpiresAt: now.AddDate(0, 0, 20),
		Status:    billing.SubscriptionStatusActive,
	}
	pending := &billing.UserSubscription{
		StartsAt:  now.AddDate(0, 0, 5),
		ExpiresAt: now.AddDate(0, 0, 35),
		Status:    billing.SubscriptionStatusPending,
	}
	expired := &billing.UserSubscription{
		StartsAt:  now.AddDate(0, 0, -40),
		ExpiresAt: now.AddDate(0, 0, -10),
		Status:    billing.SubscriptionStatusExpired,
	}

	require.Equal(t, now.Sub(active.ExpiresAt), billing.RevokeChainDelta(active, now))
	require.Equal(t, pending.StartsAt.Sub(pending.ExpiresAt), billing.RevokeChainDelta(pending, now))
	require.Equal(t, time.Duration(0), billing.RevokeChainDelta(expired, now))
}

// TestTargetSubscriptionExpiresAt_ActiveSubscriptionCountsFromNow supplies a
// started grant with remaining lifetime at a fixed UTC observation.
// Replacement validity must add the requested days to now, independent of its
// old expiry.
func TestTargetSubscriptionExpiresAt_ActiveSubscriptionCountsFromNow(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	active := &billing.UserSubscription{
		StartsAt:  now.AddDate(0, 0, -11),
		ExpiresAt: now.AddDate(0, 0, 19),
		Status:    billing.SubscriptionStatusActive,
	}

	require.Equal(t, now.AddDate(0, 0, 30), billing.TargetSubscriptionExpiresAt(active, now, 30))
}

// TestTargetSubscriptionExpiresAt_PendingSubscriptionCountsFromStartsAt
// supplies a future-start grant at a fixed UTC observation. Replacement
// validity must begin at that stored start and add the requested calendar-day
// duration.
func TestTargetSubscriptionExpiresAt_PendingSubscriptionCountsFromStartsAt(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	pending := &billing.UserSubscription{
		StartsAt:  now.AddDate(0, 0, 7),
		ExpiresAt: now.AddDate(0, 0, 37),
		Status:    billing.SubscriptionStatusPending,
	}

	require.Equal(t, pending.StartsAt.AddDate(0, 0, 15), billing.TargetSubscriptionExpiresAt(pending, now, 15))
}

// TestGetActiveSubscription_FiltersByPlanID seeds two active grants for one
// user with different plan IDs. Selection must return the matching plan's row,
// protecting plan filtering independently of expiry ordering.
func TestGetActiveSubscription_FiltersByPlanID(t *testing.T) {
	subRepo := billingtestkit.NewSubscriptionRepository()
	now := time.Now().UTC()
	subRepo.Seed(&billing.UserSubscription{
		ID:        13,
		UserID:    8,
		PlanID:    100,
		StartsAt:  now.Add(-time.Hour),
		ExpiresAt: now.Add(24 * time.Hour),
		Status:    billing.SubscriptionStatusActive,
	})
	subRepo.Seed(&billing.UserSubscription{
		ID:        14,
		UserID:    8,
		PlanID:    200,
		StartsAt:  now.Add(-time.Hour),
		ExpiresAt: now.Add(48 * time.Hour),
		Status:    billing.SubscriptionStatusActive,
	})

	svc := newSubscriptionServiceForTest(subRepo)
	sub, err := svc.GetActiveSubscription(context.Background(), 8, 200)
	require.NoError(t, err)
	require.Equal(t, int64(14), sub.ID)
	require.Equal(t, int64(200), sub.PlanID)
}

// TestRestoreSubscription_ExpiredActiveRestoresAsExpired seeds a deleted
// stored-active grant whose deadline has elapsed. Restoration must clear
// deletion and retain expired status both on the returned row and subsequent
// fixture lookup.
func TestRestoreSubscription_ExpiredActiveRestoresAsExpired(t *testing.T) {
	subRepo := billingtestkit.NewSubscriptionRepository()
	now := time.Now().UTC()
	deletedAt := now.Add(-time.Hour)
	subRepo.Seed(&billing.UserSubscription{
		ID:        101,
		UserID:    31,
		PlanID:    41,
		StartsAt:  now.Add(-48 * time.Hour),
		ExpiresAt: now.Add(-time.Minute),
		Status:    billing.SubscriptionStatusActive,
		DeletedAt: &deletedAt,
		CreatedAt: now.Add(-48 * time.Hour),
	})

	svc := newSubscriptionServiceForTest(subRepo)
	restored, err := svc.RestoreSubscription(context.Background(), 101)
	require.NoError(t, err)
	require.Equal(t, billing.SubscriptionStatusExpired, restored.Status)
	require.Nil(t, restored.DeletedAt)

	got, err := subRepo.GetByID(context.Background(), 101)
	require.NoError(t, err)
	require.Equal(t, billing.SubscriptionStatusExpired, got.Status)
	require.Nil(t, got.DeletedAt)
}

// TestRestoreSubscription_NotRevokedReturnsConflict supplies an undeleted
// active grant to restoration. The service must return the not-revoked error,
// keeping deletion presence as the precondition for this restore path.
func TestRestoreSubscription_NotRevokedReturnsConflict(t *testing.T) {
	subRepo := billingtestkit.NewSubscriptionRepository()
	now := time.Now().UTC()
	subRepo.Seed(&billing.UserSubscription{
		ID:        102,
		UserID:    31,
		PlanID:    42,
		StartsAt:  now.Add(-time.Hour),
		ExpiresAt: now.Add(24 * time.Hour),
		Status:    billing.SubscriptionStatusActive,
		CreatedAt: now.Add(-time.Hour),
	})

	svc := newSubscriptionServiceForTest(subRepo)
	_, err := svc.RestoreSubscription(context.Background(), 102)
	require.ErrorIs(t, err, billing.ErrSubscriptionNotRevoked)
}

// TestRestoreSubscription_LiveSubscriptionConflict seeds a deleted candidate
// overlapping another current grant of the same user and plan. Restoration
// must reject overlap and leave the candidate's deletion marker present.
func TestRestoreSubscription_LiveSubscriptionConflict(t *testing.T) {
	subRepo := billingtestkit.NewSubscriptionRepository()
	now := time.Now().UTC()
	deletedAt := now.Add(-30 * time.Minute)
	subRepo.Seed(&billing.UserSubscription{
		ID:        103,
		UserID:    31,
		PlanID:    43,
		StartsAt:  now.Add(-time.Hour),
		ExpiresAt: now.Add(24 * time.Hour),
		Status:    billing.SubscriptionStatusActive,
		DeletedAt: &deletedAt,
		CreatedAt: now.Add(-time.Hour),
	})
	subRepo.Seed(&billing.UserSubscription{
		ID:        104,
		UserID:    31,
		PlanID:    43,
		StartsAt:  now.Add(-30 * time.Minute),
		ExpiresAt: now.Add(48 * time.Hour),
		Status:    billing.SubscriptionStatusActive,
		CreatedAt: now.Add(-30 * time.Minute),
	})

	svc := newSubscriptionServiceForTest(subRepo)
	_, err := svc.RestoreSubscription(context.Background(), 103)
	require.ErrorIs(t, err, billing.ErrSubscriptionRestoreConflict)

	got, getErr := subRepo.GetByIDIncludeDeleted(context.Background(), 103)
	require.NoError(t, getErr)
	require.NotNil(t, got.DeletedAt)
}

// TestRestoreSubscription_FutureWindowRestoresAsPending seeds a deleted
// stored-active grant whose lifetime begins tomorrow. Restoration must clear
// deletion and return pending status derived from that future start.
func TestRestoreSubscription_FutureWindowRestoresAsPending(t *testing.T) {
	subRepo := billingtestkit.NewSubscriptionRepository()
	now := time.Now().UTC()
	deletedAt := now.Add(-time.Hour)
	subRepo.Seed(&billing.UserSubscription{
		ID:        105,
		UserID:    31,
		PlanID:    44,
		StartsAt:  now.Add(24 * time.Hour),
		ExpiresAt: now.Add(48 * time.Hour),
		Status:    billing.SubscriptionStatusActive,
		DeletedAt: &deletedAt,
		CreatedAt: now.Add(-time.Hour),
	})

	svc := newSubscriptionServiceForTest(subRepo)
	restored, err := svc.RestoreSubscription(context.Background(), 105)
	require.NoError(t, err)
	require.Equal(t, billing.SubscriptionStatusPending, restored.Status)
	require.Nil(t, restored.DeletedAt)
}

// TestNormalizeAssignValidityDays supplies zero, negative, excessive and
// ordinary positive day counts. Normalization must choose thirty for
// nonpositive input, cap excessive duration and retain the allowed positive
// value.
func TestNormalizeAssignValidityDays(t *testing.T) {
	require.Equal(t, 30, billing.NormalizeAssignValidityDays(0))
	require.Equal(t, 30, billing.NormalizeAssignValidityDays(-5))
	require.Equal(t, billing.MaxValidityDays, billing.NormalizeAssignValidityDays(billing.MaxValidityDays+100))
	require.Equal(t, 7, billing.NormalizeAssignValidityDays(7))
}

// TestCalculateProgress_BasicFields supplies a plan name and thirty-day
// remaining lifetime with no configured windows. The display must retain
// checked identity/lifetime values and rounded days while omitting all quota
// detail objects.
func TestCalculateProgress_BasicFields(t *testing.T) {
	svc := newTestSubscriptionService()
	now := time.Now().UTC()

	sub := &billing.UserSubscription{
		ID:        100,
		PlanID:    8,
		ExpiresAt: now.Add(30 * 24 * time.Hour),
		Plan:      &billing.SubscriptionPlan{Name: "Premium"},
	}

	progress := svc.CalculateProgress(sub)

	assert.Equal(t, int64(100), progress.ID)
	assert.Equal(t, int64(8), progress.PlanID)
	assert.Equal(t, "Premium", progress.PlanName)
	assert.Equal(t, sub.ExpiresAt, progress.ExpiresAt)
	assert.Equal(t, 30, progress.ExpiresInDays)
	assert.Nil(t, progress.Daily, "无日限额时 Daily 应为 nil")
	assert.Nil(t, progress.Weekly, "无周限额时 Weekly 应为 nil")
	assert.Nil(t, progress.Monthly, "无月限额时 Monthly 应为 nil")
}

// TestCalculateProgress_DailyUsage supplies a daily anchor, ten-dollar quota
// and three-dollar use. The returned daily object must retain those values and
// show seven dollars remaining with thirty percent consumption.
func TestCalculateProgress_DailyUsage(t *testing.T) {
	svc := newTestSubscriptionService()
	now := time.Now().UTC()
	dailyStart := now.Add(-12 * time.Hour)

	sub := &billing.UserSubscription{
		ID:               1,
		ExpiresAt:        now.Add(10 * 24 * time.Hour),
		DailyLimitUSD:    ptrFloat64(10.0),
		DailyUsageUSD:    3.0,
		DailyWindowStart: ptrTime(dailyStart),
	}

	progress := svc.CalculateProgress(sub)

	require.NotNil(t, progress.Daily, "有日限额和窗口时 Daily 不应为 nil")
	assert.Equal(t, 10.0, progress.Daily.LimitUSD)
	assert.Equal(t, 3.0, progress.Daily.UsedUSD)
	assert.Equal(t, 7.0, progress.Daily.RemainingUSD)
	assert.Equal(t, 30.0, progress.Daily.Percentage)
	assert.Equal(t, dailyStart, progress.Daily.WindowStart)
}

// TestCalculateProgress_DailyCardUsesExpiryAsDailyResetTime supplies a one-day
// lifetime with an activated daily quota. Its returned daily reset deadline
// must equal grant expiry, keeping that one-time allowance end visible.
func TestCalculateProgress_DailyCardUsesExpiryAsDailyResetTime(t *testing.T) {
	svc := newTestSubscriptionService()
	startsAt := time.Now().UTC().Add(-12 * time.Hour)
	dailyStart := timezone.NewCalendar(startsAt.Location()).StartOfDay(startsAt)
	expiresAt := startsAt.Add(24 * time.Hour)

	sub := &billing.UserSubscription{
		ID:               1,
		StartsAt:         startsAt,
		ExpiresAt:        expiresAt,
		DailyLimitUSD:    ptrFloat64(10.0),
		DailyUsageUSD:    3.0,
		DailyWindowStart: ptrTime(dailyStart),
	}

	progress := svc.CalculateProgress(sub)

	require.NotNil(t, progress.Daily, "日卡有日限额和窗口时 Daily 不应为 nil")
	assert.Equal(t, expiresAt, progress.Daily.ResetsAt, "日卡的一次性日额度结束时间应为订阅过期时间")
}

// TestCalculateProgress_WeeklyUsage supplies a weekly anchor and half-used
// fifty-dollar quota. The returned weekly object must show the supplied
// use/limit, twenty-five dollars remaining and fifty percent consumption.
func TestCalculateProgress_WeeklyUsage(t *testing.T) {
	svc := newTestSubscriptionService()
	now := time.Now().UTC()
	weeklyStart := now.Add(-3 * 24 * time.Hour)

	sub := &billing.UserSubscription{
		ID:                1,
		ExpiresAt:         now.Add(10 * 24 * time.Hour),
		WeeklyLimitUSD:    ptrFloat64(50.0),
		WeeklyUsageUSD:    25.0,
		WeeklyWindowStart: ptrTime(weeklyStart),
	}

	progress := svc.CalculateProgress(sub)

	require.NotNil(t, progress.Weekly, "有周限额和窗口时 Weekly 不应为 nil")
	assert.Equal(t, 50.0, progress.Weekly.LimitUSD)
	assert.Equal(t, 25.0, progress.Weekly.UsedUSD)
	assert.Equal(t, 25.0, progress.Weekly.RemainingUSD)
	assert.Equal(t, 50.0, progress.Weekly.Percentage)
}

// TestCalculateProgress_MonthlyUsage supplies a monthly anchor and
// eighty-dollar use of a hundred-dollar quota. The returned monthly object
// must retain those values and show twenty dollars remaining with eighty
// percent consumption.
func TestCalculateProgress_MonthlyUsage(t *testing.T) {
	svc := newTestSubscriptionService()
	now := time.Now().UTC()
	monthlyStart := now.Add(-15 * 24 * time.Hour)

	sub := &billing.UserSubscription{
		ID:                 1,
		ExpiresAt:          now.Add(10 * 24 * time.Hour),
		MonthlyLimitUSD:    ptrFloat64(100.0),
		MonthlyUsageUSD:    80.0,
		MonthlyWindowStart: ptrTime(monthlyStart),
	}

	progress := svc.CalculateProgress(sub)

	require.NotNil(t, progress.Monthly, "有月限额和窗口时 Monthly 不应为 nil")
	assert.Equal(t, 100.0, progress.Monthly.LimitUSD)
	assert.Equal(t, 80.0, progress.Monthly.UsedUSD)
	assert.Equal(t, 20.0, progress.Monthly.RemainingUSD)
	assert.Equal(t, 80.0, progress.Monthly.Percentage)
}

// TestCalculateProgress_MonthlyTailWindowUsesExpiryAsResetTime supplies fixed
// monthly lifetime and anchor dates with configured usage. Progress must
// include that stored window and display its expiry as the selected end; it
// does not establish a live tail at later test dates.
func TestCalculateProgress_MonthlyTailWindowUsesExpiryAsResetTime(t *testing.T) {
	svc := newTestSubscriptionService()
	startsAt := time.Date(2026, 4, 30, 8, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2026, 5, 30, 8, 0, 0, 0, time.UTC)
	monthlyStart := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)

	sub := &billing.UserSubscription{
		ID:                 1,
		StartsAt:           startsAt,
		ExpiresAt:          expiresAt,
		MonthlyLimitUSD:    ptrFloat64(100.0),
		MonthlyUsageUSD:    80.0,
		MonthlyWindowStart: ptrTime(monthlyStart),
	}

	progress := svc.CalculateProgress(sub)

	require.NotNil(t, progress.Monthly, "月限额尾段仍应返回进度")
	assert.Equal(t, expiresAt, progress.Monthly.ResetsAt, "到期尾段月额度结束时间应显示订阅过期时间")
}

// TestCalculateProgress_DailyTailWithMonthlyLimitUsesDailyResetTime supplies
// fixed daily/monthly quota fields and a stored daily anchor. Its progress
// must display the next daily deadline allowed by positive outer quota,
// independently of current active status.
func TestCalculateProgress_DailyTailWithMonthlyLimitUsesDailyResetTime(t *testing.T) {
	svc := newTestSubscriptionService()
	startsAt := time.Date(2026, 4, 30, 8, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2026, 5, 30, 8, 0, 0, 0, time.UTC)
	dailyStart := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)

	sub := &billing.UserSubscription{
		ID:               1,
		StartsAt:         startsAt,
		ExpiresAt:        expiresAt,
		DailyLimitUSD:    ptrFloat64(10.0),
		MonthlyLimitUSD:  ptrFloat64(100.0),
		DailyUsageUSD:    8.0,
		DailyWindowStart: ptrTime(dailyStart),
	}

	progress := svc.CalculateProgress(sub)

	require.NotNil(t, progress.Daily, "有限月额度保护下仍应返回日额度进度")
	assert.Equal(t, dailyStart.Add(billing.SubscriptionDailyWindow), progress.Daily.ResetsAt, "尾段日额度应显示实际刷新时间")
}

// TestCalculateProgress_OverLimit_ClampedTo100Percent supplies daily usage
// above its configured ten-dollar limit. Progress must retain a daily object
// while capping percentage at one hundred and remaining dollars at zero.
func TestCalculateProgress_OverLimit_ClampedTo100Percent(t *testing.T) {
	svc := newTestSubscriptionService()
	now := time.Now().UTC()

	sub := &billing.UserSubscription{
		ID:               1,
		ExpiresAt:        now.Add(10 * 24 * time.Hour),
		DailyLimitUSD:    ptrFloat64(10.0),
		DailyUsageUSD:    15.0, // Exceeds the daily limit.
		DailyWindowStart: ptrTime(now.Add(-1 * time.Hour)),
	}

	progress := svc.CalculateProgress(sub)

	require.NotNil(t, progress.Daily)
	assert.Equal(t, 100.0, progress.Daily.Percentage, "超额使用应被截断为 100%")
	assert.Equal(t, 0.0, progress.Daily.RemainingUSD, "超额使用时剩余应为 0")
}

// TestCalculateProgress_NoWindowStart_NoProgress supplies finite daily and
// weekly limits without stored anchors. Calculation must omit both window
// objects, keeping quota configuration distinct from activated progress data.
func TestCalculateProgress_NoWindowStart_NoProgress(t *testing.T) {
	svc := newTestSubscriptionService()
	now := time.Now().UTC()

	sub := &billing.UserSubscription{
		ID:             1,
		ExpiresAt:      now.Add(10 * 24 * time.Hour),
		DailyLimitUSD:  ptrFloat64(10.0),
		WeeklyLimitUSD: ptrFloat64(50.0),
		DailyUsageUSD:  0,
		WeeklyUsageUSD: 0,
	}

	progress := svc.CalculateProgress(sub)

	assert.Nil(t, progress.Daily, "无 DailyWindowStart 时 Daily 应为 nil")
	assert.Nil(t, progress.Weekly, "无 WeeklyWindowStart 时 Weekly 应为 nil")
}

// TestCalculateProgress_AllLimits supplies activated daily, weekly and monthly
// quotas with separate usage amounts. All three progress objects must be
// present and report their respective expected percentages.
func TestCalculateProgress_AllLimits(t *testing.T) {
	svc := newTestSubscriptionService()
	now := time.Now().UTC()

	sub := &billing.UserSubscription{
		ID:                 1,
		ExpiresAt:          now.Add(10 * 24 * time.Hour),
		DailyLimitUSD:      ptrFloat64(10.0),
		WeeklyLimitUSD:     ptrFloat64(50.0),
		MonthlyLimitUSD:    ptrFloat64(100.0),
		DailyUsageUSD:      5.0,
		WeeklyUsageUSD:     20.0,
		MonthlyUsageUSD:    60.0,
		DailyWindowStart:   ptrTime(now.Add(-6 * time.Hour)),
		WeeklyWindowStart:  ptrTime(now.Add(-3 * 24 * time.Hour)),
		MonthlyWindowStart: ptrTime(now.Add(-15 * 24 * time.Hour)),
	}

	progress := svc.CalculateProgress(sub)

	require.NotNil(t, progress.Daily)
	require.NotNil(t, progress.Weekly)
	require.NotNil(t, progress.Monthly)

	assert.Equal(t, 50.0, progress.Daily.Percentage)
	assert.Equal(t, 40.0, progress.Weekly.Percentage)
	assert.Equal(t, 60.0, progress.Monthly.Percentage)
}

// TestCalculateProgress_ExpiredSubscription supplies a deadline one day before
// current time. Progress must return zero remaining days, protecting the
// rounded lifetime display after the grant has elapsed.
func TestCalculateProgress_ExpiredSubscription(t *testing.T) {
	svc := newTestSubscriptionService()

	sub := &billing.UserSubscription{
		ID:        1,
		ExpiresAt: time.Now().UTC().Add(-24 * time.Hour), // Expired.
	}

	progress := svc.CalculateProgress(sub)

	assert.Equal(t, 0, progress.ExpiresInDays, "过期订阅的剩余天数应为 0")
}

// TestCalculateProgress_ResetsInSeconds_NotNegative supplies a daily anchor
// far enough in the past to place its reset behind current time. Progress must
// retain the object and return a nonnegative countdown rather than exposing
// elapsed negative seconds.
func TestCalculateProgress_ResetsInSeconds_NotNegative(t *testing.T) {
	svc := newTestSubscriptionService()
	// Place the anchor far enough behind current time that its selected reset
	// deadline is past. The countdown is clamped independently of keeping the
	// window progress object.
	pastStart := time.Now().UTC().Add(-48 * time.Hour)

	sub := &billing.UserSubscription{
		ID:               1,
		ExpiresAt:        time.Now().UTC().Add(10 * 24 * time.Hour),
		DailyLimitUSD:    ptrFloat64(10.0),
		DailyUsageUSD:    1.0,
		DailyWindowStart: ptrTime(pastStart),
	}

	progress := svc.CalculateProgress(sub)

	require.NotNil(t, progress.Daily)
	assert.GreaterOrEqual(t, progress.Daily.ResetsInSeconds, int64(0),
		"ResetsInSeconds 不应为负数")
}

// TestAdminResetQuota_ResetBoth selects daily and weekly resets against the
// flag-recording fixture. The service must succeed with a returned grant,
// record both selected flags and leave monthly unselected.
func TestAdminResetQuota_ResetBoth(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{
		sub: &billing.UserSubscription{ID: 1, UserID: 10, PlanID: 20},
	}
	svc := newResetQuotaSvc(stub)

	result, err := svc.AdminResetQuota(context.Background(), 1, true, true, false)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, stub.resetDailyCalled, "应调用 ResetDailyUsage")
	require.True(t, stub.resetWeeklyCalled, "应调用 ResetWeeklyUsage")
	require.False(t, stub.resetMonthlyCalled, "不应调用 ResetMonthlyUsage")
}

// TestAdminResetQuota_ResetDailyOnly selects daily reset alone against the
// fixture. A returned successful grant must accompany only the daily recorded
// flag, leaving weekly and monthly reset flags false.
func TestAdminResetQuota_ResetDailyOnly(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{
		sub: &billing.UserSubscription{ID: 2, UserID: 10, PlanID: 20},
	}
	svc := newResetQuotaSvc(stub)

	result, err := svc.AdminResetQuota(context.Background(), 2, true, false, false)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, stub.resetDailyCalled, "应调用 ResetDailyUsage")
	require.False(t, stub.resetWeeklyCalled, "不应调用 ResetWeeklyUsage")
	require.False(t, stub.resetMonthlyCalled, "不应调用 ResetMonthlyUsage")
}

// TestAdminResetQuota_ResetWeeklyOnly selects weekly reset alone against the
// fixture. A returned successful grant must accompany only the weekly recorded
// flag, leaving daily and monthly reset flags false.
func TestAdminResetQuota_ResetWeeklyOnly(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{
		sub: &billing.UserSubscription{ID: 3, UserID: 10, PlanID: 20},
	}
	svc := newResetQuotaSvc(stub)

	result, err := svc.AdminResetQuota(context.Background(), 3, false, true, false)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, stub.resetDailyCalled, "不应调用 ResetDailyUsage")
	require.True(t, stub.resetWeeklyCalled, "应调用 ResetWeeklyUsage")
	require.False(t, stub.resetMonthlyCalled, "不应调用 ResetMonthlyUsage")
}

// TestAdminResetQuota_BothFalseReturnsError selects no quota window despite a
// present fixture grant. The service must reject the request as invalid input
// before any reset flag is recorded.
func TestAdminResetQuota_BothFalseReturnsError(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{
		sub: &billing.UserSubscription{ID: 7, UserID: 10, PlanID: 20},
	}
	svc := newResetQuotaSvc(stub)

	_, err := svc.AdminResetQuota(context.Background(), 7, false, false, false)

	require.ErrorIs(t, err, billing.ErrInvalidInput)
	require.False(t, stub.resetDailyCalled)
	require.False(t, stub.resetWeeklyCalled)
	require.False(t, stub.resetMonthlyCalled)
}

// TestAdminResetQuota_SubscriptionNotFound supplies no target record to a
// selected multi-window reset. The service must return not-found and keep
// every reset flag false, exposing target lookup before mutation.
func TestAdminResetQuota_SubscriptionNotFound(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{sub: nil}
	svc := newResetQuotaSvc(stub)

	_, err := svc.AdminResetQuota(context.Background(), 999, true, true, true)

	require.ErrorIs(t, err, billing.ErrSubscriptionNotFound)
	require.False(t, stub.resetDailyCalled)
	require.False(t, stub.resetWeeklyCalled)
	require.False(t, stub.resetMonthlyCalled)
}

// TestAdminResetQuota_ResetDailyUsageError selects daily and weekly while
// injecting the fixture's daily failure. The error must propagate after both
// selected flags are recorded by the single reset call; those flags describe
// attempted selection rather than completed resets.
func TestAdminResetQuota_ResetDailyUsageError(t *testing.T) {
	dbErr := errors.New("db error")
	stub := &resetQuotaUserSubRepoStub{
		sub:           &billing.UserSubscription{ID: 4, UserID: 10, PlanID: 20},
		resetDailyErr: dbErr,
	}
	svc := newResetQuotaSvc(stub)

	_, err := svc.AdminResetQuota(context.Background(), 4, true, true, false)

	require.ErrorIs(t, err, dbErr)
	require.True(t, stub.resetDailyCalled)
	require.True(t, stub.resetWeeklyCalled, "原子重置应在一次调用中提交所选窗口")
}

// TestAdminResetQuota_ResetWeeklyUsageError selects weekly reset and injects
// its fixture failure. The error must propagate with the weekly flag recorded,
// protecting selection capture before the programmed error return.
func TestAdminResetQuota_ResetWeeklyUsageError(t *testing.T) {
	dbErr := errors.New("db error")
	stub := &resetQuotaUserSubRepoStub{
		sub:            &billing.UserSubscription{ID: 5, UserID: 10, PlanID: 20},
		resetWeeklyErr: dbErr,
	}
	svc := newResetQuotaSvc(stub)

	_, err := svc.AdminResetQuota(context.Background(), 5, false, true, false)

	require.ErrorIs(t, err, dbErr)
	require.True(t, stub.resetWeeklyCalled)
}

// TestAdminResetQuota_ResetMonthlyOnly selects monthly reset alone against the
// fixture. A returned successful grant must accompany only the monthly flag,
// leaving daily and weekly reset flags false.
func TestAdminResetQuota_ResetMonthlyOnly(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{
		sub: &billing.UserSubscription{ID: 8, UserID: 10, PlanID: 20},
	}
	svc := newResetQuotaSvc(stub)

	result, err := svc.AdminResetQuota(context.Background(), 8, false, false, true)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, stub.resetDailyCalled, "不应调用 ResetDailyUsage")
	require.False(t, stub.resetWeeklyCalled, "不应调用 ResetWeeklyUsage")
	require.True(t, stub.resetMonthlyCalled, "应调用 ResetMonthlyUsage")
}

// TestAdminResetQuota_ResetMonthlyUsageError selects monthly reset and injects
// its fixture failure. The error must propagate with monthly recorded,
// protecting that selected operation's caller-visible failure result.
func TestAdminResetQuota_ResetMonthlyUsageError(t *testing.T) {
	dbErr := errors.New("db error")
	stub := &resetQuotaUserSubRepoStub{
		sub:             &billing.UserSubscription{ID: 9, UserID: 10, PlanID: 20},
		resetMonthlyErr: dbErr,
	}
	svc := newResetQuotaSvc(stub)

	_, err := svc.AdminResetQuota(context.Background(), 9, false, false, true)

	require.ErrorIs(t, err, dbErr)
	require.True(t, stub.resetMonthlyCalled)
}

// TestAdminResetQuota_ReturnsRefreshedSub supplies a daily counter that the
// fixture clears on reset. The returned grant must show zero daily use and a
// recorded reset flag, exposing the service's post-write reread.
func TestAdminResetQuota_ReturnsRefreshedSub(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{
		sub: &billing.UserSubscription{
			ID:            6,
			UserID:        10,
			PlanID:        20,
			DailyUsageUSD: 99.9,
		},
	}

	svc := newResetQuotaSvc(stub)
	result, err := svc.AdminResetQuota(context.Background(), 6, true, false, false)

	require.NoError(t, err)
	// The reset fixture changes its retained counter before GetByID supplies a
	// new shallow outer record. This assertion observes the post-reset value
	// returned by the service.
	require.Equal(t, float64(0), result.DailyUsageUSD, "返回的订阅应反映已归零的用量")
	require.True(t, stub.resetDailyCalled)
}

// TestGetUsableSubscription_SkipsExhaustedSubscription supplies an
// earlier-expiring exhausted grant before another with remaining daily
// capacity. Selection must return the latter and report no maintenance,
// protecting the listed quota-error skip path.
func TestGetUsableSubscription_SkipsExhaustedSubscription(t *testing.T) {
	repo := billingtestkit.NewSubscriptionRepository()
	now := time.Now()
	windowStart := now.Add(-time.Hour)
	repo.Seed(&billing.UserSubscription{
		ID:               1,
		UserID:           1,
		PlanID:           1,
		StartsAt:         now.Add(-2 * time.Hour),
		ExpiresAt:        now.Add(time.Hour),
		Status:           billing.SubscriptionStatusActive,
		DailyWindowStart: &windowStart,
		DailyLimitUSD:    billingEligibilityLimitPtr(10),
		DailyUsageUSD:    10,
	})
	repo.Seed(&billing.UserSubscription{
		ID:               2,
		UserID:           1,
		PlanID:           2,
		StartsAt:         now.Add(-2 * time.Hour),
		ExpiresAt:        now.Add(2 * time.Hour),
		Status:           billing.SubscriptionStatusActive,
		DailyWindowStart: &windowStart,
		DailyLimitUSD:    billingEligibilityLimitPtr(10),
		DailyUsageUSD:    9,
	})
	svc := billing.NewSubscriptionService(subscriptionSelectionGroupFixture{}, repo, nil)

	sub, needsMaintenance, err := svc.GetUsableSubscription(context.Background(), 1)

	require.NoError(t, err)
	require.Equal(t, int64(2), sub.ID)
	require.False(t, needsMaintenance)
}

// TestGetUsableSubscription_AllExhaustedReturnsNotFound supplies an active
// grant with no remaining daily capacity. Selection must return not-found
// after rejecting the only candidate, keeping quota absence distinct from
// repository success.
func TestGetUsableSubscription_AllExhaustedReturnsNotFound(t *testing.T) {
	repo := billingtestkit.NewSubscriptionRepository()
	now := time.Now()
	windowStart := now.Add(-time.Hour)
	repo.Seed(&billing.UserSubscription{
		ID:               1,
		UserID:           1,
		PlanID:           1,
		StartsAt:         now.Add(-2 * time.Hour),
		ExpiresAt:        now.Add(time.Hour),
		Status:           billing.SubscriptionStatusActive,
		DailyWindowStart: &windowStart,
		DailyLimitUSD:    billingEligibilityLimitPtr(10),
		DailyUsageUSD:    10,
	})
	svc := billing.NewSubscriptionService(subscriptionSelectionGroupFixture{}, repo, nil)

	_, _, err := svc.GetUsableSubscription(context.Background(), 1)

	require.ErrorIs(t, err, billing.ErrSubscriptionNotFound)
}

// TestExtendSubscriptionReusesCallerTransaction carries a real SQLite Ent
// transaction while writes use an in-memory fixture. Every recorded
// extension/status write must retain that transaction in context; the scenario
// asserts binding reuse rather than SQL grant rollback.
func TestExtendSubscriptionReusesCallerTransaction(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	// Release the caller-owned SQLite transaction during test cleanup, discarding
	// cleanup error after assertions. Retained grant map writes are separate and
	// are not undone by this rollback.
	t.Cleanup(func() { _ = tx.Rollback() })
	txCtx := dbent.NewTxContext(ctx, tx)

	now := time.Now().UTC()
	repo := &transactionTrackingUserSubRepo{SubscriptionRepository: billingtestkit.NewSubscriptionRepository()}
	repo.Seed(&billing.UserSubscription{
		ID: 1, UserID: 7, PlanID: 9, StartsAt: now.Add(-24 * time.Hour),
		ExpiresAt: now.Add(10 * 24 * time.Hour), Status: billing.SubscriptionStatusActive,
	})
	svc := billing.NewSubscriptionService(nil, repo, &subscriptionContextTransactions{SubscriptionMutations: billingpostgres.NewSubscriptionMutations(nil), t: t, tx: tx}, subscriptionClockFixture())

	_, err = svc.ExtendSubscription(txCtx, 1, -1)
	require.NoError(t, err)
	require.NotEmpty(t, repo.writeContexts)
	for _, writeCtx := range repo.writeContexts {
		require.Same(t, tx, dbent.TxFromContext(writeCtx))
	}
}

// TestRevokeSubscriptionReusesCallerTransaction carries a SQLite Ent
// transaction with an in-memory target grant. Its single recorded deletion
// context must retain the same transaction, checking context selection rather
// than persisted revoke effects.
func TestRevokeSubscriptionReusesCallerTransaction(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	// Release the caller-owned SQLite transaction during test cleanup, discarding
	// cleanup error after assertions. Retained grant map writes are separate and
	// are not undone by this rollback.
	t.Cleanup(func() { _ = tx.Rollback() })
	txCtx := dbent.NewTxContext(ctx, tx)

	now := time.Now().UTC()
	repo := &transactionTrackingUserSubRepo{SubscriptionRepository: billingtestkit.NewSubscriptionRepository()}
	repo.Seed(&billing.UserSubscription{
		ID: 2, UserID: 8, PlanID: 10, StartsAt: now.Add(-24 * time.Hour),
		ExpiresAt: now.Add(10 * 24 * time.Hour), Status: billing.SubscriptionStatusActive,
	})
	svc := billing.NewSubscriptionService(nil, repo, &subscriptionContextTransactions{SubscriptionMutations: billingpostgres.NewSubscriptionMutations(nil), t: t, tx: tx}, subscriptionClockFixture())

	err = svc.RevokeSubscription(txCtx, 2)
	require.NoError(t, err)
	require.Len(t, repo.writeContexts, 1)
	require.Same(t, tx, dbent.TxFromContext(repo.writeContexts[0]))
}

// TestAssignOrExtendSubscription_ExpiredDailyCardStartsNewOneTimeQuota seeds
// an elapsed one-day grant with prior counters. New issuance must start a
// fresh active one-time lifetime, retain new notes and leave new window
// anchors/counters unconsumed.
func TestAssignOrExtendSubscription_ExpiredDailyCardStartsNewOneTimeQuota(t *testing.T) {
	subRepo := billingtestkit.NewSubscriptionRepository()
	limit := 10.0
	oldStart := time.Now().UTC().AddDate(0, 0, -3)
	oldWindowStart := timezone.NewCalendar(oldStart.Location()).StartOfDay(oldStart)
	subRepo.Seed(&billing.UserSubscription{
		ID:                 100,
		UserID:             200,
		PlanID:             1,
		StartsAt:           oldStart,
		ExpiresAt:          oldStart.AddDate(0, 0, 1),
		Status:             billing.SubscriptionStatusExpired,
		DailyWindowStart:   &oldWindowStart,
		WeeklyWindowStart:  &oldWindowStart,
		MonthlyWindowStart: &oldWindowStart,
		DailyLimitUSD:      &limit,
		DailyUsageUSD:      10,
		WeeklyUsageUSD:     20,
		MonthlyUsageUSD:    30,
		Notes:              "old",
	})
	svc := newSubscriptionServiceForTest(subRepo)

	created, queued, err := svc.AssignOrExtendSubscription(context.Background(), &billing.AssignSubscriptionInput{
		UserID:        200,
		PlanID:        1,
		ValidityDays:  1,
		DailyLimitUSD: &limit,
		Notes:         "new",
	})

	require.NoError(t, err)
	require.False(t, queued)
	require.True(t, created.HasOneTimeDailyQuota(), "过期后重新购买 1 日卡仍应被识别为一次性日额度")
	require.Equal(t, billing.SubscriptionStatusActive, created.Status)
	require.True(t, created.StartsAt.After(oldStart), "重新购买过期订阅时应创建新的当前周期")
	require.False(t, created.ExpiresAt.After(created.StartsAt.AddDate(0, 0, 1)))
	require.Nil(t, created.DailyWindowStart, "fork 当前订阅窗口保持首次使用时激活")
	require.Equal(t, 0.0, created.DailyUsageUSD)
	require.Equal(t, 0.0, created.WeeklyUsageUSD)
	require.Equal(t, 0.0, created.MonthlyUsageUSD)
	require.Equal(t, "new", created.Notes)
	require.Equal(t, 1, subRepo.CreateCalls)
}

// TestCheckAndResetWindows_DailyCardDoesNotResetDailyUsage supplies a one-day
// lifetime with old daily anchor and usage. Maintenance must leave daily reset
// uncalled and keep usage, exercising the lifetime-based one-time rule without
// a configured daily limit.
func TestCheckAndResetWindows_DailyCardDoesNotResetDailyUsage(t *testing.T) {
	now := time.Now().UTC()
	startsAt := now.Add(-23 * time.Hour)
	dailyWindowStart := now.Add(-25 * time.Hour)
	repo := &dailyResetTrackingUserSubRepo{}
	svc := newSubscriptionServiceForTest(repo)
	sub := &billing.UserSubscription{
		ID:               1,
		UserID:           10,
		PlanID:           20,
		StartsAt:         startsAt,
		ExpiresAt:        startsAt.Add(24 * time.Hour),
		DailyUsageUSD:    10,
		DailyWindowStart: &dailyWindowStart,
	}

	err := svc.CheckAndResetWindows(context.Background(), sub)

	require.NoError(t, err)
	require.False(t, repo.resetDailyCalled, "日卡作为一次性配额，过了 24 小时日窗口也不应重置 daily usage")
	require.Equal(t, 10.0, sub.DailyUsageUSD)
}

// TestCheckAndResetWindows_MultiDaySubscriptionStillResetsDailyUsage supplies
// a multiday lifetime and an older-than-day anchor. The fixture must record a
// daily reset and the supplied snapshot counter must become zero.
func TestCheckAndResetWindows_MultiDaySubscriptionStillResetsDailyUsage(t *testing.T) {
	now := time.Now().UTC()
	startsAt := now.Add(-48 * time.Hour)
	dailyWindowStart := now.Add(-25 * time.Hour)
	repo := &dailyResetTrackingUserSubRepo{}
	svc := newSubscriptionServiceForTest(repo)
	sub := &billing.UserSubscription{
		ID:               1,
		UserID:           10,
		PlanID:           20,
		StartsAt:         startsAt,
		ExpiresAt:        startsAt.AddDate(0, 0, 4),
		DailyUsageUSD:    10,
		DailyWindowStart: &dailyWindowStart,
	}

	err := svc.CheckAndResetWindows(context.Background(), sub)

	require.NoError(t, err)
	require.True(t, repo.resetDailyCalled, "多日订阅仍应重置过期 daily window")
	require.Equal(t, 0.0, sub.DailyUsageUSD)
}

// TestCheckAndResetWindows_LegacyDailyAnchorHealsToMidnight supplies a
// previous-day nonmidnight anchor and an explicit five-minutes-after-midnight
// observation. Reset must write the UTC calendar midnight and clear the local
// daily usage.
func TestCheckAndResetWindows_LegacyDailyAnchorHealsToMidnight(t *testing.T) {
	base := timezone.NewCalendar(time.UTC).
		StartOfDay(time.Now().UTC())
	legacyWindowStart := base.AddDate(0, 0, -1).Add(16*time.Hour + 49*time.Minute)
	// Observe the next calendar day after only seven hours and sixteen minutes,
	// so a twenty-four-hour elapsed check would leave this legacy anchor
	// untouched.
	now := base.Add(5 * time.Minute)
	repo := &dailyResetTrackingUserSubRepo{}
	svc := newSubscriptionServiceForTest(repo)
	sub := &billing.UserSubscription{
		ID:               1,
		UserID:           10,
		PlanID:           20,
		StartsAt:         base.AddDate(0, 0, -3),
		ExpiresAt:        base.AddDate(0, 0, 10),
		DailyUsageUSD:    10,
		DailyWindowStart: &legacyWindowStart,
	}

	err := svc.CheckAndResetWindowsAt(context.Background(), sub, now)

	require.NoError(t, err)
	require.True(t, repo.resetDailyCalled, "跨零点后应重置旧的非零点日窗口")
	require.Equal(t, base, repo.lastDailyStart, "写回的日窗口起点应为当天零点")
	require.Equal(t, base, *sub.DailyWindowStart)
	require.Zero(t, sub.DailyUsageUSD)
}

// TestCheckAndResetWindows_ExpiryTailDoesNotResetMonthlyUsage supplies fixed
// monthly lifetime and anchor dates without a configured limit. It requires no
// monthly reset and retained usage; those dates can also be elapsed at
// execution, so no live-tail-only guarantee is asserted.
func TestCheckAndResetWindows_ExpiryTailDoesNotResetMonthlyUsage(t *testing.T) {
	start := time.Date(2026, 4, 30, 8, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2026, 5, 30, 8, 0, 0, 0, time.UTC)
	monthlyWindowStart := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)
	repo := &dailyResetTrackingUserSubRepo{}
	svc := newSubscriptionServiceForTest(repo)
	sub := &billing.UserSubscription{
		ID:                 1,
		UserID:             10,
		PlanID:             20,
		StartsAt:           start,
		ExpiresAt:          expiresAt,
		MonthlyUsageUSD:    10,
		MonthlyWindowStart: &monthlyWindowStart,
	}

	err := svc.CheckAndResetWindows(context.Background(), sub)

	require.NoError(t, err)
	require.False(t, repo.resetMonthlyCalled, "到期尾段不应重置 monthly usage")
	require.Equal(t, 10.0, sub.MonthlyUsageUSD)
}

// TestValidateAndCheckLimits_ExpiryTailMissingWindowDoesNotNeedActivation
// supplies a still-active grant with positive monthly quota and less than a
// full monthly period remaining. Precheck must succeed without requesting
// maintenance for the missing anchor.
func TestValidateAndCheckLimits_ExpiryTailMissingWindowDoesNotNeedActivation(t *testing.T) {
	now := time.Now().UTC()
	monthlyLimit := 100.0
	sub := &billing.UserSubscription{
		Status:          billing.SubscriptionStatusActive,
		StartsAt:        now.AddDate(0, 0, -29),
		ExpiresAt:       now.Add(2 * time.Hour),
		MonthlyLimitUSD: &monthlyLimit,
		MonthlyUsageUSD: 90,
	}
	svc := newSubscriptionServiceForTest(billingtestkit.SubscriptionRepositoryNoop{})

	needsMaintenance, err := svc.ValidateAndCheckLimits(sub)

	require.NoError(t, err)
	require.False(t, needsMaintenance, "到期尾段不足完整月窗口时不应激活空窗口")
}

// TestDoWindowMaintenance_ExpiryTailMissingWindowDoesNotActivate supplies a
// short remaining lifetime with a missing monthly anchor and positive quota.
// Best-effort maintenance must leave activation uncalled, protecting
// insufficient lifetime for a full outer period.
func TestDoWindowMaintenance_ExpiryTailMissingWindowDoesNotActivate(t *testing.T) {
	now := time.Now().UTC()
	monthlyLimit := 100.0
	repo := &dailyResetTrackingUserSubRepo{}
	svc := newSubscriptionServiceForTest(repo)
	sub := &billing.UserSubscription{
		ID:              1,
		Status:          billing.SubscriptionStatusActive,
		StartsAt:        now.AddDate(0, 0, -29),
		ExpiresAt:       now.Add(2 * time.Hour),
		MonthlyLimitUSD: &monthlyLimit,
		MonthlyUsageUSD: 90,
	}

	svc.DoWindowMaintenance(sub)

	require.False(t, repo.activateCalled, "到期尾段不足完整月窗口时不应写入新的窗口起点")
}

// TestDoWindowMaintenance_MissingDailyCardWindowStillActivates supplies a
// two-hour lifetime with a positive daily quota and no anchor. Maintenance
// must request daily activation alone under the one-time daily rule, leaving
// weekly and monthly flags false.
func TestDoWindowMaintenance_MissingDailyCardWindowStillActivates(t *testing.T) {
	now := time.Now().UTC()
	dailyLimit := 10.0
	repo := &dailyResetTrackingUserSubRepo{}
	svc := newSubscriptionServiceForTest(repo)
	sub := &billing.UserSubscription{
		ID:            1,
		Status:        billing.SubscriptionStatusActive,
		StartsAt:      now.Add(-time.Hour),
		ExpiresAt:     now.Add(time.Hour),
		DailyLimitUSD: &dailyLimit,
	}

	svc.DoWindowMaintenance(sub)

	require.True(t, repo.activateCalled, "一次性日额度首次使用仍应激活窗口")
	require.True(t, repo.lastActivation.Daily)
	require.False(t, repo.lastActivation.Weekly)
	require.False(t, repo.lastActivation.Monthly)
}

// TestValidateAndCheckLimits_DailyCardDoesNotAllowSecondQuotaAfterMidnight
// supplies an old anchor and over-limit usage on a one-day grant. Precheck
// must retain that usage, report the daily limit error and require no reset
// maintenance.
func TestValidateAndCheckLimits_DailyCardDoesNotAllowSecondQuotaAfterMidnight(t *testing.T) {
	start := time.Now().UTC().Add(-23 * time.Hour)
	dailyWindowStart := time.Now().UTC().Add(-25 * time.Hour)
	dailyLimit := 10.0
	sub := &billing.UserSubscription{
		Status:           billing.SubscriptionStatusActive,
		StartsAt:         start,
		ExpiresAt:        start.Add(24 * time.Hour),
		DailyLimitUSD:    &dailyLimit,
		DailyWindowStart: &dailyWindowStart,
		DailyUsageUSD:    dailyLimit + 0.01,
	}
	svc := newSubscriptionServiceForTest(billingtestkit.SubscriptionRepositoryNoop{})

	needsMaintenance, err := svc.ValidateAndCheckLimits(sub)

	require.False(t, needsMaintenance, "日卡跨过日窗口后不应触发 daily reset 维护")
	require.True(t, errors.Is(err, billing.ErrDailyLimitExceeded))
	require.Equal(t, dailyLimit+0.01, sub.DailyUsageUSD, "热路径不应清零日卡已用额度")
}

// TestRevokeOwnExhaustedSubscriptionRejectsNonEligibleSubscriptions changes
// one fixture's ownership, future state, deletion or quota eligibility across
// supplied cases. Each expected error must occur while its target remains in
// the fixture map.
func TestRevokeOwnExhaustedSubscriptionRejectsNonEligibleSubscriptions(t *testing.T) {
	for _, tt := range []struct {
		name string // self-revoke eligibility case label
		user int64  // requesting fixture user identity
		// mutate optionally changes the fresh grant before Seed. Nil retains the
		// base unlimited fixture, while each supplied callback programs that case's
		// eligibility fields.
		mutate func(*billing.UserSubscription) // optional pre-seed grant mutation
		want   error                           // expected self-revoke error category
	}{
		{name: "foreign subscription", user: 99, want: billing.ErrSubscriptionNotFound},
		{name: "inactive subscription", user: 7, mutate: func(sub *billing.UserSubscription) {
			sub.Status = billing.SubscriptionStatusPending
			sub.StartsAt = time.Now().UTC().Add(time.Hour)
		}, want: billing.ErrSubscriptionNotActive},
		{name: "already revoked subscription", user: 7, mutate: func(sub *billing.UserSubscription) {
			revokedAt := time.Now().UTC().Add(-time.Minute)
			sub.DeletedAt = &revokedAt
		}, want: billing.ErrSubscriptionNotActive},
		{name: "quota still available", user: 7, mutate: func(sub *billing.UserSubscription) {
			sub.MonthlyLimitUSD = quotaPointer(100)
			sub.MonthlyUsageUSD = 99
		}, want: billing.ErrSubscriptionQuotaAvailable},
		{name: "unlimited subscription", user: 7, want: billing.ErrSubscriptionQuotaAvailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &revokeSubscriptionRepoStub{SubscriptionRepository: billingtestkit.NewSubscriptionRepository()}
			sub := revokeSubscriptionFixture()
			if tt.mutate != nil {
				tt.mutate(sub)
			}
			repo.Seed(sub)
			svc := newSubscriptionServiceForTest(repo)

			_, err := svc.RevokeOwnExhaustedSubscription(context.Background(), tt.user, sub.ID)
			require.ErrorIs(t, err, tt.want)
			require.Contains(t, repo.ByID, sub.ID)
		})
	}
}

// TestRevokeOwnExhaustedSubscriptionAdvancesQueuedPack supplies an exhausted
// active grant followed by two pending slots. Revocation must remove the
// target, choose the first successor and shift that successor near now while
// keeping later adjacency.
func TestRevokeOwnExhaustedSubscriptionAdvancesQueuedPack(t *testing.T) {
	repo := &revokeSubscriptionRepoStub{SubscriptionRepository: billingtestkit.NewSubscriptionRepository()}
	active := revokeSubscriptionFixture()
	active.MonthlyLimitUSD = quotaPointer(10)
	active.MonthlyUsageUSD = 10
	pending := revokeSubscriptionFixture()
	pending.ID = 2
	pending.StartsAt = active.ExpiresAt
	pending.ExpiresAt = active.ExpiresAt.Add(24 * time.Hour)
	pending.Status = billing.SubscriptionStatusPending
	pending.MonthlyLimitUSD = active.MonthlyLimitUSD
	later := revokeSubscriptionFixture()
	later.ID = 3
	later.StartsAt = pending.ExpiresAt
	later.ExpiresAt = pending.ExpiresAt.Add(24 * time.Hour)
	later.Status = billing.SubscriptionStatusPending
	later.MonthlyLimitUSD = active.MonthlyLimitUSD
	repo.Seed(active)
	repo.Seed(pending)
	repo.Seed(later)
	svc := newSubscriptionServiceForTest(repo)

	result, err := svc.RevokeOwnExhaustedSubscription(context.Background(), active.UserID, active.ID)
	require.NoError(t, err)
	require.Equal(t, active.ID, result.RevokedSubscriptionID)
	require.NotNil(t, result.ReplacementSubscriptionID)
	require.Equal(t, pending.ID, *result.ReplacementSubscriptionID)
	require.NotContains(t, repo.ByID, active.ID)

	advanced := repo.ByID[pending.ID]
	require.NotNil(t, advanced)
	require.Equal(t, billing.SubscriptionStatusActive, advanced.Status)
	// Allow two seconds between revoke's service-clock observation and this
	// assertion. Separate exact endpoint equality still checks the moved
	// siblings' adjacency.
	require.WithinDuration(t, time.Now().UTC(), advanced.StartsAt, 2*time.Second)
	advancedLater := repo.ByID[later.ID]
	require.NotNil(t, advancedLater)
	require.Equal(t, billing.SubscriptionStatusPending, advancedLater.Status)
	require.Equal(t, advanced.ExpiresAt, advancedLater.StartsAt)
}

// TestRevokeOwnExhaustedSubscriptionRechecksResetQuota supplies exhausted
// monthly use with an eligible stale anchor and long remaining lifetime.
// Stored fixture reset must clear usage, and self-revoke must reread that
// available quota and keep the target.
func TestRevokeOwnExhaustedSubscriptionRechecksResetQuota(t *testing.T) {
	repo := &resettingRevokeSubscriptionRepoStub{
		revokeSubscriptionRepoStub: &revokeSubscriptionRepoStub{SubscriptionRepository: billingtestkit.NewSubscriptionRepository()},
	}
	sub := revokeSubscriptionFixture()
	sub.ExpiresAt = time.Now().UTC().Add(60 * 24 * time.Hour)
	sub.MonthlyLimitUSD = quotaPointer(10)
	sub.MonthlyUsageUSD = 10
	windowStart := time.Now().UTC().Add(-31 * 24 * time.Hour)
	sub.MonthlyWindowStart = &windowStart
	repo.Seed(sub)
	svc := newSubscriptionServiceForTest(repo)

	_, err := svc.RevokeOwnExhaustedSubscription(context.Background(), sub.UserID, sub.ID)
	require.ErrorIs(t, err, billing.ErrSubscriptionQuotaAvailable)
	require.Contains(t, repo.ByID, sub.ID)
	require.Zero(t, repo.ByID[sub.ID].MonthlyUsageUSD)
}

// newTestSubscriptionService constructs a UTC-clock service with omitted
// repository and mutation dependencies for pure progress scenarios. It starts
// no worker; repository or entitlement methods need separately configured
// fixtures.
func newTestSubscriptionService() *billing.SubscriptionService {
	return billing.NewSubscriptionService(nil, nil, nil, subscriptionClockFixture())
}

// ptrFloat64 returns independently addressed quota fixture storage for the
// supplied number.
func ptrFloat64(v float64) *float64 { return &v }

// GetByIDLite panics when a scenario unexpectedly resolves a group name. These
// tests supply repository-selected grants or pure progress data; their group
// fixture performs no lookup and accepts no supported identity.
func (subscriptionSelectionGroupFixture) GetByIDLite(context.Context, int64) (*billing.SubscriptionPlanGroup, error) {
	panic("unexpected GetByIDLite call")
}

// newSubscriptionServiceForTest retains the supplied grant repository with a
// UTC runtime and a nil-client PostgreSQL mutation adapter. Without a caller
// transaction the adapter runs direct fixture mutations, with no database
// locks or recovery.
func newSubscriptionServiceForTest(repo billing.UserSubscriptionRepository) *billing.SubscriptionService {
	return billing.NewSubscriptionService(subscriptionSelectionGroupFixture{}, repo, billingpostgres.NewSubscriptionMutations(nil), subscriptionClockFixture())
}

// subscriptionClockFixture supplies a UTC calendar and a fresh system-time UTC
// observation callback. Calendar is retained through its returned pointer,
// while each Now call observes time independently for the tested service.
func subscriptionClockFixture() billing.DateRuntime {
	calendar := timezone.NewCalendar(time.UTC)
	return billing.DateRuntime{Now: func() time.Time { return time.Now().UTC() }, Calendar: &calendar}
}

// GetByID returns a shallow copy of the one retained matching grant or
// not-found for nil/mismatched input. It ignores context; copied optional
// pointers and loaded plan data remain shared with fixture state.
func (r *resetQuotaUserSubRepoStub) GetByID(_ context.Context, id int64) (*billing.UserSubscription, error) {
	if r.sub == nil || r.sub.ID != id {
		return nil, billing.ErrSubscriptionNotFound
	}
	cp := *r.sub
	return &cp, nil
}

// ResetUsageWindows records all supplied reset flags before checking
// programmed failures in daily, weekly then monthly order. It ignores context
// and ID; nil retained grant succeeds after error checks. Any selected error
// stops before counter mutation.
//
// Successful selected periods clear usage and share the supplied anchor
// pointer across their fields. Flags therefore record attempted selection even
// when no reset occurs, and this method does not simulate transaction
// rollback. Tests configure their retained grant before the call and inspect
// it after synchronous completion.
func (r *resetQuotaUserSubRepoStub) ResetUsageWindows(_ context.Context, _ int64, resetDaily, resetWeekly, resetMonthly bool, windowStart time.Time) error {
	// Capture the whole requested selection before failure injection: a daily
	// error can leave weekly selected too, while all retained counters and
	// anchors remain unwritten.
	r.resetDailyCalled = resetDaily
	r.resetWeeklyCalled = resetWeekly
	r.resetMonthlyCalled = resetMonthly
	if resetDaily && r.resetDailyErr != nil {
		return r.resetDailyErr
	}
	if resetWeekly && r.resetWeeklyErr != nil {
		return r.resetWeeklyErr
	}
	if resetMonthly && r.resetMonthlyErr != nil {
		return r.resetMonthlyErr
	}
	if r.sub == nil {
		return nil
	}
	if resetDaily {
		r.sub.DailyUsageUSD = 0
		r.sub.DailyWindowStart = &windowStart
	}
	if resetWeekly {
		r.sub.WeeklyUsageUSD = 0
		r.sub.WeeklyWindowStart = &windowStart
	}
	if resetMonthly {
		r.sub.MonthlyUsageUSD = 0
		r.sub.MonthlyWindowStart = &windowStart
	}
	return nil
}

// ResetDailyUsage records its flag and returns the programmed daily error. On
// success with a retained grant it clears daily use and replaces its anchor,
// ignoring context, ID and expected-anchor comparison.
func (r *resetQuotaUserSubRepoStub) ResetDailyUsage(_ context.Context, _ int64, _ *time.Time, windowStart time.Time) error {
	r.resetDailyCalled = true
	if r.resetDailyErr == nil && r.sub != nil {
		r.sub.DailyUsageUSD = 0
		r.sub.DailyWindowStart = &windowStart
	}
	return r.resetDailyErr
}

// ResetWeeklyUsage records its flag and returns the programmed weekly error
// unchanged. Context, identity, expected anchor and new anchor are ignored;
// this override leaves retained weekly counters untouched.
func (r *resetQuotaUserSubRepoStub) ResetWeeklyUsage(_ context.Context, _ int64, _ *time.Time, _ time.Time) error {
	r.resetWeeklyCalled = true
	return r.resetWeeklyErr
}

// ResetMonthlyUsage records its flag and returns the programmed monthly error
// unchanged. Context, identity, expected anchor and replacement start are
// ignored; this override leaves retained monthly counters untouched.
func (r *resetQuotaUserSubRepoStub) ResetMonthlyUsage(_ context.Context, _ int64, _ *time.Time, _ time.Time) error {
	r.resetMonthlyCalled = true
	return r.resetMonthlyErr
}

// newResetQuotaSvc constructs the standard UTC subscription fixture service
// using the supplied reset repository. The inherited mutation adapter has no
// database client, so ordinary calls use direct in-memory behavior.
func newResetQuotaSvc(stub *resetQuotaUserSubRepoStub) *billing.SubscriptionService {
	return newSubscriptionServiceForTest(stub)
}

// billingEligibilityLimitPtr returns independently addressed numeric storage
// for a supplied eligibility quota.
func billingEligibilityLimitPtr(v float64) *float64 {
	return &v
}

// ExtendExpiry records caller context before looking up the retained grant.
// Missing IDs return not-found; success mutates its expiry directly, with no
// database write, soft-delete filter or transaction-based map recovery.
func (r *transactionTrackingUserSubRepo) ExtendExpiry(ctx context.Context, subscriptionID int64, newExpiresAt time.Time) error {
	r.writeContexts = append(r.writeContexts, ctx)
	sub := r.ByID[subscriptionID]
	if sub == nil {
		return billing.ErrSubscriptionNotFound
	}
	sub.ExpiresAt = newExpiresAt
	return nil
}

// UpdateStatus records caller context before looking up the retained grant.
// Missing IDs return not-found; success replaces stored fixture status
// directly, without a database write or effective-time normalization.
func (r *transactionTrackingUserSubRepo) UpdateStatus(ctx context.Context, subscriptionID int64, status string) error {
	r.writeContexts = append(r.writeContexts, ctx)
	sub := r.ByID[subscriptionID]
	if sub == nil {
		return billing.ErrSubscriptionNotFound
	}
	sub.Status = status
	return nil
}

// Delete records caller context, removes the map entry and rebuilds the
// fixture index before returning success. A missing ID also succeeds, and the
// attempt remains in writeContexts. Rebuilding follows removal so promoted
// timeline reads use the new retained identity set.
//
// These map changes are direct and have no Ent transaction rollback mechanism.
// Tests inspect binding in the recorded context, not persisted deletion or
// restoration after cleanup. Callers initialize the backing fixture and
// inspect its unsynchronized state after the synchronous service operation.
func (r *transactionTrackingUserSubRepo) Delete(ctx context.Context, subscriptionID int64) error {
	r.writeContexts = append(r.writeContexts, ctx)
	// Remove the retained map entry before rebuilding the fixture index, so
	// subsequent timeline lookups stop returning this physically deleted test
	// row.
	delete(r.ByID, subscriptionID)
	r.RebuildIndex()
	return nil
}

// LockSubscription asserts that context carries this fixture's expected Ent
// transaction, then returns nil. It ignores subscription identity and issues
// no row-lock query, letting the scenario inspect binding separately from
// PostgreSQL lock behavior.
func (s *subscriptionContextTransactions) LockSubscription(ctx context.Context, _ int64) error {
	require.Same(s.t, s.tx, dbent.TxFromContext(ctx))
	return nil
}

// ActivateWindows records invocation and the supplied activation flags, then
// succeeds. It ignores context, grant identity and anchor, leaving persistence
// and counter changes outside this call-tracking fixture.
func (r *dailyResetTrackingUserSubRepo) ActivateWindows(_ context.Context, _ int64, _ time.Time, activation billing.SubscriptionWindowActivation) error {
	r.activateCalled = true
	r.lastActivation = activation
	return nil
}

// ResetDailyUsage records invocation and replacement daily anchor, then
// succeeds. Context, grant identity and expected anchor are ignored; the
// service updates its supplied snapshot while this fixture stores no counters.
func (r *dailyResetTrackingUserSubRepo) ResetDailyUsage(_ context.Context, _ int64, _ *time.Time, windowStart time.Time) error {
	r.resetDailyCalled = true
	r.lastDailyStart = windowStart
	return nil
}

// ResetWeeklyUsage records only its invocation and succeeds for any supplied
// context or anchors. It writes no grant counters; the tested service owns the
// observable local snapshot changes after this return.
func (r *dailyResetTrackingUserSubRepo) ResetWeeklyUsage(context.Context, int64, *time.Time, time.Time) error {
	r.resetWeeklyCalled = true
	return nil
}

// ResetMonthlyUsage records only its invocation and succeeds for any supplied
// context or anchors. It writes no grant counters; the tested service owns the
// observable local snapshot changes after this return.
func (r *dailyResetTrackingUserSubRepo) ResetMonthlyUsage(context.Context, int64, *time.Time, time.Time) error {
	r.resetMonthlyCalled = true
	return nil
}

// Delete removes a present retained grant, then rebuilds the index used by
// promoted timeline readers. A missing identity returns not-found before
// either step. Removing the map row first keeps the next index rebuild from
// retaining that deleted target.
//
// Context is ignored and no transaction coordinates these fixture changes. The
// operation is physical in-memory removal, whereas the ordinary PostgreSQL
// repository retains a deletion timestamp on its row. Tests initialize the
// backing map and inspect it after synchronous revocation.
func (r *revokeSubscriptionRepoStub) Delete(_ context.Context, id int64) error {
	if _, ok := r.ByID[id]; !ok {
		return billing.ErrSubscriptionNotFound
	}
	// Remove the existing retained row before rebuilding indexes, allowing later
	// chain reads to observe the fixture's physical deletion independently of
	// database soft-deletion behavior.
	delete(r.ByID, id)
	r.RebuildIndex()
	return nil
}

// ResetMonthlyUsage clears the retained grant's monthly counter and replaces
// its anchor before a later lookup. Missing IDs return not-found; context and
// expected anchor are ignored, so no conditional SQL comparison occurs.
func (r *resettingRevokeSubscriptionRepoStub) ResetMonthlyUsage(_ context.Context, id int64, _ *time.Time, newWindowStart time.Time) error {
	sub := r.ByID[id]
	if sub == nil {
		return billing.ErrSubscriptionNotFound
	}
	sub.MonthlyUsageUSD = 0
	sub.MonthlyWindowStart = &newWindowStart
	return nil
}
