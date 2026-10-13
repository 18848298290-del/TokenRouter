//go:build integration

package rediscache

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// prefixHook rewrites selected Redis command key arguments into one fixture
// namespace.
type prefixHook struct {
	// prefix is added to selected nonempty string or byte arguments when absent.
	// Unsupported command forms retain their arguments and receive no complete
	// isolation guarantee.
	prefix string // per-test key namespace
}

// IntegrationRedisSuite provides per-test context and a Redis client for
// supported prefixed commands.
type IntegrationRedisSuite struct {
	suite.Suite // assertion and test framework
	// ctx is assigned Background by SetupTest for each case. Its lifetime has no
	// deadline or test-runner cancellation linked by this fixture.
	ctx context.Context // per-test background context
	// rdb is created by testRedis and closed after its namespace cleanup succeeds.
	// It borrows copied connection options from the package's shared endpoint.
	rdb *redis.Client // per-test hooked Redis client
}

// UpdateCacheSuite binds the opaque update-info cache to each Redis suite
// fixture.
type UpdateCacheSuite struct {
	IntegrationRedisSuite // per-test Redis fixture setup
	// cache retains the suite's Redis client. SetupTest assigns the concrete
	// adapter after the embedded suite prepares its namespace.
	cache *updateCache // per-test update cache adapter
}

// TestGetUpdateInfo_Missing reads the update key before any write in its test
// namespace. It checks the adapter returns redis.Nil for that absent opaque
// value.
func (s *UpdateCacheSuite) TestGetUpdateInfo_Missing() {
	_, err := s.cache.GetUpdateInfo(s.ctx)
	require.True(s.T(), errors.Is(err, redis.Nil), "expected redis.Nil for missing update info")
}

// TestSetAndGetUpdateInfo writes an opaque version string with a positive
// expiration and reads it back. It checks byte-for-byte value retrieval through
// the shared update-info key.
func (s *UpdateCacheSuite) TestSetAndGetUpdateInfo() {
	updateTTL := 5 * time.Minute
	require.NoError(s.T(), s.cache.SetUpdateInfo(s.ctx, "v1.2.3", updateTTL), "SetUpdateInfo")

	info, err := s.cache.GetUpdateInfo(s.ctx)
	require.NoError(s.T(), err, "GetUpdateInfo")
	require.Equal(s.T(), "v1.2.3", info, "update info mismatch")
}

// TestSetUpdateInfo_TTL writes a five-minute update-info expiration and
// inspects Redis TTL for the same prefixed key. It checks the remaining
// lifetime stays within the supplied positive interval.
func (s *UpdateCacheSuite) TestSetUpdateInfo_TTL() {
	updateTTL := 5 * time.Minute
	require.NoError(s.T(), s.cache.SetUpdateInfo(s.ctx, "v1.2.3", updateTTL))

	ttl, err := s.rdb.TTL(s.ctx, updateCacheKey).Result()
	require.NoError(s.T(), err, "TTL updateCacheKey")
	// Allow the server's elapsed TTL to range from one second through the
	// requested lifetime, rather than requiring an exact post-write duration.
	s.AssertTTLWithin(ttl, 1*time.Second, updateTTL)
}

// TestSetUpdateInfo_Overwrite writes two opaque values to the same test key
// using positive expiration. It checks the second write replaces the value
// returned by the adapter.
func (s *UpdateCacheSuite) TestSetUpdateInfo_Overwrite() {
	require.NoError(s.T(), s.cache.SetUpdateInfo(s.ctx, "v1.0.0", 5*time.Minute))
	require.NoError(s.T(), s.cache.SetUpdateInfo(s.ctx, "v2.0.0", 5*time.Minute))

	info, err := s.cache.GetUpdateInfo(s.ctx)
	require.NoError(s.T(), err)
	require.Equal(s.T(), "v2.0.0", info, "expected overwritten value")
}

