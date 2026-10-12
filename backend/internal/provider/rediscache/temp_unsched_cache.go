package rediscache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

const (
	tempUnschedPrefix               = "temp_unsched:provider:"
	openAIAPIKeyHealthFailurePrefix = "openai_apikey_health:"
)

var (
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

type tempUnschedCache struct {
	rdb *redis.Client
}

func NewTempUnschedCache(rdb *redis.Client) provider.TempUnschedCache {
	return &tempUnschedCache{rdb: rdb}
}

// SetTempUnsched stores a suspension if it extends the cached deadline.
func (c *tempUnschedCache) SetTempUnsched(ctx context.Context, providerID int64, state *provider.TempUnschedState) error {
	key := fmt.Sprintf("%s%d", tempUnschedPrefix, providerID)

	stateJSON, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	ttl := time.Until(time.Unix(state.UntilUnix, 0))
	if ttl <= 0 {
		return nil // Expired suspensions are ignored.
	}

	ttlSeconds := max(int(ttl.Seconds()), 1)

	_, err = tempUnschedSetScript.Run(ctx, c.rdb, []string{key}, state.UntilUnix, string(stateJSON), ttlSeconds).Result()
	return err
}

// GetTempUnsched returns the cached suspension, or nil when absent.
func (c *tempUnschedCache) GetTempUnsched(ctx context.Context, providerID int64) (*provider.TempUnschedState, error) {
	key := fmt.Sprintf("%s%d", tempUnschedPrefix, providerID)

	val, err := c.rdb.Get(ctx, key).Result()
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

// DeleteTempUnsched removes the cached suspension.
func (c *tempUnschedCache) DeleteTempUnsched(ctx context.Context, providerID int64) error {
	key := fmt.Sprintf("%s%d", tempUnschedPrefix, providerID)
	return c.rdb.Del(ctx, key).Err()
}

func (c *tempUnschedCache) openAIAPIKeyHealthKey(providerID int64) string {
	// The hash tag places the rolling-window and derived sequence keys in the same Redis Cluster slot.
	return fmt.Sprintf("%s{%d}:failures", openAIAPIKeyHealthFailurePrefix, providerID)
}

func (c *tempUnschedCache) RecordOpenAIAPIKeyHealthFailure(ctx context.Context, providerID int64, windowMinutes, threshold int) (int64, bool, error) {
	if windowMinutes < 1 {
		windowMinutes = 1
	}
	if threshold < 1 {
		threshold = 1
	}
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
