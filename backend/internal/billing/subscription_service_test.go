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

// subscriptionSelectionGroupFixture rejects unexpected group lookups in subscription selection tests.
type subscriptionSelectionGroupFixture struct{}

// resetQuotaUserSubRepoStub implements GetByID and ResetUsageWindows.
// Other methods panic through the embedded SubscriptionRepositoryNoop.
type resetQuotaUserSubRepoStub struct {
	billingtestkit.SubscriptionRepositoryNoop

	sub *billing.UserSubscription

	resetDailyCalled   bool
	resetWeeklyCalled  bool
	resetMonthlyCalled bool
	resetDailyErr      error
	resetWeeklyErr     error
	resetMonthlyErr    error
}

// transactionTrackingUserSubRepo records the contexts used for subscription writes.
type transactionTrackingUserSubRepo struct {
	*billingtestkit.SubscriptionRepository
	writeContexts []context.Context
}

// subscriptionContextTransactions checks transaction reuse with SQLite; PostgreSQL integration tests cover locking and rollback.
type subscriptionContextTransactions struct {
	*billingpostgres.SubscriptionMutations
	t  *testing.T
	tx *dbent.Tx
}

type dailyResetTrackingUserSubRepo struct {
	billingtestkit.SubscriptionRepositoryNoop

	resetDailyCalled   bool
	resetWeeklyCalled  bool
	resetMonthlyCalled bool
	activateCalled     bool
	lastActivation     billing.SubscriptionWindowActivation
	lastDailyStart     time.Time
}

type revokeSubscriptionRepoStub struct {
	*billingtestkit.SubscriptionRepository
}

type resettingRevokeSubscriptionRepoStub struct {
	*revokeSubscriptionRepoStub
}

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
	require.WithinDuration(t, time.Now().UTC(), created.StartsAt, 2*time.Second)
	require.Equal(t, created.StartsAt.AddDate(0, 0, 7), created.ExpiresAt)
}

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

func TestTargetSubscriptionExpiresAt_ActiveSubscriptionCountsFromNow(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	active := &billing.UserSubscription{
		StartsAt:  now.AddDate(0, 0, -11),
		ExpiresAt: now.AddDate(0, 0, 19),
		Status:    billing.SubscriptionStatusActive,
	}

	require.Equal(t, now.AddDate(0, 0, 30), billing.TargetSubscriptionExpiresAt(active, now, 30))
}

func TestTargetSubscriptionExpiresAt_PendingSubscriptionCountsFromStartsAt(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	pending := &billing.UserSubscription{
		StartsAt:  now.AddDate(0, 0, 7),
		ExpiresAt: now.AddDate(0, 0, 37),
		Status:    billing.SubscriptionStatusPending,
	}

	require.Equal(t, pending.StartsAt.AddDate(0, 0, 15), billing.TargetSubscriptionExpiresAt(pending, now, 15))
}

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

func TestNormalizeAssignValidityDays(t *testing.T) {
	require.Equal(t, 30, billing.NormalizeAssignValidityDays(0))
	require.Equal(t, 30, billing.NormalizeAssignValidityDays(-5))
	require.Equal(t, billing.MaxValidityDays, billing.NormalizeAssignValidityDays(billing.MaxValidityDays+100))
	require.Equal(t, 7, billing.NormalizeAssignValidityDays(7))
}

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

