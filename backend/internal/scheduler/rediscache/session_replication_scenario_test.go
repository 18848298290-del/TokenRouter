package rediscache

// These scenarios check Lua replication settings in concurrency_cache.go, session_limit.go, and user_msg_queue_cache.go.

import (
	"context"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type luaScriptSourceRecorder struct {
	scripts []string
}

func (r *luaScriptSourceRecorder) Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
	r.scripts = append(r.scripts, script)
	cmd := redis.NewCmd(ctx)
	cmd.SetVal(nil)
	return cmd
}

func (r *luaScriptSourceRecorder) EvalSha(ctx context.Context, sha1 string, keys []string, args ...any) *redis.Cmd {
	return redis.NewCmd(ctx)
}

func (r *luaScriptSourceRecorder) EvalRO(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
	return redis.NewCmd(ctx)
}

func (r *luaScriptSourceRecorder) EvalShaRO(ctx context.Context, sha1 string, keys []string, args ...any) *redis.Cmd {
	return redis.NewCmd(ctx)
}

func (r *luaScriptSourceRecorder) ScriptExists(ctx context.Context, hashes ...string) *redis.BoolSliceCmd {
	return redis.NewBoolSliceCmd(ctx)
}

func (r *luaScriptSourceRecorder) ScriptLoad(ctx context.Context, script string) *redis.StringCmd {
	return redis.NewStringCmd(ctx)
}

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
			// Scripts using TIME enable effects replication first for Redis 3.2 through 4.x write replication.
			require.Less(t, replicateIndex, timeCallIndex)
		})
	}
}
