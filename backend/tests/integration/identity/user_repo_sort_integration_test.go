//go:build integration

package identity_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	dbprovider "github.com/TokenFlux/TokenRouter/ent/provider"
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// mustCreateProvider creates a sorting fixture and fills database-generated fields.
func mustCreateProvider(t *testing.T, client *dbent.Client, a *providercore.Record) *providercore.Record {
	t.Helper()
	ctx := context.Background()

	if a.Platform == "" {
		a.Platform = capability.PlatformAnthropic
	}
	if a.Type == "" {
		a.Type = capability.ProviderTypeOAuth
	}
	if a.Status == "" {
		a.Status = providercore.StatusActive
	}
	if a.Concurrency == 0 {
		a.Concurrency = 3
	}
	if a.Priority == 0 {
		a.Priority = 50
	}
	if !a.Schedulable {
		a.Schedulable = true
	}
	if a.Credentials == nil {
		a.Credentials = map[string]any{}
	}
	if a.Extra == nil {
		a.Extra = map[string]any{}
	}

	create := client.Provider.Create().
		SetName(a.Name).
		SetPlatform(a.Platform).
		SetType(a.Type).
		SetCredentials(a.Credentials).
		SetExtra(a.Extra).
		SetConcurrency(a.Concurrency).
		SetPriority(a.Priority).
		SetStatus(a.Status).
		SetSchedulable(a.Schedulable).
		SetErrorMessage(a.ErrorMessage)

	if a.ProxyID != nil {
		create.SetProxyID(*a.ProxyID)
	}
	if a.LastUsedAt != nil {
		create.SetLastUsedAt(*a.LastUsedAt)
	}
	if a.RateLimitedAt != nil {
		create.SetRateLimitedAt(*a.RateLimitedAt)
	}
	if a.RateLimitResetAt != nil {
		create.SetRateLimitResetAt(*a.RateLimitResetAt)
	}
	if a.OverloadUntil != nil {
		create.SetOverloadUntil(*a.OverloadUntil)
	}
	if a.SessionWindowStart != nil {
		create.SetSessionWindowStart(*a.SessionWindowStart)
	}
	if a.SessionWindowEnd != nil {
		create.SetSessionWindowEnd(*a.SessionWindowEnd)
	}
	if a.SessionWindowStatus != "" {
		create.SetSessionWindowStatus(a.SessionWindowStatus)
	}
	if !a.CreatedAt.IsZero() {
		create.SetCreatedAt(a.CreatedAt)
	}
	if !a.UpdatedAt.IsZero() {
		create.SetUpdatedAt(a.UpdatedAt)
	}
	if a.ParentProviderID != nil {
		create.SetParentProviderID(*a.ParentProviderID)
	}
	if a.QuotaDimension != "" {
		create.SetQuotaDimension(dbprovider.QuotaDimension(a.QuotaDimension))
	}

	created, err := create.Save(ctx)
	require.NoError(t, err, "create provider")

	a.ID = created.ID
	a.CreatedAt = created.CreatedAt
	a.UpdatedAt = created.UpdatedAt
	return a
}

func (s *UserRepoSuite) mustInsertUsageLog(userID int64, createdAt time.Time) {
	s.T().Helper()

	provider := mustCreateProvider(s.T(), s.client, &providercore.Record{Name: "usage-log-provider"})
	apiKey := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: userID})

	_, err := s.db.ExecContext(
		s.ctx,
		`INSERT INTO usage_logs (user_id, api_key_id, provider_id, model, input_tokens, output_tokens, total_cost, actual_cost, created_at)
		 VALUES ($1, $2, $3, 'gpt-test', 1, 1, 0.01, 0.01, $4)`,
		userID,
		apiKey.ID,
		provider.ID,
		createdAt.UTC(),
	)
	s.Require().NoError(err)
}

func (s *UserRepoSuite) TestListWithFilters_SortByEmailAsc() {
	s.mustCreateUser(&identity.User{Email: "z-last@example.com", Username: "z-user"})
	s.mustCreateUser(&identity.User{Email: "a-first@example.com", Username: "a-user"})

	users, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{
		Page:      1,
		PageSize:  10,
		SortBy:    "email",
		SortOrder: "asc",
	}, identity.UserListFilters{})
	s.Require().NoError(err)
	s.Require().Len(users, 2)
	s.Require().Equal("a-first@example.com", users[0].Email)
	s.Require().Equal("z-last@example.com", users[1].Email)
}

func (s *UserRepoSuite) TestList_DefaultSortByNewestFirst() {
	first := s.mustCreateUser(&identity.User{Email: "first@example.com"})
	second := s.mustCreateUser(&identity.User{Email: "second@example.com"})

	users, _, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10})
	s.Require().NoError(err)
	s.Require().Len(users, 2)
	s.Require().Equal(second.ID, users[0].ID)
	s.Require().Equal(first.ID, users[1].ID)
}

