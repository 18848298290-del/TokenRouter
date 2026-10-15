package rediscache

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// email.go stores email verification challenges, password-reset credentials,
// and notification send limits in Redis.
// Email challenge credentials, password-reset state, and recent-send markers
// use separate Redis prefixes even when they refer to one address. Key construction
// folds case while retaining the supplied address otherwise, matching the identity
// flow's case-insensitive comparisons. JSON records are decoded afresh on reads,
// and missing credentials remain Redis errors for the calling verification logic.
//
// The notification-code user counter records delivered, saved codes across addresses.
// Its increment precedes expiry refresh, so expiry failure can leave an increased
// count with the old expiry or no expiry. Cooldown lookup failures select false;
// other credential reads propagate their failures. The identity services own mail
// delivery ordering and credential-use decisions around these storage operations.

// These prefixes separate login challenges, notification challenges, reset
// credentials, reset-send cooldowns, and per-user notification-code request counters.
const (
	verifyCodeKeyPrefix          = "verify_code:"
	notifyVerifyKeyPrefix        = "notify_verify:"
	passwordResetKeyPrefix       = "password_reset:"
	passwordResetSentAtKeyPrefix = "password_reset_sent:"
	notifyCodeUserRateKeyPrefix  = "notify_code_user_rate:"
)

// emailCache implements identity credential and cooldown storage with Redis commands.
type emailCache struct {
	// rdb is retained for every cache operation. The caller owns this shared
	// client and closes it after identity services stop using the cache.
	rdb *redis.Client
}

// verifyCodeKey builds the login-email challenge key using the lowercased address.
// Whitespace and mailbox aliases remain as supplied, so callers provide the
// normalized address appropriate to their verification flow.
func verifyCodeKey(email string) string {
	return verifyCodeKeyPrefix + strings.ToLower(email)
}

// notifyVerifyKey builds the notification-email challenge key using lowercase text.
// Its separate prefix keeps notification-address verification distinct from the
// login-email challenge stored for the same address and supplied spelling.
func notifyVerifyKey(email string) string {
	return notifyVerifyKeyPrefix + strings.ToLower(email)
}

// passwordResetKey builds the reset-token key using the lowercased email address.
// Callers select the account address before this conversion; whitespace and alias
// normalization are left to the identity flow.
func passwordResetKey(email string) string {
	return passwordResetKeyPrefix + strings.ToLower(email)
}

// passwordResetSentAtKey builds the reset-mail cooldown key from a lowercased
// address. The cooldown record is separate from the reset token, allowing the
// service to reuse a valid token while checking recent sends.
func passwordResetSentAtKey(email string) string {
	return passwordResetSentAtKeyPrefix + strings.ToLower(email)
}

// NewEmailCache binds identity challenge storage to the supplied Redis client.
// The cache retains that client, and its owner keeps the connection available
// until all challenge and cooldown operations finish.
func NewEmailCache(rdb *redis.Client) identity.EmailCache {
	return &emailCache{rdb: rdb}
}

// GetVerificationCode decodes the login-email challenge saved for the address.
// A missing key returns redis.Nil; storage and JSON failures remain errors so the
// identity service can distinguish an unavailable code from a usable record.
func (c *emailCache) GetVerificationCode(ctx context.Context, email string) (*identity.VerificationCodeData, error) {
	key := verifyCodeKey(email)
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	var data identity.VerificationCodeData
	if err := json.Unmarshal([]byte(val), &data); err != nil {
		return nil, err
	}
	return &data, nil
}

// SetVerificationCode serializes the login-email challenge before writing its
// Redis record. Positive TTLs expire the JSON; zero and negative durations store
// it without expiry except redis.KeepTTL, which retains a previous expiry. Each
// subsequent read decodes its own record value from the saved JSON.
func (c *emailCache) SetVerificationCode(ctx context.Context, email string, data *identity.VerificationCodeData, ttl time.Duration) error {
	key := verifyCodeKey(email)
	val, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, key, val, ttl).Err()
}

// DeleteVerificationCode removes the address's login-email challenge key through
// Redis DEL. A key that is already absent is successful, allowing verification
// cleanup to repeat after other request paths have removed it.
func (c *emailCache) DeleteVerificationCode(ctx context.Context, email string) error {
	key := verifyCodeKey(email)
	return c.rdb.Del(ctx, key).Err()
}

// GetPasswordResetToken decodes the address's saved password-reset token state.
// Missing keys return redis.Nil, while storage or decoding failures remain errors
// for the service deciding whether an existing reset credential can be reused.
func (c *emailCache) GetPasswordResetToken(ctx context.Context, email string) (*identity.PasswordResetTokenData, error) {
	key := passwordResetKey(email)
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	var data identity.PasswordResetTokenData
	if err := json.Unmarshal([]byte(val), &data); err != nil {
		return nil, err
	}
	return &data, nil
}