// TestSetUpdateInfo_ZeroTTL writes an update value with zero expiration and
// reads it back. It checks Redis reports the persistent-key TTL sentinel rather
// than a missing or expiring key.
func (s *UpdateCacheSuite) TestSetUpdateInfo_ZeroTTL() {
	// Use zero expiration to select Redis persistent-key storage; the follow-up
	// TTL assertion distinguishes that state from absence or a positive countdown.
	require.NoError(s.T(), s.cache.SetUpdateInfo(s.ctx, "v0.0.0", 0))

	info, err := s.cache.GetUpdateInfo(s.ctx)
	require.NoError(s.T(), err)
	require.Equal(s.T(), "v0.0.0", info)

	ttl, err := s.rdb.TTL(s.ctx, updateCacheKey).Result()
	require.NoError(s.T(), err)
	// go-redis returns negative TTL sentinels as duration values: minus one
	// denotes no expiration, while minus two denotes a missing key.
	require.Equal(s.T(), time.Duration(-1), ttl, "expected TTL=-1 for key with no expiry")
}

// TestUpdateCacheSuite runs the container-backed update cache scenarios through
// their per-test Redis fixture setup. The cases cover missing values, ordinary
// reads, overwrite, expiration, and persistent zero-TTL storage.
func TestUpdateCacheSuite(t *testing.T) {
	suite.Run(t, new(UpdateCacheSuite))
}

// testRedis creates a client with copied endpoint options and a per-test
// prefix input formed from name, wall-clock nanoseconds, and an atomic
// sequence. Call it after TestMain has published integrationRedis.
//
// The hook rewrites only supported command forms, so namespace isolation
// follows those key-position rules. Cleanup scans through the unprefixed shared
// client, unlinks matching names, then closes this client. Scan or unlink
// assertions can end cleanup before the final close. Option pointer members
// remain shared through the shallow copy, and this helper performs no global
// endpoint mutation.
func testRedis(t *testing.T) *redis.Client {
	t.Helper()

	// Combine a readable test label, current timestamp, and atomically advanced
	// sequence so concurrent fixtures do not rely on wall-clock uniqueness alone.
	prefix := fmt.Sprintf(
		"it:%s:%d:%d:",
		sanitizeRedisNamespace(t.Name()),
		time.Now().UnixNano(),
		atomic.AddUint64(&redisNamespaceSeq, 1),
	)

	// Copy the shared endpoint's options value before adding this client's
	// namespace hook; pointer-valued option members retain their shared
	// references.
	opts := *integrationRedis.Options()
	rdb := redis.NewClient(&opts)
	rdb.AddHook(prefixHook{prefix: prefix})

	// Use the unprefixed client to scan and unlink this namespace before closing
	// the hooked client, keeping cleanup operations outside its argument
	// rewriting.
	t.Cleanup(func() {
		ctx := context.Background()

		var cursor uint64
		// Continue SCAN until its cursor reaches zero; each page is unlinked before
		// advancing, and count five hundred is only a scan hint.
		for {
			keys, nextCursor, err := integrationRedis.Scan(ctx, cursor, prefix+"*", 500).Result()
			require.NoError(t, err, "scan redis keys for cleanup")
			if len(keys) > 0 {
				require.NoError(t, integrationRedis.Unlink(ctx, keys...).Err(), "unlink redis keys for cleanup")
			}

			cursor = nextCursor
			if cursor == 0 {
				break
			}
		}

		_ = rdb.Close()
	})

	return rdb
}

// assertTTLWithin checks inclusive duration bounds using fatal test assertions.
// Callers choose bounds appropriate to an observed Redis TTL, including
// scheduling delay; this helper performs no unit conversion or normalization.
func assertTTLWithin(t *testing.T, ttl time.Duration, min, max time.Duration) {
	t.Helper()
	require.GreaterOrEqual(t, ttl, min, "ttl should be >= min")
	require.LessOrEqual(t, ttl, max, "ttl should be <= max")
}

// sanitizeRedisNamespace replaces slashes and spaces with underscores in a
// fixture-name component.
func sanitizeRedisNamespace(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, " ", "_")
	return name
}

// DialHook returns the supplied dial function without modifying connection
// setup.
func (h prefixHook) DialHook(next redis.DialHook) redis.DialHook { return next }

// ProcessHook wraps one command execution by rewriting supported key arguments
// before invoking next. It mutates the command's Args storage directly and
// returns the delegated execution error unchanged.
func (h prefixHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		h.prefixCmd(cmd)
		return next(ctx, cmd)
	}
}

