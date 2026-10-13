//go:build integration

package identity_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/authidentity"
	"github.com/TokenFlux/TokenRouter/ent/authidentitychannel"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// User repository edits combine profile columns, allowed-group relationships,
// email identities, and funding fields, with masks selecting what an
// administrator changes. PostgreSQL suite cases inspect filters, loaded
// subscriptions, balance and concurrency adjustments, mailbox alias rules, and
// missing rows while reusing the suite's cleared user fixtures.
//
// Two redemption cases run overlapping negative adjustments and join both
// outcomes before checking the stored nonnegative amount. The mailbox-guard
// case runs competing Gmail aliases and checks one successful creation, one
// email conflict, and one stored user. Buffered error channels let workers
// finish before the suite counts results, separating these concurrency
// observations from the sequential CRUD and filter cases.

// TestUserRepoSuite runs PostgreSQL user persistence cases in the shared
// user-repository fixture. Cases inspect profile masks, lookup and list
// behavior, funding changes, mailbox guards, and joined concurrent adjustments.
func TestUserRepoSuite(t *testing.T) {
	suite.Run(t, new(UserRepoSuite))
}

// TestUpdateForkSpecificFields selects the user's key allowance and disabled
// public groups in its update mask. Reloading records a key limit of seven and
// the supplied disabled group relation.
func (s *UserRepoSuite) TestUpdateForkSpecificFields() {
	user := s.mustCreateUser(&identitycore.User{Email: "fork-update-fields@example.com", APIKeyLimit: 100})
	group := s.mustCreateGroup("fork-update-public-group")

	loaded, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	loaded.APIKeyLimit = 7
	loaded.DisabledPublicGroups = []int64{group.ID}

	// Select the key-count column and disabled-group relationship together so this
	// edit checks persisted scalar policy alongside the separately stored group
	// access configuration.
	s.Require().NoError(s.repo.Update(s.ctx, loaded, identitycore.UserUpdateFields{
		APIKeyLimit:          true,
		DisabledPublicGroups: true,
	}))

	updated, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().Equal(7, updated.APIKeyLimit)
	s.Require().Equal([]int64{group.ID}, updated.DisabledPublicGroups)
}

// mustCreatePlan inserts a thirty-day plan for sale at the suite fixture price
// and returns its billing view. Save errors fail the case before dependent
// subscription creation begins.
func (s *UserRepoSuite) mustCreatePlan(name string) *billing.SubscriptionPlan {
	s.T().Helper()

	plan, err := s.client.SubscriptionPlan.Create().
		SetName(name).
		SetDescription("test plan").
		SetPrice(9.9).
		SetValidityDays(30).
		SetValidityUnit("day").
		SetFeatures("").
		SetProductName("").
		SetForSale(true).
		SetSortOrder(0).
		Save(s.ctx)
	s.Require().NoError(err, "create plan")
	return billingpostgres.PlanFromEntity(plan)
}

// mustCreateSubscription inserts an active grant with current fixture
// assignment and default effective dates. A nonnil mutate callback edits its
// builder before saving, and failures fail the current suite case.
func (s *UserRepoSuite) mustCreateSubscription(userID, planID int64, mutate func(*dbent.UserSubscriptionCreate)) *dbent.UserSubscription {
	s.T().Helper()

	now := time.Now()
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
	s.Require().NoError(err, "create subscription")
	return sub
}

// TestCreate creates an active user with a supplied email through the suite
// repository. Storage fills a nonzero ID, and a later ID lookup returns the
// expected email.
func (s *UserRepoSuite) TestCreate() {
	user := s.mustCreateUser(&identitycore.User{
		Email:        "create@test.com",
		Username:     "testuser",
		PasswordHash: "test-password-hash",
		Role:         identitycore.RoleUser,
		Status:       billing.StatusActive,
	})

	s.Require().NotZero(user.ID, "expected ID to be set")

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().Equal("create@test.com", got.Email)
}

// TestGetByID_NotFound queries a numeric user ID absent from the current suite
// fixture. The repository reports an error when no stored user matches that
// input.
func (s *UserRepoSuite) TestGetByID_NotFound() {
	_, err := s.repo.GetByID(s.ctx, 999999)
	s.Require().Error(err, "expected error for non-existent ID")
}

// TestGetByEmail creates a user with one supplied email before querying by that
// same value. The returned identity has the persisted user's expected storage
// ID.
func (s *UserRepoSuite) TestGetByEmail() {
	user := s.mustCreateUser(&identitycore.User{Email: "byemail@test.com"})

	got, err := s.repo.GetByEmail(s.ctx, user.Email)
	s.Require().NoError(err, "GetByEmail")
	s.Require().Equal(user.ID, got.ID)
}

// TestGetByEmail_NotFound requests an email absent from the current suite
// fixture. The repository returns an error when that supplied address has no
// matching stored user.
func (s *UserRepoSuite) TestGetByEmail_NotFound() {
	_, err := s.repo.GetByEmail(s.ctx, "nonexistent@test.com")
	s.Require().Error(err, "expected error for non-existent email")
}

