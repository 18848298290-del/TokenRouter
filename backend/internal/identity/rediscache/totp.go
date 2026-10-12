package rediscache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// TOTP setup and login challenges use separate Redis records, while attempt
// counts and step-up grants carry different admission decisions. Reads leave
// challenge records available for the core to validate and delete at its chosen
// point. Missing sessions become nil results, missing counts become zero, and
// missing grants become false; other storage failures remain errors for callers.
//
// Attempt recording pipelines an increment before a fifteen-minute expiry refresh.
// Successful expiry refreshes reset that lifetime; failures can leave an
// increased value with incomplete expiry handling. Step-up grants are presence
// markers keyed by both user and JWT session identifier, so the sensitive-operation
// caller checks the same session that received the grant.

// The key prefixes separate setup, login, attempt, and step-up state;
// totpAttemptsTTL supplies the rolling idle lifetime refreshed after each attempt.
const (
	totpSetupKeyPrefix    = "totp:setup:"
	totpLoginKeyPrefix    = "totp:login:"
	totpAttemptsKeyPrefix = "totp:attempts:"
	totpStepUpKeyPrefix   = "totp:stepup:"
	// totpAttemptsTTL expires a user's counter fifteen minutes after its most
	// recent attempt is recorded successfully with an expiry refresh.
	totpAttemptsTTL = 15 * time.Minute
)

// TotpCache stores identity TOTP challenges, attempt counters, and session grants in Redis.
type TotpCache struct {
	// rdb is retained across all TOTP cache calls. Its caller owns connection
	// cleanup after challenge and grant consumers finish using this shared client.
	rdb *redis.Client
}

// NewTotpCache binds TOTP sessions, attempts, and step-up markers to the supplied
// Redis client. The caller owns that retained connection and closes it after
// authentication services finish using the cache.
func NewTotpCache(rdb *redis.Client) identity.TotpCache {
	return &TotpCache{rdb: rdb}
}

// GetSetupSession decodes the pending TOTP setup record for a user. Missing state
// returns nil with no error so the core can reject an absent challenge; other
// storage and decoding failures remain errors.
func (c *TotpCache) GetSetupSession(ctx context.Context, userID int64) (*identity.TotpSetupSession, error) {
	key := fmt.Sprintf("%s%d", totpSetupKeyPrefix, userID)
	data, err := c.rdb.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			// Absence supplies no active challenge to the core; return a nil session
			// while keeping other Redis failures distinguishable as unavailable storage.
			return nil, nil
		}
		return nil, fmt.Errorf("get setup session: %w", err)
	}

	var session identity.TotpSetupSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("unmarshal setup session: %w", err)
	}

	return &session, nil
}

// SetSetupSession serializes pending TOTP setup state before replacing the user's
// Redis record. Positive TTLs expire the JSON; zero and negative durations store
// it without expiry except redis.KeepTTL, which retains a previous expiry.
// GetSetupSession returns a separately decoded value on each read.
func (c *TotpCache) SetSetupSession(ctx context.Context, userID int64, session *identity.TotpSetupSession, ttl time.Duration) error {
	key := fmt.Sprintf("%s%d", totpSetupKeyPrefix, userID)
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("marshal setup session: %w", err)
	}

	if err := c.rdb.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("set setup session: %w", err)
	}

	return nil
}

// DeleteSetupSession removes a user's pending TOTP setup record through Redis
// DEL. An absent record succeeds, so completion or cleanup can repeat after another
// request already removed the saved challenge.
func (c *TotpCache) DeleteSetupSession(ctx context.Context, userID int64) error {
	key := fmt.Sprintf("%s%d", totpSetupKeyPrefix, userID)
	return c.rdb.Del(ctx, key).Err()
}

// GetLoginSession decodes the TOTP login challenge selected by the supplied
// temporary token. A missing Redis key returns nil without error; storage and JSON
// failures remain distinct errors for the identity caller.
func (c *TotpCache) GetLoginSession(ctx context.Context, tempToken string) (*identity.TotpLoginSession, error) {
	key := totpLoginKeyPrefix + tempToken
	data, err := c.rdb.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			// Absence supplies no active challenge to the core; return a nil session
			// while keeping other Redis failures distinguishable as unavailable storage.
			return nil, nil
		}
		return nil, fmt.Errorf("get login session: %w", err)
	}

	var session identity.TotpLoginSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("unmarshal login session: %w", err)
	}

	return &session, nil
}

