package rediscache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

const (
	FingerprintKeyPrefix   = "fingerprint:"
	FingerprintTTL         = 7 * 24 * time.Hour // Renewed lazily for active providers.
	maskedSessionKeyPrefix = "masked_session:"
	maskedSessionTTL       = 15 * time.Minute
)

type FingerprintStore struct {
	rdb *redis.Client
}

// fingerprintKey generates the Redis key for provider fingerprint cache.
func fingerprintKey(providerID int64) string {
	return fmt.Sprintf("%s%d", FingerprintKeyPrefix, providerID)
}

// maskedSessionKey generates the Redis key for masked session ID cache.
func maskedSessionKey(providerID int64) string {
	return fmt.Sprintf("%s%d", maskedSessionKeyPrefix, providerID)
}

func NewFingerprintStore(rdb *redis.Client) anthropic.FingerprintCache {
	return &FingerprintStore{rdb: rdb}
}

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

func (c *FingerprintStore) SetFingerprint(ctx context.Context, providerID int64, fp *anthropic.Fingerprint) error {
	key := fingerprintKey(providerID)
	val, err := json.Marshal(fp)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, key, val, FingerprintTTL).Err()
}

func (c *FingerprintStore) GetMaskedSessionID(ctx context.Context, providerID int64) (string, error) {
	key := maskedSessionKey(providerID)
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return "", nil
		}
		return "", err
	}
	return val, nil
}

func (c *FingerprintStore) SetMaskedSessionID(ctx context.Context, providerID int64, sessionID string) error {
	key := maskedSessionKey(providerID)
	return c.rdb.Set(ctx, key, sessionID, maskedSessionTTL).Err()
}
