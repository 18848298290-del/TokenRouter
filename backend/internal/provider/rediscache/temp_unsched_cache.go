package rediscache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Temporary provider suspension records an absolute deadline with its complete
// error observation. Redis expiry uses the remaining whole-second lifetime,
// and a Lua comparison retains an existing state when a
// later write supplies the same or an earlier deadline, preventing concurrent
// failures from shortening a longer pause; expired input is ignored before
// Redis is called.
//
// OpenAI API Key failures use a separate rolling window measured by Redis
// time. Each insertion has a sequence suffix so failures in one millisecond
// remain distinct, and reaching the threshold removes both the window and its
// sequence key. The provider core uses that returned threshold result to
// decide health actions, while this adapter owns the shared Redis
// observations.

// These prefixes separate serialized suspension observations from rolling OpenAI
// API Key failures while retaining the provider namespaces consumed
// by existing instances.
const (
	tempUnschedPrefix               = "temp_unsched:provider:"
	openAIAPIKeyHealthFailurePrefix = "openai_apikey_health:"
)

var (
	// openAIAPIKeyHealthFailureScript counts unique failures at Redis server time,
	// removes expired scores, and clears window and sequence when their
	// threshold trips.
	openAIAPIKeyHealthFailureScript = redis.NewScript(`
	local key = KEYS[1]
	local sequence_key = key .. ':sequence'
	local now = redis.call('TIME')
	local now_ms = (tonumber(now[1]) * 1000) + math.floor(tonumber(now[2]) / 1000)
	local window_ms = tonumber(ARGV[1]) * 60 * 1000
	local threshold = tonumber(ARGV[2])
	local sequence = redis.call('INCR', sequence_key)

	redis.call('ZREMRANGEBYSCORE', key, '-inf', now_ms - window_ms)
	redis.call('ZADD', key, now_ms, tostring(now_ms) .. ':' .. tostring(sequence))
	local count = redis.call('ZCARD', key)
	local ttl = math.max(60, (tonumber(ARGV[1]) + 1) * 60)
	redis.call('EXPIRE', key, ttl)
	redis.call('EXPIRE', sequence_key, ttl)
	if count >= threshold then
		redis.call('DEL', key, sequence_key)
		return {count, 1}
	end
	return {count, 0}
`)

	// tempUnschedSetScript replaces invalid JSON or earlier suspension state as one
	// operation, retaining a decodable existing deadline when the incoming
	// value cannot extend it.
	tempUnschedSetScript = redis.NewScript(`
	local key = KEYS[1]
	local new_until = tonumber(ARGV[1])
	local new_value = ARGV[2]
	local new_ttl = tonumber(ARGV[3])

	local existing = redis.call('GET', key)
	if existing then
		local ok, existing_data = pcall(cjson.decode, existing)
		if ok and existing_data and existing_data.until_unix then
			local existing_until = tonumber(existing_data.until_unix)
			if existing_until and new_until <= existing_until then
				return 0
			end
		end
	end

	redis.call('SET', key, new_value, 'EX', new_ttl)
	return 1
`)
)

// tempUnschedCache stores suspension and rolling API Key health observations in Redis.
type tempUnschedCache struct {
	// rdb stays caller-owned and usable through all synchronous cache operations;
	// application lifecycle closes the connection after provider health work
	// has finished.
	rdb *redis.Client
}

// NewTempUnschedCache retains a caller-owned Redis client for suspension and
// rolling-failure observations. Its concrete adapter also implements
// OpenAIAPIKeyHealthCache; the provider core supplies health policy, and the
// caller keeps the client usable for every operation.
func NewTempUnschedCache(rdb *redis.Client) provider.TempUnschedCache {
	return &tempUnschedCache{rdb: rdb}
}