// ProcessPipelineHook rewrites supported keys on each pipeline command before
// invoking next once. Commands retain their mutated argument slices; the hook
// returns the pipeline callback's error without per-command recovery.
func (h prefixHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			h.prefixCmd(cmd)
		}
		return next(ctx, cmds)
	}
}

// prefixCmd mutates recognized key positions in a Redis command's argument
// slice before execution. It handles nonempty string and byte values,
// preserving already-prefixed values and replacing byte arguments with new
// prefixed bytes.
//
// Single-key forms rewrite position one, including only the first key for
// EXISTS. MGET, DEL, and UNLINK rewrite all following arguments. Script forms
// use declared numkeys, and SCAN rewrites an existing MATCH pattern; absent
// MATCH or unsupported commands remain unchanged. This dispatch is a fixture
// compatibility list, so callers cannot assume every command or multi-key shape
// is isolated.
func (h prefixHook) prefixCmd(cmd redis.Cmder) {
	args := cmd.Args()
	if len(args) < 2 {
		return
	}

	// Mutate the command's argument storage only for supported string or byte
	// keys, skipping empty and already-prefixed values to avoid repeated namespace
	// insertion.
	prefixOne := func(i int) {
		if i < 0 || i >= len(args) {
			return
		}

		switch v := args[i].(type) {
		case string:
			if v != "" && !strings.HasPrefix(v, h.prefix) {
				args[i] = h.prefix + v
			}
		case []byte:
			s := string(v)
			if s != "" && !strings.HasPrefix(s, h.prefix) {
				args[i] = []byte(h.prefix + s)
			}
		}
	}

	switch strings.ToLower(cmd.Name()) {
	// Rewrite position one for these forms so SET and GETDEL reach the same
	// fixture key; multi-key EXISTS still rewrites only its first key.
	case "get", "getdel", "set", "setnx", "setex", "psetex", "incr", "decr", "incrby", "expire", "pexpire", "ttl", "pttl",
		"hgetall", "hget", "hset", "hdel", "hincrbyfloat", "exists",
		"zadd", "zcard", "zrange", "zrangebyscore", "zrem", "zremrangebyscore", "zrevrange", "zrevrangebyscore", "zscore":
		prefixOne(1)
	case "mget":
		for i := 1; i < len(args); i++ {
			prefixOne(i)
		}
	case "del", "unlink":
		for i := 1; i < len(args); i++ {
			prefixOne(i)
		}
	case "eval", "evalsha", "eval_ro", "evalsha_ro":
		if len(args) < 3 {
			return
		}
		// Restrict script rewriting to declared KEYS positions; script text and ARGV
		// remain supplied, so keys embedded in either are outside this hook.
		numKeys, err := strconv.Atoi(fmt.Sprint(args[2]))
		// Leave script arguments untouched when declared key count is invalid or
		// nonpositive, avoiding a guessed rewrite of script text or ARGV.
		if err != nil || numKeys <= 0 {
			return
		}
		for i := 0; i < numKeys && 3+i < len(args); i++ {
			prefixOne(3 + i)
		}
	// Rewrite an existing MATCH argument for supported scans; a SCAN with no MATCH
	// is delegated with no namespace restriction.
	case "scan":
		for i := 2; i+1 < len(args); i++ {
			if strings.EqualFold(fmt.Sprint(args[i]), "match") {
				prefixOne(i + 1)
				break
			}
		}
	}
}

// SetupTest installs a Background context and creates this test's hooked Redis
// client. testRedis registers namespace cleanup with the current test; suite
// users keep the context and client alive through their case.
func (s *IntegrationRedisSuite) SetupTest() {
	s.ctx = context.Background()
	s.rdb = testRedis(s.T())
}

// AssertTTLWithin forwards an observed duration and its inclusive bounds to the
// fatal assertion helper using the suite's current test. It adds helper
// attribution but changes no TTL value.
func (s *IntegrationRedisSuite) AssertTTLWithin(ttl, min, max time.Duration) {
	s.T().Helper()
	assertTTLWithin(s.T(), ttl, min, max)
}

// SetupTest prepares the embedded Redis fixture, constructs its update cache,
// and requires the expected concrete adapter type. The cache shares that
// per-test client and its cleanup lifetime.
func (s *UpdateCacheSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	cache, ok := NewUpdateCache(s.rdb).(*updateCache)
	s.Require().True(ok, "update cache constructor type")
	s.cache = cache
}