// TestExistsByEmail_NormalizesSpacingAndCaseOnPostgres creates an address
// containing leading and trailing spaces and mixed case. A differently spaced
// uppercase lookup still reports the existing user.
func (s *UserRepoSuite) TestExistsByEmail_NormalizesSpacingAndCaseOnPostgres() {
	s.mustCreateUser(&identitycore.User{Email: " Legacy@Example.com "})

	exists, err := s.repo.ExistsByEmail(s.ctx, "  LEGACY@example.com  ")
	s.Require().NoError(err, "ExistsByEmail normalized lookup")
	s.Require().True(exists)
}

// TestUpdate loads a user, changes its username, and selects that field in the
// mask. The following ID read returns the persisted updated username.
func (s *UserRepoSuite) TestUpdate() {
	user := s.mustCreateUser(&identitycore.User{Email: "update@test.com", Username: "original"})

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	got.Username = "updated"
	s.Require().NoError(s.repo.Update(s.ctx, got, identitycore.UserUpdateFields{Username: true}), "Update")

	updated, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err, "GetByID after update")
	s.Require().Equal("updated", updated.Username)
}

// TestBatchUpdateLimitsUpdatesOnlyProvidedFields supplies concurrency while
// leaving the RPM pointer nil. Exactly one user is affected, concurrency
// changes to nine, and the earlier RPM value remains twenty.
func (s *UserRepoSuite) TestBatchUpdateLimitsUpdatesOnlyProvidedFields() {
	user := s.mustCreateUser(&identitycore.User{
		Email:       "batch-limits-one-field@test.com",
		Concurrency: 4,
		RPMLimit:    20,
	})
	concurrency := 9

	affected, err := s.repo.BatchUpdateLimits(s.ctx, []int64{user.ID}, &concurrency, nil)
	s.Require().NoError(err)
	s.Equal(1, affected)

	updated, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Equal(9, updated.Concurrency)
	s.Equal(20, updated.RPMLimit)
}

// TestBatchUpdateLimitsUpdatesBothFieldsToZero supplies nonnil zero pointers
// for concurrency and RPM. The batch affects one row, and reloading shows both
// stored limits cleared to zero.
func (s *UserRepoSuite) TestBatchUpdateLimitsUpdatesBothFieldsToZero() {
	user := s.mustCreateUser(&identitycore.User{
		Email:       "batch-limits-zero@test.com",
		Concurrency: 4,
		RPMLimit:    20,
	})
	zero := 0

	affected, err := s.repo.BatchUpdateLimits(s.ctx, []int64{user.ID}, &zero, &zero)
	s.Require().NoError(err)
	s.Equal(1, affected)

	updated, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Zero(updated.Concurrency)
	s.Zero(updated.RPMLimit)
}

// TestBatchUpdateLimitsIgnoresDeletedUsersAndReturnsAffectedRows targets active
// and deleted users in one RPM update. The count is one, the active value
// changes, and the retained deleted row keeps its prior limit.
func (s *UserRepoSuite) TestBatchUpdateLimitsIgnoresDeletedUsersAndReturnsAffectedRows() {
	active := s.mustCreateUser(&identitycore.User{Email: "batch-limits-active@test.com", RPMLimit: 10})
	deleted := s.mustCreateUser(&identitycore.User{Email: "batch-limits-deleted@test.com", RPMLimit: 10})
	s.Require().NoError(s.client.User.DeleteOneID(deleted.ID).Exec(s.ctx))
	rpmLimit := 45

	affected, err := s.repo.BatchUpdateLimits(s.ctx, []int64{active.ID, deleted.ID}, nil, &rpmLimit)
	s.Require().NoError(err)
	s.Equal(1, affected)

	updatedActive, err := s.repo.GetByID(s.ctx, active.ID)
	s.Require().NoError(err)
	s.Equal(45, updatedActive.RPMLimit)
	updatedDeleted, err := s.repo.GetByIDIncludeDeleted(s.ctx, deleted.ID)
	s.Require().NoError(err)
	s.Equal(10, updatedDeleted.RPMLimit)
}

// TestUpdateIgnoresNoRowsFromConflictingEmailIdentityUpsert loads a user whose
// email identity already exists before a username-only edit. Update succeeds
// and the following read returns its selected new username.
func (s *UserRepoSuite) TestUpdateIgnoresNoRowsFromConflictingEmailIdentityUpsert() {
	user := s.mustCreateUser(&identitycore.User{Email: "update-existing-identity@test.com", Username: "original"})

	identityCount, err := s.client.AuthIdentity.Query().
		Where(
			authidentity.UserIDEQ(user.ID),
			authidentity.ProviderTypeEQ("email"),
			authidentity.ProviderKeyEQ("email"),
			authidentity.ProviderSubjectEQ("update-existing-identity@test.com"),
		).
		Count(s.ctx)
	s.Require().NoError(err)
	s.Require().Equal(1, identityCount)

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	got.Username = "updated"
	s.Require().NoError(s.repo.Update(s.ctx, got, identitycore.UserUpdateFields{Username: true}), "Update should tolerate ON CONFLICT DO NOTHING returning no rows")

	updated, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().Equal("updated", updated.Username)
}

