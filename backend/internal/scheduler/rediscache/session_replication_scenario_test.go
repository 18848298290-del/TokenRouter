package rediscache

import (
	"context"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// TIME-dependent scripts opt into effects replication before reading the
// Redis clock for compatibility with older Redis deployments. A recorder
// captures the source supplied to EVAL, and the test checks ordering in that
// source without executing Redis commands.

// luaScriptSourceRecorder captures EVAL source for inspection through the
// go-redis Scripter interface.
type luaScriptSourceRecorder struct {
	// scripts accumulates captured EVAL source in call order; each subtest
	// creates its own recorder and inspects the single appended entry.
	scripts []string // captured script source texts
}

// Eval records the supplied script and returns a command carrying a nil
// result. It supplies source inspection for this fixture; keys and arguments
// are not executed against Redis.
func (r *luaScriptSourceRecorder) Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
	r.scripts = append(r.scripts, script)
	cmd := redis.NewCmd(ctx)
	cmd.SetVal(nil)
	return cmd
}

// EvalSha returns an unset command to satisfy the unused hashed-script
// fixture method.
func (r *luaScriptSourceRecorder) EvalSha(ctx context.Context, sha1 string, keys []string, args ...any) *redis.Cmd {
	return redis.NewCmd(ctx)
}

// EvalRO returns an unset command for the unused read-only source fixture
// method.
func (r *luaScriptSourceRecorder) EvalRO(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
	return redis.NewCmd(ctx)
}

// EvalShaRO returns an unset command for the unused read-only hashed fixture
// method.
func (r *luaScriptSourceRecorder) EvalShaRO(ctx context.Context, sha1 string, keys []string, args ...any) *redis.Cmd {
	return redis.NewCmd(ctx)
}

// ScriptExists returns an unset result for the unused script-presence fixture
// method.
func (r *luaScriptSourceRecorder) ScriptExists(ctx context.Context, hashes ...string) *redis.BoolSliceCmd {
	return redis.NewBoolSliceCmd(ctx)
}

// ScriptLoad returns an unset result for the unused script-loading fixture
// method.
func (r *luaScriptSourceRecorder) ScriptLoad(ctx context.Context, script string) *redis.StringCmd {
	return redis.NewStringCmd(ctx)
}

// TestRedisLuaTimeScriptsEnableEffectsReplication captures eight scripts that
// read the Redis clock. It checks that both calls are present and
// replicate_commands occurs before TIME in each captured source.
func TestRedisLuaTimeScriptsEnableEffectsReplication(t *testing.T) {
	scripts := map[string]*redis.Script{
		"acquireScript": acquireScript, "getCountScript": getCountScript, "cleanupExpiredSlotsScript": cleanupExpiredSlotsScript,
		"registerSessionScript": registerSessionScript, "refreshSessionScript": refreshSessionScript, "getActiveSessionCountScript": getActiveSessionCountScript, "isSessionActiveScript": isSessionActiveScript, "releaseLockScript": releaseLockScript,
	}

	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			recorder := &luaScriptSourceRecorder{}
			script.Eval(context.Background(), recorder, nil)
			require.Len(t, recorder.scripts, 1)

			source := recorder.scripts[0]
			timeCallIndex := strings.Index(source, "redis.call('TIME')")
			replicateIndex := strings.Index(source, "redis.replicate_commands()")

			require.NotEqual(t, -1, timeCallIndex)
			require.NotEqual(t, -1, replicateIndex)
			// Check the compatibility opt-in before the clock call using captured
			// source text; this fixture establishes ordering, not executed replication
			// behavior.
			require.Less(t, replicateIndex, timeCallIndex)
		})
	}
}
