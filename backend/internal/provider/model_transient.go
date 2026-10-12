package provider

import (
	"strings"
	"sync"
	"time"
)

// Transient upstream failures accumulate per provider and normalized model, so
// one failing model does not block other models on the account. The first failure
// records a streak, the second starts a short cooldown, and subsequent failures
// use a longer cooldown. Successful execution deletes the pair's state.
//
// Cooldown expiry permits another attempt while retaining the failure streak.
// The longer retention interval lets sparse failures escalate even when requests
// arrive after each cooldown. Reads lazily remove stale entries, new keys evict
// the least recently touched entry at capacity, and a mutex keeps each decision
// consistent with concurrent failure, success, and admission checks. Failure
// recording resets its streak when the supplied clock moves backward.

const (
	// openAIModelTransientStreakTTL bounds retention of idle failure streaks.
	// Success clears a streak immediately; access removes or resets stale pairs.
	//
	// Keep this substantially longer than every cooldown. A short expiration would
	// reset streaks between sparse requests, preventing a failing upstream from
	// reaching a cooldown and forcing each request to pay for another failed attempt.
	openAIModelTransientStreakTTL = 30 * time.Minute
	// Cooldown durations govern escalation after repeated failures: the second
	// failure uses the short interval, and later failures use the long interval.
	openAIModelTransientShortCooldown = 10 * time.Second // second consecutive failure pause
	openAIModelTransientLongCooldown  = 45 * time.Second // third and later failure pause
	// Capacity and input length limits bound retained state for arbitrary requested
	// models. Oversized trimmed inputs are ignored rather than stored as cache keys.
	openAIModelTransientDefaultMax    = 4096 // default retained pair count
	openAIModelTransientMaxModelBytes = 512  // maximum trimmed input bytes
)

// openAIProviderModelKey identifies one provider's case-normalized requested
// model for transient failure tracking and admission cooldowns.
type openAIProviderModelKey struct {
	ProviderID int64  // positive provider record identifier
	Model      string // trimmed lowercased model identity
}

// openAIProviderModelTransientEntry stores failure escalation and retention
// timestamps for one normalized provider/model pair under ModelTransientState's mutex.
type openAIProviderModelTransientEntry struct {
	// failureStreak increases on failure until success, retention expiry, or a
	// backward failure timestamp resets the pair's accumulated failure history.
	failureStreak int
	// lastFailure determines streak retention. Failure recording replaces it;
	// admission reads use it for lazy expiry without extending failure retention.
	lastFailure time.Time // latest recorded failure time
	// blockUntil is zero after the first failure. Later failures replace it with
	// the newly selected cooldown deadline, and equality means admission is allowed.
	blockUntil time.Time
	// lastTouched determines capacity eviction. Accepted failures and nonstale
	// admission reads update it, including reads after the cooldown has ended.
	lastTouched time.Time // latest retained failure or read
}

// ModelTransientDecision describes the failure count and selected cooldown
// produced by one transient failure observation for scheduling consumers.
type ModelTransientDecision struct {
	// FailureStreak selects Cooldown, and BlockUntil is the observation time plus
	// that duration. The first failure has zero cooldown and a zero deadline;
	// rejected observations return all three fields as zero values.
	FailureStreak int           // accumulated failures for the pair
	Cooldown      time.Duration // selected scheduling pause duration
	BlockUntil    time.Time     // selected scheduling pause deadline
}

// ModelTransientState tracks bounded failure streaks and admission cooldowns
// for provider/model pairs, with synchronized reads, failure updates, and clearing.
type ModelTransientState struct {
	// mu protects entries and maxEntries. Recording, clearing, retention cleanup,
	// and admission reads share this lock so a pair's decision uses consistent state.
	mu      sync.Mutex                                                   // transient state access lock
	entries map[openAIProviderModelKey]openAIProviderModelTransientEntry // tracked provider model pairs
	// maxEntries limits retained pairs. The constructor and zero-value failure
	// recording use the default capacity when this value is nonpositive.
	maxEntries int
}

// NewModelTransientState creates empty transient failure state with a retained
// pair limit. Nonpositive maxEntries selects the default capacity. The state can
// be shared by failure observers and scheduling admission checks.
func NewModelTransientState(maxEntries int) *ModelTransientState {
	if maxEntries <= 0 {
		maxEntries = openAIModelTransientDefaultMax
	}
	return &ModelTransientState{
		entries:    make(map[openAIProviderModelKey]openAIProviderModelTransientEntry),
		maxEntries: maxEntries,
	}
}

// NormalizeTransientModel returns a trimmed, lowercased transient model key.
// Trimmed input exceeding the byte limit returns an empty string, as does blank
// input. Length is checked before lowercasing, and Unicode case conversion can
// change the resulting byte length.
func NormalizeTransientModel(model string) string {
	model = strings.TrimSpace(model)
	// Limit trimmed input bytes before allocating its lowercase key.
	if len(model) > openAIModelTransientMaxModelBytes {
		return ""
	}
	return strings.ToLower(model)
}

// openAIProviderModelTransientKey returns a validated normalized pair and success
// flag. Nonpositive provider IDs, blank models, and oversized trimmed model inputs
// return a zero key with false, so callers skip state creation for those requests.
func openAIProviderModelTransientKey(providerID int64, model string) (openAIProviderModelKey, bool) {
	model = NormalizeTransientModel(model)
	if providerID <= 0 || model == "" {
		return openAIProviderModelKey{}, false
	}
	return openAIProviderModelKey{ProviderID: providerID, Model: model}, true
}