func TestCalculateProgress_NoWindowStart_NoProgress(t *testing.T) {
	svc := newTestSubscriptionService()
	now := time.Now().UTC()

	// Finite quotas have no window start until activation.
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

func TestCalculateProgress_ExpiredSubscription(t *testing.T) {
	svc := newTestSubscriptionService()

	sub := &billing.UserSubscription{
		ID:        1,
		ExpiresAt: time.Now().UTC().Add(-24 * time.Hour), // Expired.
	}

	progress := svc.CalculateProgress(sub)

	assert.Equal(t, 0, progress.ExpiresInDays, "过期订阅的剩余天数应为 0")
}

func TestCalculateProgress_ResetsInSeconds_NotNegative(t *testing.T) {
	svc := newTestSubscriptionService()
	// Place the reset deadline in the past to check that the countdown stays nonnegative.
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

func TestAdminResetQuota_SubscriptionNotFound(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{sub: nil}
	svc := newResetQuotaSvc(stub)

	_, err := svc.AdminResetQuota(context.Background(), 999, true, true, true)

	require.ErrorIs(t, err, billing.ErrSubscriptionNotFound)
	require.False(t, stub.resetDailyCalled)
	require.False(t, stub.resetWeeklyCalled)
	require.False(t, stub.resetMonthlyCalled)
}

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
	// ResetUsageWindows clears daily usage in the stub.
	// The service rereads that value with GetByID before returning.
	require.Equal(t, float64(0), result.DailyUsageUSD, "返回的订阅应反映已归零的用量")
	require.True(t, stub.resetDailyCalled)
}

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

func TestExtendSubscriptionReusesCallerTransaction(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
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

func TestRevokeSubscriptionReusesCallerTransaction(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
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

func TestCheckAndResetWindows_LegacyDailyAnchorHealsToMidnight(t *testing.T) {
	base := timezone.NewCalendar(time.UTC).
		StartOfDay(time.Now().UTC())
	legacyWindowStart := base.AddDate(0, 0, -1).Add(16*time.Hour + 49*time.Minute)
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

func TestRevokeOwnExhaustedSubscriptionRejectsNonEligibleSubscriptions(t *testing.T) {
	for _, tt := range []struct {
		name   string
		user   int64
		mutate func(*billing.UserSubscription)
		want   error
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
	require.WithinDuration(t, time.Now().UTC(), advanced.StartsAt, 2*time.Second)
	advancedLater := repo.ByID[later.ID]
	require.NotNil(t, advancedLater)
	require.Equal(t, billing.SubscriptionStatusPending, advancedLater.Status)
	require.Equal(t, advanced.ExpiresAt, advancedLater.StartsAt)
}

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

func newTestSubscriptionService() *billing.SubscriptionService {
	return billing.NewSubscriptionService(nil, nil, nil, subscriptionClockFixture())
}

func ptrFloat64(v float64) *float64 { return &v }

func (subscriptionSelectionGroupFixture) GetByIDLite(context.Context, int64) (*billing.SubscriptionPlanGroup, error) {
	panic("unexpected GetByIDLite call")
}

// newSubscriptionServiceForTest pairs the repository with a transaction adapter that has no database connection.
func newSubscriptionServiceForTest(repo billing.UserSubscriptionRepository) *billing.SubscriptionService {
	return billing.NewSubscriptionService(subscriptionSelectionGroupFixture{}, repo, billingpostgres.NewSubscriptionMutations(nil), subscriptionClockFixture())
}

// subscriptionClockFixture returns a UTC clock and calendar.
func subscriptionClockFixture() billing.DateRuntime {
	calendar := timezone.NewCalendar(time.UTC)
	return billing.DateRuntime{Now: func() time.Time { return time.Now().UTC() }, Calendar: &calendar}
}

func (r *resetQuotaUserSubRepoStub) GetByID(_ context.Context, id int64) (*billing.UserSubscription, error) {
	if r.sub == nil || r.sub.ID != id {
		return nil, billing.ErrSubscriptionNotFound
	}
	cp := *r.sub
	return &cp, nil
}

func (r *resetQuotaUserSubRepoStub) ResetUsageWindows(_ context.Context, _ int64, resetDaily, resetWeekly, resetMonthly bool, windowStart time.Time) error {
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

func (r *resetQuotaUserSubRepoStub) ResetDailyUsage(_ context.Context, _ int64, _ *time.Time, windowStart time.Time) error {
	r.resetDailyCalled = true
	if r.resetDailyErr == nil && r.sub != nil {
		r.sub.DailyUsageUSD = 0
		r.sub.DailyWindowStart = &windowStart
	}
	return r.resetDailyErr
}

func (r *resetQuotaUserSubRepoStub) ResetWeeklyUsage(_ context.Context, _ int64, _ *time.Time, _ time.Time) error {
	r.resetWeeklyCalled = true
	return r.resetWeeklyErr
}

func (r *resetQuotaUserSubRepoStub) ResetMonthlyUsage(_ context.Context, _ int64, _ *time.Time, _ time.Time) error {
	r.resetMonthlyCalled = true
	return r.resetMonthlyErr
}

func newResetQuotaSvc(stub *resetQuotaUserSubRepoStub) *billing.SubscriptionService {
	return newSubscriptionServiceForTest(stub)
}

func billingEligibilityLimitPtr(v float64) *float64 {
	return &v
}

func (r *transactionTrackingUserSubRepo) ExtendExpiry(ctx context.Context, subscriptionID int64, newExpiresAt time.Time) error {
	r.writeContexts = append(r.writeContexts, ctx)
	sub := r.ByID[subscriptionID]
	if sub == nil {
		return billing.ErrSubscriptionNotFound
	}
	sub.ExpiresAt = newExpiresAt
	return nil
}

func (r *transactionTrackingUserSubRepo) UpdateStatus(ctx context.Context, subscriptionID int64, status string) error {
	r.writeContexts = append(r.writeContexts, ctx)
	sub := r.ByID[subscriptionID]
	if sub == nil {
		return billing.ErrSubscriptionNotFound
	}
	sub.Status = status
	return nil
}

func (r *transactionTrackingUserSubRepo) Delete(ctx context.Context, subscriptionID int64) error {
	r.writeContexts = append(r.writeContexts, ctx)
	delete(r.ByID, subscriptionID)
	r.RebuildIndex()
	return nil
}

func (s *subscriptionContextTransactions) LockSubscription(ctx context.Context, _ int64) error {
	require.Same(s.t, s.tx, dbent.TxFromContext(ctx))
	return nil
}

func (r *dailyResetTrackingUserSubRepo) ActivateWindows(_ context.Context, _ int64, _ time.Time, activation billing.SubscriptionWindowActivation) error {
	r.activateCalled = true
	r.lastActivation = activation
	return nil
}

func (r *dailyResetTrackingUserSubRepo) ResetDailyUsage(_ context.Context, _ int64, _ *time.Time, windowStart time.Time) error {
	r.resetDailyCalled = true
	r.lastDailyStart = windowStart
	return nil
}

func (r *dailyResetTrackingUserSubRepo) ResetWeeklyUsage(context.Context, int64, *time.Time, time.Time) error {
	r.resetWeeklyCalled = true
	return nil
}

func (r *dailyResetTrackingUserSubRepo) ResetMonthlyUsage(context.Context, int64, *time.Time, time.Time) error {
	r.resetMonthlyCalled = true
	return nil
}

func (r *revokeSubscriptionRepoStub) Delete(_ context.Context, id int64) error {
	if _, ok := r.ByID[id]; !ok {
		return billing.ErrSubscriptionNotFound
	}
	delete(r.ByID, id)
	r.RebuildIndex()
	return nil
}

func (r *resettingRevokeSubscriptionRepoStub) ResetMonthlyUsage(_ context.Context, id int64, _ *time.Time, newWindowStart time.Time) error {
	sub := r.ByID[id]
	if sub == nil {
		return billing.ErrSubscriptionNotFound
	}
	sub.MonthlyUsageUSD = 0
	sub.MonthlyWindowStart = &newWindowStart
	return nil
}
