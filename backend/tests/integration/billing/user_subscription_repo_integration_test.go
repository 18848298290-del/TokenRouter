//go:build integration

package billing_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Subscription console lists can present deleted grants as revoked while an
// include-deleted detail read retains the stored status. PostgreSQL suite cases
// inspect that distinction alongside effective-date filtering, plan coverage,
// relation loading, source-order lookup, restoration, and spending-window
// mutation through transaction-bound fixtures.

// UserSubscriptionRepoSuite binds subscription storage and fixture builders to
// one case transaction.
type UserSubscriptionRepoSuite struct {
	suite.Suite // assertion and suite lifecycle support
	// ctx, client, and repo are replaced together before each case. Ent
	// builders and subscription reads share the transaction later rolled
	// back by entitlementTx cleanup.
	ctx    context.Context // case execution context
	client *dbent.Client   // case transaction client
	// transaction-bound subscription repository fixture
	repo *billingpostgres.SubscriptionStore
}

// SetupTest replaces the suite context, client, and subscription repository
// with a new rollback transaction. Ent fixture builders and repository
// operations then observe the same case-local writes.
func (s *UserSubscriptionRepoSuite) SetupTest() {
	s.ctx = context.Background()
	tx := entitlementTx(s.T())
	s.client = tx.Client()
	s.repo = billingpostgres.NewUserSubscriptionRepository(s.client)
}

// TestUserSubscriptionRepoSuite runs PostgreSQL subscription storage cases with
// per-case rollback fixtures. Its cases inspect loaded relations, status views,
// filtering, restoration, and selected usage-window writes.
func TestUserSubscriptionRepoSuite(t *testing.T) {
	suite.Run(t, new(UserSubscriptionRepoSuite))
}

// mustCreateUser inserts an active fixture user and returns its identity view
// after persistence. Empty role selects the ordinary user role, and save
// failures fail the current suite case.
func (s *UserSubscriptionRepoSuite) mustCreateUser(email string, role string) *identity.User {
	s.T().Helper()
	if role == "" {
		role = identity.RoleUser
	}

	user, err := s.client.User.Create().
		SetEmail(email).
		SetPasswordHash("test-password-hash").
		SetStatus(billing.StatusActive).
		SetRole(role).
		Save(s.ctx)
	s.Require().NoError(err, "create user")
	return identitypostgres.UserFromEntity(user)
}

// mustCreatePlan inserts a subscription plan for sale at the fixture price and
// returns its billing view. Nonpositive validity selects thirty days, and save
// failures fail the suite case.
func (s *UserSubscriptionRepoSuite) mustCreatePlan(name string, validityDays int) *billing.SubscriptionPlan {
	s.T().Helper()
	if validityDays <= 0 {
		validityDays = 30
	}

	plan, err := s.client.SubscriptionPlan.Create().
		SetName(name).
		SetDescription("test plan").
		SetPrice(19.9).
		SetValidityDays(validityDays).
		SetValidityUnit("day").
		SetFeatures("").
		SetProductName("").
		SetForSale(true).
		SetSortOrder(0).
		Save(s.ctx)
	s.Require().NoError(err, "create plan")
	return billingpostgres.PlanFromEntity(plan)
}

// mustCreateSubscription inserts an active grant with default start, expiry,
// and assignment times rounded to whole seconds. A nonnil mutate callback edits
// its builder before saving; errors fail the suite case.
func (s *UserSubscriptionRepoSuite) mustCreateSubscription(userID, planID int64, mutate func(*dbent.UserSubscriptionCreate)) *dbent.UserSubscription {
	s.T().Helper()

	// Whole-second times keep persisted fixture dates comparable across
	// storage reads.
	now := time.Now().UTC().Truncate(time.Second)
	create := s.client.UserSubscription.Create().
		SetUserID(userID).
		SetPlanID(planID).
		SetStartsAt(now.Add(-1 * time.Hour)).
		SetExpiresAt(now.Add(24 * time.Hour)).
		SetStatus(billing.SubscriptionStatusActive).
		SetAssignedAt(now).
		SetNotes("")

	if mutate != nil {
		mutate(create)
	}

	sub, err := create.Save(s.ctx)
	s.Require().NoError(err, "create user subscription")
	return sub
}

