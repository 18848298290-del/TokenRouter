package provider

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	grokModelQuotaBlockDefaultTTL = 2 * time.Hour
	grokModelQuotaBlockMaxTTL     = 6 * time.Hour
	grokModelQuotaBlockMinTTL     = 20 * time.Minute

	grokModelTransientBlockMinTTL = 500 * time.Millisecond
	grokModelTransientBlockMaxTTL = 5 * time.Minute
)

var globalGrokModelQuotaBlocks = &grokModelQuotaBlockStore{
	items: make(map[string]grokModelQuotaBlock),
}

// grokModelQuotaBlock records a process-local cooldown for one provider and model.
// Model-specific exhaustion leaves the provider's other models eligible.
// Each instance learns these blocks from the upstream errors it observes.
type grokModelQuotaBlock struct {
	Until time.Time
}
type grokModelQuotaBlockStore struct {
	mu    sync.Mutex
	items map[string]grokModelQuotaBlock // Keys use normalized model|providerID.
}

func GrokModelQuotaBlockKey(providerID int64, model string) string {
	return strings.TrimSpace(strings.ToLower(model)) + "|" + strconv.FormatInt(providerID, 10)
}

// MarkGrokModelQuotaBlock blocks a provider's model until a deadline bounded by the quota cooldown policy.
func MarkGrokModelQuotaBlock(providerID int64, model string, until time.Time) {
	model = strings.TrimSpace(model)
	if providerID <= 0 || model == "" || until.IsZero() {
		return
	}
	now := time.Now()
	if !until.After(now.Add(grokModelQuotaBlockMinTTL)) {
		until = now.Add(grokModelQuotaBlockDefaultTTL)
	}
	if max := now.Add(grokModelQuotaBlockMaxTTL); until.After(max) {
		until = max
	}
	StoreGrokModelQuotaBlock(providerID, model, until, now)
}

// MarkGrokModelTransientBlock applies a short model-specific capacity cooldown.
// It uses transient TTL bounds rather than the longer free-quota cooldown policy.
func MarkGrokModelTransientBlock(providerID int64, model string, until time.Time) {
	model = strings.TrimSpace(model)
	if providerID <= 0 || model == "" || until.IsZero() {
		return
	}
	now := time.Now()
	if !until.After(now.Add(grokModelTransientBlockMinTTL)) {
		until = now.Add(grokModelTransientBlockMinTTL)
	}
	if max := now.Add(grokModelTransientBlockMaxTTL); until.After(max) {
		until = max
	}
	StoreGrokModelQuotaBlock(providerID, model, until, now)
}

func StoreGrokModelQuotaBlock(providerID int64, model string, until, now time.Time) {
	key := GrokModelQuotaBlockKey(providerID, model)
	globalGrokModelQuotaBlocks.mu.Lock()
	defer globalGrokModelQuotaBlocks.mu.Unlock()
	if cur, ok := globalGrokModelQuotaBlocks.items[key]; ok && cur.Until.After(until) {
		return
	}
	globalGrokModelQuotaBlocks.items[key] = grokModelQuotaBlock{Until: until}
	for k, v := range globalGrokModelQuotaBlocks.items {
		if !v.Until.After(now) {
			delete(globalGrokModelQuotaBlocks.items, k)
		}
	}
}

// IsGrokModelQuotaBlocked reports whether the provider's model has an unexpired local block.
func IsGrokModelQuotaBlocked(providerID int64, model string, now time.Time) bool {
	model = strings.TrimSpace(model)
	if providerID <= 0 || model == "" {
		return false
	}
	key := GrokModelQuotaBlockKey(providerID, model)
	globalGrokModelQuotaBlocks.mu.Lock()
	defer globalGrokModelQuotaBlocks.mu.Unlock()
	cur, ok := globalGrokModelQuotaBlocks.items[key]
	if !ok {
		return false
	}
	if !cur.Until.After(now) {
		delete(globalGrokModelQuotaBlocks.items, key)
		return false
	}
	return true
}

// IsGrokModelSpecificFreeUsage reports whether a lowercase error message identifies model-specific exhaustion.
// Other models on that provider may remain eligible.
func IsGrokModelSpecificFreeUsage(low, model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" || low == "" {
		return false
	}
	if strings.Contains(low, "for model") || strings.Contains(low, "模型") {
		return true
	}
	// Also accept messages such as "used all the included free usage for model grok-4.5".
	if strings.Contains(low, "free usage") && strings.Contains(low, model) {
		return true
	}
	return false
}