// TestDelete removes a created user through the suite repository. A later ID
// lookup returns an error for that user after the deletion call has succeeded.
func (s *UserRepoSuite) TestDelete() {
	user := s.mustCreateUser(&identitycore.User{Email: "delete@test.com"})

	err := s.repo.Delete(s.ctx, user.ID)
	s.Require().NoError(err, "Delete")

	_, err = s.repo.GetByID(s.ctx, user.ID)
	s.Require().Error(err, "expected error after delete")
}

// TestDeleteRemovesAuthIdentitiesAndChannels attaches a provider identity and
// channel to a created user before deletion. The user's identity count and that
// identity's channel count both become zero.
func (s *UserRepoSuite) TestDeleteRemovesAuthIdentitiesAndChannels() {
	user := s.mustCreateUser(&identitycore.User{Email: "delete-oauth@test.com"})

	identity, err := s.client.AuthIdentity.Create().
		SetUserID(user.ID).
		SetProviderType("linuxdo").
		SetProviderKey("linuxdo").
		SetProviderSubject("delete-oauth-subject").
		Save(s.ctx)
	s.Require().NoError(err)

	_, err = s.client.AuthIdentityChannel.Create().
		SetIdentityID(identity.ID).
		SetProviderType("wechat").
		SetProviderKey("wechat").
		SetChannel("open").
		SetChannelAppID("app-id").
		SetChannelSubject("openid-123").
		Save(s.ctx)
	s.Require().NoError(err)

	err = s.repo.Delete(s.ctx, user.ID)
	s.Require().NoError(err)

	identityCount, err := s.client.AuthIdentity.Query().Where(authidentity.UserIDEQ(user.ID)).Count(s.ctx)
	s.Require().NoError(err)
	s.Require().Zero(identityCount)

	channelCount, err := s.client.AuthIdentityChannel.Query().Where(authidentitychannel.IdentityIDEQ(identity.ID)).Count(s.ctx)
	s.Require().NoError(err)
	s.Require().Zero(channelCount)
}

// TestList creates two users in the current suite fixture before requesting
// ordinary pagination. The returned list contains both rows and reports total
// count two.
func (s *UserRepoSuite) TestList() {
	s.mustCreateUser(&identitycore.User{Email: "list1@test.com"})
	s.mustCreateUser(&identitycore.User{Email: "list2@test.com"})

	users, page, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10})
	s.Require().NoError(err, "List")
	s.Require().Len(users, 2)
	s.Require().Equal(int64(2), page.Total)
}

// TestListWithFilters_Status stores active and disabled users and requests
// active status. The resulting list contains one row whose user status matches
// the supplied filter.
func (s *UserRepoSuite) TestListWithFilters_Status() {
	s.mustCreateUser(&identitycore.User{Email: "active@test.com", Status: billing.StatusActive})
	s.mustCreateUser(&identitycore.User{Email: "disabled@test.com", Status: billing.StatusDisabled})

	users, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, identitycore.UserListFilters{Status: billing.StatusActive})
	s.Require().NoError(err)
	s.Require().Len(users, 1)
	s.Require().Equal(billing.StatusActive, users[0].Status)
}

// TestListWithFilters_Role stores ordinary and administrator users then
// requests administrator role. The resulting list contains one row whose role
// matches the selected admin value.
func (s *UserRepoSuite) TestListWithFilters_Role() {
	s.mustCreateUser(&identitycore.User{Email: "user@test.com", Role: identitycore.RoleUser})
	s.mustCreateUser(&identitycore.User{Email: "admin@test.com", Role: identitycore.RoleAdmin})

	users, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, identitycore.UserListFilters{Role: identitycore.RoleAdmin})
	s.Require().NoError(err)
	s.Require().Len(users, 1)
	s.Require().Equal(identitycore.RoleAdmin, users[0].Role)
}

// TestListWithFilters_Search stores Alice and Bob email fixtures and searches
// for alice. The returned list has one row whose email contains that requested
// fragment.
func (s *UserRepoSuite) TestListWithFilters_Search() {
	s.mustCreateUser(&identitycore.User{Email: "alice@test.com", Username: "Alice"})
	s.mustCreateUser(&identitycore.User{Email: "bob@test.com", Username: "Bob"})

	users, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, identitycore.UserListFilters{Search: "alice"})
	s.Require().NoError(err)
	s.Require().Len(users, 1)
	s.Require().Contains(users[0].Email, "alice")
}

// TestListWithFilters_SearchByUsername supplies unrelated emails and distinct
// usernames before searching john. The single returned row has the matching
// JohnDoe username in the current fixture.
func (s *UserRepoSuite) TestListWithFilters_SearchByUsername() {
	s.mustCreateUser(&identitycore.User{Email: "u1@test.com", Username: "JohnDoe"})
	s.mustCreateUser(&identitycore.User{Email: "u2@test.com", Username: "JaneSmith"})

	users, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, identitycore.UserListFilters{Search: "john"})
	s.Require().NoError(err)
	s.Require().Len(users, 1)
	s.Require().Equal("JohnDoe", users[0].Username)
}