// TestCreateAndGetByID_WithPreloads creates a grant linked to an owner, plan,
// and assigning administrator. Its ID read returns each loaded relationship
// with the expected persisted identity.
func (s *UserSubscriptionRepoSuite) TestCreateAndGetByID_WithPreloads() {
	user := s.mustCreateUser("sub-create@test.com", identity.RoleUser)
	plan := s.mustCreatePlan("plan-create", 30)
	admin := s.mustCreateUser("sub-admin@test.com", identity.RoleAdmin)

	// Whole-second times keep persisted fixture dates comparable across
	// storage reads.
	now := time.Now().UTC().Truncate(time.Second)
	sub := &billing.UserSubscription{
		UserID:        user.ID,
		PlanID:        plan.ID,
		Status:        billing.SubscriptionStatusActive,
		StartsAt:      now.Add(-2 * time.Hour),
		ExpiresAt:     now.Add(48 * time.Hour),
		AssignedBy:    &admin.ID,
		AssignedAt:    now,
		Notes:         "created by repo test",
		SourceOrderID: func() *int64 { v := int64(42); return &v }(),
	}

	err := s.repo.Create(s.ctx, sub)
	s.Require().NoError(err, "Create")
	s.Require().NotZero(sub.ID)

	got, err := s.repo.GetByID(s.ctx, sub.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().Equal(sub.UserID, got.UserID)
	s.Require().Equal(sub.PlanID, got.PlanID)
	s.Require().NotNil(got.User)
	s.Require().NotNil(got.Plan)
	s.Require().NotNil(got.AssignedByUser)
	s.Require().Equal(user.ID, got.User.ID)
	s.Require().Equal(plan.ID, got.Plan.ID)
	s.Require().Equal(admin.ID, got.AssignedByUser.ID)
}

// TestGetLatestByUserIDAndPlanID creates an earlier grant and a later pending
// grant for one user and plan. The latest lookup selects the later pending
// fixture.
func (s *UserSubscriptionRepoSuite) TestGetLatestByUserIDAndPlanID() {
	user := s.mustCreateUser("latest@test.com", identity.RoleUser)
	plan := s.mustCreatePlan("plan-latest", 30)

	older := s.mustCreateSubscription(user.ID, plan.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetStartsAt(time.Now().Add(-10 * 24 * time.Hour))
		c.SetExpiresAt(time.Now().Add(5 * 24 * time.Hour))
	})
	newer := s.mustCreateSubscription(user.ID, plan.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetStartsAt(time.Now().Add(6 * 24 * time.Hour))
		c.SetExpiresAt(time.Now().Add(35 * 24 * time.Hour))
		c.SetStatus(billing.SubscriptionStatusPending)
	})

	got, err := s.repo.GetLatestByUserIDAndPlanID(s.ctx, user.ID, plan.ID)
	s.Require().NoError(err)
	s.Require().Equal(newer.ID, got.ID)
	s.Require().NotEqual(older.ID, got.ID)
}

// TestListByUserIDAndPlanID_OrderedByStartsAt creates successive grants with
// fixed January and February start times. Listing both for their user and plan
// returns increasing start order.
func (s *UserSubscriptionRepoSuite) TestListByUserIDAndPlanID_OrderedByStartsAt() {
	user := s.mustCreateUser("plan-list@test.com", identity.RoleUser)
	plan := s.mustCreatePlan("plan-list", 30)

	first := s.mustCreateSubscription(user.ID, plan.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetStartsAt(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		c.SetExpiresAt(time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC))
	})
	second := s.mustCreateSubscription(user.ID, plan.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetStartsAt(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
		c.SetExpiresAt(time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC))
		c.SetStatus(billing.SubscriptionStatusPending)
	})

	subs, err := s.repo.ListByUserIDAndPlanID(s.ctx, user.ID, plan.ID)
	s.Require().NoError(err)
	s.Require().Len(subs, 2)
	s.Require().Equal(first.ID, subs[0].ID)
	s.Require().Equal(second.ID, subs[1].ID)
}

