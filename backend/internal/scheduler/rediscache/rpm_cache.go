package rediscache

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Provider request counts are keyed by the minute sampled from Redis TIME, so
// instances use one shared clock for RPM observations. INCR and EXPIRE run in
// one transaction after the key is known, and old minute keys expire after
// two minutes.

// Provider RPM keys encode provider identity and Redis minute index; their
// lifetime retains the active minute and lets older observations expire.
const (
	rpmKeyPrefix = "rpm:" // provider minute counter namespace

	rpmKeyTTL = 120 * time.Second // counter retention duration
)

// RPMCacheImpl records provider requests in Redis-clock minute counter keys.
type RPMCacheImpl struct {
	// rdb is borrowed from the application; TIME, counter writes and reads
	// require it to stay usable for this cache's callers.
	rdb *redis.Client // shared provider counter client
}

// NewRPMCache constructs provider RPM storage over the supplied Redis client.
// The caller retains that client, and writes use Redis time to choose their
// minute keys.
func NewRPMCache(rdb *redis.Client) scheduler.RPMCache {
	return &RPMCacheImpl{rdb: rdb}
}

// currentMinuteKey reads Redis TIME and returns this provider's key for its
// current minute index. A clock-read error aborts key selection before any
// counter write or lookup.
func (c *RPMCacheImpl) currentMinuteKey(ctx context.Context, providerID int64) (string, error) {
	serverTime, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return "", fmt.Errorf("redis TIME: %w", err)
	}
	// Divide Redis Unix seconds into sixty-second bucket indexes.
	minuteTS := serverTime.Unix() / 60
	return fmt.Sprintf("%s%d:%d", rpmKeyPrefix, providerID, minuteTS), nil
}

// currentMinuteSuffix reads Redis TIME and returns one minute index for a
// batch of provider keys. Reusing this result keeps every requested provider
// in the same sampled minute.
func (c *RPMCacheImpl) currentMinuteSuffix(ctx context.Context) (string, error) {
	serverTime, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return "", fmt.Errorf("redis TIME: %w", err)
	}
	// Divide Redis Unix seconds into sixty-second bucket indexes.
	minuteTS := serverTime.Unix() / 60
	return strconv.FormatInt(minuteTS, 10), nil
}

// IncrementRPM increments this provider's sampled minute counter and returns
// its recorded count. The transaction refreshes the two-minute expiry; a
// Redis response error leaves the increment result unconfirmed for callers.
func (c *RPMCacheImpl) IncrementRPM(ctx context.Context, providerID int64) (int, error) {
	key, err := c.currentMinuteKey(ctx, providerID)
	if err != nil {
		return 0, fmt.Errorf("rpm increment: %w", err)
	}

	// Queue expiry with the increment so EXEC applies both commands together;
	// the minute was resolved beforehand because transaction commands need their
	// final key.
	pipe := c.rdb.TxPipeline()
	incrCmd := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, rpmKeyTTL)

	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("rpm increment: %w", err)
	}

	return int(incrCmd.Val()), nil
}

// GetRPM reads the provider counter for the current sampled Redis minute. A
// missing key returns zero for that minute; TIME, read and integer-conversion
// failures remain errors.
func (c *RPMCacheImpl) GetRPM(ctx context.Context, providerID int64) (int, error) {
	key, err := c.currentMinuteKey(ctx, providerID)
	if err != nil {
		return 0, fmt.Errorf("rpm get: %w", err)
	}

	val, err := c.rdb.Get(ctx, key).Int()
	// An absent Redis-clock minute key means no requests have been recorded for
	// that minute, so report zero to RPM selection consumers.
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("rpm get: %w", err)
	}
	return val, nil
}

// GetRPMBatch reads requested providers using one Redis minute sample and
// returns counts by provider ID. Empty input returns an empty map; the
// pipeline's first error aborts the batch unless it is redis.Nil.
//
// When that error is absent or denotes a missing key, individual command and
// integer-conversion failures become zero. A missing key reported first can
// therefore conceal a later command failure while other counts remain usable.
func (c *RPMCacheImpl) GetRPMBatch(ctx context.Context, providerIDs []int64) (map[int64]int, error) {
	if len(providerIDs) == 0 {
		return map[int64]int{}, nil
	}

	// Resolve one minute before constructing provider keys so a batch crossing a
	// clock rollover still queries a common sampled counter interval.
	minuteSuffix, err := c.currentMinuteSuffix(ctx)
	if err != nil {
		return nil, fmt.Errorf("rpm batch get: %w", err)
	}

	pipe := c.rdb.Pipeline()
	cmds := make(map[int64]*redis.StringCmd, len(providerIDs))
	for _, id := range providerIDs {
		key := fmt.Sprintf("%s%d:%s", rpmKeyPrefix, id, minuteSuffix)
		cmds[id] = pipe.Get(ctx, key)
	}

	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("rpm batch get: %w", err)
	}

	result := make(map[int64]int, len(providerIDs))
	for id, cmd := range cmds {
		if val, err := cmd.Int(); err == nil {
			result[id] = val
		} else {
			// Missing or unparsable individual counters contribute zero while
			// retaining other provider observations, so one conversion failure does
			// not discard the successful batch values.
			result[id] = 0
		}
	}
	return result, nil
}