// SetTempUnsched stores a caller-supplied nonnil suspension only when its
// future deadline extends any decodable cached deadline. Nil state panics
// after JSON serialization. Expired input succeeds immediately; retained
// state keeps its metadata and expiry, while invalid cached JSON can be replaced.
//
// Remaining lifetime is truncated to whole seconds and floored at one for
// Redis EX. The Lua comparison and replacement occur together, so concurrent
// shorter observations cannot shorten the accepted pause. The supplied state
// remains caller-owned during synchronous serialization, and the returned
// error reports Redis failure rather than whether a longer cached state won.
func (c *tempUnschedCache) SetTempUnsched(ctx context.Context, providerID int64, state *provider.TempUnschedState) error {
	key := fmt.Sprintf("%s%d", tempUnschedPrefix, providerID)

	stateJSON, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	ttl := time.Until(time.Unix(state.UntilUnix, 0))
	// An expired observation has no remaining suspension to publish; retain any
	// current Redis state that may have arrived from a later independent failure.
	if ttl <= 0 {
		return nil
	}

	// Redis EX uses integer seconds; floor positive subsecond lifetimes at one.
	ttlSeconds := max(int(ttl.Seconds()), 1)

	_, err = tempUnschedSetScript.Run(ctx, c.rdb, []string{key}, state.UntilUnix, string(stateJSON), ttlSeconds).Result()
	return err
}

// GetTempUnsched decodes a provider's stored suspension into a new state
// value. A missing key returns nil without error; malformed JSON returns a
// decoding error, and Redis expiry rather than an additional local deadline
// check determines cache absence.
func (c *tempUnschedCache) GetTempUnsched(ctx context.Context, providerID int64) (*provider.TempUnschedState, error) {
	key := fmt.Sprintf("%s%d", tempUnschedPrefix, providerID)

	val, err := c.rdb.Get(ctx, key).Result()
	// Redis expiration is the absence signal for this adapter; a missing key
	// leaves the provider core free to consult its persistent health observation.
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var state provider.TempUnschedState
	if err := json.Unmarshal([]byte(val), &state); err != nil {
		return nil, fmt.Errorf("unmarshal state: %w", err)
	}

	return &state, nil
}

// DeleteTempUnsched removes the provider's suspension key and succeeds when
// it is already absent. It returns Redis deletion failures so core recovery
// can account for incomplete shared-state cleanup.
func (c *tempUnschedCache) DeleteTempUnsched(ctx context.Context, providerID int64) error {
	key := fmt.Sprintf("%s%d", tempUnschedPrefix, providerID)
	return c.rdb.Del(ctx, key).Err()
}

// openAIAPIKeyHealthKey returns the rolling-failure key with a provider-ID
// hash tag. The Lua script appends its sequence suffix to that key, keeping
// both names in the same Redis Cluster slot.
func (c *tempUnschedCache) openAIAPIKeyHealthKey(providerID int64) string {
	// Derive the sequence key under the same hash tag as the rolling window,
	// keeping the two-key Lua state eligible for one Redis Cluster slot.
	return fmt.Sprintf("%s{%d}:failures", openAIAPIKeyHealthFailurePrefix, providerID)
}

// RecordOpenAIAPIKeyHealthFailure records one event in a Redis-time rolling
// window and returns its count and threshold result. Window minutes and
// threshold are clamped to at least one before the script runs.
//
// The script removes scores through the window's lower cutoff, then adds a
// unique timestamp/sequence member so simultaneous failures remain countable.
// Both keys receive a lifetime beyond the configured window, and reaching
// threshold clears both before returning the trip flag. Unexpected result
// count or types are errors, leaving the caller to account for a script that
// may already have changed Redis.
func (c *tempUnschedCache) RecordOpenAIAPIKeyHealthFailure(ctx context.Context, providerID int64, windowMinutes, threshold int) (int64, bool, error) {
	if windowMinutes < 1 {
		windowMinutes = 1
	}
	if threshold < 1 {
		threshold = 1
	}
	// Use Redis time and sequence allocation in one invocation so simultaneous
	// callers share the same window cutoff and retain distinct failure members.
	result, err := openAIAPIKeyHealthFailureScript.Run(ctx, c.rdb, []string{c.openAIAPIKeyHealthKey(providerID)}, windowMinutes, threshold).Slice()
	if err != nil {
		return 0, false, fmt.Errorf("record OpenAI API key health failure: %w", err)
	}
	if len(result) != 2 {
		return 0, false, fmt.Errorf("record OpenAI API key health failure: unexpected result length %d", len(result))
	}
	count, countOK := result[0].(int64)
	tripped, trippedOK := result[1].(int64)
	if !countOK || !trippedOK {
		return 0, false, fmt.Errorf("record OpenAI API key health failure: unexpected result types %T/%T", result[0], result[1])
	}
	return count, tripped == 1, nil
}