// TestListActiveByUserID_OnlyReturnsEffectiveSubscriptions creates current,
// expired, and future pending grants on separate plans. The active list
// contains the current grant with its matching plan ID.
func (s *UserSubscriptionRepoSuite) TestListActiveByUserID_OnlyReturnsEffectiveSubscriptions() {
	user := s.mustCreateUser("active-list@test.com", identity.RoleUser)
	activePlan := s.mustCreatePlan("plan-active", 30)
	expiredPlan := s.mustCreatePlan("plan-expired", 30)
	pendingPlan := s.mustCreatePlan("plan-pending", 30)

	active := s.mustCreateSubscription(user.ID, activePlan.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetStartsAt(time.Now().Add(-2 * time.Hour))
		c.SetExpiresAt(time.Now().Add(2 * time.Hour))
	})
	_ = s.mustCreateSubscription(user.ID, expiredPlan.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetStartsAt(time.Now().Add(-48 * time.Hour))
		c.SetExpiresAt(time.Now().Add(-24 * time.Hour))
		c.SetStatus(billing.SubscriptionStatusExpired)
	})
	_ = s.mustCreateSubscription(user.ID, pendingPlan.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetStartsAt(time.Now().Add(24 * time.Hour))
		c.SetExpiresAt(time.Now().Add(48 * time.Hour))
		c.SetStatus(billing.SubscriptionStatusPending)
	})

	subs, err := s.repo.ListActiveByUserID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().Len(subs, 1)
	s.Require().Equal(active.ID, subs[0].ID)
	s.Require().Equal(activePlan.ID, subs[0].PlanID)
}

// TestFilterByGroup_KeepsGlobalAndMatchingPlans creates unrestricted and
// group-specific active subscriptions. Filtering for group A retains the global
// grant and group A grant while returning two rows.
func (s *UserSubscriptionRepoSuite) TestFilterByGroup_KeepsGlobalAndMatchingPlans() {
	user := s.mustCreateUser("group-filter@test.com", identity.RoleUser)
	groupA := mustCreateGroup(s.T(), s.client, &routing.Group{
		Name: "subscription-filter-a",
	})
	groupB := mustCreateGroup(s.T(), s.client, &routing.Group{
		Name: "subscription-filter-b",
	})
	globalPlan := s.mustCreatePlan("plan-global", 30)
	planA := s.mustCreatePlan("plan-group-a", 30)
	planB := s.mustCreatePlan("plan-group-b", 30)

	_, err := s.client.ExecContext(s.ctx, `
		INSERT INTO subscription_plan_groups (plan_id, group_id)
		VALUES ($1, $2), ($3, $4)
	`, planA.ID, groupA.ID, planB.ID, groupB.ID)
	s.Require().NoError(err)

	globalSub := s.mustCreateSubscription(user.ID, globalPlan.ID, nil)
	subA := s.mustCreateSubscription(user.ID, planA.ID, nil)
	_ = s.mustCreateSubscription(user.ID, planB.ID, nil)
	active, err := s.repo.ListActiveByUserID(s.ctx, user.ID)
	s.Require().NoError(err)

	filtered, err := s.repo.FilterByGroup(s.ctx, active, groupA.ID)
	s.Require().NoError(err)
	s.Require().Len(filtered, 2)
	ids := map[int64]struct{}{}
	for i := range filtered {
		ids[filtered[i].ID] = struct{}{}
	}
	s.Require().Contains(ids, globalSub.ID)
	s.Require().Contains(ids, subA.ID)
}

// TestList_FilterByPlanAndPendingStatus supplies two plans with current and
// pending grants. A user, plan A, and pending-status query returns the pending
// grant with total one.
func (s *UserSubscriptionRepoSuite) TestList_FilterByPlanAndPendingStatus() {
	user := s.mustCreateUser("filter@test.com", identity.RoleUser)
	planA := s.mustCreatePlan("plan-filter-a", 30)
	planB := s.mustCreatePlan("plan-filter-b", 30)

	pending := s.mustCreateSubscription(user.ID, planA.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetStartsAt(time.Now().Add(24 * time.Hour))
		c.SetExpiresAt(time.Now().Add(48 * time.Hour))
		c.SetStatus(billing.SubscriptionStatusPending)
	})
	_ = s.mustCreateSubscription(user.ID, planB.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetStartsAt(time.Now().Add(-2 * time.Hour))
		c.SetExpiresAt(time.Now().Add(4 * time.Hour))
	})

	planID := planA.ID
	subs, page, err := s.repo.List(
		s.ctx,
		pagination.PaginationParams{Page: 1, PageSize: 10},
		&user.ID,
		&planID,
		billing.SubscriptionStatusPending,
		"",
		"starts_at",
		"asc",
	)
	s.Require().NoError(err)
	s.Require().Equal(int64(1), page.Total)
	s.Require().Len(subs, 1)
	s.Require().Equal(pending.ID, subs[0].ID)
}

