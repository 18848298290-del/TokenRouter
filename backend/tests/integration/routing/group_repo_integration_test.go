//go:build integration

package routing_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
)

// Group storage combines persisted group settings with provider-link counts.
// The suite uses one migrated PostgreSQL client and separate test transactions,
// allowing direct SQL fixtures to exercise search, duplication, scheduling
// eligibility, and soft-deleted visibility while requesting rollback after each
// case.

// GroupRepoSuite runs group-storage assertions against per-test PostgreSQL transactions.
type GroupRepoSuite struct {
	suite.Suite // assertion and subtest helpers
	// client is created once for the suite; routingDatabase registers cleanup
	// of its underlying SQL pool and dedicated PostgreSQL container.
	client *dbent.Client
	// ctx, tx, and repo are replaced before each case. repo uses tx's client and
	// SQL executor, so its writes and the test's direct fixtures share that transaction.
	ctx  context.Context             // per-test operation context
	tx   *dbent.Tx                   // per-test database transaction
	repo *routingpostgres.GroupStore // transaction-bound group persistence
}

// forbidSQLExecutor detects auxiliary SQL access during the lite-lookup scenario.
type forbidSQLExecutor struct {
	// called becomes true after either executor method runs. The lite-lookup
	// test inspects this flag to detect an unexpected provider-count query path.
	called bool
}

// ExecContext records any auxiliary SQL write as unexpected and returns the
// fixture error. The supplied context, query, and arguments are ignored, so the
// caller receives no database result.
func (s *forbidSQLExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	s.called = true
	return nil, errors.New("unexpected sql exec")
}

// QueryContext records any auxiliary SQL query as unexpected and returns the
// fixture error. It ignores the supplied context, statement, and arguments, so
// callers receive no rows to consume.
func (s *forbidSQLExecutor) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	s.called = true
	return nil, errors.New("unexpected sql query")
}

// SetupTest gives each suite case a fresh PostgreSQL transaction and group
// storage using its client and executor. The transaction helper registers rollback
// with the current test for disposal after assertions finish.
func (s *GroupRepoSuite) SetupTest() {
	s.ctx = context.Background()
	tx := s.transaction(s.T())
	s.tx = tx
	s.repo = newGroupStoreFixture(tx.Client(), tx)
}

// TestGroupRepoSuite runs PostgreSQL group-storage scenarios in transactions
// registered for cleanup after each case. The cases protect record visibility,
// provider counts, search, duplication, ordering, and stored protocol settings.
func TestGroupRepoSuite(t *testing.T) {
	suite.Run(t, new(GroupRepoSuite))
}

// TestCreate stores an active group through the assembled PostgreSQL repository.
// The supplied value must receive an ID, and subsequent lookup must return its
// saved group name.
func (s *GroupRepoSuite) TestCreate() {
	group := &routing.Group{
		Name: "test-create",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformOpenAI),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformOpenAI),
		ResponsesImagePolicy: "inherit",
	}

	err := s.repo.Create(s.ctx, group)
	s.Require().NoError(err, "Create")
	s.Require().NotZero(group.ID, "expected ID to be set")

	got, err := s.repo.GetByID(s.ctx, group.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().Equal("test-create", got.Name)
}

// TestCreateFromSourcePreservesPriorityAndFiltersIneligibleProviders duplicates
// an OAuth-only group with OAuth, API-key, and deleted provider links. The copy
// must contain the eligible OAuth link, be recoverable by operation ID, and have
// one scheduler outbox row.
func (s *GroupRepoSuite) TestCreateFromSourcePreservesPriorityAndFiltersIneligibleProviders() {
	source := &routing.Group{
		Name: "duplicate-source",

		RateMultiplier:   1,
		Status:           billing.StatusActive,
		RequireOAuthOnly: true,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformOpenAI),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformOpenAI),
		ResponsesImagePolicy: "inherit",
	}
	s.Require().NoError(s.repo.Create(s.ctx, source))

	// insertProvider writes one provider and returns its generated ID for source
	// group links. providerType supplies the auth type for the duplicate's API-key
	// filter; deleted sets deleted_at while leaving membership setup to the caller.
	insertProvider := func(name, providerType string, deleted bool) int64 {
		var id int64
		deletedAt := any(nil)
		// Pre-mark this provider deleted in its insert, then retain its source
		// membership below so duplication has a stale provider link to filter.
		if deleted {
			deletedAt = "2026-07-16T00:00:00Z"
		}
		s.Require().NoError(postgresinfra.ScanSingleRow(
			s.ctx,
			s.tx,
			"INSERT INTO providers (name, platform, type, deleted_at) VALUES ($1, $2, $3, $4) RETURNING id",
			[]any{name, capability.PlatformOpenAI, providerType, deletedAt},
			&id,
		))
		return id
	}
	oauthID := insertProvider("duplicate-oauth", capability.ProviderTypeOAuth, false)
	apiKeyID := insertProvider("duplicate-apikey", capability.ProviderTypeAPIKey, false)
	deletedID := insertProvider("duplicate-deleted", capability.ProviderTypeOAuth, true)
	for _, providerID := range []int64{oauthID, apiKeyID, deletedID} {
		_, err := s.tx.ExecContext(
			s.ctx,
			"INSERT INTO provider_groups (provider_id, group_id, created_at) VALUES ($1, $2, NOW())",
			providerID,
			source.ID,
		)
		s.Require().NoError(err)
	}

	duplicate := &routing.Group{
		Name: "duplicate-source (Copy)",

		RateMultiplier:       source.RateMultiplier,
		Status:               "inactive",
		RequireOAuthOnly:     true,
		DuplicateOperationID: strings.Repeat("a", 64),

		AllowedProtocols:     capability.DefaultGroupClientProtocols(""),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(""),
		ResponsesImagePolicy: "inherit",
	}
	s.Require().NoError(s.repo.CreateFromSource(s.ctx, duplicate, source.ID))
	s.Require().EqualValues(1, duplicate.ProviderCount)

	rows, err := s.tx.QueryContext(
		s.ctx,
		"SELECT provider_id FROM provider_groups WHERE group_id = $1 ORDER BY provider_id",
		duplicate.ID,
	)
	s.Require().NoError(err)
	// Close the inspection on early assertion exit so an unfinished result is
	// released before this test's registered transaction rollback starts.
	defer func() { _ = rows.Close() }()
	s.Require().True(rows.Next())
	var copiedProviderID int64
	s.Require().NoError(rows.Scan(&copiedProviderID))
	s.Require().Equal(oauthID, copiedProviderID)
	s.Require().False(rows.Next(), "API-key and soft-deleted providers must not be copied")

	recovered, err := s.repo.FindByDuplicateOperationID(s.ctx, duplicate.DuplicateOperationID)
	s.Require().NoError(err)
	s.Require().Equal(duplicate.ID, recovered.ID)

	var outboxCount int
	s.Require().NoError(postgresinfra.ScanSingleRow(
		s.ctx,
		s.tx,
		"SELECT COUNT(*) FROM scheduler_outbox WHERE group_id = $1",
		[]any{duplicate.ID},
		&outboxCount,
	))
	s.Require().Equal(1, outboxCount)
}