func (s *UserRepoSuite) TestCreateAndRead_PreservesSignupSourceAndActivityTimestamps() {
	lastLoginAt := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Microsecond)
	lastActiveAt := time.Now().Add(-30 * time.Minute).UTC().Truncate(time.Microsecond)

	created := s.mustCreateUser(&identity.User{
		Email:        "identity-meta@example.com",
		SignupSource: "linuxdo",
		LastLoginAt:  &lastLoginAt,
		LastActiveAt: &lastActiveAt,
	})

	got, err := s.repo.GetByID(s.ctx, created.ID)
	s.Require().NoError(err)
	s.Require().Equal("linuxdo", got.SignupSource)
	s.Require().NotNil(got.LastLoginAt)
	s.Require().NotNil(got.LastActiveAt)
	s.Require().True(got.LastLoginAt.Equal(lastLoginAt))
	s.Require().True(got.LastActiveAt.Equal(lastActiveAt))
}

func (s *UserRepoSuite) TestUpdate_PersistsSignupSourceAndActivityTimestamps() {
	created := s.mustCreateUser(&identity.User{Email: "identity-update@example.com"})
	lastLoginAt := time.Now().Add(-90 * time.Minute).UTC().Truncate(time.Microsecond)
	lastActiveAt := time.Now().Add(-15 * time.Minute).UTC().Truncate(time.Microsecond)

	created.SignupSource = "oidc"
	created.LastLoginAt = &lastLoginAt
	created.LastActiveAt = &lastActiveAt

	s.Require().NoError(s.repo.Update(s.ctx, created, identity.UserUpdateFields{SignupSource: true, LastLoginAt: true, LastActiveAt: true}))

	got, err := s.repo.GetByID(s.ctx, created.ID)
	s.Require().NoError(err)
	s.Require().Equal("oidc", got.SignupSource)
	s.Require().NotNil(got.LastLoginAt)
	s.Require().NotNil(got.LastActiveAt)
	s.Require().True(got.LastLoginAt.Equal(lastLoginAt))
	s.Require().True(got.LastActiveAt.Equal(lastActiveAt))
}

func (s *UserRepoSuite) TestListWithFilters_SortByLastActiveAtAsc() {
	earlier := time.Now().Add(-3 * time.Hour).UTC().Truncate(time.Microsecond)
	later := time.Now().Add(-45 * time.Minute).UTC().Truncate(time.Microsecond)

	s.mustCreateUser(&identity.User{Email: "nil-active@example.com"})
	s.mustCreateUser(&identity.User{Email: "later-active@example.com", LastActiveAt: &later})
	s.mustCreateUser(&identity.User{Email: "earlier-active@example.com", LastActiveAt: &earlier})

	users, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{
		Page:      1,
		PageSize:  10,
		SortBy:    "last_active_at",
		SortOrder: "asc",
	}, identity.UserListFilters{})
	s.Require().NoError(err)
	s.Require().Len(users, 3)
	s.Require().Equal("earlier-active@example.com", users[0].Email)
	s.Require().Equal("later-active@example.com", users[1].Email)
	s.Require().Equal("nil-active@example.com", users[2].Email)
}

func (s *UserRepoSuite) TestGetLatestUsedAtByUserIDs_UsesUsageLogs() {
	older := time.Now().Add(-4 * time.Hour).UTC().Truncate(time.Second)
	newer := time.Now().Add(-90 * time.Minute).UTC().Truncate(time.Second)

	userWithUsage := s.mustCreateUser(&identity.User{Email: "usage-source@example.com"})
	userWithoutUsage := s.mustCreateUser(&identity.User{Email: "usage-missing@example.com"})
	s.mustInsertUsageLog(userWithUsage.ID, older)
	s.mustInsertUsageLog(userWithUsage.ID, newer)

	got, err := s.repo.GetLatestUsedAtByUserIDs(s.ctx, []int64{userWithUsage.ID, userWithoutUsage.ID})
	s.Require().NoError(err)
	s.Require().Contains(got, userWithUsage.ID)
	s.Require().NotContains(got, userWithoutUsage.ID)
	s.Require().NotNil(got[userWithUsage.ID])
	s.Require().True(got[userWithUsage.ID].Equal(newer))
}

func (s *UserRepoSuite) TestListWithFilters_SortByLastUsedAtDesc_UsesUsageLogsNotLastActiveAt() {
	lastUsedOlder := time.Now().Add(-6 * time.Hour).UTC().Truncate(time.Second)
	lastUsedNewer := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	lastActiveVeryRecent := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Second)

	nilUsage := s.mustCreateUser(&identity.User{Email: "nil-last-used@example.com"})
	wrongSource := s.mustCreateUser(&identity.User{
		Email:        "active-not-usage@example.com",
		LastActiveAt: &lastActiveVeryRecent,
	})
	rightSource := s.mustCreateUser(&identity.User{Email: "usage-wins@example.com"})

	s.mustInsertUsageLog(wrongSource.ID, lastUsedOlder)
	s.mustInsertUsageLog(rightSource.ID, lastUsedNewer)

	users, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{
		Page:      1,
		PageSize:  10,
		SortBy:    "last_used_at",
		SortOrder: "desc",
	}, identity.UserListFilters{})
	s.Require().NoError(err)
	s.Require().Len(users, 3)
	s.Require().Equal(rightSource.ID, users[0].ID)
	s.Require().Equal(wrongSource.ID, users[1].ID)
	s.Require().Equal(nilUsage.ID, users[2].ID)
}

func TestUserRepoSortSuiteSmoke(_ *testing.T) {}