// TestList_IncludesRevokedWhenStatusEmpty deletes one of two subscriptions
// before an unfiltered status list. Both remain visible, and the deleted view
// is revoked with user and plan loaded.
func (s *UserSubscriptionRepoSuite) TestList_IncludesRevokedWhenStatusEmpty() {
	user := s.mustCreateUser("revoked-all@test.com", identity.RoleUser)
	plan := s.mustCreatePlan("plan-revoked-all", 30)

	active := s.mustCreateSubscription(user.ID, plan.ID, nil)
	revoked := s.mustCreateSubscription(user.ID, plan.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetStartsAt(time.Now().Add(24 * time.Hour))
		c.SetExpiresAt(time.Now().Add(48 * time.Hour))
		c.SetStatus(billing.SubscriptionStatusPending)
	})
	s.Require().NoError(s.repo.Delete(s.ctx, revoked.ID))

	subs, page, err := s.repo.List(
		s.ctx,
		pagination.PaginationParams{Page: 1, PageSize: 10},
		&user.ID,
		nil,
		"",
		"",
		"created_at",
		"desc",
	)
	s.Require().NoError(err)
	s.Require().Equal(int64(2), page.Total)
	s.Require().Len(subs, 2)

	byID := make(map[int64]billing.UserSubscription, len(subs))
	for _, sub := range subs {
		byID[sub.ID] = sub
	}
	s.Require().Equal(billing.SubscriptionStatusActive, byID[active.ID].Status)
	s.Require().Equal(billing.SubscriptionStatusRevoked, byID[revoked.ID].Status)
	s.Require().NotNil(byID[revoked.ID].DeletedAt)
	s.Require().NotNil(byID[revoked.ID].User)
	s.Require().NotNil(byID[revoked.ID].Plan)
}

// TestList_FilterByRevokedStatus deletes one of two grants and requests revoked
// status for their owner. The result contains the deleted grant with revoked
// view status and a deletion timestamp.
func (s *UserSubscriptionRepoSuite) TestList_FilterByRevokedStatus() {
	user := s.mustCreateUser("revoked-filter@test.com", identity.RoleUser)
	plan := s.mustCreatePlan("plan-revoked-filter", 30)

	_ = s.mustCreateSubscription(user.ID, plan.ID, nil)
	revoked := s.mustCreateSubscription(user.ID, plan.ID, nil)
	s.Require().NoError(s.repo.Delete(s.ctx, revoked.ID))

	subs, page, err := s.repo.List(
		s.ctx,
		pagination.PaginationParams{Page: 1, PageSize: 10},
		&user.ID,
		nil, billing.SubscriptionStatusRevoked, "",
		"created_at",
		"desc",
	)
	s.Require().NoError(err)
	s.Require().Equal(int64(1), page.Total)
	s.Require().Len(subs, 1)
	s.Require().Equal(revoked.ID, subs[0].ID)
	s.Require().Equal(billing.SubscriptionStatusRevoked, subs[0].Status)
	s.Require().NotNil(subs[0].DeletedAt)
}