// TestGetByID_NotFound looks up an ID absent from the migrated PostgreSQL fixture.
// The repository must return an error matching ErrGroupNotFound for that missing
// group.
func (s *GroupRepoSuite) TestGetByID_NotFound() {
	_, err := s.repo.GetByID(s.ctx, 999999)
	s.Require().Error(err, "expected error for non-existent ID")
	s.Require().ErrorIs(err, routing.ErrGroupNotFound)
}

// TestGetByIDLite_DoesNotUseProviderCount loads a saved group through Ent while
// replacing the auxiliary SQL executor with a failing spy. The lookup must succeed
// and leave that spy unused.
func (s *GroupRepoSuite) TestGetByIDLite_DoesNotUseProviderCount() {
	group := &routing.Group{
		Name: "lite-group",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	s.Require().NoError(s.repo.Create(s.ctx, group))

	spy := &forbidSQLExecutor{}
	repo := newGroupStoreFixture(s.tx.Client(), spy)

	got, err := repo.GetByIDLite(s.ctx, group.ID)
	s.Require().NoError(err)
	s.Require().Equal(group.ID, got.ID)
	s.Require().False(spy.called, "expected no direct sql executor usage")
}

// TestUpdate changes the name of a previously stored group through the repository.
// A subsequent detail lookup must return the updated name for the same persisted
// record.
func (s *GroupRepoSuite) TestUpdate() {
	group := &routing.Group{
		Name: "original",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	s.Require().NoError(s.repo.Create(s.ctx, group))

	group.Name = "updated"
	err := s.repo.Update(s.ctx, group)
	s.Require().NoError(err, "Update")

	got, err := s.repo.GetByID(s.ctx, group.ID)
	s.Require().NoError(err, "GetByID after update")
	s.Require().Equal("updated", got.Name)
}

// TestDelete creates a group and deletes it through the repository's normal
// operation. Detail lookup must then return an error matching ErrGroupNotFound
// for that group's ID.
func (s *GroupRepoSuite) TestDelete() {
	group := &routing.Group{
		Name: "to-delete",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	s.Require().NoError(s.repo.Create(s.ctx, group))

	err := s.repo.Delete(s.ctx, group.ID)
	s.Require().NoError(err, "Delete")

	_, err = s.repo.GetByID(s.ctx, group.ID)
	s.Require().Error(err, "expected error after delete")
	s.Require().ErrorIs(err, routing.ErrGroupNotFound)
}

// TestList adds two groups after reading the fixture's baseline page and count.
// The same list query must include two additional rows and increase its total by
// two.
func (s *GroupRepoSuite) TestList() {
	baseGroups, basePage, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10})
	s.Require().NoError(err, "List base")

	s.Require().NoError(s.repo.Create(s.ctx, &routing.Group{
		Name: "g1",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}))
	s.Require().NoError(s.repo.Create(s.ctx, &routing.Group{
		Name: "g2",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}))

	groups, page, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10})
	s.Require().NoError(err, "List")
	s.Require().Len(groups, len(baseGroups)+2)
	s.Require().Equal(basePage.Total+2, page.Total)
}

// TestListWithFiltersIncludesAllGroups creates groups with different protocol
// configurations and leaves every filter empty. The filtered list must contain
// two additional rows compared with its initial fixture page.
func (s *GroupRepoSuite) TestListWithFiltersIncludesAllGroups() {
	baseGroups, _, err := s.repo.ListWithFilters(
		s.ctx,
		pagination.PaginationParams{Page: 1, PageSize: 10},
		"",
		"",
		"",
		nil,
	)
	s.Require().NoError(err, "ListWithFilters base")

	s.Require().NoError(s.repo.Create(s.ctx, &routing.Group{
		Name: "g1",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}))
	s.Require().NoError(s.repo.Create(s.ctx, &routing.Group{
		Name: "g2",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformOpenAI),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformOpenAI),
		ResponsesImagePolicy: "inherit",
	}))

	groups, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "", "", "", nil)
	s.Require().NoError(err)
	s.Require().Len(groups, len(baseGroups)+2)
}

// TestListWithFilters_Status inserts one active group and one disabled group
// before selecting the disabled status. The filtered result must contain one row
// whose status is disabled.
func (s *GroupRepoSuite) TestListWithFilters_Status() {
	s.Require().NoError(s.repo.Create(s.ctx, &routing.Group{
		Name: "g1",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}))
	s.Require().NoError(s.repo.Create(s.ctx, &routing.Group{
		Name: "g2",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusDisabled,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}))

	groups, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "", billing.StatusDisabled, "", nil)
	s.Require().NoError(err)
	s.Require().Len(groups, 1)
	s.Require().Equal(billing.StatusDisabled, groups[0].Status)
}