// SetLoginSession encodes the login challenge before storing it under the supplied
// temporary token. Positive TTLs expire the JSON; zero and negative durations store
// it without expiry except redis.KeepTTL, which retains a previous expiry. Reading
// the token later leaves the record available for the core's separate deletion.
func (c *TotpCache) SetLoginSession(ctx context.Context, tempToken string, session *identity.TotpLoginSession, ttl time.Duration) error {
	key := totpLoginKeyPrefix + tempToken
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("marshal login session: %w", err)
	}

	if err := c.rdb.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("set login session: %w", err)
	}

	return nil
}

// DeleteLoginSession removes the supplied temporary token's login record through
// Redis DEL. Deleting an absent record succeeds; this separate operation lets the
// core control when a successfully verified challenge is discarded.
func (c *TotpCache) DeleteLoginSession(ctx context.Context, tempToken string) error {
	key := totpLoginKeyPrefix + tempToken
	return c.rdb.Del(ctx, key).Err()
}

// IncrementVerifyAttempts increases the user's TOTP attempt counter and refreshes
// its lifetime to fifteen minutes. The returned count describes the increment
// result after pipeline execution, while execution or result errors prevent a
// usable count from being returned.
//
// INCR precedes EXPIRE in an ordinary Redis pipeline. Each attempt extends the
// expiry, rather than keeping a window fixed at the first attempt. The commands
// can be partly applied if execution fails, so an increased counter may retain
// its earlier expiry or lack one; no compensation is attempted here.
func (c *TotpCache) IncrementVerifyAttempts(ctx context.Context, userID int64) (int, error) {
	key := fmt.Sprintf("%s%d", totpAttemptsKeyPrefix, userID)

	// Queue INCR before EXPIRE so a newly created attempt counter receives the
	// idle lifetime. Pipeline errors can follow an increment whose expiry was not applied.
	pipe := c.rdb.Pipeline()
	incrCmd := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, totpAttemptsTTL)

	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("increment verify attempts: %w", err)
	}

	count, err := incrCmd.Result()
	if err != nil {
		return 0, fmt.Errorf("get increment result: %w", err)
	}

	// Convert the Redis int64 result to the core's Go int attempt count.
	return int(count), nil
}

// GetVerifyAttempts reads the user's current TOTP attempt count. A missing Redis
// key is zero attempts with no error, while malformed counters and storage failures
// remain errors for the core's verification decision.
func (c *TotpCache) GetVerifyAttempts(ctx context.Context, userID int64) (int, error) {
	key := fmt.Sprintf("%s%d", totpAttemptsKeyPrefix, userID)
	count, err := c.rdb.Get(ctx, key).Int()
	if err != nil {
		if err == redis.Nil {
			// An expired or missing counter has no recorded attempts, letting the core
			// start a fresh limit window while other read failures remain errors.
			return 0, nil
		}
		return 0, fmt.Errorf("get verify attempts: %w", err)
	}
	return count, nil
}

// ClearVerifyAttempts removes the user's TOTP attempt counter through Redis DEL.
// An absent counter succeeds, allowing the successful-verification path to reset
// its attempt history even after that history has expired.
func (c *TotpCache) ClearVerifyAttempts(ctx context.Context, userID int64) error {
	key := fmt.Sprintf("%s%d", totpAttemptsKeyPrefix, userID)
	return c.rdb.Del(ctx, key).Err()
}

// totpStepUpKey builds the Redis grant key from user and session identifiers.
func totpStepUpKey(userID int64, sessionKey string) string {
	return fmt.Sprintf("%s%d:%s", totpStepUpKeyPrefix, userID, sessionKey)
}

// SetStepUpGrant writes a presence marker for the supplied user and session.
// Positive TTLs expire the marker; zero and negative durations store it without
// expiry except redis.KeepTTL, which retains a previous expiry. Callers choose the
// session identifier used by later HasStepUpGrant checks for sensitive operations.
func (c *TotpCache) SetStepUpGrant(ctx context.Context, userID int64, sessionKey string, ttl time.Duration) error {
	return c.rdb.Set(ctx, totpStepUpKey(userID, sessionKey), "1", ttl).Err()
}

// HasStepUpGrant reports whether the user's session grant key can be read. A
// present value authorizes the presence check regardless of its text; absence
// returns false without error, and storage failures remain errors.
func (c *TotpCache) HasStepUpGrant(ctx context.Context, userID int64, sessionKey string) (bool, error) {
	_, err := c.rdb.Get(ctx, totpStepUpKey(userID, sessionKey)).Result()
	if err != nil {
		if err == redis.Nil {
			// A missing session marker provides no step-up permission. Storage failures
			// remain errors so the sensitive-operation caller can reject an unavailable check.
			return false, nil
		}
		return false, fmt.Errorf("get step-up grant: %w", err)
	}
	return true, nil
}
