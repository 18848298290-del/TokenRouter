package postgres

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// SQLite account tests compare normalized email lookup, mailbox alias probes,
// registration-domain guards, and two-caller creation results. SQL mocks inspect
// redeem adjustment expressions and missing-row results, while a
// migration-text assertion compares the normalized email expression after
// whitespace removal.

// TestUserRepositoryExistsByEmailAlias compares stored and probed addresses
// across Gmail, plus suffixes, root dots, literal wildcards, and distinct
// mailboxes. It checks the expected alias match for each fixture.
func TestUserRepositoryExistsByEmailAlias(t *testing.T) {
	cases := []struct {
		name   string // email alias fixture case
		stored string // stored account email text
		probe  string // queried mailbox email text
		want   bool   // expected mailbox match flag
	}{
		{name: "same address", stored: "someone@gmail.com", probe: "someone@gmail.com", want: true},
		{name: "gmail plus alias", stored: "someone@gmail.com", probe: "someone+bulk@gmail.com", want: true},
		{name: "gmail dot alias", stored: "d.axis.2026@gmail.com", probe: "daxis2026@gmail.com", want: true},
		{name: "googlemail alias", stored: "someone@googlemail.com", probe: "some.one@gmail.com", want: true},
		{name: "root dot", stored: "d.axis.2026@gmail.com.", probe: "daxis2026@gmail.com", want: true},
		{name: "legacy spacing", stored: "  D.Axis.2026@Gmail.com  ", probe: "daxis2026@gmail.com", want: true},
		{name: "non gmail plus alias", stored: "first.last@qq.com", probe: "first.last+tag@qq.com", want: true},
		{name: "different inbox", stored: "someone@gmail.com", probe: "someoneelse@gmail.com", want: false},
		{name: "non gmail dots significant", stored: "first.last@qq.com", probe: "firstlast@qq.com", want: false},
		{name: "different domain", stored: "someone@gmail.com", probe: "someone@qq.com", want: false},
		{name: "leading plus locals distinct", stored: "+alice@gmail.com", probe: "+bob@gmail.com", want: false},
		{name: "underscore is literal", stored: "user_x@qq.com", probe: "userax@qq.com", want: false},
		{name: "percent is literal", stored: "a%b@qq.com", probe: "axxb@qq.com", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, _ := newUserEntRepo(t)
			seedAliasUser(t, repo, tc.stored)

			got, err := repo.ExistsByEmailAlias(context.Background(), tc.probe)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestUserRepositoryEmailAliasOwnerPrefersOtherUser stores two aliases for one
// mailbox and identifies the caller as one owner. It checks that another owner
// wins the lookup so a binding conflict can be detected.
func TestUserRepositoryEmailAliasOwnerPrefersOtherUser(t *testing.T) {
	repo, _ := newUserEntRepo(t)
	ctx := context.Background()
	self := seedAliasUser(t, repo, "inbox+own@gmail.com")
	other := seedAliasUser(t, repo, "inbox+legacy@gmail.com")

	ownerID, exists, err := repo.EmailAliasOwnerID(ctx, "inbox+new@gmail.com", self.ID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, other.ID, ownerID)
}

// TestUserRepositoryEmailAliasIgnoresMalformedInput probes malformed email
// text beside one valid stored Gmail account. It checks a false alias match
// and no lookup error for that unsupported input.
func TestUserRepositoryEmailAliasIgnoresMalformedInput(t *testing.T) {
	repo, _ := newUserEntRepo(t)
	seedAliasUser(t, repo, "someone@gmail.com")

	got, err := repo.ExistsByEmailAlias(context.Background(), "not-an-email")
	require.NoError(t, err)
	require.False(t, got)
}

// TestUserRepositoryGetByEmailNormalizesLegacySpacingAndCase stores an email
// containing surrounding spaces and mixed case. It checks normalized lookup
// success and retention of the stored email text in the returned account.
func TestUserRepositoryGetByEmailNormalizesLegacySpacingAndCase(t *testing.T) {
	repo, _ := newUserEntRepo(t)
	ctx := context.Background()

	err := repo.Create(ctx, &identity.User{
		Email:        " Legacy@Example.com ",
		Username:     "legacy-user",
		PasswordHash: "hash",
		Role:         identity.RoleUser,
		Status:       identity.StatusActive,
	})
	require.NoError(t, err)

	got, err := repo.GetByEmail(ctx, "legacy@example.com")
	require.NoError(t, err)
	require.Equal(t, " Legacy@Example.com ", got.Email)
}

// TestUserRepositoryExistsByEmailNormalizesLegacySpacingAndCase stores a
// spaced mixed-case email and probes another spelling of that address. It
// checks that exact normalized lookup reports an existing account.
func TestUserRepositoryExistsByEmailNormalizesLegacySpacingAndCase(t *testing.T) {
	repo, _ := newUserEntRepo(t)
	ctx := context.Background()

	err := repo.Create(ctx, &identity.User{
		Email:        " Legacy@Example.com ",
		Username:     "legacy-user",
		PasswordHash: "hash",
		Role:         identity.RoleUser,
		Status:       identity.StatusActive,
	})
	require.NoError(t, err)

	exists, err := repo.ExistsByEmail(ctx, "  LEGACY@example.com  ")
	require.NoError(t, err)
	require.True(t, exists)
}

// TestUserRepositoryCreateRejectsNormalizedEmailDuplicate creates one spaced
// mixed-case account before a normalized duplicate. It checks ErrEmailExists
// for the second repository create using the same normalized address.
func TestUserRepositoryCreateRejectsNormalizedEmailDuplicate(t *testing.T) {
	repo, _ := newUserEntRepo(t)
	ctx := context.Background()

	err := repo.Create(ctx, &identity.User{
		Email:        " Existing@Example.com ",
		Username:     "existing-user",
		PasswordHash: "hash",
		Role:         identity.RoleUser,
		Status:       identity.StatusActive,
	})
	require.NoError(t, err)

	err = repo.Create(ctx, &identity.User{
		Email:        "existing@example.com",
		Username:     "duplicate-user",
		PasswordHash: "hash",
		Role:         identity.RoleUser,
		Status:       identity.StatusActive,
	})
	require.ErrorIs(t, err, identity.ErrEmailExists)
}

// TestUserRepositoryUpdateRejectsNormalizedEmailDuplicate creates two accounts
// then requests the first normalized address on the second. It checks
// ErrEmailExists when the guarded update attempts that duplicate.
func TestUserRepositoryUpdateRejectsNormalizedEmailDuplicate(t *testing.T) {
	repo, _ := newUserEntRepo(t)
	ctx := context.Background()

	first := &identity.User{
		Email:        " Existing@Example.com ",
		Username:     "existing-user",
		PasswordHash: "hash",
		Role:         identity.RoleUser,
		Status:       identity.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, first))

	second := &identity.User{
		Email:        "second@example.com",
		Username:     "second-user",
		PasswordHash: "hash",
		Role:         identity.RoleUser,
		Status:       identity.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, second))

	second.Email = " existing@example.com "
	err := repo.Update(ctx, second, identity.UserUpdateFields{Email: true})
	require.ErrorIs(t, err, identity.ErrEmailExists)
}

// TestUserRepositoryGetByEmailReportsNormalizedEmailConflict seeds two
// conflicting email rows directly through Ent before repository lookup. It
// checks the multiple-user normalized-email error instead of choosing one row.
func TestUserRepositoryGetByEmailReportsNormalizedEmailConflict(t *testing.T) {
	repo, client := newUserEntRepo(t)
	ctx := context.Background()

	_, err := client.User.Create().
		SetEmail("Conflict@Example.com").
		SetUsername("conflict-user-1").
		SetPasswordHash("hash").
		SetRole(identity.RoleUser).
		SetStatus(identity.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.User.Create().
		SetEmail(" conflict@example.com ").
		SetUsername("conflict-user-2").
		SetPasswordHash("hash").
		SetRole(identity.RoleUser).
		SetStatus(identity.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	_, err = repo.GetByEmail(ctx, "conflict@example.com")
	require.Error(t, err)
	require.ErrorContains(t, err, "normalized email lookup matched multiple users")
}

// TestUserRepositoryCreateSerializesNormalizedEmailConflictsUnderConcurrency
// gates one create while another call uses the same normalized address. It
// checks one success, one ErrEmailExists result, and one stored matching
// account.
func TestUserRepositoryCreateSerializesNormalizedEmailConflictsUnderConcurrency(t *testing.T) {
	repo, client := newUserEntRepo(t)
	ctx := context.Background()

	// Pause the first create after entering mutation and start the other caller
	// against the same normalized email. Collect both results after release to
	// inspect storage serialization.
	firstCreateStarted := make(chan struct{})
	releaseFirstCreate := make(chan struct{})
	var firstCreate sync.Once
	client.User.Use(func(next dbent.Mutator) dbent.Mutator {
		return dbent.MutateFunc(func(ctx context.Context, m dbent.Mutation) (dbent.Value, error) {
			blocked := false
			if m.Op().Is(dbent.OpCreate) {
				firstCreate.Do(func() {
					blocked = true
					close(firstCreateStarted)
				})
			}
			if blocked {
				<-releaseFirstCreate
			}
			return next.Mutate(ctx, m)
		})
	})

	// createResult carries one concurrent account-create failure back to the
	// assertion goroutine.
	type createResult struct {
		err error // returned account creation failure
	}

	results := make(chan createResult, 2)
	go func() {
		results <- createResult{err: repo.Create(ctx, &identity.User{
			Email:        " Race@Example.com ",
			Username:     "race-user-1",
			PasswordHash: "hash",
			Role:         identity.RoleUser,
			Status:       identity.StatusActive,
		})}
	}()

	<-firstCreateStarted

	go func() {
		results <- createResult{err: repo.Create(ctx, &identity.User{
			Email:        "race@example.com",
			Username:     "race-user-2",
			PasswordHash: "hash",
			Role:         identity.RoleUser,
			Status:       identity.StatusActive,
		})}
	}()

	// Give the second caller time to be scheduled before releasing the first
	// create. This delay supplies overlap opportunity, while row counts and
	// returned errors establish the observed result.
	time.Sleep(100 * time.Millisecond)
	close(releaseFirstCreate)

	first := <-results
	second := <-results

	errors := []error{first.err, second.err}
	successes := 0
	conflicts := 0
	for _, err := range errors {
		switch err {
		case nil:
			successes++
		case identity.ErrEmailExists:
			conflicts++
		default:
			t.Fatalf("unexpected create error: %v", err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)

	count, err := client.User.Query().Where(IdentityUserEmailLookupPredicate("race@example.com")).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

// TestUserRepositoryCountUsersByEmailDomain stores root-domain, subdomain, and
// unrelated accounts before a subdomain probe. It checks that
// registrable-domain counting includes the root and its subdomain together.
func TestUserRepositoryCountUsersByEmailDomain(t *testing.T) {
	repo, _ := newUserEntRepo(t)
	ctx := context.Background()

	for index, email := range []string{"first@custom.example", "second@sub.custom.example", "other@example.com"} {
		require.NoError(t, repo.Create(ctx, &identity.User{
			Email:        email,
			Username:     fmt.Sprintf("domain-user-%d", index),
			PasswordHash: "hash",
			Role:         identity.RoleUser,
			Status:       identity.StatusActive,
		}))
	}

	count, err := repo.CountUsersByEmailDomain(ctx, "sub.custom.example")
	require.NoError(t, err)
	require.Equal(t, 2, count)
}

// TestUserRepositoryCountUsersByEmailDomainIgnoresDeletedAndEscapesWildcards
// stores literal-underscore domain accounts plus a similar wildcard-looking
// domain and deletes one match. It checks one current literal-domain account
// in the count.
func TestUserRepositoryCountUsersByEmailDomainIgnoresDeletedAndEscapesWildcards(t *testing.T) {
	repo, _ := newUserEntRepo(t)
	ctx := context.Background()

	active := &identity.User{Email: "active@foo_bar.com", Username: "active", PasswordHash: "hash", Role: identity.RoleUser, Status: identity.StatusActive}
	deleted := &identity.User{Email: "deleted@foo_bar.com", Username: "deleted", PasswordHash: "hash", Role: identity.RoleUser, Status: identity.StatusActive}
	other := &identity.User{Email: "other@fooxbar.com", Username: "other", PasswordHash: "hash", Role: identity.RoleUser, Status: identity.StatusActive}
	require.NoError(t, repo.Create(ctx, active))
	require.NoError(t, repo.Create(ctx, deleted))
	require.NoError(t, repo.Create(ctx, other))
	require.NoError(t, repo.Delete(ctx, deleted.ID))

	count, err := repo.CountUsersByEmailDomain(ctx, "foo_bar.com")
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

// TestUserRepositoryCreateWithRegistrationEmailGuardsRejectsSecondDomainAccount
// creates one custom-domain account then attempts its subdomain with the
// registration guard. It checks the shared registrable-domain quota error on
// the second account.
func TestUserRepositoryCreateWithRegistrationEmailGuardsRejectsSecondDomainAccount(t *testing.T) {
	repo, _ := newUserEntRepo(t)
	ctx := context.Background()

	first := &identity.User{Email: "first@custom.example.", Username: "first", PasswordHash: "hash", Role: identity.RoleUser, Status: identity.StatusActive}
	second := &identity.User{Email: "second@sub.custom.example", Username: "second", PasswordHash: "hash", Role: identity.RoleUser, Status: identity.StatusActive}
	require.NoError(t, repo.CreateWithRegistrationEmailGuards(ctx, first, "", "custom.example"))

	err := repo.CreateWithRegistrationEmailGuards(ctx, second, "", "sub.custom.example")
	require.ErrorIs(t, err, identity.ErrEmailDomainRegistrationLimit)
}

// TestNormalizedUserEmailSQLMatchesMigrationIndex loads the published
// normalized-email index migration and removes whitespace from it and the
// query expression. It checks inclusion of the complete current query
// expression in that migration.
func TestNormalizedUserEmailSQLMatchesMigrationIndex(t *testing.T) {
	content, err := migrations.FS.ReadFile("220_users_registration_email_normalized_index_notx.sql")
	require.NoError(t, err)

	stripWhitespace := func(value string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, value)
	}
	compactMigration := stripWhitespace(string(content))
	compactQueryExpression := stripWhitespace(IdentityNormalizedUserEmailSQL)
	require.Contains(t, compactMigration, compactQueryExpression)
}

// TestApplyRedeemBalanceAdjustment_UsesAtomicFloor submits a negative redeem
// amount through the account facade with a SQL mock. It checks the zero-floor
// expression, current-row filter, arguments, and completed expectation.
func TestApplyRedeemBalanceAdjustment_UsesAtomicFloor(t *testing.T) {
	repo, mock := newRedeemAdjustmentRepoMock(t)
	mock.ExpectExec(`UPDATE users SET balance = GREATEST\(balance \+ \$1, 0\), updated_at = NOW\(\) WHERE id = \$2 AND deleted_at IS NULL`).
		WithArgs(-7.0, int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.ApplyRedeemBalanceAdjustment(context.Background(), 42, -7))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestApplyRedeemAdjustment_MissingUser returns zero affected rows for a
// mocked redeem balance adjustment. It checks ErrUserNotFound and completed
// SQL expectations for the missing-account result.
func TestApplyRedeemAdjustment_MissingUser(t *testing.T) {
	repo, mock := newRedeemAdjustmentRepoMock(t)
	mock.ExpectExec(`UPDATE users SET balance = GREATEST\(balance \+ \$1, 0\), updated_at = NOW\(\) WHERE id = \$2 AND deleted_at IS NULL`).
		WithArgs(-1.0, int64(404)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.ApplyRedeemBalanceAdjustment(context.Background(), 404, -1)
	require.ErrorIs(t, err, identity.ErrUserNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

// seedAliasUser creates an active account with the supplied email for alias
// probes. Repository creation errors fail the test, and the returned account
// contains its stored ID and timestamps.
func seedAliasUser(t *testing.T, repo *UserStore, email string) *identity.User {
	t.Helper()
	user := &identity.User{
		Email:        email,
		Username:     email,
		PasswordHash: "hash",
		Role:         identity.RoleUser,
		Status:       identity.StatusActive,
	}
	require.NoError(t, repo.Create(context.Background(), user))
	return user
}