// TestListWithFilters_LoadsActiveSubscriptions gives one user active and
// expired subscription fixtures before searching their email. The returned user
// includes one subscription whose loaded plan matches the active grant.
func (s *UserRepoSuite) TestListWithFilters_LoadsActiveSubscriptions() {
	user := s.mustCreateUser(&identitycore.User{Email: "sub@test.com", Status: billing.StatusActive})
	planActive := s.mustCreatePlan("plan-sub-active")
	planExpired := s.mustCreatePlan("plan-sub-expired")

	_ = s.mustCreateSubscription(user.ID, planActive.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetStatus(billing.SubscriptionStatusActive)
		c.SetExpiresAt(time.Now().Add(1 * time.Hour))
	})
	_ = s.mustCreateSubscription(user.ID, planExpired.ID, func(c *dbent.UserSubscriptionCreate) {
		c.SetStatus(billing.SubscriptionStatusExpired)
		c.SetExpiresAt(time.Now().Add(-1 * time.Hour))
	})

	users, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, identitycore.UserListFilters{Search: "sub@"})
	s.Require().NoError(err, "ListWithFilters")
	s.Require().Len(users, 1, "expected 1 user")
	s.Require().Len(users[0].Subscriptions, 1, "expected 1 active subscription")
	s.Require().NotNil(users[0].Subscriptions[0].Plan, "expected subscription plan preload")
	s.Require().Equal(planActive.ID, users[0].Subscriptions[0].Plan.ID, "plan ID mismatch")
}

// TestListWithFilters_CombinedFilters supplies users with competing role,
// status, and email values. Combining active administrator and b@ search
// returns one target row and total one.
func (s *UserRepoSuite) TestListWithFilters_CombinedFilters() {
	s.mustCreateUser(&identitycore.User{
		Email:    "a@example.com",
		Username: "Alice",
		Role:     identitycore.RoleUser,
		Status:   billing.StatusActive,
		Balance:  10,
	})
	target := s.mustCreateUser(&identitycore.User{
		Email:    "b@example.com",
		Username: "Bob",
		Role:     identitycore.RoleAdmin,
		Status:   billing.StatusActive,
		Balance:  1,
	})
	s.mustCreateUser(&identitycore.User{
		Email:  "c@example.com",
		Role:   identitycore.RoleAdmin,
		Status: billing.StatusDisabled,
	})

	users, page, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, identitycore.UserListFilters{Status: billing.StatusActive, Role: identitycore.RoleAdmin, Search: "b@"})
	s.Require().NoError(err, "ListWithFilters")
	s.Require().Equal(int64(1), page.Total, "ListWithFilters total mismatch")
	s.Require().Len(users, 1, "ListWithFilters len mismatch")
	s.Require().Equal(target.ID, users[0].ID, "ListWithFilters result mismatch")
}

// TestUpdateBalance starts a stored user at balance ten and applies a positive
// delta of 2.5. Reloading returns 12.5 within the case's numeric comparison
// tolerance.
func (s *UserRepoSuite) TestUpdateBalance() {
	user := s.mustCreateUser(&identitycore.User{Email: "bal@test.com", Balance: 10})

	err := s.repo.UpdateBalance(s.ctx, user.ID, 2.5)
	s.Require().NoError(err, "UpdateBalance")

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	// A micro-dollar tolerance compares the persisted balance after its positive
	// adjustment.
	s.Require().InDelta(12.5, got.Balance, 1e-6)
}

// TestUpdateBalance_Negative starts a stored user at balance ten and applies a
// negative delta of three. Reloading returns seven within the case's numeric
// comparison tolerance.
func (s *UserRepoSuite) TestUpdateBalance_Negative() {
	user := s.mustCreateUser(&identitycore.User{Email: "balneg@test.com", Balance: 10})

	err := s.repo.UpdateBalance(s.ctx, user.ID, -3)
	s.Require().NoError(err, "UpdateBalance with negative")

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	// Compare the saved balance after the negative delta within a micro-dollar
	// difference.
	s.Require().InDelta(7.0, got.Balance, 1e-6)
}

// TestApplyRedeemBalanceAdjustment_ConcurrentNeverNegative runs two
// negative-seven redemption balance adjustments against ten available dollars.
// Both calls succeed, and after joining them the stored balance is zero.
func (s *UserRepoSuite) TestApplyRedeemBalanceAdjustment_ConcurrentNeverNegative() {
	user := s.mustCreateUser(&identitycore.User{Email: "redeem-bal-concurrent@test.com", Balance: 10})

	// Buffer both adjustment outcomes and join their writers before closing
	// errors; the final read then observes both completed PostgreSQL updates on
	// the same user.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			errs <- s.repo.ApplyRedeemBalanceAdjustment(context.Background(), user.ID, -7)
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		s.Require().NoError(err)
	}

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	// A micro-dollar tolerance checks the stored lower bound after both
	// adjustments finish.
	s.Require().InDelta(0, got.Balance, 1e-6)
}

