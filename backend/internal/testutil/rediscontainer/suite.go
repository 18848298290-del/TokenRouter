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

// Redis suite tests reuse one container while each test client receives a new
// key prefix. SetupTest resets the context and namespaced client for that test,
// and the registered testing cleanups remove its keys and close its pool before
// the shared base client's cleanup.

// Suite embeds testify lifecycle handling with a shared Redis container client
// and per-test namespaces.
type Suite struct {
	suite.Suite // current testify fixture context
	// Ctx and RDB are replaced by SetupTest for the current test. Suite methods
	// use testify's current T to register namespace cleanup on that test's lifecycle.
	Ctx context.Context // current test operation context
	RDB *redis.Client   // current test namespaced Redis client
	// base is acquired by SetupSuite and remains open until suite-level testing
	// cleanup runs, after the individual namespace clients have finished their cleanup.
	base *redis.Client
}

// SetupSuite starts the dedicated shared Redis fixture and retains its base
// client. New registers cleanup against the suite test, so individual namespace
// cleanups finish before base-client closure and container termination.
func (s *Suite) SetupSuite() { s.base = New(s.T()) }

// SetupTest installs a fresh Background context and namespaced client for the
// current test. The client cleanup belongs to this test, and the embedded
// testify suite identifies that lifecycle through s.T.
func (s *Suite) SetupTest() {
	// These fields describe the current testify test. Replace both before its
	// operations so each test uses a distinct client prefix and cleanup registration.
	s.Ctx = context.Background()
	s.RDB = s.Client(s.T())
}

// Client constructs a new namespace client against the suite's initialized base
// connection. The supplied test owns namespace cleanup, and callers keep the
// suite base alive until that cleanup completes.
func (s *Suite) Client(t *testing.T) *redis.Client {
	t.Helper()
	return Namespaced(t, s.base)
}

// RequireNoError reports err through require using the suite's current test
// context.
func (s *Suite) RequireNoError(err error, message ...any) {
	s.T().Helper()
	require.NoError(s.T(), err, message...)
}

// AssertTTLWithin checks the supplied expiration duration against inclusive
// limits for this suite test. It delegates comparison and failure reporting to
// assertTTLWithin, so callers supply their own observation margin.
func (s *Suite) AssertTTLWithin(ttl, minimum, maximum time.Duration) {
	s.T().Helper()
	assertTTLWithin(s.T(), ttl, minimum, maximum)
}
