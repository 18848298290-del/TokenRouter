package provider

import (
	"strings"
	"sync"
	"time"
)

const (
	// openAIModelTransientStreakTTL bounds retention of idle failure streaks.
	// Success clears a streak immediately; the TTL reclaims inactive provider/model pairs.
	//
	// Keep this substantially longer than every cooldown. A short expiration would
	// reset streaks between sparse requests, preventing a failing upstream from
	// reaching a cooldown and forcing each request to pay for another failed attempt.
	openAIModelTransientStreakTTL     = 30 * time.Minute
	openAIModelTransientShortCooldown = 10 * time.Second
	openAIModelTransientLongCooldown  = 45 * time.Second
	openAIModelTransientDefaultMax    = 4096
	openAIModelTransientMaxModelBytes = 512
)

type openAIProviderModelKey struct {
	ProviderID int64
	Model      string
}

type openAIProviderModelTransientEntry struct {
	failureStreak int
	lastFailure   time.Time
	blockUntil    time.Time
	lastTouched   time.Time
}

type ModelTransientDecision struct {
	FailureStreak int
	Cooldown      time.Duration
	BlockUntil    time.Time
}

// ModelTransientState tracks transient failures per provider and model.
// Its capacity limit bounds memory use when requests contain arbitrary model names.
type ModelTransientState struct {
	mu         sync.Mutex
	entries    map[openAIProviderModelKey]openAIProviderModelTransientEntry
	maxEntries int
}

// NewModelTransientState creates bounded failure state, using the default capacity for nonpositive values.
func NewModelTransientState(maxEntries int) *ModelTransientState {
	if maxEntries <= 0 {
		maxEntries = openAIModelTransientDefaultMax
	}
	return &ModelTransientState{
		entries:    make(map[openAIProviderModelKey]openAIProviderModelTransientEntry),
		maxEntries: maxEntries,
	}
}

// NormalizeTransientModel trims and lowercases a model name, returning empty for oversized input.
func NormalizeTransientModel(model string) string {
	model = strings.TrimSpace(model)
	if len(model) > openAIModelTransientMaxModelBytes {
		return ""
	}
	return strings.ToLower(model)
}

func openAIProviderModelTransientKey(providerID int64, model string) (openAIProviderModelKey, bool) {
	model = NormalizeTransientModel(model)
	if providerID <= 0 || model == "" {
		return openAIProviderModelKey{}, false
	}
	return openAIProviderModelKey{ProviderID: providerID, Model: model}, true
}

// RecordFailure increments the failure streak and returns its escalating cooldown.
func (s *ModelTransientState) RecordFailure(providerID int64, model string, now time.Time) ModelTransientDecision {
	key, ok := openAIProviderModelTransientKey(providerID, model)
	if s == nil || !ok {
		return ModelTransientDecision{}
	}
	if now.IsZero() {
		now = time.Now()
	}

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
	// Expired entries and backward clock jumps reset the streak; RecordSuccess clears it separately.
	if !exists || entry.lastFailure.IsZero() || now.Sub(entry.lastFailure) > openAIModelTransientStreakTTL || now.Before(entry.lastFailure) {
		entry.failureStreak = 0
		entry.blockUntil = time.Time{}
	}
	entry.failureStreak++
	entry.lastFailure = now
	entry.lastTouched = now

	// The second failure starts a short cooldown; the third and later failures use the long cooldown.
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

// RecordSuccess clears the failure streak and cooldown for a provider/model pair.
func (s *ModelTransientState) RecordSuccess(providerID int64, model string) {
	key, ok := openAIProviderModelTransientKey(providerID, model)
	if s == nil || !ok {
		return
	}
	s.mu.Lock()
	delete(s.entries, key)
	s.mu.Unlock()
}

// IsBlocked reports whether the model's cooldown is still active.
func (s *ModelTransientState) IsBlocked(providerID int64, model string, now time.Time) bool {
	key, ok := openAIProviderModelTransientKey(providerID, model)
	if s == nil || !ok {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.entries[key]
	if !exists {
		return false
	}
	if !entry.lastFailure.IsZero() && now.Sub(entry.lastFailure) > openAIModelTransientStreakTTL {
		delete(s.entries, key)
		return false
	}
	entry.lastTouched = now
	s.entries[key] = entry
	return !entry.blockUntil.IsZero() && now.Before(entry.blockUntil)
}

// evictOldestLocked evicts the least recently touched entry when full. The caller holds s.mu.
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
	if found {
		delete(s.entries, oldestKey)
	}
}