// TestDeductBalance requests a deduction of five from a ten-dollar balance. The
// returned deducted amount and the reloaded remaining balance each equal five
// within tolerance.
func (s *UserRepoSuite) TestDeductBalance() {
	user := s.mustCreateUser(&identitycore.User{Email: "deduct@test.com", Balance: 10})

	deducted, err := s.repo.DeductBalance(s.ctx, user.ID, 5)
	s.Require().NoError(err, "DeductBalance")
	// Compare returned deduction and remaining wallet amounts within a
	// micro-dollar difference.
	s.Require().InDelta(5.0, deducted, 1e-6)

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().InDelta(5.0, got.Balance, 1e-6)
}

// TestDeductBalance_InsufficientFunds requests a deduction exceeding a
// five-dollar balance. The call succeeds with five deducted, and the stored
// balance becomes zero within the numeric tolerance.
func (s *UserRepoSuite) TestDeductBalance_InsufficientFunds() {
	user := s.mustCreateUser(&identitycore.User{Email: "insuf@test.com", Balance: 5})

	deducted, err := s.repo.DeductBalance(s.ctx, user.ID, 999)
	s.Require().NoError(err, "DeductBalance should clamp to available balance")
	// A micro-dollar tolerance checks the clamped deduction and the empty
	// remaining wallet.
	s.Require().InDelta(5.0, deducted, 1e-6)

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().InDelta(0.0, got.Balance, 1e-6, "Balance should clamp to zero")
}

// TestDeductBalance_ExactAmount deducts the complete ten-dollar balance from a
// stored user. The reported deduction is ten and a later read shows zero
// remaining balance.
func (s *UserRepoSuite) TestDeductBalance_ExactAmount() {
	user := s.mustCreateUser(&identitycore.User{Email: "exact@test.com", Balance: 10})

	deducted, err := s.repo.DeductBalance(s.ctx, user.ID, 10)
	s.Require().NoError(err, "DeductBalance exact amount")
	// The numeric tolerance compares complete deduction with a zero remaining
	// balance.
	s.Require().InDelta(10.0, deducted, 1e-6)

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().InDelta(0.0, got.Balance, 1e-6)
}

// TestDeductBalance_LeavesLegacyNegativeBalanceUnchanged starts with a stored
// negative-five balance and requests another deduction. The reported deducted
// amount is zero and the preceding negative amount remains unchanged.
func (s *UserRepoSuite) TestDeductBalance_LeavesLegacyNegativeBalanceUnchanged() {
	user := s.mustCreateUser(&identitycore.User{Email: "legacy-negative@test.com", Balance: -5.0})

	deducted, err := s.repo.DeductBalance(s.ctx, user.ID, 10.0)
	s.Require().NoError(err, "DeductBalance should ignore already non-positive balance")
	// Allow micro-dollar comparison differences while checking zero deduction and
	// retained legacy debt.
	s.Require().InDelta(0.0, deducted, 1e-6)

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().InDelta(-5.0, got.Balance, 1e-6, "Existing negative balance should remain unchanged")
}

// TestUpdateConcurrency applies a positive concurrency delta of three to a
// stored value of five. The reloaded user reports eight after the update
// succeeds.
func (s *UserRepoSuite) TestUpdateConcurrency() {
	user := s.mustCreateUser(&identitycore.User{Email: "conc@test.com", Concurrency: 5})

	err := s.repo.UpdateConcurrency(s.ctx, user.ID, 3)
	s.Require().NoError(err, "UpdateConcurrency")

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().Equal(8, got.Concurrency)
}

// TestUpdateConcurrency_Negative applies a negative concurrency delta of two to
// a stored value of five. The reloaded user reports three after the update
// succeeds.
func (s *UserRepoSuite) TestUpdateConcurrency_Negative() {
	user := s.mustCreateUser(&identitycore.User{Email: "concneg@test.com", Concurrency: 5})

	err := s.repo.UpdateConcurrency(s.ctx, user.ID, -2)
	s.Require().NoError(err, "UpdateConcurrency negative")

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().Equal(3, got.Concurrency)
}

// TestApplyRedeemConcurrencyAdjustment_ConcurrentNeverNegative runs two
// negative-seven redemption concurrency adjustments against a stored value of
// ten. Both succeed, and joining the workers precedes the final zero-value
// assertion.
func (s *UserRepoSuite) TestApplyRedeemConcurrencyAdjustment_ConcurrentNeverNegative() {
	user := s.mustCreateUser(&identitycore.User{Email: "redeem-concurrency-concurrent@test.com", Concurrency: 10})

	// Buffer both adjustment outcomes and join their writers before closing
	// errors; the final read then observes both completed PostgreSQL updates on
	// the same user.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			errs <- s.repo.ApplyRedeemConcurrencyAdjustment(context.Background(), user.ID, -7)
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		s.Require().NoError(err)
	}

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().Equal(0, got.Concurrency)
}

