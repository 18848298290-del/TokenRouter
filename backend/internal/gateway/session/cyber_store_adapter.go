package session

import (
	"context"
	"strings"
	"time"
)

const cyberSessionTranscriptLookupOverflowBlockKey = "transcript_lookup_limit_exceeded"

// legacyCyberSessionBlockStore is the single-key cache interface supported by the compatibility adapter.
type legacyCyberSessionBlockStore interface {
	SetCyberSessionBlocked(ctx context.Context, key string, ttl time.Duration) error
	IsCyberSessionBlocked(ctx context.Context, key string) (bool, error)
}

type legacyCyberSessionBlockStoreAdapter struct{ legacy legacyCyberSessionBlockStore }

func (a legacyCyberSessionBlockStoreAdapter) SetCyberSessionBlocked(ctx context.Context, scopeKey string, keys []string, ttl time.Duration) error {
	key := strings.TrimSpace(scopeKey)
	if key == "" && len(keys) > 0 {
		key = strings.TrimSpace(keys[0])
	}
	if key == "" {
		return nil
	}
	return a.legacy.SetCyberSessionBlocked(ctx, key, ttl)
}

func (a legacyCyberSessionBlockStoreAdapter) IsCyberSessionScopeActive(ctx context.Context, scopeKey string) (bool, error) {
	if strings.TrimSpace(scopeKey) == "" {
		return false, nil
	}
	return a.legacy.IsCyberSessionBlocked(ctx, scopeKey)
}

func (a legacyCyberSessionBlockStoreAdapter) FindCyberSessionBlocked(ctx context.Context, keys []string) (string, error) {
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		blocked, err := a.legacy.IsCyberSessionBlocked(ctx, key)
		if err != nil {
			return "", err
		}
		if blocked {
			return key, nil
		}
	}
	return "", nil
}

// AdaptCyberSessionBlockStore prefers batch lookups and otherwise adapts the single-key cache interface.
func AdaptCyberSessionBlockStore(cache GatewayCache) CyberSessionBlockStore {
	if cache == nil {
		return nil
	}
	if store, ok := cache.(CyberSessionBlockStore); ok {
		return store
	}
	if legacy, ok := cache.(legacyCyberSessionBlockStore); ok {
		return legacyCyberSessionBlockStoreAdapter{legacy: legacy}
	}
	return nil
}
