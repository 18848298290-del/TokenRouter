package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// store.go stores OAuth session JSON and coordinates one-time consumption
// markers in Redis.
// OAuth session data remains readable after one caller claims its separate
// consumption marker. The store trims IDs and shares a normalized prefix
// across both keys, serializing data as JSON with a configured lifetime;
// Delete removes data and its marker together so later recreation can be
// consumed again.
//
// Consumption first uses SET NX to claim the marker and then checks the data
// key. A positive, second-resolution TTL read chooses the marker lifetime;
// unavailable or nonpositive TTL uses the configured lifetime. Missing data
// triggers best-effort marker removal, while an existence-query error leaves
// the claimed marker behind. These separate operations require callers to
// handle errors and session-expiry races.

// ErrNotConfigured identifies an absent store or Redis client before session
// data and consumption operations can issue any commands to the backend.
var ErrNotConfigured = errors.New("redis session store not configured")

// Store saves JSON sessions and consumption markers in one Redis namespace.
type Store struct {
	// rdb remains caller-owned; session operations require it to stay open
	// until the application has finished using this store and its markers.
	rdb    *redis.Client
	prefix string        // normalized colon-terminated key prefix
	ttl    time.Duration // configured data and marker lifetime
}

// New creates a JSON session store retaining rdb as a caller-owned client.
// Nonpositive ttl selects thirty minutes; blank prefix selects oauth:session,
// and a missing trailing colon is added after trimming.
func New(rdb *redis.Client, prefix string, ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "oauth:session"
	}
	// Normalize the namespace separator once before constructing both key forms,
	// so data and consumption markers share the same trimmed, colon-terminated prefix.
	if !strings.HasSuffix(prefix, ":") {
		prefix += ":"
	}
	return &Store{rdb: rdb, prefix: prefix, ttl: ttl}
}

// dataKey appends the trimmed session ID to the normalized data prefix.
func (s *Store) dataKey(id string) string {
	return s.prefix + strings.TrimSpace(id)
}

// usedKey appends the trimmed ID after the prefix's consumption-marker
// segment.
func (s *Store) usedKey(id string) string {
	return s.prefix + "used:" + strings.TrimSpace(id)
}

// Set serializes value as JSON and replaces the trimmed ID's data key with
// the configured lifetime. Blank IDs are errors, an absent store/client
// returns ErrNotConfigured, and nil ctx selects background; marshal and Redis
// errors are returned.
func (s *Store) Set(ctx context.Context, id string, value any) error {
	if s == nil || s.rdb == nil {
		return ErrNotConfigured
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("session id is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, s.dataKey(id), raw, s.ttl).Err()
}

// Get decodes the trimmed ID's JSON session into caller-supplied dest. An
// absent store/client returns ErrNotConfigured before ID handling. Configured
// stores return false without error for blank or missing IDs, and nil ctx
// selects background. Decode failure returns false with an error and may
// have partially changed dest.
func (s *Store) Get(ctx context.Context, id string, dest any) (bool, error) {
	if s == nil || s.rdb == nil {
		return false, ErrNotConfigured
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	raw, err := s.rdb.Get(ctx, s.dataKey(id)).Bytes()
	// Redis reports a missing data key through its sentinel. Treat that result
	// as session absence, matching blank IDs while leaving other backend failures observable.
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return false, err
	}
	return true, nil
}

// Delete removes both the trimmed ID's data and consumption marker in one
// Redis DEL. An absent store/client returns ErrNotConfigured before ID
// handling; a configured store ignores blank IDs and uses background for nil
// ctx. Redis deletion failures are returned.
func (s *Store) Delete(ctx context.Context, id string) error {
	if s == nil || s.rdb == nil {
		return ErrNotConfigured
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return s.rdb.Del(ctx, s.dataKey(id), s.usedKey(id)).Err()
}

// TryConsume claims the trimmed ID's consumption marker with SET NX and
// reports whether it then finds session data. Blank IDs return false; nil ctx
// selects background, and an absent client returns ErrNotConfigured.
//
// A positive second-resolution data TTL chooses the marker lifetime,
// otherwise the configured lifetime is used. Contention returns false. An
// existence-query failure leaves the claimed marker in Redis and returns an
// error; missing data attempts to remove that marker and returns false.
// Successful consumption leaves the data readable until deletion or expiry.
func (s *Store) TryConsume(ctx context.Context, id string) (bool, error) {
	if s == nil || s.rdb == nil {
		return false, ErrNotConfigured
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// A failed or nonpositive TTL read still permits a claim using the
	// configured lifetime; a positive sampled TTL supplies the observed marker lifetime.
	ttl := s.ttl
	if remaining, err := s.rdb.TTL(ctx, s.dataKey(id)).Result(); err == nil && remaining > 0 {
		ttl = remaining
	}
	// Claim before checking data so consumers contend on the marker while
	// it exists. The later existence check leaves session data available to readers.
	ok, err := s.rdb.SetNX(ctx, s.usedKey(id), "1", ttl).Result()
	if err != nil || !ok {
		return ok, err
	}
	exists, err := s.rdb.Exists(ctx, s.dataKey(id)).Result()
	if err != nil {
		return false, err
	}
	if exists == 0 {
		// Data can expire between the claim and existence check. Remove the
		// stale marker best-effort; a failed deletion remains until the chosen marker expiry.
		_ = s.rdb.Del(ctx, s.usedKey(id)).Err()
		return false, nil
	}
	return true, nil
}
