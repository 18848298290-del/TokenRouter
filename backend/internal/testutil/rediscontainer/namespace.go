//go:build integration

package rediscontainer

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	redisclient "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// namespace.go prefixes supported Redis command keys and cleans up each test
// namespace.
// Redis integration suites share one server while each test needs
// distinguishable key names. Namespaced creates a client using the base
// connection settings and adds a hook that prefixes recognized command key
// positions. The prefix combines the sanitized test name, current nanoseconds,
// and an atomic sequence so separate clients remain distinguishable even when
// test names repeat.
//
// Key rewriting is command-specific: the supported single-key commands modify
// their first key, bulk commands modify their listed keys, script commands
// modify declared KEYS arguments, and SCAN modifies its first MATCH pattern.
// Other arguments and returned key names stay as Redis provides them. Cleanup
// scans the physical prefix through the base client, unlinks matching keys,
// then closes the namespaced client.

// redisNamespaceSeq is atomically incremented for each client namespace so
// repeated test names and identical timestamp observations still receive
// distinct sequence components.
var redisNamespaceSeq uint64

// prefixHook rewrites recognized Redis key arguments using one test client's
// assigned namespace prefix.
type prefixHook struct {
	// prefix is assigned before hook registration and reused unchanged by command
	// and pipeline hooks so operations on this client share one key namespace.
	prefix string
}

// Namespaced returns a client that prefixes supported command keys for this
// test. base must remain open through test cleanup, and its connection options
// are copied into a new client with its own pool.
//
// Isolation depends on prefixCmd's recognized key positions. Unsupported
// commands, scripts constructing keys independently, additional keys to listed
// single-key commands, and SCAN without MATCH can reach shared server data.
// Cleanup uses the base client to scan and unlink the physical prefix, reports
// scan/unlink failures through the test, and discards the final client-close
// error. Test-name sanitization replaces slash and space but does not escape
// Redis glob characters in cleanup patterns.
func Namespaced(t *testing.T, base *redisclient.Client) *redisclient.Client {
	t.Helper()

	prefix := fmt.Sprintf(
		"it:%s:%d:%d:",
		sanitizeRedisNamespace(t.Name()),
		time.Now().UnixNano(),
		atomic.AddUint64(&redisNamespaceSeq, 1),
	)

	opts := *base.Options()
	rdb := redisclient.NewClient(&opts)
	rdb.AddHook(prefixHook{prefix: prefix})

	t.Cleanup(func() {
		ctx := context.Background()

		// Use the unhooked base client to scan physical names, then unlink matching
		// keys before closing this test client. The server remains shared with other fixtures.
		var cursor uint64
		// SCAN count hints page size; Redis determines how many keys are returned.
		for {
			keys, nextCursor, err := base.Scan(ctx, cursor, prefix+"*", 500).Result()
			require.NoError(t, err, "scan redis keys for cleanup")
			if len(keys) > 0 {
				require.NoError(t, base.Unlink(ctx, keys...).Err(), "unlink redis keys for cleanup")
			}

			cursor = nextCursor
			if cursor == 0 {
				break
			}
		}

		// Namespace data cleanup has completed before pool closure. Discard the
		// client-close error here while scan and unlink errors are reported through require.
		_ = rdb.Close()
	})

	return rdb
}

// assertTTLWithin checks ttl against inclusive min and max duration values. It
// marks failures through require on the calling test, and callers choose
// margins that accommodate elapsed time between writing and observing
// expiration.
func assertTTLWithin(t *testing.T, ttl time.Duration, min, max time.Duration) {
	t.Helper()
	require.GreaterOrEqual(t, ttl, min, "ttl should be >= min")
	require.LessOrEqual(t, ttl, max, "ttl should be <= max")
}

// sanitizeRedisNamespace replaces slash and space in a test name with
// underscores. Other characters remain unchanged, so timestamp and sequence
// components in Namespaced distinguish normalized names while glob characters
// can still affect cleanup patterns.
func sanitizeRedisNamespace(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, " ", "_")
	return name
}

// DialHook returns the next Redis dialing function with its connection behavior
// unchanged.
func (h prefixHook) DialHook(next redisclient.DialHook) redisclient.DialHook { return next }

// ProcessHook wraps a command handler to rewrite recognized keys before
// dispatch. next must be callable, and it receives the same context and command
// object after prefixCmd mutates the object's arguments.
func (h prefixHook) ProcessHook(next redisclient.ProcessHook) redisclient.ProcessHook {
	return func(ctx context.Context, cmd redisclient.Cmder) error {
		h.prefixCmd(cmd)
		return next(ctx, cmd)
	}
}

// ProcessPipelineHook wraps pipeline dispatch by rewriting recognized keys in
// every command first. next must be callable and receives the supplied command
// objects in order, retaining their values and response handling.
func (h prefixHook) ProcessPipelineHook(next redisclient.ProcessPipelineHook) redisclient.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redisclient.Cmder) error {
		for _, cmd := range cmds {
			h.prefixCmd(cmd)
		}
		return next(ctx, cmds)
	}
}

// prefixCmd rewrites recognized key arguments in the command's existing
// argument slice. String and byte-slice keys are prefixed when nonempty and not
// already carrying this hook's prefix, retaining their argument representation.
//
// The listed ordinary commands prefix argument one, including only the first
// key for multi-key-capable entries such as EXISTS. MGET, DEL, and UNLINK
// prefix each supplied key. EVAL variants use numkeys to locate declared keys
// and leave script text and ARGV intact. SCAN prefixes the first MATCH pattern;
// unsupported commands and invalid declared key counts retain their arguments
// for downstream handling.
func (h prefixHook) prefixCmd(cmd redisclient.Cmder) {
	args := cmd.Args()
	if len(args) < 2 {
		return
	}

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
	// GETDEL and SET share this rewrite so one-time session fixtures consume the
	// value they wrote. Each command in this group rewrites its first key position.
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
		// Script argument layout separates declared KEYS from ARGV. Prefix the
		// declared positions so data arguments and the script source keep their supplied identities.
		numKeys, err := strconv.Atoi(fmt.Sprint(args[2]))
		if err != nil || numKeys <= 0 {
			// An invalid or empty declared-key count gives this hook no key positions.
			// Leave the arguments for Redis command handling rather than rewriting payload values.
			return
		}
		for i := 0; i < numKeys && 3+i < len(args); i++ {
			prefixOne(3 + i)
		}
	case "scan":
		for i := 2; i+1 < len(args); i++ {
			if strings.EqualFold(fmt.Sprint(args[i]), "match") {
				prefixOne(i + 1)
				break
			}
		}
	}
}
