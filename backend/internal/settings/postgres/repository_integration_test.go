//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/testutil/postgrescontainer"
)

type SettingRepoSuite struct {
	suite.Suite
	client *dbent.Client
	ctx    context.Context
	repo   settingscore.Repository
}

// TestSettingsAtomicFailure uses a PostgreSQL trigger failure to check that the entire settings batch rolls back.
func TestSettingsAtomicFailure(t *testing.T) {
	integrationEntClient, integrationDB := settingsDatabase(t)
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, `CREATE FUNCTION test_reject_setting() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test rejected settings write'; END $$;
 CREATE TRIGGER test_reject_setting BEFORE INSERT OR UPDATE ON settings FOR EACH ROW WHEN (NEW.key='test_atomic_failure') EXECUTE FUNCTION test_reject_setting();`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := integrationDB.ExecContext(context.Background(), `DROP TRIGGER test_reject_setting ON settings; DROP FUNCTION test_reject_setting(); DELETE FROM settings WHERE key IN ('test_atomic_site','test_atomic_failure');`)
		require.NoError(t, cleanupErr)
	})
	repo := NewSettingRepository(integrationEntClient)
	require.NoError(t, repo.Set(ctx, "test_atomic_site", "before"))
	update, err := settingscore.New(repo).Updates().Begin(ctx)
	require.NoError(t, err)
	defer update.Close()
	applied := false
	err = update.Commit(settingscore.PreparedChange{Module: "site", Values: map[string]string{"test_atomic_site": "after"}, Apply: func(context.Context) error { applied = true; return nil }}, settingscore.PreparedChange{Module: "payment", Values: map[string]string{"test_atomic_failure": "true"}})
	require.Error(t, err)
	require.False(t, applied)
	actual, err := repo.GetValue(ctx, "test_atomic_site")
	require.NoError(t, err)
	require.Equal(t, "before", actual)
	_, err = repo.GetValue(ctx, "test_atomic_failure")
	require.ErrorIs(t, err, settingscore.ErrSettingNotFound)
}

// settingsDatabase creates an isolated database client; the fixture closes its connection.
func settingsDatabase(t *testing.T) (*dbent.Client, *sql.DB) {
	t.Helper()
	db := postgrescontainer.New(t)
	return dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db))), db
}

// TestLocalizedSettingsConcurrentWrite checks version conflicts and batch rollback across two Store instances.
func TestLocalizedSettingsConcurrentWrite(t *testing.T) {
	client, _ := settingsDatabase(t)
	ctx := context.Background()
	repo := NewSettingRepository(client)
	const key = "test_localization_cas"
	const sibling = "test_localization_sibling"
	before := `{"revision":1}`
	require.NoError(t, repo.Set(ctx, key, before))
	t.Cleanup(func() { _ = repo.Delete(ctx, key); _ = repo.Delete(ctx, sibling) })
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	start := make(chan struct{})
	for _, value := range []string{"first", "second"} {
		go func(value string) {
			store := settingscore.New(repo)
			session, err := store.Updates().Begin(ctx)
			if err != nil {
				ready.Done()
				results <- err
				return
			}
			defer session.Close()
			ready.Done()
			<-start
			results <- session.Commit(settingscore.PreparedChange{Module: "site", Values: map[string]string{key: value, sibling: value}, Expected: map[string]*string{key: &before}})
		}(value)
	}
	ready.Wait()
	close(start)
	first, second := <-results, <-results
	if first == nil {
		require.ErrorIs(t, second, locale.ErrConflict)
	} else {
		require.ErrorIs(t, first, locale.ErrConflict)
		require.NoError(t, second)
	}
	actual, err := repo.GetValue(ctx, key)
	require.NoError(t, err)
	other, err := repo.GetValue(ctx, sibling)
	require.NoError(t, err)
	require.Equal(t, actual, other)
}

func (s *SettingRepoSuite) SetupTest() {
	s.ctx = context.Background()
	tx, err := s.client.Tx(s.ctx)
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = tx.Rollback() })
	s.repo = settingscore.New(NewSettingRepository(tx.Client()))
}

func TestSettingRepoSuite(t *testing.T) {
	suite.Run(t, new(SettingRepoSuite))
}

func (s *SettingRepoSuite) TestSetAndGetValue() {
	s.Require().NoError(s.repo.Set(s.ctx, "k1", "v1"), "Set")
	got, err := s.repo.GetValue(s.ctx, "k1")
	s.Require().NoError(err, "GetValue")
	s.Require().Equal("v1", got, "GetValue mismatch")
}

func (s *SettingRepoSuite) TestSet_Upsert() {
	s.Require().NoError(s.repo.Set(s.ctx, "k1", "v1"), "Set")
	s.Require().NoError(s.repo.Set(s.ctx, "k1", "v2"), "Set upsert")
	got, err := s.repo.GetValue(s.ctx, "k1")
	s.Require().NoError(err, "GetValue after upsert")
	s.Require().Equal("v2", got, "upsert mismatch")
}

func (s *SettingRepoSuite) TestGetValue_Missing() {
	_, err := s.repo.GetValue(s.ctx, "nonexistent")
	s.Require().Error(err, "expected error for missing key")
	s.Require().ErrorIs(err, settingscore.ErrSettingNotFound)
}

func (s *SettingRepoSuite) TestSetMultiple_AndGetMultiple() {
	s.Require().NoError(s.repo.SetMultiple(s.ctx, map[string]string{"k2": "v2", "k3": "v3"}), "SetMultiple")
	m, err := s.repo.GetMultiple(s.ctx, []string{"k2", "k3"})
	s.Require().NoError(err, "GetMultiple")
	s.Require().Equal("v2", m["k2"])
	s.Require().Equal("v3", m["k3"])
}