// TestExistsByEmail creates one email fixture and checks both that address and
// an absent address. Existence reports true for the stored value and false for
// the other.
func (s *UserRepoSuite) TestExistsByEmail() {
	s.mustCreateUser(&identitycore.User{Email: "exists@test.com"})

	exists, err := s.repo.ExistsByEmail(s.ctx, "exists@test.com")
	s.Require().NoError(err, "ExistsByEmail")
	s.Require().True(exists)

	notExists, err := s.repo.ExistsByEmail(s.ctx, "notexists@test.com")
	s.Require().NoError(err)
	s.Require().False(notExists)
}

// TestExistsByNormalizedEmail supplies GoogleMail and QQ aliases with plus
// suffixes before normalized lookups. The tested Gmail and QQ aliases match, QQ
// dot removal does not, and an unrelated address remains absent.
func (s *UserRepoSuite) TestExistsByNormalizedEmail() {
	s.mustCreateUser(&identitycore.User{Email: " Y.o.u.r.N.a.m.e+promo@GoogleMail.com. "})
	s.mustCreateUser(&identitycore.User{Email: "first.last+promo@qq.com"})

	exists, err := s.repo.ExistsByNormalizedEmail(s.ctx, "yourname@gmail.com")
	s.Require().NoError(err, "ExistsByNormalizedEmail")
	s.Require().True(exists)

	exists, err = s.repo.ExistsByNormalizedEmail(s.ctx, "first.last@qq.com")
	s.Require().NoError(err)
	s.Require().True(exists)

	dotDistinct, err := s.repo.ExistsByNormalizedEmail(s.ctx, "firstlast@qq.com")
	s.Require().NoError(err)
	s.Require().False(dotDistinct)

	notExists, err := s.repo.ExistsByNormalizedEmail(s.ctx, "other@example.com")
	s.Require().NoError(err)
	s.Require().False(notExists)
}

// TestCreateWithNormalizedEmailGuardSerializesProviderAliases creates competing
// Gmail and GoogleMail aliases in separate workers. One creation succeeds, one
// reports EmailExists, and the final user count is one.
func (s *UserRepoSuite) TestCreateWithNormalizedEmailGuardSerializesProviderAliases() {
	candidates := []*identitycore.User{
		{
			Email:        "d.axis.2026+first@gmail.com",
			Username:     "gmail-alias-first",
			PasswordHash: "hash",
			Role:         identitycore.RoleUser,
			Status:       billing.StatusActive,
		},
		{
			Email:        "da.xis.2026+second@googlemail.com.",
			Username:     "gmail-alias-second",
			PasswordHash: "hash",
			Role:         identitycore.RoleUser,
			Status:       billing.StatusActive,
		},
	}

	// Give each distinct candidate its own object and buffer both creation
	// results; workers contend on normalized mailbox ownership before the test
	// joins and counts them.
	errs := make(chan error, len(candidates))
	var wg sync.WaitGroup
	for _, candidate := range candidates {
		wg.Go(func() {
			errs <- s.repo.CreateWithNormalizedEmailGuard(
				s.ctx,
				candidate,
				identitycore.NormalizeRegistrationEmailAddress(candidate.Email),
			)
		})
	}
	wg.Wait()
	close(errs)

	successes := 0
	conflicts := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, identitycore.ErrEmailExists):
			conflicts++
		default:
			s.Require().NoError(err)
		}
	}
	s.Require().Equal(1, successes)
	s.Require().Equal(1, conflicts)
	count, err := s.client.User.Query().Count(s.ctx)
	s.Require().NoError(err)
	s.Require().Equal(1, count)
}

// TestUpdateWithNormalizedEmailGuard_RejectsConflict changes another user's
// address to an alias of an existing Gmail owner. Update returns EmailExists
// and reloading retains the previous address.
func (s *UserRepoSuite) TestUpdateWithNormalizedEmailGuard_RejectsConflict() {
	s.mustCreateUser(&identitycore.User{Email: "your.name@gmail.com"})
	other := s.mustCreateUser(&identitycore.User{Email: "second@example.com"})

	got, err := s.repo.GetByID(s.ctx, other.ID)
	s.Require().NoError(err)
	got.Email = "yourname+alias@googlemail.com."

	err = s.repo.UpdateWithNormalizedEmailGuard(s.ctx, got, identitycore.NormalizeRegistrationEmailAddress(got.Email), identitycore.UserUpdateFields{Email: true})
	s.Require().ErrorIs(err, identitycore.ErrEmailExists)

	reloaded, err := s.repo.GetByID(s.ctx, other.ID)
	s.Require().NoError(err)
	s.Require().Equal("second@example.com", reloaded.Email)
}

