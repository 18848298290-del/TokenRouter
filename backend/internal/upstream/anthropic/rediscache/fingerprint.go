package rediscache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// Provider fingerprints and masked session IDs use separate Redis keys and
// expiration periods. Fingerprints are JSON snapshots with a seven-day write
// TTL; masked sessions store text with a fifteen-minute idle TTL renewed by
// request fingerprint handling.

// Fingerprint and masked-session namespaces keep provider identity values
// separate, while their TTLs determine expiration after each successful Redis
// write.
const (
	FingerprintKeyPrefix   = "fingerprint:"
	FingerprintTTL         = 7 * 24 * time.Hour // Renewed lazily for active providers.
	maskedSessionKeyPrefix = "masked_session:"
	maskedSessionTTL       = 15 * time.Minute
)

// FingerprintStore stores provider identity snapshots through a caller-supplied
// Redis client.
type FingerprintStore struct {
	// rdb is supplied by the caller and remains caller-owned; its connection needs
	// to stay usable until cache operations finish.
	rdb *redis.Client // shared Redis client
}

// fingerprintKey joins the fingerprint namespace and decimal provider
// identifier.
func fingerprintKey(providerID int64) string {
	return fmt.Sprintf("%s%d", FingerprintKeyPrefix, providerID)
}

// maskedSessionKey joins the masked-session namespace and decimal provider
// identifier.
func maskedSessionKey(providerID int64) string {
	return fmt.Sprintf("%s%d", maskedSessionKeyPrefix, providerID)
}

// NewFingerprintStore wraps a supplied Redis client as a FingerprintCache. The
// client remains caller-owned and must stay usable during operations; the store
// does not add creation locks or compare-and-set behavior.
func NewFingerprintStore(rdb *redis.Client) anthropic.FingerprintCache {
	return &FingerprintStore{rdb: rdb}
}

// GetFingerprint loads provider JSON and returns a newly decoded fingerprint.
// Missing keys propagate redis.Nil, malformed data returns a decoding error,
// and stored null decodes to a zero-valued fingerprint.
func (c *FingerprintStore) GetFingerprint(ctx context.Context, providerID int64) (*anthropic.Fingerprint, error) {
	key := fingerprintKey(providerID)
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	var fp anthropic.Fingerprint
	if err := json.Unmarshal([]byte(val), &fp); err != nil {
		return nil, err
	}
	return &fp, nil
}

// SetFingerprint serializes fp and replaces the provider's fingerprint with a
// seven-day TTL. A nil pointer serializes as null, and encoding or Redis errors
// return to the request fingerprint caller.
func (c *FingerprintStore) SetFingerprint(ctx context.Context, providerID int64, fp *anthropic.Fingerprint) error {
	key := fingerprintKey(providerID)
	val, err := json.Marshal(fp)
	if err != nil {
		return err
	}
	// Replacing the JSON snapshot also resets its expiry, allowing
	// RequestFingerprint's lazy write renewal to keep an active provider's cached
	// identity available.
	return c.rdb.Set(ctx, key, val, FingerprintTTL).Err()
}

// GetMaskedSessionID reads the provider's masked session text, returning empty
// without error for redis.Nil. Other Redis errors propagate, letting request
// fingerprint handling distinguish lookup failure from a missing or expired
// mask.
func (c *FingerprintStore) GetMaskedSessionID(ctx context.Context, providerID int64) (string, error) {
	key := maskedSessionKey(providerID)
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		// A missing or expired session is represented by empty text so
		// RequestFingerprint can generate a mask; other read errors retain their
		// failure signal.
		if err == redis.Nil {
			return "", nil
		}
		return "", err
	}
	return val, nil
}

// SetMaskedSessionID replaces the provider's session text and renews its
// fifteen-minute TTL. Every call is an independent Redis write; callers
// coordinate any read-generate-write decision before relying on a shared
// identity.
func (c *FingerprintStore) SetMaskedSessionID(ctx context.Context, providerID int64, sessionID string) error {
	key := maskedSessionKey(providerID)
	// RequestFingerprint writes the selected session on active use, renewing idle
	// expiration for that provider while leaving read-generate-write coordination
	// outside this Redis operation.
	return c.rdb.Set(ctx, key, sessionID, maskedSessionTTL).Err()
}
