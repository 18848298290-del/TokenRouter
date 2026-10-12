package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

const (
	grokTeamRateLimitDefaultTTL = 10 * time.Minute
	grokTeamRateLimitMaxTTL     = time.Hour
	grokTeamRateLimitMinTTL     = 30 * time.Second
)

var globalGrokTeamModelRateLimits = &grokTeamModelRateLimitStore{
	items: make(map[string]grokTeamModelRateLimit),
}

// grokTeamModelRateLimit records a process-local cooldown shared by OAuth providers in the same team.

// Each instance learns from its own 429 responses; the short TTL bounds stale observations.
type grokTeamModelRateLimit struct {
	Until time.Time
}
type grokTeamModelRateLimitStore struct {
	mu    sync.Mutex
	items map[string]grokTeamModelRateLimit
}

func GrokTeamFingerprint(teamID string) string {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.ToLower(teamID)))
	return hex.EncodeToString(sum[:8])
}

func GrokTeamModelRateLimitKey(teamFingerprint, model string) string {
	return teamFingerprint + "|" + strings.ToLower(strings.TrimSpace(model))
}

func ProviderGrokTeamID(provider *Record) string {
	if provider == nil {
		return ""
	}
	return strings.TrimSpace(provider.GetCredential("team_id"))
}

// MarkGrokTeamModelRateLimit blocks the team's model until a bounded deadline; missing team IDs or models are
// ignored.
func MarkGrokTeamModelRateLimit(provider *Record, model string, until time.Time) {
	if provider == nil || !provider.IsGrokOAuth() {
		return
	}
	fp := GrokTeamFingerprint(ProviderGrokTeamID(provider))
	model = strings.TrimSpace(model)
	if fp == "" || model == "" || until.IsZero() {
		return
	}
	now := time.Now()
	if !until.After(now) {
		until = now.Add(grokTeamRateLimitDefaultTTL)
	}
	maxUntil := now.Add(grokTeamRateLimitMaxTTL)
	if until.After(maxUntil) {
		until = maxUntil
	}
	key := GrokTeamModelRateLimitKey(fp, model)
	globalGrokTeamModelRateLimits.mu.Lock()
	defer globalGrokTeamModelRateLimits.mu.Unlock()
	if cur, ok := globalGrokTeamModelRateLimits.items[key]; ok && cur.Until.After(until) {
		return
	}
	globalGrokTeamModelRateLimits.items[key] = grokTeamModelRateLimit{Until: until}
	for k, v := range globalGrokTeamModelRateLimits.items {
		if !v.Until.After(now) {
			delete(globalGrokTeamModelRateLimits.items, k)
		}
	}
}

// IsGrokTeamModelRateLimited reports whether the provider's team has an active cooldown for the model.
func IsGrokTeamModelRateLimited(provider *Record, model string, now time.Time) bool {
	if provider == nil || !provider.IsGrokOAuth() {
		return false
	}
	fp := GrokTeamFingerprint(ProviderGrokTeamID(provider))
	model = strings.TrimSpace(model)
	if fp == "" || model == "" {
		return false
	}
	key := GrokTeamModelRateLimitKey(fp, model)
	globalGrokTeamModelRateLimits.mu.Lock()
	defer globalGrokTeamModelRateLimits.mu.Unlock()
	cur, ok := globalGrokTeamModelRateLimits.items[key]
	if !ok {
		return false
	}
	if !cur.Until.After(now) {
		delete(globalGrokTeamModelRateLimits.items, key)
		return false
	}
	return true
}

// ResolveGrokTeamRateLimitUntil bounds a team cooldown derived from the provider's reset time.
func ResolveGrokTeamRateLimitUntil(resetAt, now time.Time) time.Time {
	if resetAt.After(now.Add(grokTeamRateLimitMinTTL)) {
		maxUntil := now.Add(grokTeamRateLimitMaxTTL)
		if resetAt.After(maxUntil) {
			return maxUntil
		}
		return resetAt
	}
	return now.Add(grokTeamRateLimitDefaultTTL)
}