// TestGetByIDIncludeDeleted_PreservesPersistedStatus deletes an active grant
// before its include-deleted detail lookup. The result retains active stored
// status, deletion time, and loaded user and plan.
func (s *UserSubscriptionRepoSuite) TestGetByIDIncludeDeleted_PreservesPersistedStatus() {
	user := s.mustCreateUser("include-deleted@test.com", identity.RoleUser)
	plan := s.mustCreatePlan("plan-include-deleted", 30)

	sub := s.mustCreateSubscription(user.ID, plan.ID, nil)
	s.Require().NoError(s.repo.Delete(s.ctx, sub.ID))

	got, err := s.repo.GetByIDIncludeDeleted(s.ctx, sub.ID)
	s.Require().NoError(err)
	s.Require().Equal(sub.ID, got.ID)
	s.Require().Equal(billing.SubscriptionStatusActive, got.Status)
	s.Require().NotNil(got.DeletedAt)
	s.Require().NotNil(got.User)
	s.Require().NotNil(got.Plan)
}

// TestRestore deletes a grant and restores it using an expired status request.
// Both returned and reloaded values report expired status with their deletion
// timestamp cleared.
func (s *UserSubscriptionRepoSuite) TestRestore() {
	user := s.mustCreateUser("restore@test.com", identity.RoleUser)
	plan := s.mustCreatePlan("plan-restore", 30)

	sub := s.mustCreateSubscription(user.ID, plan.ID, nil)
	s.Require().NoError(s.repo.Delete(s.ctx, sub.ID))

	restored, err := s.repo.Restore(s.ctx, sub.ID, billing.SubscriptionStatusExpired)
	s.Require().NoError(err)
	s.Require().Equal(sub.ID, restored.ID)
	s.Require().Equal(billing.SubscriptionStatusExpired, restored.Status)
	s.Require().Nil(restored.DeletedAt)

	got, err := s.repo.GetByID(s.ctx, sub.ID)
	s.Require().NoError(err)
	s.Require().Equal(billing.SubscriptionStatusExpired, got.Status)
	s.Require().Nil(got.DeletedAt)
}

// TestResetDailyUsage_StaleResetDoesNotClearNewWindowUsage advances a daily
// window and records usage before retrying the old reset. The new window
// retains its start and three usage units.
func (s *UserSubscriptionRepoSuite) TestResetDailyUsage_StaleResetDoesNotClearNewWindowUsage() {
	user := s.mustCreateUser("resetd-cas@test.com", identity.RoleUser)
	plan := s.mustCreatePlan("plan-resetd-cas", 30)
	oldWindowStart := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	sub := s.mustCreateSubscription(user.ID, plan.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetDailyWindowStart(oldWindowStart)
		c.SetDailyUsageUsd(10)
	})

	newWindowStart := oldWindowStart.Add(24 * time.Hour)
	s.Require().NoError(s.repo.ResetDailyUsage(s.ctx, sub.ID, &oldWindowStart, newWindowStart))
	s.Require().NoError(s.repo.IncrementUsage(s.ctx, sub.ID, 3))
	// Reuse the old start after recording current-window usage so this
	// retry can expose a stale reset that incorrectly clears the newer
	// spending observation.
	s.Require().NoError(s.repo.ResetDailyUsage(s.ctx, sub.ID, &oldWindowStart, newWindowStart))

	got, err := s.repo.GetByID(s.ctx, sub.ID)
	s.Require().NoError(err)
	// Compare spending numerically and window timestamps within one
	// microsecond.
	s.Require().InDelta(3, got.DailyUsageUSD, 1e-6)
	s.Require().WithinDuration(newWindowStart, *got.DailyWindowStart, time.Microsecond)
}

// TestResetUsageWindows_ClearsUsageAfterAutomaticWindowAdvance advances the
// daily window and adds usage before an administrator reset. Daily usage clears
// while the selected current window start remains.
func (s *UserSubscriptionRepoSuite) TestResetUsageWindows_ClearsUsageAfterAutomaticWindowAdvance() {
	user := s.mustCreateUser("admin-reset-current@test.com", identity.RoleUser)
	plan := s.mustCreatePlan("plan-admin-reset-current", 30)
	oldWindowStart := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	sub := s.mustCreateSubscription(user.ID, plan.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetDailyWindowStart(oldWindowStart)
		c.SetDailyUsageUsd(10)
	})

	newWindowStart := oldWindowStart.Add(24 * time.Hour)
	s.Require().NoError(s.repo.ResetDailyUsage(s.ctx, sub.ID, &oldWindowStart, newWindowStart))
	s.Require().NoError(s.repo.IncrementUsage(s.ctx, sub.ID, 3))
	s.Require().NoError(s.repo.ResetUsageWindows(s.ctx, sub.ID, true, false, false, newWindowStart))

	got, err := s.repo.GetByID(s.ctx, sub.ID)
	s.Require().NoError(err)
	// Compare the zeroed amount numerically and the retained start within
	// one microsecond.
	s.Require().InDelta(0, got.DailyUsageUSD, 1e-6)
	s.Require().WithinDuration(newWindowStart, *got.DailyWindowStart, time.Microsecond)
}