// SetPasswordResetToken encodes the reset state before replacing the address's
// Redis record. Positive TTLs expire the JSON; zero and negative durations store
// it without expiry except redis.KeepTTL, which retains a previous expiry. The
// calling service manages reset-mail cooldown storage as a separate operation.
func (c *emailCache) SetPasswordResetToken(ctx context.Context, email string, data *identity.PasswordResetTokenData, ttl time.Duration) error {
	key := passwordResetKey(email)
	val, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, key, val, ttl).Err()
}

// DeletePasswordResetToken removes the address's reset credential through Redis
// DEL. An absent record succeeds, so callers can clean up credentials after
// consumption or expiry without requiring a prior existence check.
func (c *emailCache) DeletePasswordResetToken(ctx context.Context, email string) error {
	key := passwordResetKey(email)
	return c.rdb.Del(ctx, key).Err()
}

// IsPasswordResetEmailInCooldown reports whether the address's cooldown key
// exists. A Redis read failure returns false, allowing the calling mail flow to
// continue as though no recent-send marker had been observed.
func (c *emailCache) IsPasswordResetEmailInCooldown(ctx context.Context, email string) bool {
	key := passwordResetSentAtKey(email)
	exists, err := c.rdb.Exists(ctx, key).Result()
	// A failed lookup supplies no usable cooldown observation, so the mail flow
	// proceeds; credential reads elsewhere still return their storage failures.
	return err == nil && exists > 0
}

// SetPasswordResetEmailCooldown writes a fixed presence marker for the address.
// Positive TTLs expire the marker; zero and negative durations store it without
// expiry except redis.KeepTTL, which retains a previous expiry. Reset credentials
// and their validity are stored separately from this recent-send marker.
func (c *emailCache) SetPasswordResetEmailCooldown(ctx context.Context, email string, ttl time.Duration) error {
	key := passwordResetSentAtKey(email)
	return c.rdb.Set(ctx, key, "1", ttl).Err()
}

// GetNotifyVerifyCode decodes the notification-address challenge saved for the
// lowercased email. Missing keys return redis.Nil, while other Redis or JSON errors
// remain available to the notification-email verification service.
func (c *emailCache) GetNotifyVerifyCode(ctx context.Context, email string) (*identity.VerificationCodeData, error) {
	key := notifyVerifyKey(email)
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	var data identity.VerificationCodeData
	if err := json.Unmarshal([]byte(val), &data); err != nil {
		return nil, err
	}
	return &data, nil
}

// SetNotifyVerifyCode encodes the notification-address challenge before writing
// its Redis record. Positive TTLs expire the JSON; zero and negative durations
// store it without expiry except redis.KeepTTL, which retains a previous expiry.
// Its prefix separates this credential from login-email verification for the same
// address.
func (c *emailCache) SetNotifyVerifyCode(ctx context.Context, email string, data *identity.VerificationCodeData, ttl time.Duration) error {
	key := notifyVerifyKey(email)
	val, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, key, val, ttl).Err()
}

// DeleteNotifyVerifyCode removes the notification-address challenge through Redis
// DEL. Repeating cleanup after the key disappears succeeds; other Redis failures
// are returned to the service completing verification.
func (c *emailCache) DeleteNotifyVerifyCode(ctx context.Context, email string) error {
	key := notifyVerifyKey(email)
	return c.rdb.Del(ctx, key).Err()
}

// notifyCodeUserRateKey builds the notification-code request counter key for a user.
func notifyCodeUserRateKey(userID int64) string {
	return notifyCodeUserRateKeyPrefix + fmt.Sprintf("%d", userID)
}

// IncrNotifyCodeUserRate increments the user's notification-code request count and
// then applies window through Redis EXPIRE. Positive windows refresh the lifetime
// after each increment using go-redis's whole-second conversion; nonpositive
// windows delete the counter. Profile rate checking reads the saved count on later
// requests, and this call returns the increment result.
//
// The two Redis commands execute separately. If increment succeeds but expiry
// fails, the increment remains and its count is returned with the error; an existing
// TTL may remain or a newly created key may have none. A later successful call
// refreshes the expiry again.
func (c *emailCache) IncrNotifyCodeUserRate(ctx context.Context, userID int64, window time.Duration) (int64, error) {
	key := notifyCodeUserRateKey(userID)
	count, err := c.rdb.Incr(ctx, key).Result()
	if err != nil {
		return 0, err
	}
	// With a positive window, refreshing after every increment extends the user
	// counter's lifetime and can repair expiry left incomplete by an earlier failure.
	if err := c.rdb.Expire(ctx, key, window).Err(); err != nil {
		return count, fmt.Errorf("expire notify code rate key: %w", err)
	}
	return count, nil
}

// GetNotifyCodeUserRate reads the user's notification-code request counter as an
// integer. Missing keys return redis.Nil, leaving the identity rate-checking caller
// to select its absent-counter behavior and distinguish other storage failures.
func (c *emailCache) GetNotifyCodeUserRate(ctx context.Context, userID int64) (int64, error) {
	key := notifyCodeUserRateKey(userID)
	count, err := c.rdb.Get(ctx, key).Int64()
	if err != nil {
		return 0, err
	}
	return count, nil
}
