//go:build integration

package rediscontainer

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// Suite shares one Redis instance and gives each Client call a separate test namespace.
type Suite struct {
	suite.Suite
	Ctx  context.Context
	RDB  *redis.Client
	base *redis.Client
}

func (s *Suite) SetupSuite() { s.base = New(s.T()) }

func (s *Suite) SetupTest() {
	s.Ctx = context.Background()
	s.RDB = s.Client(s.T())
}

func (s *Suite) Client(t *testing.T) *redis.Client {
	t.Helper()
	return Namespaced(t, s.base)
}

func (s *Suite) RequireNoError(err error, message ...any) {
	s.T().Helper()
	require.NoError(s.T(), err, message...)
}

func (s *Suite) AssertTTLWithin(ttl, minimum, maximum time.Duration) {
	s.T().Helper()
	assertTTLWithin(s.T(), ttl, minimum, maximum)
}
