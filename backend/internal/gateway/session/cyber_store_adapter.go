package session

import (
	"context"
	"strings"
	"time"
)

// Cyber session storage can expose a batch interface or the historical single-key
// operations. The adapter prefers batches; a legacy write selects its source
// scope or first supplied key, and legacy lookup probes trimmed candidates in
// caller order until one is blocked.

// cyberSessionTranscriptLookupOverflowBlockKey identifies an active scope whose
// transcript candidates exceed the lookup limit, allowing callers to reject
// incomplete block evidence.
const cyberSessionTranscriptLookupOverflowBlockKey = "transcript_lookup_limit_exceeded"

// legacyCyberSessionBlockStore supplies single-key Cyber block writes and presence reads.
type legacyCyberSessionBlockStore interface {
	// SetCyberSessionBlocked records one exact key for the supplied lifetime.
	// The adapter selects its scope or first candidate before invoking this method,
	// and the returned error controls CyberBlocks' warning behavior.
	SetCyberSessionBlocked(ctx context.Context, key string, ttl time.Duration) error
	// IsCyberSessionBlocked reports whether the supplied exact key is blocked.
	// Legacy adaptation invokes it for scope presence and for individual candidates;
	// read errors stop the adapter's sequence and propagate to CyberBlocks.
	IsCyberSessionBlocked(ctx context.Context, key string) (bool, error)
}

// legacyCyberSessionBlockStoreAdapter delegates batch-shaped Cyber block
// operations to single-key storage.
type legacyCyberSessionBlockStoreAdapter struct {
	legacy legacyCyberSessionBlockStore /* selected single-key storage implementation */
}

// SetCyberSessionBlocked writes the trimmed scope key, or the first key when scope
// is blank. It writes at most one selected key, skips a blank selection, and
// returns the legacy store's error to CyberBlocks.
func (a legacyCyberSessionBlockStoreAdapter) SetCyberSessionBlocked(ctx context.Context, scopeKey string, keys []string, ttl time.Duration) error {
	key := strings.TrimSpace(scopeKey)
	// Legacy storage accepts one key, so a blank scope selects the first
	// supplied candidate; later candidates are not part of this write operation.
	if key == "" && len(keys) > 0 {
		key = strings.TrimSpace(keys[0])
	}
	if key == "" {
		return nil
	}
	return a.legacy.SetCyberSessionBlocked(ctx, key, ttl)
}

// IsCyberSessionScopeActive queries the legacy store for the supplied source scope.
// A whitespace-only scope returns false with nil error; nonblank scope text is
// passed through untrimmed, matching the historical adapter's read behavior.
func (a legacyCyberSessionBlockStoreAdapter) IsCyberSessionScopeActive(ctx context.Context, scopeKey string) (bool, error) {
	if strings.TrimSpace(scopeKey) == "" {
		return false, nil
	}
	return a.legacy.IsCyberSessionBlocked(ctx, scopeKey)
}

// FindCyberSessionBlocked probes trimmed nonblank keys in caller order and returns
// the first blocked key. A legacy read error stops the sequence and propagates;
// no match returns empty with nil error.
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

// AdaptCyberSessionBlockStore returns batch storage or adapts a legacy single-key
// cache. A nil cache or one supporting neither capability returns nil, allowing
// CyberBlocks to skip its optional storage path.
func AdaptCyberSessionBlockStore(cache GatewayCache) CyberSessionBlockStore {
	if cache == nil {
		return nil
	}
	if store, ok := cache.(CyberSessionBlockStore); ok {
		return store
	}
	// A cache lacking batch methods can still serve historical single-key
	// operations; the adapter exposes that limited write selection to CyberBlocks.
	if legacy, ok := cache.(legacyCyberSessionBlockStore); ok {
		return legacyCyberSessionBlockStoreAdapter{legacy: legacy}
	}
	return nil
}
