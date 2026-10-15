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

// queue.go coordinates creative queue admission, reservation, recovery, and
// job locking in Redis.
// Redis ready work, active ownership, and inflight admission need to move
// together when a task is reserved. The reserve script pops one ready ID and
// publishes its active timestamp and new lease in one operation, while enqueue
// creates the inflight marker with the ready entry. Polling surrounds the
// reserve script so cancellation and finite wait budgets can be observed
// between attempts.
//
// Acknowledgment, requeue, and heartbeat compare active membership and the
// current lease token before changing queue state. Stale recovery uses the
// active age, clears the old lease, and moves work back to ready; a later
// reservation installs a new token. A separate token-owned job lock prevents
// simultaneous processing and can be renewed without changing queue ownership.
// Inflight TTL bounds duplicate admission independently from job-lock TTL and
// heartbeat timestamps.

// Default keys separate ready, delayed, active, inflight, and job-lock data.
// Their retention durations govern duplicate admission and processing-lock
// lifetime when QueueOptions supplies no positive override.
const (
	defaultCreativeReadyKey       = "creative:queue:ready"
	defaultCreativeDelayedKey     = "creative:queue:delayed"
	defaultCreativeActiveKey      = "creative:queue:active"
	defaultCreativeInflightPrefix = "creative:queue:inflight:"
	defaultCreativeLockPrefix     = "creative:queue:lock:"
	defaultCreativeInflightTTL    = 7 * 24 * time.Hour
	defaultCreativeJobLockTTL     = 5 * time.Minute

	// creativeReservePollInterval spaces retries when the atomic reserve
	// script finds no work.
	// Polling lets the script pop a task and mark it active in one atomic operation.
	creativeReservePollInterval = time.Second
)