func (s *SettingRepoSuite) TestGetMultiple_EmptyKeys() {
	m, err := s.repo.GetMultiple(s.ctx, []string{})
	s.Require().NoError(err, "GetMultiple with empty keys")
	s.Require().Empty(m, "expected empty map")
}

func (s *SettingRepoSuite) TestGetMultiple_Subset() {
	s.Require().NoError(s.repo.SetMultiple(s.ctx, map[string]string{"a": "1", "b": "2", "c": "3"}))
	m, err := s.repo.GetMultiple(s.ctx, []string{"a", "c", "nonexistent"})
	s.Require().NoError(err, "GetMultiple subset")
	s.Require().Equal("1", m["a"])
	s.Require().Equal("3", m["c"])
	_, exists := m["nonexistent"]
	s.Require().False(exists, "nonexistent key should not be in map")
}

func (s *SettingRepoSuite) TestGetAll() {
	s.Require().NoError(s.repo.SetMultiple(s.ctx, map[string]string{"x": "1", "y": "2"}))
	all, err := s.repo.GetAll(s.ctx)
	s.Require().NoError(err, "GetAll")
	s.Require().GreaterOrEqual(len(all), 2, "expected at least 2 settings")
	s.Require().Equal("1", all["x"])
	s.Require().Equal("2", all["y"])
}

func (s *SettingRepoSuite) TestDelete() {
	s.Require().NoError(s.repo.Set(s.ctx, "todelete", "val"))
	s.Require().NoError(s.repo.Delete(s.ctx, "todelete"), "Delete")
	_, err := s.repo.GetValue(s.ctx, "todelete")
	s.Require().Error(err, "expected missing key error after Delete")
	s.Require().ErrorIs(err, settingscore.ErrSettingNotFound)
}

func (s *SettingRepoSuite) TestDelete_Idempotent() {
	s.Require().NoError(s.repo.Delete(s.ctx, "nonexistent_delete"), "Delete nonexistent should be idempotent")
}

func (s *SettingRepoSuite) TestSetMultiple_Upsert() {
	s.Require().NoError(s.repo.Set(s.ctx, "upsert_key", "old_value"))
	s.Require().NoError(s.repo.SetMultiple(s.ctx, map[string]string{"upsert_key": "new_value", "new_key": "new_val"}))

	got, err := s.repo.GetValue(s.ctx, "upsert_key")
	s.Require().NoError(err)
	s.Require().Equal("new_value", got, "SetMultiple should upsert existing key")

	got2, err := s.repo.GetValue(s.ctx, "new_key")
	s.Require().NoError(err)
	s.Require().Equal("new_val", got2)
}

// TestSet_EmptyValue checks that an optional setting can store an empty string.
func (s *SettingRepoSuite) TestSet_EmptyValue() {
	s.Require().NoError(s.repo.Set(s.ctx, "empty_key", ""), "Set with empty value should succeed")

	got, err := s.repo.GetValue(s.ctx, "empty_key")
	s.Require().NoError(err, "GetValue for empty value")
	s.Require().Equal("", got, "empty value should be preserved")
}

// TestSetMultiple_WithEmptyValues checks a site settings batch with both populated and empty optional fields.
func (s *SettingRepoSuite) TestSetMultiple_WithEmptyValues() {
	settings := map[string]string{
		"site_name":     "Sub2api",
		"site_subtitle": "Subscription to API",
		"site_logo":     "", // no uploaded logo
		"api_base_url":  "", // no API base URL
		"contact_info":  "", // no contact information
		"doc_url":       "", // no documentation URL
	}

	s.Require().NoError(s.repo.SetMultiple(s.ctx, settings), "SetMultiple with empty values should succeed")

	result, err := s.repo.GetMultiple(s.ctx, []string{"site_name", "site_subtitle", "site_logo", "api_base_url", "contact_info", "doc_url"})
	s.Require().NoError(err, "GetMultiple after SetMultiple with empty values")

	s.Require().Equal("Sub2api", result["site_name"])
	s.Require().Equal("Subscription to API", result["site_subtitle"])
	s.Require().Equal("", result["site_logo"], "empty site_logo should be preserved")
	s.Require().Equal("", result["api_base_url"], "empty api_base_url should be preserved")
	s.Require().Equal("", result["contact_info"], "empty contact_info should be preserved")
	s.Require().Equal("", result["doc_url"], "empty doc_url should be preserved")
}

// TestSetMultiple_UpdateToEmpty checks that a batch can clear a previously populated setting.
func (s *SettingRepoSuite) TestSetMultiple_UpdateToEmpty() {
	s.Require().NoError(s.repo.Set(s.ctx, "clearable_key", "initial_value"))

	got, err := s.repo.GetValue(s.ctx, "clearable_key")
	s.Require().NoError(err)
	s.Require().Equal("initial_value", got)

	s.Require().NoError(s.repo.SetMultiple(s.ctx, map[string]string{"clearable_key": ""}), "Update to empty should succeed")

	got, err = s.repo.GetValue(s.ctx, "clearable_key")
	s.Require().NoError(err)
	s.Require().Equal("", got, "value should be updated to empty string")
}

// SetupSuite creates the shared database; each test uses its own transaction.
func (s *SettingRepoSuite) SetupSuite() { s.client, _ = settingsDatabase(s.T()) }
