//go:build integration

package apikey_test

import (
	"context"
	"database/sql"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/suite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	apikeypostgres "github.com/TokenFlux/TokenRouter/internal/apikey/postgres"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/testutil/postgrescontainer"
)

// APIKeyRepoSuite shares database and repository fixtures across API key storage tests.
type APIKeyRepoSuite struct {
	root *dbent.Client
	suite.Suite
	ctx    context.Context
	client *dbent.Client
	repo   *apikeypostgres.KeyStore
}

// SetupSuite creates one isolated database for the suite; SetupTest wraps each test in a rolled-back transaction.
func (s *APIKeyRepoSuite) SetupSuite() {
	_, s.root = keyDatabase(s.T())
}

func (s *APIKeyRepoSuite) SetupTest() {
	s.ctx = context.Background()
	tx, err := s.root.Tx(s.ctx)
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = tx.Rollback() })
	s.client = tx.Client()
	s.repo = newKeyStoreFixture(s.client, tx)
}

func (s *APIKeyRepoSuite) mustCreateUser(email string) *identity.User {
	s.T().Helper()

	u, err := s.client.User.Create().
		SetEmail(email).
		SetPasswordHash("test-password-hash").
		SetStatus(billing.StatusActive).
		SetRole(identity.RoleUser).
		Save(s.ctx)
	s.Require().NoError(err, "create user")
	return identitypostgres.UserFromEntity(u)
}

func (s *APIKeyRepoSuite) mustCreateGroup(name string) *routing.Group {
	s.T().Helper()

	g, err := s.client.Group.Create().
		SetName(name).
		SetStatus(billing.StatusActive).
		Save(s.ctx)
	s.Require().NoError(err, "create group")
	return routingpostgres.GroupFromEnt(g)
}

func (s *APIKeyRepoSuite) mustCreateApiKey(userID int64, key, name string, groupID *int64) *apikey.APIKey {
	s.T().Helper()

	k := &apikey.APIKey{
		UserID:  userID,
		Key:     key,
		Name:    name,
		GroupID: groupID,
		Status:  billing.StatusActive,
	}
	s.Require().NoError(s.repo.Create(s.ctx, k), "create api key")
	return k
}

// keyDatabase creates an isolated database that supports committed transactions for concurrency tests.
func keyDatabase(t *testing.T) (*sql.DB, *dbent.Client) {
	t.Helper()
	db := postgrescontainer.New(t)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	return db, client
}
