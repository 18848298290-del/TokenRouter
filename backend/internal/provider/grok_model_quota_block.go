package provider

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// grok_model_quota_block.go stores provider-and-model Grok cooldowns and
// distinguishes long quota pauses from transient blocks.
// Grok exhaustion can affect one model while other models on the same provider
// remain usable. Local blocks therefore key a normalized model together with
// provider ID. Long quota pauses and short capacity pauses apply distinct
// deadline ranges before reaching the same process-local store.
//
// Concurrent observations can request different deadlines for that key. The
// store compares and installs under one mutex, retaining an already longer
// deadline and pruning expired entries during installation. Reads remove only
// their expired key. These observations stay within the process that received
// them, and the direct store helper trusts caller-supplied identity and timing;
// the public marking helpers supply their own validation and duration limits.

const (
	// Quota durations replace short reset evidence and cap long model
	// exhaustion pauses before storing provider-specific deadlines.
	grokModelQuotaBlockDefaultTTL = 2 * time.Hour
	grokModelQuotaBlockMaxTTL     = 6 * time.Hour
	grokModelQuotaBlockMinTTL     = 20 * time.Minute

	// Transient durations bound capacity pauses independently from the
	// longer quota policy while sharing the same provider/model map.
	grokModelTransientBlockMinTTL = 500 * time.Millisecond
	grokModelTransientBlockMaxTTL = 5 * time.Minute
)

// globalGrokModelQuotaBlocks retains this process's provider/model observations
// for request admission and expiry cleanup under its store mutex.
var globalGrokModelQuotaBlocks = &grokModelQuotaBlockStore{
	items: make(map[string]grokModelQuotaBlock),
}

// grokModelQuotaBlock records one provider/model cooldown deadline observed by
// this process.
type grokModelQuotaBlock struct {
	Until time.Time // exclusive block expiry instant
}

// grokModelQuotaBlockStore serializes deadline comparison, installation, and
// expiry removal.
type grokModelQuotaBlockStore struct {
	// mu protects items through comparison, installation, and expiry
	// removal.
	// Marking an existing key retains a longer deadline, so independently
	// arriving
	// observations cannot shorten that stored pause.
	mu    sync.Mutex                     // deadline comparison and mutation lock
	items map[string]grokModelQuotaBlock // normalized model and provider deadlines
}

// GrokModelQuotaBlockKey joins a trimmed lowercase model and decimal provider
// ID.
func GrokModelQuotaBlockKey(providerID int64, model string) string {
	return strings.TrimSpace(strings.ToLower(model)) + "|" + strconv.FormatInt(providerID, 10)
}

// MarkGrokModelQuotaBlock validates a positive provider ID, nonblank model, and
// nonzero deadline before storing quota exhaustion. Deadlines at or below the
// twenty-minute threshold select two hours, and later deadlines cap at six
// hours.
func MarkGrokModelQuotaBlock(providerID int64, model string, until time.Time) {
	model = strings.TrimSpace(model)
	if providerID <= 0 || model == "" || until.IsZero() {
		return
	}
	now := time.Now()
	// The quota policy substitutes its standard pause for short reset
	// evidence, then bounds that result before publishing it to local
	// storage.
	if !until.After(now.Add(grokModelQuotaBlockMinTTL)) {
		until = now.Add(grokModelQuotaBlockDefaultTTL)
	}
	// Bound model quota isolation using its six-hour policy maximum.
	if max := now.Add(grokModelQuotaBlockMaxTTL); until.After(max) {
		until = max
	}
	StoreGrokModelQuotaBlock(providerID, model, until, now)
}

// MarkGrokModelTransientBlock validates provider/model identity and stores a
// short capacity pause. A nonzero deadline at or below the half-second
// threshold is raised to that minimum; later deadlines cap at five minutes.
func MarkGrokModelTransientBlock(providerID int64, model string, until time.Time) {
	model = strings.TrimSpace(model)
	if providerID <= 0 || model == "" || until.IsZero() {
		return
	}
	now := time.Now()
	// Capacity pauses retain at least half a second of spacing.
	if !until.After(now.Add(grokModelTransientBlockMinTTL)) {
		until = now.Add(grokModelTransientBlockMinTTL)
	}
	// Bound capacity isolation separately from the longer quota policy.
	if max := now.Add(grokModelTransientBlockMaxTTL); until.After(max) {
		until = max
	}
	StoreGrokModelQuotaBlock(providerID, model, until, now)
}

// StoreGrokModelQuotaBlock installs a caller-supplied provider/model deadline
// while retaining a longer existing observation. Callers validate the identity
// and choose duration policy before using this low-level helper.
//
// Comparison and installation share the store mutex with reads and cleanup, so
// two observations cannot compare against the same old value and then publish a
// shorter winner. Successful installation prunes every expired key using now.
// Returning early for a longer existing value skips that pruning pass; expiry
// checks still remove the requested key during reads.
func StoreGrokModelQuotaBlock(providerID int64, model string, until, now time.Time) {
	key := GrokModelQuotaBlockKey(providerID, model)
	// Hold comparison and replacement together so a concurrent shorter
	// notice
	// cannot overwrite a longer deadline; the following pruning uses the
	// same
	// protected map.
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

// IsGrokModelQuotaBlocked reports whether a valid provider/model key has a
// deadline after now. Expired matching entries are removed under the store
// mutex; absent or invalid identity returns false.
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
	// Remove this expired observation while retaining the lock used by
	// installation, so this read cannot erase a concurrently extended key.
	if !cur.Until.After(now) {
		delete(globalGrokModelQuotaBlocks.items, key)
		return false
	}
	return true
}

// IsGrokModelSpecificFreeUsage classifies a caller-lowercased message as
// model-specific exhaustion. It accepts a generic model phrase or Chinese model
// marker, or free-usage text containing the normalized model; callers decide
// the resulting cooldown.
func IsGrokModelSpecificFreeUsage(low, model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" || low == "" {
		return false
	}
	if strings.Contains(low, "for model") || strings.Contains(low, "模型") {
		return true
	}
	// Also accept messages such as "used all the included free usage for
	// model grok-4.5".
	if strings.Contains(low, "free usage") && strings.Contains(low, model) {
		return true
	}
	return false
}