// These Lua operations move queue entries and compare lease or lock tokens
// inside Redis so an operation cannot interleave its membership check with
// another worker bookkeeping write. Compile-time assertions cover the domain interfaces.
var (
	// creativeMoveDueDelayedScript transfers due delayed IDs to ready in one
	// Redis operation, retaining inflight admission until later reservation or
	// acknowledgment.
	creativeMoveDueDelayedScript = redis.NewScript(`
local jobs = redis.call("ZRANGEBYSCORE", KEYS[1], "-inf", ARGV[1], "LIMIT", 0, ARGV[2])
for _, job in ipairs(jobs) do
  redis.call("ZREM", KEYS[1], job)
  redis.call("LPUSH", KEYS[2], job)
end
return #jobs
`)

	// creativeRecoverStaleActiveScript moves old active timestamps to ready and
	// deletes their queue lease keys, fencing the previous reserved worker on its
	// later updates.
	creativeRecoverStaleActiveScript = redis.NewScript(`
local jobs = redis.call("ZRANGEBYSCORE", KEYS[1], "-inf", ARGV[1], "LIMIT", 0, ARGV[2])
for _, job in ipairs(jobs) do
  redis.call("ZREM", KEYS[1], job)
  redis.call("LPUSH", KEYS[2], job)
  redis.call("DEL", KEYS[3] .. job)
end
return #jobs
`)

	// creativeAckScript removes active, delayed, and inflight bookkeeping only
	// when active membership and the supplied lease token both still match.
	creativeAckScript = redis.NewScript(`
if redis.call("ZSCORE", KEYS[1], ARGV[1]) and redis.call("GET", KEYS[3] .. ARGV[1]) == ARGV[2] then
  redis.call("ZREM", KEYS[1], ARGV[1])
  redis.call("ZREM", KEYS[2], ARGV[1])
  redis.call("DEL", KEYS[3] .. ARGV[1])
  return 1
end
return 0
`)

	// creativeRequeueScript moves an owned active task to ready or delayed while
	// retaining its inflight marker, blocking duplicate enqueue during the wait.
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

	// creativeHeartbeatScript updates an owned active timestamp after membership
	// and token checks, so heartbeat cannot resurrect an acknowledged or requeued
	// entry.
	creativeHeartbeatScript = redis.NewScript(`
if redis.call("ZSCORE", KEYS[1], ARGV[1]) and redis.call("GET", KEYS[2] .. ARGV[1]) == ARGV[2] then
  redis.call("ZADD", KEYS[1], ARGV[3], ARGV[1])
  return 1
end
return 0
`)

	// creativeReleaseLockScript deletes a job lock only when its current token
	// matches, leaving a later owner lock intact after expiry and replacement.
	creativeReleaseLockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

	// creativeRefreshLockScript extends job-lock expiry only for its current
	// matching token, returning false ownership after expiration or replacement.
	creativeRefreshLockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0
`)

	// creativeReserveScript atomically moves a ready task into the active
	// set and assigns a lease.
	// Separate BRPop and ZAdd calls could lose a task if the process exits
	// between them,
	// while the inflight key blocks re-enqueueing for its TTL (seven days
	// by default).
	creativeReserveScript = redis.NewScript(`
local job = redis.call("RPOP", KEYS[1])
if not job then
  return nil
end
redis.call("ZADD", KEYS[2], ARGV[1], job)
redis.call("SET", KEYS[3] .. job, ARGV[2], "PX", ARGV[3])
return job
`)

	// creativeEnqueueScript atomically creates the inflight key and pushes
	// the task onto ready.
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

// creativeQueue coordinates Redis task admission, active leases, and separate
// processing locks.
type creativeQueue struct {
	// rdb is borrowed by queue and lock operations. Its owner keeps the client
	// usable through their completion and closes it after those users finish.
	rdb *redis.Client // borrowed Redis queue client
	// readyKey holds queued run IDs, delayedKey scores their due Unix milliseconds,
	// and activeKey scores their last reserve or heartbeat Unix milliseconds.
	// Lua scripts coordinate membership with the run inflight lease marker.
	readyKey       string // ready task list key
	delayedKey     string // delayed availability sorted-set key
	activeKey      string // active heartbeat sorted-set key
	inflightPrefix string // duplicate admission and lease prefix
	lockPrefix     string // independent job lock key prefix
	// inflightTTL bounds duplicate admission while its marker remains present.
	// Reservation replaces the marker value with a lease token and resets this TTL.
	inflightTTL time.Duration // inflight marker expiration duration
	lockTTL     time.Duration // default job lock expiration duration
}

// creativeRedisJobLock retains one Redis lock identity and token for release
// or renewal.
type creativeRedisJobLock struct {
	// rdb retains the borrowed client while key and token identify this acquired
	// lock. Release and Refresh compare that token before changing key state,
	// leaving an independently acquired replacement lock untouched.
	rdb   *redis.Client // borrowed Redis lock client
	key   string        // acquired job lock key
	token string        // acquired job lock owner token
}

// QueueOptions supplies Redis key names and retention overrides for creative
// queue construction.
type QueueOptions struct {
	InflightKeyPrefix  string // inflight admission and lease prefix
	InflightTTLSeconds int    // seconds
	JobLockTTLSeconds  int    // seconds
	LockKeyPrefix      string // task processing lock prefix
	QueueActiveKey     string // active heartbeat sorted-set key
	QueueDelayedKey    string // delayed availability sorted-set key
	QueueReadyKey      string // ready task list key
}

// NewCreativeQueue retains the supplied Redis client and applies nonempty key
// and positive TTL overrides. Nil options use defaults, and callers keep the
// shared client available for queue and lock operations.
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
		// Configured whole seconds become the Redis expiration durations used for
		// duplicate admission and independent job-lock ownership by
		// later operations.
		if cfg.InflightTTLSeconds > 0 {
			queue.inflightTTL = time.Duration(cfg.InflightTTLSeconds) * time.Second
		}
		if cfg.JobLockTTLSeconds > 0 {
			queue.lockTTL = time.Duration(cfg.JobLockTTLSeconds) * time.Second
		}
	}
	return queue
}

// Enqueue atomically adds valid creative work with its inflight admission
// marker. Invalid IDs fail before Redis access, and an existing marker returns
// ErrCreativeAlreadyQueued until acknowledgment, recovery, or expiration
// removes it.
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

// Reserve returns one ready task and its new active lease, or an empty-queue
// result after the wait budget. It always makes an immediate attempt,
// including for nonpositive block timeouts.
//
// Only empty results are polled again, with waits capped at the smaller of
// remaining budget and one second. Context cancellation interrupts waiting,
// while other script errors return directly. reserveOnce publishes task
// movement and lease ownership atomically, so a successful result carries the
// token workers need for their later queue bookkeeping operations.
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
		// Retry only confirmed empty reserves within the remaining wait budget.
		// The timer exposes cancellation between atomic attempts while keeping
		// a short caller timeout from inheriting the full polling interval.
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

// reserveOnce creates a token and invokes the atomic ready-to-active
// reservation script. An empty script result returns ErrCreativeQueueEmpty,
// while other failures preserve the store error.
//
// The script has already marked returned string IDs active before Go validates
// their prefix. Invalid nonempty IDs therefore receive best-effort active and
// inflight cleanup before the payload error is returned. Those cleanup errors
// are discarded, so any surviving entry remains subject to queue TTL and stale
// recovery instead of being reported as an executable task.
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
		// Remove the active entry and marker to keep stale recovery from repeatedly
		// returning invalid work. Cleanup errors are discarded, so surviving entries
		// remain eligible for that recovery.
		if ok && runID != "" {
			_ = q.rdb.ZRem(ctx, q.activeKey, runID).Err()
			_ = q.rdb.Del(ctx, q.inflightKey(runID)).Err()
		}
		return "", "", creative.ErrInvalidCreativeQueuePayload
	}
	return runID, leaseToken, nil
}

// RequeueAfter moves an owned active task to delayed work or ready when delay
// is nonpositive. It validates the ID and token, reports stale ownership as
// ErrCreativeLeaseLost, and retains inflight admission during the requeue.
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

// Ack removes active, delayed, and inflight bookkeeping for a matching valid
// task and token. Empty tokens or absent/replaced ownership return
// ErrCreativeLeaseLost; callers acknowledge only after their own task
// processing finishes.
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

// Heartbeat refreshes a matching active lease timestamp and reports ownership.
// Invalid IDs or empty tokens fail before Redis, and a nonmatching or absent
// active lease returns false without recreating the task.
func (q *creativeQueue) Heartbeat(ctx context.Context, runID, leaseToken string) (bool, error) {
	if !creative.IsValidCreativeRunID(runID) {
		return false, creative.ErrInvalidCreativeQueuePayload
	}
	if leaseToken == "" {
		return false, creative.ErrCreativeLeaseLost
	}
	// Check active membership and lease ownership in the same script as the
	// timestamp write. A heartbeat arriving after acknowledgment or requeue cannot
	// recreate the removed active entry through an unconditional ZAdd.
	result, err := creativeHeartbeatScript.Run(ctx, q.rdb,
		[]string{q.activeKey, q.inflightPrefix}, runID, leaseToken, time.Now().UnixMilli()).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

// MoveDueDelayedToReady atomically moves one due batch from delayed to ready
// using the current millisecond time. Nonpositive limits select one hundred,
// and returned counts include the entries moved by that script.
func (q *creativeQueue) MoveDueDelayedToReady(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	return creativeMoveDueDelayedScript.Run(ctx, q.rdb, []string{q.delayedKey, q.readyKey}, time.Now().UnixMilli(), limit).Int()
}

// RecoverStaleActive atomically returns old active entries to ready and
// removes their lease keys. The stale duration must be positive; nonpositive
// limits select one hundred, and later reservation assigns replacement
// ownership.
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

// TryAcquireJobLock attempts a token-owned Redis task lock for a valid
// creative ID. Nonpositive TTL selects the queue default, contention returns
// no handle and false, and callers release a successfully returned handle
// after processing.
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

// inflightKey returns the configured inflight prefix followed by the supplied
// run identifier.
func (q *creativeQueue) inflightKey(runID string) string {
	return q.inflightPrefix + runID
}

// lockKey returns the configured job-lock prefix followed by the supplied run
// identifier.
func (q *creativeQueue) lockKey(runID string) string {
	return q.lockPrefix + runID
}

// Release deletes this job lock only while its token still matches. Nil or
// incomplete handles succeed directly, and expired or replacement locks remain
// untouched; Redis transport failures are returned.
func (l *creativeRedisJobLock) Release(ctx context.Context) error {
	if l == nil || l.rdb == nil || l.key == "" || l.token == "" {
		return nil
	}
	return creativeReleaseLockScript.Run(ctx, l.rdb, []string{l.key}, l.token).Err()
}

// Refresh renews the matching job-lock token and reports whether Redis applied
// the expiry. Nonpositive TTL selects five minutes, nil or incomplete handles
// return false, and store errors leave ownership uncertain.
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

// newCreativeLockToken returns sixteen random bytes encoded as a lowercase
// hexadecimal ownership value. A returned token has a nil error.
// crypto/rand.Read terminates the process when its io.ReadFull operation
// reports an error.
func newCreativeLockToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
