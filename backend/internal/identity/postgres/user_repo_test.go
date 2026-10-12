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

func TestUserRepositoryExistsByEmailAlias(t *testing.T) {
	cases := []struct {
		name   string
		stored string
		probe  string
		want   bool
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

func TestUserRepositoryEmailAliasIgnoresMalformedInput(t *testing.T) {
	repo, _ := newUserEntRepo(t)
	seedAliasUser(t, repo, "someone@gmail.com")

	got, err := repo.ExistsByEmailAlias(context.Background(), "not-an-email")
	require.NoError(t, err)
	require.False(t, got)
}

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

func TestUserRepositoryCreateSerializesNormalizedEmailConflictsUnderConcurrency(t *testing.T) {
	repo, client := newUserEntRepo(t)
	ctx := context.Background()

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

	type createResult struct {
		err error
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

func TestUserRepositoryCreateWithRegistrationEmailGuardsRejectsSecondDomainAccount(t *testing.T) {
	repo, _ := newUserEntRepo(t)
	ctx := context.Background()

	first := &identity.User{Email: "first@custom.example.", Username: "first", PasswordHash: "hash", Role: identity.RoleUser, Status: identity.StatusActive}
	second := &identity.User{Email: "second@sub.custom.example", Username: "second", PasswordHash: "hash", Role: identity.RoleUser, Status: identity.StatusActive}
	require.NoError(t, repo.CreateWithRegistrationEmailGuards(ctx, first, "", "custom.example"))

	err := repo.CreateWithRegistrationEmailGuards(ctx, second, "", "sub.custom.example")
	require.ErrorIs(t, err, identity.ErrEmailDomainRegistrationLimit)
}

// TestNormalizedUserEmailSQLMatchesMigrationIndex guards query and expression-index consistency.
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

func TestApplyRedeemBalanceAdjustment_UsesAtomicFloor(t *testing.T) {
	repo, mock := newRedeemAdjustmentRepoMock(t)
	mock.ExpectExec(`UPDATE users SET balance = GREATEST\(balance \+ \$1, 0\), updated_at = NOW\(\) WHERE id = \$2 AND deleted_at IS NULL`).
		WithArgs(-7.0, int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.ApplyRedeemBalanceAdjustment(context.Background(), 42, -7))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyRedeemAdjustment_MissingUser(t *testing.T) {
	repo, mock := newRedeemAdjustmentRepoMock(t)
	mock.ExpectExec(`UPDATE users SET balance = GREATEST\(balance \+ \$1, 0\), updated_at = NOW\(\) WHERE id = \$2 AND deleted_at IS NULL`).
		WithArgs(-1.0, int64(404)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.ApplyRedeemBalanceAdjustment(context.Background(), 404, -1)
	require.ErrorIs(t, err, identity.ErrUserNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

// seedAliasUser creates a user for email alias tests.
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