// TestListWithFilters_IsExclusive creates ordinary and exclusive groups before
// selecting the exclusive filter. The repository must return one row with its
// exclusive flag set for that query.
func (s *GroupRepoSuite) TestListWithFilters_IsExclusive() {
	s.Require().NoError(s.repo.Create(s.ctx, &routing.Group{
		Name: "g1",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}))
	s.Require().NoError(s.repo.Create(s.ctx, &routing.Group{
		Name: "g2",

		RateMultiplier: 1.0,
		IsExclusive:    true,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}))

	isExclusive := true
	groups, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "", "", "", &isExclusive)
	s.Require().NoError(err)
	s.Require().Len(groups, 1)
	s.Require().True(groups[0].IsExclusive)
}

// TestListWithFilters_Search searches independently created group fixtures by
// name and description. Matching must ignore case, treat percent and underscore
// as literal characters, and return empty rows for the tested absent phrase.
func (s *GroupRepoSuite) TestListWithFilters_Search() {
	// newRepo opens an additional transaction for the current search subtest and
	// assembles group storage on that transaction. The transaction helper registers
	// rollback with this subtest, isolating its independently created fixtures.
	newRepo := func() (*routingpostgres.GroupStore, context.Context) {
		tx := s.transaction(s.T())
		return newGroupStoreFixture(tx.Client(), tx), context.Background()
	}

	// containsID reports whether the returned page contains a group with id.
	containsID := func(groups []routing.Group, id int64) bool {
		for i := range groups {
			if groups[i].ID == id {
				return true
			}
		}
		return false
	}

	// mustCreate persists the supplied group and requires a generated ID before a
	// search assertion uses it. The repository mutates g in place; the helper
	// returns that same pointer for later ID assertions.
	mustCreate := func(repo *routingpostgres.GroupStore, ctx context.Context, g *routing.Group) *routing.Group {
		s.Require().NoError(repo.Create(ctx, g))
		s.Require().NotZero(g.ID)
		return g
	}

	// newGroup builds an active, ordinary group with the Anthropic protocol defaults
	// used by the search scenarios. It leaves identity assignment to the repository
	// when mustCreate persists the supplied name.
	newGroup := func(name string) *routing.Group {
		return &routing.Group{
			Name: name,

			RateMultiplier: 1.0,
			IsExclusive:    false,
			Status:         billing.StatusActive,

			AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
			ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
			ResponsesImagePolicy: "inherit",
		}
	}

	s.Run("search_name_should_match", func() {
		repo, ctx := newRepo()

		target := mustCreate(repo, ctx, newGroup("it-group-search-name-target"))
		other := mustCreate(repo, ctx, newGroup("it-group-search-name-other"))

		groups, _, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 50}, "", "", "name-target", nil)
		s.Require().NoError(err)
		s.Require().True(containsID(groups, target.ID), "expected target group to match by name")
		s.Require().False(containsID(groups, other.ID), "expected other group to be filtered out")
	})

	s.Run("search_description_should_match", func() {
		repo, ctx := newRepo()

		target := newGroup("it-group-search-desc-target")
		target.Description = "something about desc-needle in here"
		target = mustCreate(repo, ctx, target)

		other := newGroup("it-group-search-desc-other")
		other.Description = "nothing to see here"
		other = mustCreate(repo, ctx, other)

		groups, _, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 50}, "", "", "desc-needle", nil)
		s.Require().NoError(err)
		s.Require().True(containsID(groups, target.ID), "expected target group to match by description")
		s.Require().False(containsID(groups, other.ID), "expected other group to be filtered out")
	})

	s.Run("search_nonexistent_should_return_empty", func() {
		repo, ctx := newRepo()

		_ = mustCreate(repo, ctx, newGroup("it-group-search-nonexistent-baseline"))

		search := s.T().Name() + "__no_such_group__"
		groups, _, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 50}, "", "", search, nil)
		s.Require().NoError(err)
		s.Require().Empty(groups)
	})

	s.Run("search_should_be_case_insensitive", func() {
		repo, ctx := newRepo()

		target := mustCreate(repo, ctx, newGroup("MiXeDCaSe-Needle"))
		other := mustCreate(repo, ctx, newGroup("it-group-search-case-other"))

		groups, _, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 50}, "", "", "mixedcase-needle", nil)
		s.Require().NoError(err)
		s.Require().True(containsID(groups, target.ID), "expected case-insensitive match")
		s.Require().False(containsID(groups, other.ID), "expected other group to be filtered out")
	})

	// Pair literal LIKE metacharacters with single-character alternatives so the
	// assertions distinguish escaped user text from wildcard matching in the SQL search.
	s.Run("search_should_escape_like_wildcards", func() {
		repo, ctx := newRepo()

		percentTarget := mustCreate(repo, ctx, newGroup("it-group-search-100%-target"))
		percentOther := mustCreate(repo, ctx, newGroup("it-group-search-100X-other"))

		groups, _, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 50}, "", "", "100%", nil)
		s.Require().NoError(err)
		s.Require().True(containsID(groups, percentTarget.ID), "expected literal %% match")
		s.Require().False(containsID(groups, percentOther.ID), "expected %% not to act as wildcard")

		underscoreTarget := mustCreate(repo, ctx, newGroup("it-group-search-ab_cd-target"))
		underscoreOther := mustCreate(repo, ctx, newGroup("it-group-search-abXcd-other"))

		groups, _, err = repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 50}, "", "", "ab_cd", nil)
		s.Require().NoError(err)
		s.Require().True(containsID(groups, underscoreTarget.ID), "expected literal _ match")
		s.Require().False(containsID(groups, underscoreOther.ID), "expected _ not to act as wildcard")
	})
}

