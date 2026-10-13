//go:build integration

package identity_test

import (
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// A saved profile snapshot can precede billing, account status, limit, or
// access-grant changes. The PostgreSQL suite applies those changes sequentially
// before a username-only edit, then checks that omitted fields retain their
// newer persisted values. Separate adjustment and replacement cases inspect
// returned balance changes, negative-result errors, and missing users.

// TestUpdate_DoesNotRevertConcurrentBalanceDeduction loads a profile before a
// sequential deduction and then saves its username. The name changes while the
// remaining stored balance stays at 0.05.
func (s *UserRepoSuite) TestUpdate_DoesNotRevertConcurrentBalanceDeduction() {
	user := s.mustCreateUser(&identity.User{
		Email:    "lost-update-balance@example.com",
		Username: "before",
		Balance:  0.30,
	})

	// Capture the balance before the deduction.
	stale, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err, "GetByID")
	// A small numeric tolerance compares pre-deduction and retained wallet values.
	s.Require().InDelta(0.30, stale.Balance, 1e-9)

	// Apply the deduction after loading the profile so the later username-only
	// save carries a stale balance and can expose an unselected-column overwrite.
	_, err = s.repo.DeductBalance(s.ctx, user.ID, 0.25)
	s.Require().NoError(err, "DeductBalance")

	// Save the profile using the stale snapshot.
	stale.Username = "after"
	s.Require().NoError(
		s.repo.Update(s.ctx, stale, identity.UserUpdateFields{Username: true}),
		"Update",
	)

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err, "GetByID after update")
	s.Require().Equal("after", got.Username, "declared column must still be written")
	s.Require().InDelta(0.05, got.Balance, 1e-9, "balance must not be reverted by a stale profile save")
}

// TestUpdate_DoesNotRevertConcurrentBan loads a profile before a separate
// disabled-status write, then saves its username. Reloading reflects the new
// name and retains the stored disabled account state.
func (s *UserRepoSuite) TestUpdate_DoesNotRevertConcurrentBan() {
	user := s.mustCreateUser(&identity.User{
		Email:    "lost-update-ban@example.com",
		Username: "before",
		Status:   billing.StatusActive,
	})

	stale, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().Equal(billing.StatusActive, stale.Status)

	banned, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err, "GetByID for ban")
	banned.Status = billing.StatusDisabled
	s.Require().NoError(
		s.repo.Update(s.ctx, banned, identity.UserUpdateFields{Status: true}),
		"ban",
	)

	stale.Username = "after"
	s.Require().NoError(
		s.repo.Update(s.ctx, stale, identity.UserUpdateFields{Username: true}),
		"stale profile save",
	)

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err, "GetByID after update")
	s.Require().Equal("after", got.Username)
	s.Require().Equal(billing.StatusDisabled, got.Status, "ban must survive a stale profile save")
}

// TestUpdate_DoesNotRevertConcurrentLimitChanges loads a profile before a
// sequential batch update of concurrency and RPM limits. A later username-only
// save retains both newly stored limit values.
func (s *UserRepoSuite) TestUpdate_DoesNotRevertConcurrentLimitChanges() {
	user := s.mustCreateUser(&identity.User{
		Email:       "lost-update-limits@example.com",
		Username:    "before",
		Concurrency: 3,
		RPMLimit:    30,
	})

	stale, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err, "GetByID")

	concurrency, rpmLimit := 9, 90
	affected, err := s.repo.BatchUpdateLimits(s.ctx, []int64{user.ID}, &concurrency, &rpmLimit)
	s.Require().NoError(err, "BatchUpdateLimits")
	s.Require().Equal(1, affected)

	stale.Username = "after"
	s.Require().NoError(
		s.repo.Update(s.ctx, stale, identity.UserUpdateFields{Username: true}),
		"stale profile save",
	)

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err, "GetByID after update")
	s.Require().Equal(9, got.Concurrency, "concurrency must not be reverted")
	s.Require().Equal(90, got.RPMLimit, "rpm limit must not be reverted")
}

// TestUpdate_DoesNotRevertConcurrentAllowedGroupGrant captures a profile before
// granting group access. Saving its username with AllowedGroups omitted retains
// the granted group in the reloaded account.
func (s *UserRepoSuite) TestUpdate_DoesNotRevertConcurrentAllowedGroupGrant() {
	group := s.mustCreateGroup("lost-update-group")
	user := s.mustCreateUser(&identity.User{
		Email:    "lost-update-groups@example.com",
		Username: "before",
	})

	stale, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().Empty(stale.AllowedGroups)

	s.Require().NoError(s.repo.AddGroupToAllowedGroups(s.ctx, user.ID, group.ID), "AddGroupToAllowedGroups")

	stale.Username = "after"
	s.Require().NoError(
		s.repo.Update(s.ctx, stale, identity.UserUpdateFields{Username: true}),
		"stale profile save",
	)

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err, "GetByID after update")
	s.Require().Equal([]int64{group.ID}, got.AllowedGroups, "granted group must not be reverted")
}