// TestUpdateWithNormalizedEmailGuard_AllowsSameUser changes a Gmail address to
// its own normalized alias. The guarded update succeeds, and the following user
// read returns the selected new email.
func (s *UserRepoSuite) TestUpdateWithNormalizedEmailGuard_AllowsSameUser() {
	user := s.mustCreateUser(&identitycore.User{Email: "your.name+seed@gmail.com"})

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	got.Email = "yourname@gmail.com"

	err = s.repo.UpdateWithNormalizedEmailGuard(s.ctx, got, identitycore.NormalizeRegistrationEmailAddress(got.Email), identitycore.UserUpdateFields{Email: true})
	s.Require().NoError(err)

	reloaded, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().Equal("yourname@gmail.com", reloaded.Email)
}

// TestRemoveGroupFromAllowedGroups targets one user's grant while another user
// has only an unrelated group. Removal reports one affected row and retains the
// first user's other grant.
func (s *UserRepoSuite) TestRemoveGroupFromAllowedGroups() {
	target := s.mustCreateGroup("target-42")
	other := s.mustCreateGroup("other-7")

	userA := s.mustCreateUser(&identitycore.User{
		Email:         "a1@example.com",
		AllowedGroups: []int64{target.ID, other.ID},
	})
	s.mustCreateUser(&identitycore.User{
		Email:         "a2@example.com",
		AllowedGroups: []int64{other.ID},
	})

	affected, err := s.repo.RemoveGroupFromAllowedGroups(s.ctx, target.ID)
	s.Require().NoError(err, "RemoveGroupFromAllowedGroups")
	s.Require().Equal(int64(1), affected, "expected 1 affected row")

	got, err := s.repo.GetByID(s.ctx, userA.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().NotContains(got.AllowedGroups, target.ID)
	s.Require().Contains(got.AllowedGroups, other.ID)
}

// TestRemoveGroupFromAllowedGroups_NoMatch supplies a group ID absent from two
// existing grants on a stored user. The repository succeeds and reports zero
// affected rows for that removal.
func (s *UserRepoSuite) TestRemoveGroupFromAllowedGroups_NoMatch() {
	groupA := s.mustCreateGroup("nomatch-a")
	groupB := s.mustCreateGroup("nomatch-b")

	s.mustCreateUser(&identitycore.User{
		Email:         "nomatch@test.com",
		AllowedGroups: []int64{groupA.ID, groupB.ID},
	})

	affected, err := s.repo.RemoveGroupFromAllowedGroups(s.ctx, 999999)
	s.Require().NoError(err)
	s.Require().Zero(affected, "expected no affected rows")
}

// TestGetFirstAdmin creates two active administrators sequentially in the suite
// fixture. The repository returns the first administrator's ID when asked for
// the first usable admin.
func (s *UserRepoSuite) TestGetFirstAdmin() {
	admin1 := s.mustCreateUser(&identitycore.User{
		Email:  "admin1@example.com",
		Role:   identitycore.RoleAdmin,
		Status: billing.StatusActive,
	})
	s.mustCreateUser(&identitycore.User{
		Email:  "admin2@example.com",
		Role:   identitycore.RoleAdmin,
		Status: billing.StatusActive,
	})

	got, err := s.repo.GetFirstAdmin(s.ctx)
	s.Require().NoError(err, "GetFirstAdmin")
	s.Require().Equal(admin1.ID, got.ID, "GetFirstAdmin mismatch")
}

// TestGetFirstAdmin_NoAdmin supplies an active ordinary user and no
// administrator fixture. The repository reports an error for the request to
// obtain the first administrator.
func (s *UserRepoSuite) TestGetFirstAdmin_NoAdmin() {
	s.mustCreateUser(&identitycore.User{
		Email:  "user@example.com",
		Role:   identitycore.RoleUser,
		Status: billing.StatusActive,
	})

	_, err := s.repo.GetFirstAdmin(s.ctx)
	s.Require().Error(err, "expected error when no admin exists")
}

// TestGetFirstAdmin_DisabledAdminIgnored creates a disabled administrator
// before an active one. The returned first-administrator result identifies the
// active fixture and excludes the earlier disabled account.
func (s *UserRepoSuite) TestGetFirstAdmin_DisabledAdminIgnored() {
	s.mustCreateUser(&identitycore.User{
		Email:  "disabled@example.com",
		Role:   identitycore.RoleAdmin,
		Status: billing.StatusDisabled,
	})
	activeAdmin := s.mustCreateUser(&identitycore.User{
		Email:  "active@example.com",
		Role:   identitycore.RoleAdmin,
		Status: billing.StatusActive,
	})

	got, err := s.repo.GetFirstAdmin(s.ctx)
	s.Require().NoError(err, "GetFirstAdmin")
	s.Require().Equal(activeAdmin.ID, got.ID, "should return only active admin")
}

// TestCRUD_And_Filters_And_AtomicUpdates combines user lookup, a masked
// username edit, sequential wallet changes, concurrency adjustment, and
// filtered listing. Reads reflect each asserted value and identify the active
// administrator target.
func (s *UserRepoSuite) TestCRUD_And_Filters_And_AtomicUpdates() {
	user1 := s.mustCreateUser(&identitycore.User{
		Email:    "a@example.com",
		Username: "Alice",
		Role:     identitycore.RoleUser,
		Status:   billing.StatusActive,
		Balance:  10,
	})
	user2 := s.mustCreateUser(&identitycore.User{
		Email:    "b@example.com",
		Username: "Bob",
		Role:     identitycore.RoleAdmin,
		Status:   billing.StatusActive,
		Balance:  1,
	})
	s.mustCreateUser(&identitycore.User{
		Email:  "c@example.com",
		Role:   identitycore.RoleAdmin,
		Status: billing.StatusDisabled,
	})

	got, err := s.repo.GetByID(s.ctx, user1.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().Equal(user1.Email, got.Email, "GetByID email mismatch")

	gotByEmail, err := s.repo.GetByEmail(s.ctx, user2.Email)
	s.Require().NoError(err, "GetByEmail")
	s.Require().Equal(user2.ID, gotByEmail.ID, "GetByEmail ID mismatch")

	got.Username = "Alice2"
	s.Require().NoError(s.repo.Update(s.ctx, got, identitycore.UserUpdateFields{Username: true}), "Update")
	got2, err := s.repo.GetByID(s.ctx, user1.ID)
	s.Require().NoError(err, "GetByID after update")
	s.Require().Equal("Alice2", got2.Username, "Update did not persist")

	s.Require().NoError(s.repo.UpdateBalance(s.ctx, user1.ID, 2.5), "UpdateBalance")
	got3, err := s.repo.GetByID(s.ctx, user1.ID)
	s.Require().NoError(err, "GetByID after UpdateBalance")
	// Compare this sequence of returned and persisted wallet amounts within a
	// micro-dollar difference.
	s.Require().InDelta(12.5, got3.Balance, 1e-6)

	deducted, err := s.repo.DeductBalance(s.ctx, user1.ID, 5)
	s.Require().NoError(err, "DeductBalance")
	s.Require().InDelta(5.0, deducted, 1e-6)
	got4, err := s.repo.GetByID(s.ctx, user1.ID)
	s.Require().NoError(err, "GetByID after DeductBalance")
	s.Require().InDelta(7.5, got4.Balance, 1e-6)

	deducted, err = s.repo.DeductBalance(s.ctx, user1.ID, 999)
	s.Require().NoError(err, "DeductBalance should clamp to remaining balance")
	s.Require().InDelta(7.5, deducted, 1e-6)
	gotOverdraft, err := s.repo.GetByID(s.ctx, user1.ID)
	s.Require().NoError(err, "GetByID after clamp")
	s.Require().InDelta(0.0, gotOverdraft.Balance, 1e-6, "Balance should clamp to zero")

	s.Require().NoError(s.repo.UpdateConcurrency(s.ctx, user1.ID, 3), "UpdateConcurrency")
	got5, err := s.repo.GetByID(s.ctx, user1.ID)
	s.Require().NoError(err, "GetByID after UpdateConcurrency")
	s.Require().Equal(user1.Concurrency+3, got5.Concurrency)

	params := pagination.PaginationParams{Page: 1, PageSize: 10}
	users, page, err := s.repo.ListWithFilters(s.ctx, params, identitycore.UserListFilters{Status: billing.StatusActive, Role: identitycore.RoleAdmin, Search: "b@"})
	s.Require().NoError(err, "ListWithFilters")
	s.Require().Equal(int64(1), page.Total, "ListWithFilters total mismatch")
	s.Require().Len(users, 1, "ListWithFilters len mismatch")
	s.Require().Equal(user2.ID, users[0].ID, "ListWithFilters result mismatch")
}

// TestUpdateBalance_NotFound applies a positive balance delta to a numeric ID
// absent from the fixture. The repository returns an error carrying
// UserNotFound for that requested row.
func (s *UserRepoSuite) TestUpdateBalance_NotFound() {
	err := s.repo.UpdateBalance(s.ctx, 999999, 10.0)
	s.Require().Error(err, "expected error for non-existent user")
	s.Require().ErrorIs(err, identitycore.ErrUserNotFound)
}

// TestUpdateConcurrency_NotFound applies a concurrency delta to a numeric ID
// absent from the fixture. The repository returns an error carrying
// UserNotFound for that requested storage row.
func (s *UserRepoSuite) TestUpdateConcurrency_NotFound() {
	err := s.repo.UpdateConcurrency(s.ctx, 999999, 5)
	s.Require().Error(err, "expected error for non-existent user")
	s.Require().ErrorIs(err, identitycore.ErrUserNotFound)
}

// TestDeductBalance_NotFound requests a deduction for a numeric user ID absent
// from the current fixture. The repository returns an error carrying the
// UserNotFound identity for that row.
func (s *UserRepoSuite) TestDeductBalance_NotFound() {
	_, err := s.repo.DeductBalance(s.ctx, 999999, 5)
	s.Require().Error(err, "expected error for non-existent user")
	s.Require().ErrorIs(err, identitycore.ErrUserNotFound)
}