// TestUpdateSortOrders_BatchCaseWhen submits sort orders for three groups and
// repeats one ID with another value. Readback must show each requested order,
// using the last value for the repeated group.
func (s *GroupRepoSuite) TestUpdateSortOrders_BatchCaseWhen() {
	g1 := &routing.Group{
		Name: "sort-g1",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	g2 := &routing.Group{
		Name: "sort-g2",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	g3 := &routing.Group{
		Name: "sort-g3",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	s.Require().NoError(s.repo.Create(s.ctx, g1))
	s.Require().NoError(s.repo.Create(s.ctx, g2))
	s.Require().NoError(s.repo.Create(s.ctx, g3))

	err := s.repo.UpdateSortOrders(s.ctx, []routing.GroupSortOrderUpdate{
		{ID: g1.ID, SortOrder: 30},
		{ID: g2.ID, SortOrder: 10},
		{ID: g3.ID, SortOrder: 20},
		{ID: g2.ID, SortOrder: 15},
	})
	s.Require().NoError(err)

	got1, err := s.repo.GetByID(s.ctx, g1.ID)
	s.Require().NoError(err)
	got2, err := s.repo.GetByID(s.ctx, g2.ID)
	s.Require().NoError(err)
	got3, err := s.repo.GetByID(s.ctx, g3.ID)
	s.Require().NoError(err)
	s.Require().Equal(30, got1.SortOrder)
	s.Require().Equal(15, got2.SortOrder)
	s.Require().Equal(20, got3.SortOrder)
}

// TestUpdateSortOrders_MissingGroupNoPartialUpdate combines one saved group
// with an absent ID in a sort-order batch. The call must return ErrGroupNotFound
// and leave the existing group's order unchanged.
func (s *GroupRepoSuite) TestUpdateSortOrders_MissingGroupNoPartialUpdate() {
	g1 := &routing.Group{
		Name: "sort-no-partial",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	s.Require().NoError(s.repo.Create(s.ctx, g1))

	before, err := s.repo.GetByID(s.ctx, g1.ID)
	s.Require().NoError(err)
	beforeSort := before.SortOrder

	err = s.repo.UpdateSortOrders(s.ctx, []routing.GroupSortOrderUpdate{
		{ID: g1.ID, SortOrder: 99},
		{ID: 99999999, SortOrder: 1},
	})
	s.Require().Error(err)
	s.Require().ErrorIs(err, routing.ErrGroupNotFound)

	after, err := s.repo.GetByID(s.ctx, g1.ID)
	s.Require().NoError(err)
	s.Require().Equal(beforeSort, after.SortOrder)
}

// TestListWithFilters_ProviderCount links one provider to ordinary and exclusive
// groups, then selects the exclusive group. The filtered page must return that
// group with a total provider count of one.
func (s *GroupRepoSuite) TestListWithFilters_ProviderCount() {
	g1 := &routing.Group{
		Name: "g1",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	g2 := &routing.Group{
		Name: "g2",

		RateMultiplier: 1.0,
		IsExclusive:    true,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	s.Require().NoError(s.repo.Create(s.ctx, g1))
	s.Require().NoError(s.repo.Create(s.ctx, g2))

	var providerID int64
	s.Require().NoError(postgresinfra.ScanSingleRow(
		s.ctx,
		s.tx,
		"INSERT INTO providers (name, platform, type) VALUES ($1, $2, $3) RETURNING id",
		[]any{"acc1", capability.PlatformAnthropic, capability.ProviderTypeOAuth},
		&providerID,
	))
	_, err := s.tx.ExecContext(s.ctx, "INSERT INTO provider_groups (provider_id, group_id, created_at) VALUES ($1, $2, NOW())", providerID, g1.ID)
	s.Require().NoError(err)
	_, err = s.tx.ExecContext(s.ctx, "INSERT INTO provider_groups (provider_id, group_id, created_at) VALUES ($1, $2, NOW())", providerID, g2.ID)
	s.Require().NoError(err)

	isExclusive := true
	groups, page, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, capability.PlatformAnthropic, billing.StatusActive, "", &isExclusive)
	s.Require().NoError(err, "ListWithFilters")
	s.Require().Equal(int64(1), page.Total)
	s.Require().Len(groups, 1)
	s.Require().Equal(g2.ID, groups[0].ID, "ListWithFilters returned wrong group")
	s.Require().Equal(int64(1), groups[0].ProviderCount, "ProviderCount mismatch")
}

// TestListActive creates an active group and a disabled group after reading
// the initial active list. The result must gain one row and include the newly
// created active group.
func (s *GroupRepoSuite) TestListActive() {
	baseGroups, err := s.repo.ListActive(s.ctx)
	s.Require().NoError(err, "ListActive base")

	s.Require().NoError(s.repo.Create(s.ctx, &routing.Group{
		Name: "active1",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}))
	s.Require().NoError(s.repo.Create(s.ctx, &routing.Group{
		Name: "inactive1",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusDisabled,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}))

	groups, err := s.repo.ListActive(s.ctx)
	s.Require().NoError(err, "ListActive")
	s.Require().Len(groups, len(baseGroups)+1)
	var found bool
	for _, g := range groups {
		if g.Name == "active1" {
			found = true
			break
		}
	}
	s.Require().True(found, "active1 group should be in results")
}

// TestListActiveDoesNotPartitionGroups adds two active groups with different
// protocol configurations and one disabled group. The active list must grow by
// two rows and contain the tested active group.
func (s *GroupRepoSuite) TestListActiveDoesNotPartitionGroups() {
	before, err := s.repo.ListActive(s.ctx)
	s.Require().NoError(err)
	s.Require().NoError(s.repo.Create(s.ctx, &routing.Group{
		Name: "g1",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}))
	s.Require().NoError(s.repo.Create(s.ctx, &routing.Group{
		Name: "g2",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformOpenAI),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformOpenAI),
		ResponsesImagePolicy: "inherit",
	}))
	s.Require().NoError(s.repo.Create(s.ctx, &routing.Group{
		Name: "g3",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusDisabled,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}))

	groups, err := s.repo.ListActive(s.ctx)
	s.Require().NoError(err, "ListActiveByPlatform")
	s.Require().Len(groups, len(before)+2)
	var found bool
	for _, g := range groups {
		if g.Name == "g1" {
			found = true
			break
		}
	}
	s.Require().True(found, "g1 group should be in results")
}

// TestExistsByName stores a group before querying its name and another absent
// name. The repository must return true for the saved name and false for the
// missing one.
func (s *GroupRepoSuite) TestExistsByName() {
	s.Require().NoError(s.repo.Create(s.ctx, &routing.Group{
		Name: "existing-group",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}))

	exists, err := s.repo.ExistsByName(s.ctx, "existing-group")
	s.Require().NoError(err, "ExistsByName")
	s.Require().True(exists)

	notExists, err := s.repo.ExistsByName(s.ctx, "non-existing")
	s.Require().NoError(err)
	s.Require().False(notExists)
}

// TestGetProviderCount links two directly inserted providers to a saved group
// in the same transaction. The repository's total-count result must equal two
// for that group's ID.
func (s *GroupRepoSuite) TestGetProviderCount() {
	group := &routing.Group{
		Name: "g-count",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	s.Require().NoError(s.repo.Create(s.ctx, group))

	var a1 int64
	s.Require().NoError(postgresinfra.ScanSingleRow(
		s.ctx,
		s.tx,
		"INSERT INTO providers (name, platform, type) VALUES ($1, $2, $3) RETURNING id",
		[]any{"a1", capability.PlatformAnthropic, capability.ProviderTypeOAuth},
		&a1,
	))
	var a2 int64
	s.Require().NoError(postgresinfra.ScanSingleRow(
		s.ctx,
		s.tx,
		"INSERT INTO providers (name, platform, type) VALUES ($1, $2, $3) RETURNING id",
		[]any{"a2", capability.PlatformAnthropic, capability.ProviderTypeOAuth},
		&a2,
	))

	_, err := s.tx.ExecContext(s.ctx, "INSERT INTO provider_groups (provider_id, group_id, created_at) VALUES ($1, $2, NOW())", a1, group.ID)
	s.Require().NoError(err)
	_, err = s.tx.ExecContext(s.ctx, "INSERT INTO provider_groups (provider_id, group_id, created_at) VALUES ($1, $2, NOW())", a2, group.ID)
	s.Require().NoError(err)

	count, _, err := s.repo.GetProviderCount(s.ctx, group.ID)
	s.Require().NoError(err, "GetProviderCount")
	s.Require().Equal(int64(2), count)
}

// TestGetProviderCount_Empty queries a saved group that has no linked provider
// rows in the transaction. The repository must return a zero total provider
// count for that group.
func (s *GroupRepoSuite) TestGetProviderCount_Empty() {
	group := &routing.Group{
		Name: "g-empty",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	s.Require().NoError(s.repo.Create(s.ctx, group))

	count, _, err := s.repo.GetProviderCount(s.ctx, group.ID)
	s.Require().NoError(err)
	s.Require().Zero(count)
}

// TestListWithFilters_ActiveProviderCount_LessThanTotal links healthy, disabled,
// and unschedulable providers to one group. Listing must count all three in the
// total, count one as active, and agree with the direct count query.
func (s *GroupRepoSuite) TestListWithFilters_ActiveProviderCount_LessThanTotal() {
	g := &routing.Group{
		Name: "g-mixed-status",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	s.Require().NoError(s.repo.Create(s.ctx, g))

	// insertProvider creates one provider with the supplied status and scheduling
	// flag in this case's transaction. It returns the generated ID so link can
	// attach each eligibility category to the same group.
	insertProvider := func(name, status string, schedulable bool) int64 {
		var id int64
		s.Require().NoError(postgresinfra.ScanSingleRow(
			s.ctx, s.tx,
			"INSERT INTO providers (name, platform, type, status, schedulable) VALUES ($1, $2, $3, $4, $5) RETURNING id",
			[]any{name, capability.PlatformAnthropic, capability.ProviderTypeOAuth, status, schedulable},
			&id,
		))
		return id
	}
	// link attaches a provider to this test's group using the active transaction.
	// It requires successful insertion before list and direct-count reads compare
	// that group's total and active provider counts.
	link := func(providerID int64) {
		_, err := s.tx.ExecContext(s.ctx,
			"INSERT INTO provider_groups (provider_id, group_id, created_at) VALUES ($1, $2, NOW())",
			providerID, g.ID)
		s.Require().NoError(err)
	}

	// Active and schedulable: included in both counts.
	link(insertProvider("acc-active-sched", billing.StatusActive, true))
	// Disabled: included in the total count.
	link(insertProvider("acc-disabled", billing.StatusDisabled, true))
	// Active but unschedulable: included in the total count.
	link(insertProvider("acc-unschedulable", billing.StatusActive, false))

	isExclusive := false
	groups, _, err := s.repo.ListWithFilters(s.ctx,
		pagination.PaginationParams{Page: 1, PageSize: 100},
		capability.PlatformAnthropic, billing.StatusActive, "", &isExclusive)
	s.Require().NoError(err)

	var found *routing.Group
	for i := range groups {
		if groups[i].ID == g.ID {
			found = &groups[i]
			break
		}
	}
	s.Require().NotNil(found, "created group must appear in ListWithFilters result")
	s.Assert().Equal(int64(3), found.ProviderCount, "ProviderCount must count all 3 providers")
	s.Assert().Equal(int64(1), found.ActiveProviderCount, "ActiveProviderCount must count only the active+schedulable provider")

	total, active, err := s.repo.GetProviderCount(s.ctx, g.ID)
	s.Require().NoError(err)
	s.Assert().Equal(found.ProviderCount, total, "GetProviderCount total must match ListWithFilters ProviderCount")
	s.Assert().Equal(found.ActiveProviderCount, active, "GetProviderCount active must match ListWithFilters ActiveProviderCount")
}

// TestListWithFilters_RateLimitedProviderCount links healthy, temporarily
// blocked, and expired providers to one group. Listing must distinguish five
// total, one active, and three temporarily limited providers, and detail and
// direct-count reads must agree.
func (s *GroupRepoSuite) TestListWithFilters_RateLimitedProviderCount() {
	g := &routing.Group{
		Name: "g-rate-limited",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	s.Require().NoError(s.repo.Create(s.ctx, g))

	var normalID int64
	s.Require().NoError(postgresinfra.ScanSingleRow(s.ctx, s.tx,
		"INSERT INTO providers (name, platform, type) VALUES ($1, $2, $3) RETURNING id",
		[]any{"acc-normal", capability.PlatformAnthropic, capability.ProviderTypeOAuth},
		&normalID))

	// Keep temporary block times an hour ahead of the database clock and expiry
	// an hour behind it, so count assertions exercise stable eligibility categories.
	var rateLimitedID int64
	s.Require().NoError(postgresinfra.ScanSingleRow(s.ctx, s.tx,
		"INSERT INTO providers (name, platform, type, rate_limit_reset_at) VALUES ($1, $2, $3, NOW() + INTERVAL '1 hour') RETURNING id",
		[]any{"acc-rate-limited", capability.PlatformAnthropic, capability.ProviderTypeOAuth},
		&rateLimitedID))

	var overloadedID int64
	s.Require().NoError(postgresinfra.ScanSingleRow(s.ctx, s.tx,
		"INSERT INTO providers (name, platform, type, overload_until) VALUES ($1, $2, $3, NOW() + INTERVAL '1 hour') RETURNING id",
		[]any{"acc-overloaded", capability.PlatformAnthropic, capability.ProviderTypeOAuth},
		&overloadedID))

	var tempUnschedulableID int64
	s.Require().NoError(postgresinfra.ScanSingleRow(s.ctx, s.tx,
		"INSERT INTO providers (name, platform, type, temp_unschedulable_until) VALUES ($1, $2, $3, NOW() + INTERVAL '1 hour') RETURNING id",
		[]any{"acc-temp-unschedulable", capability.PlatformAnthropic, capability.ProviderTypeOAuth},
		&tempUnschedulableID))

	var expiredID int64
	s.Require().NoError(postgresinfra.ScanSingleRow(s.ctx, s.tx,
		"INSERT INTO providers (name, platform, type, expires_at, auto_pause_on_expired) VALUES ($1, $2, $3, NOW() - INTERVAL '1 hour', TRUE) RETURNING id",
		[]any{"acc-expired", capability.PlatformAnthropic, capability.ProviderTypeOAuth},
		&expiredID))

	_, err := s.tx.ExecContext(s.ctx,
		"INSERT INTO provider_groups (provider_id, group_id, created_at) VALUES ($1, $2, NOW())",
		normalID, g.ID)
	s.Require().NoError(err)
	_, err = s.tx.ExecContext(s.ctx,
		"INSERT INTO provider_groups (provider_id, group_id, created_at) VALUES ($1, $2, NOW())",
		rateLimitedID, g.ID)
	s.Require().NoError(err)
	_, err = s.tx.ExecContext(s.ctx,
		"INSERT INTO provider_groups (provider_id, group_id, created_at) VALUES ($1, $2, NOW())",
		overloadedID, g.ID)
	s.Require().NoError(err)
	_, err = s.tx.ExecContext(s.ctx,
		"INSERT INTO provider_groups (provider_id, group_id, created_at) VALUES ($1, $2, NOW())",
		tempUnschedulableID, g.ID)
	s.Require().NoError(err)
	_, err = s.tx.ExecContext(s.ctx,
		"INSERT INTO provider_groups (provider_id, group_id, created_at) VALUES ($1, $2, NOW())",
		expiredID, g.ID)
	s.Require().NoError(err)

	isExclusive := false
	groups, _, err := s.repo.ListWithFilters(s.ctx,
		pagination.PaginationParams{Page: 1, PageSize: 100},
		capability.PlatformAnthropic, billing.StatusActive, "", &isExclusive)
	s.Require().NoError(err)

	var found *routing.Group
	for i := range groups {
		if groups[i].ID == g.ID {
			found = &groups[i]
			break
		}
	}
	s.Require().NotNil(found, "created group must appear in ListWithFilters result")
	s.Assert().Equal(int64(5), found.ProviderCount, "ProviderCount must include all linked providers")
	s.Assert().Equal(int64(1), found.ActiveProviderCount, "ActiveProviderCount must include only currently schedulable providers")
	s.Assert().Equal(int64(3), found.RateLimitedProviderCount, "RateLimitedProviderCount must include temporarily limited providers")

	total, active, err := s.repo.GetProviderCount(s.ctx, g.ID)
	s.Require().NoError(err)
	s.Assert().Equal(found.ProviderCount, total, "GetProviderCount total must match ListWithFilters ProviderCount")
	s.Assert().Equal(found.ActiveProviderCount, active, "GetProviderCount active must match ListWithFilters ActiveProviderCount")

	detail, err := s.repo.GetByID(s.ctx, g.ID)
	s.Require().NoError(err)
	s.Assert().Equal(found.ProviderCount, detail.ProviderCount, "GetByID ProviderCount must match ListWithFilters")
	s.Assert().Equal(found.ActiveProviderCount, detail.ActiveProviderCount, "GetByID ActiveProviderCount must match ListWithFilters")
	s.Assert().Equal(found.RateLimitedProviderCount, detail.RateLimitedProviderCount, "GetByID RateLimitedProviderCount must match ListWithFilters")
}

// TestDeleteProviderGroupsByGroupID removes one provider association from a
// saved group. The repository must report one affected row, and the group's
// subsequent total provider count must be zero.
func (s *GroupRepoSuite) TestDeleteProviderGroupsByGroupID() {
	g := &routing.Group{
		Name: "g-del",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	s.Require().NoError(s.repo.Create(s.ctx, g))
	var providerID int64
	s.Require().NoError(postgresinfra.ScanSingleRow(
		s.ctx,
		s.tx,
		"INSERT INTO providers (name, platform, type) VALUES ($1, $2, $3) RETURNING id",
		[]any{"acc-del", capability.PlatformAnthropic, capability.ProviderTypeOAuth},
		&providerID,
	))
	_, err := s.tx.ExecContext(s.ctx, "INSERT INTO provider_groups (provider_id, group_id, created_at) VALUES ($1, $2, NOW())", providerID, g.ID)
	s.Require().NoError(err)

	affected, err := s.repo.DeleteProviderGroupsByGroupID(s.ctx, g.ID)
	s.Require().NoError(err, "DeleteProviderGroupsByGroupID")
	s.Require().Equal(int64(1), affected, "expected 1 affected row")

	count, _, err := s.repo.GetProviderCount(s.ctx, g.ID)
	s.Require().NoError(err, "GetProviderCount")
	s.Require().Equal(int64(0), count, "expected 0 provider groups")
}

// TestDeleteProviderGroupsByGroupID_MultipleProviders removes three provider
// associations from one saved group. The affected-row result must be three, and
// the subsequent returned total must be zero.
func (s *GroupRepoSuite) TestDeleteProviderGroupsByGroupID_MultipleProviders() {
	g := &routing.Group{
		Name: "g-multi",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	s.Require().NoError(s.repo.Create(s.ctx, g))

	// insertProvider creates an OAuth provider in the current transaction and
	// returns its ID. The caller attaches each returned provider to g before
	// DeleteProviderGroupsByGroupID removes the resulting association rows.
	insertProvider := func(name string) int64 {
		var id int64
		s.Require().NoError(postgresinfra.ScanSingleRow(
			s.ctx,
			s.tx,
			"INSERT INTO providers (name, platform, type) VALUES ($1, $2, $3) RETURNING id",
			[]any{name, capability.PlatformAnthropic, capability.ProviderTypeOAuth},
			&id,
		))
		return id
	}
	a1 := insertProvider("a1")
	a2 := insertProvider("a2")
	a3 := insertProvider("a3")
	_, err := s.tx.ExecContext(s.ctx, "INSERT INTO provider_groups (provider_id, group_id, created_at) VALUES ($1, $2, NOW())", a1, g.ID)
	s.Require().NoError(err)
	_, err = s.tx.ExecContext(s.ctx, "INSERT INTO provider_groups (provider_id, group_id, created_at) VALUES ($1, $2, NOW())", a2, g.ID)
	s.Require().NoError(err)
	_, err = s.tx.ExecContext(s.ctx, "INSERT INTO provider_groups (provider_id, group_id, created_at) VALUES ($1, $2, NOW())", a3, g.ID)
	s.Require().NoError(err)

	affected, err := s.repo.DeleteProviderGroupsByGroupID(s.ctx, g.ID)
	s.Require().NoError(err)
	s.Require().Equal(int64(3), affected)

	// The affected-row result is checked above. This final assertion inspects the
	// returned count while discarding its query error, limiting that secondary check.
	count, _, _ := s.repo.GetProviderCount(s.ctx, g.ID)
	s.Require().Zero(count)
}

// TestDelete_SoftDelete_NotVisibleInList deletes a group after recording the
// list size in the same transaction. The next list must lose one row, and detail
// lookup must return ErrGroupNotFound.
func (s *GroupRepoSuite) TestDelete_SoftDelete_NotVisibleInList() {
	group := &routing.Group{
		Name: "to-soft-delete",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	s.Require().NoError(s.repo.Create(s.ctx, group))

	listBefore, _, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 100})
	s.Require().NoError(err)
	beforeCount := len(listBefore)

	err = s.repo.Delete(s.ctx, group.ID)
	s.Require().NoError(err, "Delete (soft delete)")

	listAfter, _, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 100})
	s.Require().NoError(err)
	s.Require().Len(listAfter, beforeCount-1, "soft deleted group should not appear in list")

	_, err = s.repo.GetByID(s.ctx, group.ID)
	s.Require().Error(err)
	s.Require().ErrorIs(err, routing.ErrGroupNotFound)
}

// TestDelete_SoftDeletedGroup_lockForUpdate creates and deletes a group before
// performing a detail lookup on its ID. The lookup must return an error matching
// ErrGroupNotFound for the deleted record.
func (s *GroupRepoSuite) TestDelete_SoftDeletedGroup_lockForUpdate() {
	group := &routing.Group{
		Name: "lock-soft-delete",

		RateMultiplier: 1.0,
		IsExclusive:    false,
		Status:         billing.StatusActive,

		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	s.Require().NoError(s.repo.Create(s.ctx, group))

	err := s.repo.Delete(s.ctx, group.ID)
	s.Require().NoError(err)

	_, err = s.repo.GetByID(s.ctx, group.ID)
	s.Require().Error(err, "should fail to get soft-deleted group")
	s.Require().ErrorIs(err, routing.ErrGroupNotFound)
}

// SetupSuite initializes the migrated PostgreSQL client shared by this suite's
// cases. routingDatabase registers cleanup for its underlying pool and container,
// and each test opens a separate transaction through the helper.
func (s *GroupRepoSuite) SetupSuite() { s.client, _ = routingDatabase(s.T()) }

// transaction opens a transaction on the suite client and registers rollback
// with the supplied test. Callers leave completion to that cleanup; its rollback
// result is discarded after the test finishes.
func (s *GroupRepoSuite) transaction(t *testing.T) *dbent.Tx {
	t.Helper()
	tx, err := s.client.Tx(context.Background())
	s.Require().NoError(err)
	// Register disposal with this test so setup and assertion failures also
	// request rollback of its fixtures before the suite proceeds to later cases.
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// TestListWithProviderCountSort_AttachesActiveCount sorts groups whose total
// provider counts differ while their active counts match. The list must attach
// both counts and place the group with two total providers first.
func (s *GroupRepoSuite) TestListWithProviderCountSort_AttachesActiveCount() {
	gA := &routing.Group{
		Name: "sort-count-a", RateMultiplier: 1, Status: billing.StatusActive,
		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	gB := &routing.Group{
		Name: "sort-count-b", RateMultiplier: 1, Status: billing.StatusActive,
		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	s.Require().NoError(s.repo.Create(s.ctx, gA))
	s.Require().NoError(s.repo.Create(s.ctx, gB))

	// insertProvider writes an OAuth provider with the supplied status and returns
	// its ID for count-sort fixtures. The caller varies status while leaving other
	// eligibility columns at database defaults, separating active counts from
	// membership totals.
	insertProvider := func(name, status string) int64 {
		var id int64
		s.Require().NoError(postgresinfra.ScanSingleRow(s.ctx, s.tx,
			"INSERT INTO providers (name, platform, type, status) VALUES ($1, $2, $3, $4) RETURNING id",
			[]any{name, capability.PlatformAnthropic, capability.ProviderTypeOAuth, status},
			&id))
		return id
	}
	// link associates an existing provider and group in the current transaction.
	// Callers pass IDs created in this test; a failed insert aborts the case before
	// counts or ordering are asserted.
	link := func(providerID, groupID int64) {
		_, err := s.tx.ExecContext(s.ctx,
			"INSERT INTO provider_groups (provider_id, group_id, created_at) VALUES ($1, $2, NOW())",
			providerID, groupID)
		s.Require().NoError(err)
	}

	// Group A has two providers in total and one active provider.
	link(insertProvider("sa-active", billing.StatusActive), gA.ID)
	link(insertProvider("sa-disabled", billing.StatusDisabled), gA.ID)
	// Group B has one provider, which is active.
	link(insertProvider("sb-active", billing.StatusActive), gB.ID)

	groups, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{
		Page: 1, PageSize: 100, SortBy: "provider_count", SortOrder: "desc",
	}, capability.PlatformAnthropic, billing.StatusActive, "", nil)
	s.Require().NoError(err)

	byID := make(map[int64]routing.Group, len(groups))
	for _, g := range groups {
		byID[g.ID] = g
	}

	s.Require().Contains(byID, gA.ID, "gA must appear in results")
	s.Require().Contains(byID, gB.ID, "gB must appear in results")

	cA := byID[gA.ID]
	s.Assert().Equal(int64(2), cA.ProviderCount, "gA ProviderCount must be 2")
	s.Assert().Equal(int64(1), cA.ActiveProviderCount, "gA ActiveProviderCount must be 1")

	cB := byID[gB.ID]
	s.Assert().Equal(int64(1), cB.ProviderCount, "gB ProviderCount must be 1")
	s.Assert().Equal(int64(1), cB.ActiveProviderCount, "gB ActiveProviderCount must be 1")

	indexByID := make(map[int64]int, len(groups))
	for i, g := range groups {
		indexByID[g.ID] = i
	}
	s.Assert().Less(indexByID[gA.ID], indexByID[gB.ID], "gA (total=2) must rank above gB (total=1) with provider_count desc")
}

// TestList_DefaultSortBySortOrderAsc stores two groups with different manual
// sort orders and queries the default list. The lower sort order must appear
// before the higher one in that result.
func (s *GroupRepoSuite) TestList_DefaultSortBySortOrderAsc() {
	g1 := &routing.Group{
		Name: "g1", RateMultiplier: 1, Status: billing.StatusActive, SortOrder: 20,
		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	g2 := &routing.Group{
		Name: "g2", RateMultiplier: 1, Status: billing.StatusActive, SortOrder: 10,
		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	s.Require().NoError(s.repo.Create(s.ctx, g1))
	s.Require().NoError(s.repo.Create(s.ctx, g2))

	groups, _, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 100})
	s.Require().NoError(err)
	s.Require().GreaterOrEqual(len(groups), 2)
	indexByID := make(map[int64]int, len(groups))
	for i, g := range groups {
		indexByID[g.ID] = i
	}
	s.Require().Contains(indexByID, g1.ID)
	s.Require().Contains(indexByID, g2.ID)
	s.Require().Less(indexByID[g2.ID], indexByID[g1.ID])
}

// TestList_SortBySortOrderDesc stores groups with different manual sort orders
// and selects descending sort_order ordering. The higher-valued group must appear
// before the lower-valued group in the returned list.
func (s *GroupRepoSuite) TestList_SortBySortOrderDesc() {
	g1 := &routing.Group{
		Name: "g1", RateMultiplier: 1, Status: billing.StatusActive, SortOrder: 40,
		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	g2 := &routing.Group{
		Name: "g2", RateMultiplier: 1, Status: billing.StatusActive, SortOrder: 50,
		AllowedProtocols:     capability.DefaultGroupClientProtocols(capability.PlatformAnthropic),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(capability.PlatformAnthropic),
		ResponsesImagePolicy: "inherit",
	}
	s.Require().NoError(s.repo.Create(s.ctx, g1))
	s.Require().NoError(s.repo.Create(s.ctx, g2))

	groups, _, err := s.repo.List(s.ctx, pagination.PaginationParams{
		Page:      1,
		PageSize:  10,
		SortBy:    "sort_order",
		SortOrder: "desc",
	})
	s.Require().NoError(err)
	s.Require().GreaterOrEqual(len(groups), 2)
	indexByID := make(map[int64]int, len(groups))
	for i, group := range groups {
		indexByID[group.ID] = i
	}
	s.Require().Contains(indexByID, g1.ID)
	s.Require().Contains(indexByID, g2.ID)
	s.Require().Less(indexByID[g2.ID], indexByID[g1.ID])
}

// TestUnifiedProtocolRoundTrip saves allowed protocols, protocol fallbacks, and
// an image policy, then clears the collections through Update. Lookup must return
// the saved values first and empty collections with the changed policy afterward.
func (s *GroupRepoSuite) TestUnifiedProtocolRoundTrip() {
	original := &routing.Group{Name: "protocol-group", Status: billing.StatusActive, RateMultiplier: 1, AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolAnthropicMessages}, ProtocolFallbacks: map[protocol.ProtocolID][]protocol.ProtocolID{protocol.ProtocolAnthropicMessages: {protocol.ProtocolOpenAIResponses}}, ResponsesImagePolicy: "disabled"}
	s.Require().NoError(s.repo.Create(s.ctx, original))
	got, err := s.repo.GetByIDLite(s.ctx, original.ID)
	s.Require().NoError(err)
	s.Require().Equal(original.AllowedProtocols, got.AllowedProtocols)
	s.Require().Equal(original.ProtocolFallbacks, got.ProtocolFallbacks)
	s.Require().Equal("disabled", got.ResponsesImagePolicy)
	got.AllowedProtocols = []protocol.ProtocolID{}
	got.ProtocolFallbacks = map[protocol.ProtocolID][]protocol.ProtocolID{}
	got.ResponsesImagePolicy = "block"
	s.Require().NoError(s.repo.Update(s.ctx, got))
	got, err = s.repo.GetByID(s.ctx, original.ID)
	s.Require().NoError(err)
	s.Require().Empty(got.AllowedProtocols)
	s.Require().Empty(got.ProtocolFallbacks)
	s.Require().Equal("block", got.ResponsesImagePolicy)
}