// TestListBySourceOrderIDAndUsageMutations loads a grant by its source order,
// resets all spending windows, and increments usage. Each counter becomes 2.5
// and the daily start matches resetAt.
func (s *UserSubscriptionRepoSuite) TestListBySourceOrderIDAndUsageMutations() {
	user := s.mustCreateUser("source-order@test.com", identity.RoleUser)
	plan := s.mustCreatePlan("plan-source-order", 30)
	// Whole-second times keep persisted fixture dates comparable across
	// storage reads.
	now := time.Now().UTC().Truncate(time.Second)
	sourceOrderID := int64(12345)

	sub := s.mustCreateSubscription(user.ID, plan.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetSourceOrderID(sourceOrderID)
		c.SetDailyUsageUsd(4)
		c.SetWeeklyUsageUsd(5)
		c.SetMonthlyUsageUsd(6)
		c.SetDailyWindowStart(now.Add(-2 * time.Hour))
		c.SetWeeklyWindowStart(now.Add(-3 * 24 * time.Hour))
		c.SetMonthlyWindowStart(now.Add(-10 * 24 * time.Hour))
	})

	list, err := s.repo.ListBySourceOrderID(s.ctx, sourceOrderID)
	s.Require().NoError(err)
	s.Require().Len(list, 1)
	s.Require().Equal(sub.ID, list[0].ID)

	resetAt := now.Add(1 * time.Hour)
	s.Require().NoError(s.repo.ResetDailyUsage(s.ctx, sub.ID, sub.DailyWindowStart, resetAt))
	s.Require().NoError(s.repo.ResetWeeklyUsage(s.ctx, sub.ID, sub.WeeklyWindowStart, resetAt))
	s.Require().NoError(s.repo.ResetMonthlyUsage(s.ctx, sub.ID, sub.MonthlyWindowStart, resetAt))
	s.Require().NoError(s.repo.IncrementUsage(s.ctx, sub.ID, 2.5))

	got, err := s.repo.GetByID(s.ctx, sub.ID)
	s.Require().NoError(err)
	s.Require().Equal(2.5, got.DailyUsageUSD)
	s.Require().Equal(2.5, got.WeeklyUsageUSD)
	s.Require().Equal(2.5, got.MonthlyUsageUSD)
	s.Require().NotNil(got.DailyWindowStart)
	s.Require().Equal(resetAt, *got.DailyWindowStart)
}

// TestBatchUpdateExpiredStatus creates one past-expiry active grant and one
// currently valid active grant. The batch reports one changed row and reloads
// the expired fixture with expired status.
func (s *UserSubscriptionRepoSuite) TestBatchUpdateExpiredStatus() {
	user := s.mustCreateUser("expire@test.com", identity.RoleUser)
	plan := s.mustCreatePlan("plan-expire", 30)

	expired := s.mustCreateSubscription(user.ID, plan.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetStartsAt(time.Now().Add(-48 * time.Hour))
		c.SetExpiresAt(time.Now().Add(-24 * time.Hour))
		c.SetStatus(billing.SubscriptionStatusActive)
	})
	_ = s.mustCreateSubscription(user.ID, plan.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetStartsAt(time.Now().Add(-2 * time.Hour))
		c.SetExpiresAt(time.Now().Add(24 * time.Hour))
		c.SetStatus(billing.SubscriptionStatusActive)
	})
	updated, err := s.repo.BatchUpdateExpiredStatus(s.ctx)
	s.Require().NoError(err)
	s.Require().Equal(int64(1), updated)

	got, err := s.repo.GetByID(s.ctx, expired.ID)
	s.Require().NoError(err)
	s.Require().Equal(billing.SubscriptionStatusExpired, got.Status)
}