// RecordFailure records one failure and returns its streak and cooldown decision.
// Nil state or invalid keys return a zero decision; a zero time uses time.Now.
// The zero-value state initializes its map and default capacity on the first
// accepted failure.
//
// Retention expiry and backward failure timestamps start a fresh streak, while
// an expired cooldown alone leaves the streak intact. The second failure selects
// the short cooldown and later failures select the long cooldown. Creating a new
// pair can evict the least recently touched pair. The mutex covers eviction,
// streak selection, and publication of the returned deadline together.
func (s *ModelTransientState) RecordFailure(providerID int64, model string, now time.Time) ModelTransientDecision {
	key, ok := openAIProviderModelTransientKey(providerID, model)
	if s == nil || !ok {
		return ModelTransientDecision{}
	}
	if now.IsZero() {
		now = time.Now()
	}

	// Admission reads and successes can arrive concurrently. Keep eviction, streak
	// reset, and the new deadline under one lock before publishing the decision.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[openAIProviderModelKey]openAIProviderModelTransientEntry)
	}
	if s.maxEntries <= 0 {
		s.maxEntries = openAIModelTransientDefaultMax
	}

	entry, exists := s.entries[key]
	if !exists {
		s.evictOldestLocked()
	}
	// Expired retention and a backward failure timestamp invalidate the prior
	// streak's timing. Reset its count and deadline together before recording this failure.
	if !exists || entry.lastFailure.IsZero() || now.Sub(entry.lastFailure) > openAIModelTransientStreakTTL || now.Before(entry.lastFailure) {
		entry.failureStreak = 0
		entry.blockUntil = time.Time{}
	}
	entry.failureStreak++
	entry.lastFailure = now
	entry.lastTouched = now

	// A first transient failure leaves immediate retry available. Repeated failures
	// escalate this pair's pause while other models on the same provider remain eligible.
	cooldown := time.Duration(0)
	switch {
	case entry.failureStreak >= 3:
		cooldown = openAIModelTransientLongCooldown
	case entry.failureStreak == 2:
		cooldown = openAIModelTransientShortCooldown
	}
	if cooldown > 0 {
		entry.blockUntil = now.Add(cooldown)
	} else {
		entry.blockUntil = time.Time{}
	}
	s.entries[key] = entry
	return ModelTransientDecision{
		FailureStreak: entry.failureStreak,
		Cooldown:      cooldown,
		BlockUntil:    entry.blockUntil,
	}
}

// RecordSuccess deletes the streak and cooldown for one normalized pair.
// Nil state or invalid keys are ignored. Clearing shares the failure/admission
// mutex, so a later recorded failure begins a fresh streak.
func (s *ModelTransientState) RecordSuccess(providerID int64, model string) {
	key, ok := openAIProviderModelTransientKey(providerID, model)
	if s == nil || !ok {
		return
	}
	// Success removes both escalation and admission state under their shared lock,
	// so a subsequent failure cannot reuse a streak from before this successful call.
	s.mu.Lock()
	delete(s.entries, key)
	s.mu.Unlock()
}

// IsBlocked reports whether a normalized pair has a deadline after now.
// Nil state, invalid keys, and missing entries return false; a zero time uses
// time.Now. Equality with the cooldown deadline permits admission.
//
// A failure older than the retention interval is deleted before deadline checks.
// Other reads update lastTouched for capacity eviction, even after a cooldown
// expires. Reads do not reset a streak for backward clock movement; that reset
// belongs to RecordFailure. The mutex keeps cleanup and the returned admission
// decision consistent with concurrent failure or success updates.
func (s *ModelTransientState) IsBlocked(providerID int64, model string, now time.Time) bool {
	key, ok := openAIProviderModelTransientKey(providerID, model)
	if s == nil || !ok {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}

	// Keep retention deletion and touch publication synchronized with failure and
	// success observations so an admission check cannot overwrite their pair state.
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.entries[key]
	if !exists {
		return false
	}
	// Retention expiry reclaims a pair lazily on access. Cooldown expiry alone
	// keeps its streak, allowing sparse failures to continue escalating on later attempts.
	if !entry.lastFailure.IsZero() && now.Sub(entry.lastFailure) > openAIModelTransientStreakTTL {
		delete(s.entries, key)
		return false
	}
	entry.lastTouched = now
	s.entries[key] = entry
	return !entry.blockUntil.IsZero() && now.Before(entry.blockUntil)
}

// evictOldestLocked removes one least recently touched pair when capacity is full.
// The caller holds s.mu and invokes it before inserting a new key. Equal touch
// times have no specified eviction order.
func (s *ModelTransientState) evictOldestLocked() {
	if len(s.entries) < s.maxEntries {
		return
	}
	var oldestKey openAIProviderModelKey
	var oldestTime time.Time
	found := false
	for key, entry := range s.entries {
		if !found || entry.lastTouched.Before(oldestTime) {
			oldestKey = key
			oldestTime = entry.lastTouched
			found = true
		}
	}
	// Eviction discards the entire pair, including any active cooldown. The shared
	// lock keeps bounded insertion from racing with reads that refresh touch times.
	if found {
		delete(s.entries, oldestKey)
	}
}
