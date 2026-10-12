package rediscache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/creative"
)

const (
	defaultCreativeReadyKey       = "creative:queue:ready"
	defaultCreativeDelayedKey     = "creative:queue:delayed"
	defaultCreativeActiveKey      = "creative:queue:active"
	defaultCreativeInflightPrefix = "creative:queue:inflight:"
	defaultCreativeLockPrefix     = "creative:queue:lock:"
	defaultCreativeInflightTTL    = 7 * 24 * time.Hour
	defaultCreativeJobLockTTL     = 5 * time.Minute

	// creativeReservePollInterval spaces retries when the atomic reserve script finds no work.
	// Polling lets the script pop a task and mark it active in one atomic operation.
	creativeReservePollInterval = time.Second
)

var (
	creativeMoveDueDelayedScript = redis.NewScript(`
local jobs = redis.call("ZRANGEBYSCORE", KEYS[1], "-inf", ARGV[1], "LIMIT", 0, ARGV[2])
for _, job in ipairs(jobs) do
  redis.call("ZREM", KEYS[1], job)
  redis.call("LPUSH", KEYS[2], job)
end
return #jobs
`)

	creativeRecoverStaleActiveScript = redis.NewScript(`
local jobs = redis.call("ZRANGEBYSCORE", KEYS[1], "-inf", ARGV[1], "LIMIT", 0, ARGV[2])
for _, job in ipairs(jobs) do
  redis.call("ZREM", KEYS[1], job)
  redis.call("LPUSH", KEYS[2], job)
  redis.call("DEL", KEYS[3] .. job)
end
return #jobs
`)

	creativeAckScript = redis.NewScript(`
if redis.call("ZSCORE", KEYS[1], ARGV[1]) and redis.call("GET", KEYS[3] .. ARGV[1]) == ARGV[2] then
  redis.call("ZREM", KEYS[1], ARGV[1])
  redis.call("ZREM", KEYS[2], ARGV[1])
  redis.call("DEL", KEYS[3] .. ARGV[1])
  return 1
end
return 0
`)

	creativeRequeueScript = redis.NewScript(`
if redis.call("ZSCORE", KEYS[1], ARGV[1]) and redis.call("GET", KEYS[3] .. ARGV[1]) == ARGV[2] then
  redis.call("ZREM", KEYS[1], ARGV[1])
  redis.call("ZREM", KEYS[2], ARGV[1])
  if tonumber(ARGV[4]) <= 0 then
    redis.call("LPUSH", KEYS[4], ARGV[1])
  else
    redis.call("ZADD", KEYS[2], ARGV[3], ARGV[1])
  end
  return 1
end
return 0
`)

	creativeHeartbeatScript = redis.NewScript(`
if redis.call("ZSCORE", KEYS[1], ARGV[1]) and redis.call("GET", KEYS[2] .. ARGV[1]) == ARGV[2] then
  redis.call("ZADD", KEYS[1], ARGV[3], ARGV[1])
  return 1
end
return 0
`)

	creativeReleaseLockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

	creativeRefreshLockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0
`)

	// creativeReserveScript atomically moves a ready task into the active set and assigns a lease.
	// Separate BRPop and ZAdd calls could lose a task if the process exits between them,
	// while the inflight key blocks re-enqueueing for its TTL (seven days by default).
	creativeReserveScript = redis.NewScript(`
local job = redis.call("RPOP", KEYS[1])
if not job then
  return nil
end
redis.call("ZADD", KEYS[2], ARGV[1], job)
redis.call("SET", KEYS[3] .. job, ARGV[2], "PX", ARGV[3])
return job
`)

	// creativeEnqueueScript atomically creates the inflight key and pushes the task onto ready.
	// Separate SetNX and LPush calls could leave only the inflight key after a crash,
	// blocking later enqueue attempts for its TTL (seven days by default).
	creativeEnqueueScript = redis.NewScript(`
if redis.call("SET", KEYS[1], ARGV[1], "NX", "PX", ARGV[2]) then
  redis.call("LPUSH", KEYS[2], ARGV[1])
  return 1
end
return 0
`)

	_ creative.CreativeRunQueue            = (*creativeQueue)(nil)
	_ creative.CreativeRunJobLockRefresher = (*creativeRedisJobLock)(nil)
)

type creativeQueue struct {
	rdb            *redis.Client
	readyKey       string
	delayedKey     string
	activeKey      string
	inflightPrefix string
	lockPrefix     string
	inflightTTL    time.Duration
	lockTTL        time.Duration
}

type creativeRedisJobLock struct {
	rdb   *redis.Client
	key   string
	token string
}

type QueueOptions struct {
	InflightKeyPrefix  string
	InflightTTLSeconds int
	JobLockTTLSeconds  int
	LockKeyPrefix      string
	QueueActiveKey     string
	QueueDelayedKey    string
	QueueReadyKey      string
}

// NewCreativeQueue creates a Redis queue with defaults overridden by nonempty keys and positive TTLs.
func NewCreativeQueue(rdb *redis.Client, cfg *QueueOptions) creative.CreativeRunQueue {
	queue := &creativeQueue{
		rdb:            rdb,
		readyKey:       defaultCreativeReadyKey,
		delayedKey:     defaultCreativeDelayedKey,
		activeKey:      defaultCreativeActiveKey,
		inflightPrefix: defaultCreativeInflightPrefix,
		lockPrefix:     defaultCreativeLockPrefix,
		inflightTTL:    defaultCreativeInflightTTL,
		lockTTL:        defaultCreativeJobLockTTL,
	}
	if cfg != nil {
		if cfg.QueueReadyKey != "" {
			queue.readyKey = cfg.QueueReadyKey
		}
		if cfg.QueueDelayedKey != "" {
			queue.delayedKey = cfg.QueueDelayedKey
		}
		if cfg.QueueActiveKey != "" {
			queue.activeKey = cfg.QueueActiveKey
		}
		if cfg.InflightKeyPrefix != "" {
			queue.inflightPrefix = cfg.InflightKeyPrefix
		}
		if cfg.LockKeyPrefix != "" {
			queue.lockPrefix = cfg.LockKeyPrefix
		}
		if cfg.InflightTTLSeconds > 0 {
			queue.inflightTTL = time.Duration(cfg.InflightTTLSeconds) * time.Second
		}
		if cfg.JobLockTTLSeconds > 0 {
			queue.lockTTL = time.Duration(cfg.JobLockTTLSeconds) * time.Second
		}
	}
	return queue
}

func (q *creativeQueue) Enqueue(ctx context.Context, runID string) error {
	if !creative.IsValidCreativeRunID(runID) {
		return creative.ErrInvalidCreativeQueuePayload
	}
	applied, err := creativeEnqueueScript.Run(ctx, q.rdb,
		[]string{q.inflightKey(runID), q.readyKey},
		runID, q.inflightTTL.Milliseconds(),
	).Int()
	if err != nil {
		return err
	}
	if applied == 0 {
		return creative.ErrCreativeAlreadyQueued
	}
	return nil
}

func (q *creativeQueue) Reserve(ctx context.Context, blockTimeout time.Duration) (creative.ReservedCreativeRun, error) {
	deadline := time.Now().Add(blockTimeout)
	for {
		runID, leaseToken, err := q.reserveOnce(ctx)
		if err == nil {
			return creative.ReservedCreativeRun{RunID: runID, LeaseToken: leaseToken}, nil
		}
		if !errors.Is(err, creative.ErrCreativeQueueEmpty) {
			return creative.ReservedCreativeRun{}, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return creative.ReservedCreativeRun{}, creative.ErrCreativeQueueEmpty
		}
		wait := min(remaining, creativeReservePollInterval)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return creative.ReservedCreativeRun{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (q *creativeQueue) reserveOnce(ctx context.Context) (string, string, error) {
	leaseToken, err := newCreativeLockToken()
	if err != nil {
		return "", "", err
	}
	raw, err := creativeReserveScript.Run(ctx, q.rdb, []string{q.readyKey, q.activeKey, q.inflightPrefix}, time.Now().UnixMilli(), leaseToken, q.inflightTTL.Milliseconds()).Result()
	if errors.Is(err, redis.Nil) {
		return "", "", creative.ErrCreativeQueueEmpty
	}
	if err != nil {
		return "", "", err
	}
	runID, ok := raw.(string)
	if !ok || !creative.IsValidCreativeRunID(runID) {
		// The script has already marked this invalid payload active. Remove it so stale
		// recovery cannot repeatedly return it to ready.
		if ok && runID != "" {
			_ = q.rdb.ZRem(ctx, q.activeKey, runID).Err()
			_ = q.rdb.Del(ctx, q.inflightKey(runID)).Err()
		}
		return "", "", creative.ErrInvalidCreativeQueuePayload
	}
	return runID, leaseToken, nil
}

func (q *creativeQueue) RequeueAfter(ctx context.Context, runID, leaseToken string, delay time.Duration) error {
	if !creative.IsValidCreativeRunID(runID) {
		return creative.ErrInvalidCreativeQueuePayload
	}
	if leaseToken == "" {
		return creative.ErrCreativeLeaseLost
	}
	result, err := creativeRequeueScript.Run(ctx, q.rdb,
		[]string{q.activeKey, q.delayedKey, q.inflightPrefix, q.readyKey},
		runID, leaseToken, time.Now().Add(delay).UnixMilli(), delay.Milliseconds()).Int()
	if err != nil {
		return err
	}
	if result == 0 {
		return creative.ErrCreativeLeaseLost
	}
	return nil
}

func (q *creativeQueue) Ack(ctx context.Context, runID, leaseToken string) error {
	if !creative.IsValidCreativeRunID(runID) {
		return creative.ErrInvalidCreativeQueuePayload
	}
	if leaseToken == "" {
		return creative.ErrCreativeLeaseLost
	}
	result, err := creativeAckScript.Run(ctx, q.rdb,
		[]string{q.activeKey, q.delayedKey, q.inflightPrefix}, runID, leaseToken).Int()
	if err != nil {
		return err
	}
	if result == 0 {
		return creative.ErrCreativeLeaseLost
	}
	return nil
}

func (q *creativeQueue) Heartbeat(ctx context.Context, runID, leaseToken string) (bool, error) {
	if !creative.IsValidCreativeRunID(runID) {
		return false, creative.ErrInvalidCreativeQueuePayload
	}
	if leaseToken == "" {
		return false, creative.ErrCreativeLeaseLost
	}
	// Check active membership and lease ownership before updating the timestamp.
	// An unconditional ZAdd could resurrect an entry after Ack or Requeue wins the race.
	result, err := creativeHeartbeatScript.Run(ctx, q.rdb,
		[]string{q.activeKey, q.inflightPrefix}, runID, leaseToken, time.Now().UnixMilli()).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (q *creativeQueue) MoveDueDelayedToReady(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	return creativeMoveDueDelayedScript.Run(ctx, q.rdb, []string{q.delayedKey, q.readyKey}, time.Now().UnixMilli(), limit).Int()
}

func (q *creativeQueue) RecoverStaleActive(ctx context.Context, staleAfter time.Duration, limit int) (int, error) {
	if staleAfter <= 0 {
		return 0, creative.ErrInvalidCreativeQueuePayload
	}
	if limit <= 0 {
		limit = 100
	}
	cutoff := time.Now().Add(-staleAfter).UnixMilli()
	return creativeRecoverStaleActiveScript.Run(ctx, q.rdb, []string{q.activeKey, q.readyKey, q.inflightPrefix}, cutoff, limit).Int()
}

func (q *creativeQueue) TryAcquireJobLock(ctx context.Context, runID string, ttl time.Duration) (creative.CreativeRunJobLock, bool, error) {
	if !creative.IsValidCreativeRunID(runID) {
		return nil, false, creative.ErrInvalidCreativeQueuePayload
	}
	if ttl <= 0 {
		ttl = q.lockTTL
	}
	token, err := newCreativeLockToken()
	if err != nil {
		return nil, false, err
	}
	key := q.lockKey(runID)
	ok, err := q.rdb.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return &creativeRedisJobLock{rdb: q.rdb, key: key, token: token}, true, nil
}

func (q *creativeQueue) inflightKey(runID string) string {
	return q.inflightPrefix + runID
}

func (q *creativeQueue) lockKey(runID string) string {
	return q.lockPrefix + runID
}

func (l *creativeRedisJobLock) Release(ctx context.Context) error {
	if l == nil || l.rdb == nil || l.key == "" || l.token == "" {
		return nil
	}
	return creativeReleaseLockScript.Run(ctx, l.rdb, []string{l.key}, l.token).Err()
}

// Refresh renews the TTL when the token still owns the lock and reports whether renewal succeeded.
func (l *creativeRedisJobLock) Refresh(ctx context.Context, ttl time.Duration) (bool, error) {
	if l == nil || l.rdb == nil || l.key == "" || l.token == "" {
		return false, nil
	}
	if ttl <= 0 {
		ttl = defaultCreativeJobLockTTL
	}
	result, err := creativeRefreshLockScript.Run(ctx, l.rdb, []string{l.key}, l.token, ttl.Milliseconds()).Int()
	return result == 1, err
}

func newCreativeLockToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