// TestAdjustBalance_AppliesDeltaAndReportsChange adds and subtracts five
// through sequential balance adjustments. Returned old and new amounts follow
// each write, and reloading returns the final balance of ten.
func (s *UserRepoSuite) TestAdjustBalance_AppliesDeltaAndReportsChange() {
	user := s.mustCreateUser(&identity.User{Email: "adjust-balance@example.com", Balance: 10})

	change, err := s.repo.AdjustBalance(s.ctx, user.ID, 5)
	s.Require().NoError(err, "AdjustBalance add")
	// Compare each reported wallet transition using the same small numeric
	// tolerance.
	s.Require().InDelta(10, change.Old, 1e-9)
	s.Require().InDelta(15, change.New, 1e-9)

	change, err = s.repo.AdjustBalance(s.ctx, user.ID, -5)
	s.Require().NoError(err, "AdjustBalance subtract")
	s.Require().InDelta(15, change.Old, 1e-9)
	s.Require().InDelta(10, change.New, 1e-9)

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().InDelta(10, got.Balance, 1e-9)
}

// TestAdjustBalance_RefusesNegativeResult requests a deduction exceeding the
// available balance. The error reports current and attempted amounts, while
// reloading shows that the stored balance remains three.
func (s *UserRepoSuite) TestAdjustBalance_RefusesNegativeResult() {
	user := s.mustCreateUser(&identity.User{Email: "adjust-balance-negative@example.com", Balance: 3})

	change, err := s.repo.AdjustBalance(s.ctx, user.ID, -4)
	s.Require().ErrorIs(err, identity.ErrBalanceNegative)
	// The small tolerance compares returned current and attempted balance amounts.
	s.Require().InDelta(3, change.Old, 1e-9, "error must report the real current balance")
	s.Require().InDelta(-1, change.New, 1e-9)

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().InDelta(3, got.Balance, 1e-9, "refused adjustment must not write")
}

// TestAdjustBalance_UserNotFound requests a positive adjustment for a numeric
// user ID absent from the suite fixture. The repository returns the
// UserNotFound error identity for that operation.
func (s *UserRepoSuite) TestAdjustBalance_UserNotFound() {
	_, err := s.repo.AdjustBalance(s.ctx, 99999999, 1)
	s.Require().ErrorIs(err, identity.ErrUserNotFound)
}

// TestSetBalance_ReplacesValueAndReportsPrevious replaces seven with two
// through the repository. Its returned change identifies both amounts, and the
// reloaded user reflects the stored replacement balance.
func (s *UserRepoSuite) TestSetBalance_ReplacesValueAndReportsPrevious() {
	user := s.mustCreateUser(&identity.User{Email: "set-balance@example.com", Balance: 7})

	change, err := s.repo.SetBalance(s.ctx, user.ID, 2)
	s.Require().NoError(err, "SetBalance")
	// Use a small numeric tolerance for the reported replacement and saved amount.
	s.Require().InDelta(7, change.Old, 1e-9)
	s.Require().InDelta(2, change.New, 1e-9)

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().InDelta(2, got.Balance, 1e-9)
}

// TestSetBalance_RejectsNegativeValue requests a negative replacement for a
// user whose balance is seven. The repository returns BalanceNegative and a
// later read retains the preceding stored amount.
func (s *UserRepoSuite) TestSetBalance_RejectsNegativeValue() {
	user := s.mustCreateUser(&identity.User{Email: "set-balance-negative@example.com", Balance: 7})

	_, err := s.repo.SetBalance(s.ctx, user.ID, -1)
	s.Require().ErrorIs(err, identity.ErrBalanceNegative)

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err, "GetByID")
	// A small numeric tolerance checks the untouched wallet after rejected
	// replacement.
	s.Require().InDelta(7, got.Balance, 1e-9)
}

// TestSetBalance_UserNotFound requests a replacement amount for a numeric user
// ID absent from the suite fixture. The repository returns UserNotFound for
// that missing storage row.
func (s *UserRepoSuite) TestSetBalance_UserNotFound() {
	_, err := s.repo.SetBalance(s.ctx, 99999999, 1)
	s.Require().ErrorIs(err, identity.ErrUserNotFound)
}
