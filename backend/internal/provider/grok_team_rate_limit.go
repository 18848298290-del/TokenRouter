package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// grok_team_rate_limit.go shares process-local Grok model cooldowns among
// providers with matching team fingerprints.
// A Grok OAuth team can share model quota across several providers. Team
// cooldowns therefore use a case-folded, truncated team fingerprint with a
// normalized model, allowing those providers to consult one process-local
// deadline without placing the raw team ID in its key.
//
// Marking compares and installs under the store mutex, retaining a longer
// existing deadline and pruning expired observations. Reads remove only the
// queried expired entry. Direct marking bounds an expired deadline and caps
// distant deadlines, while ResolveGrokTeamRateLimitUntil also applies its
// separate thirty-second threshold. Those entry rules differ: a direct future
// deadline below that threshold remains usable until it expires. Other
// processes learn their own observations.

// Team cooldown timings govern default and maximum pauses; the minimum is
// used by reset resolution before callers request shared model isolation.
const (
	grokTeamRateLimitDefaultTTL = 10 * time.Minute
	grokTeamRateLimitMaxTTL     = time.Hour
	grokTeamRateLimitMinTTL     = 30 * time.Second
)

// globalGrokTeamModelRateLimits retains team/model cooldowns observed by this
// process for shared provider admission and protected expiry cleanup.
var globalGrokTeamModelRateLimits = &grokTeamModelRateLimitStore{
	items: make(map[string]grokTeamModelRateLimit),
}

// grokTeamModelRateLimit represents one team's process-local model cooldown
// deadline.
type grokTeamModelRateLimit struct {
	Until time.Time // exclusive shared cooldown expiry
}

// grokTeamModelRateLimitStore serializes shared team deadline publication and
// expiry removal.
type grokTeamModelRateLimitStore struct {
	// mu protects items while comparing deadlines, installing observations,
	// and
	// deleting expired keys. Each key shares one deadline across providers
	// whose
	// normalized team fingerprints and models agree.
	mu    sync.Mutex                        // team deadline comparison lock
	items map[string]grokTeamModelRateLimit // fingerprint and model deadlines
}

// GrokTeamFingerprint returns a sixteen-digit hexadecimal team fingerprint
// after trimming and case folding. Blank input returns empty; SHA-256 is
// truncated to eight bytes, so callers treat this as a compact key rather than
// a unique team identifier.
func GrokTeamFingerprint(teamID string) string {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.ToLower(teamID)))
	// Use eight digest bytes for the compact shared cooldown key.
	return hex.EncodeToString(sum[:8])
}

// GrokTeamModelRateLimitKey joins the supplied fingerprint with a normalized
// model.
func GrokTeamModelRateLimitKey(teamFingerprint, model string) string {
	return teamFingerprint + "|" + strings.ToLower(strings.TrimSpace(model))
}

// ProviderGrokTeamID returns the trimmed stored team identity, or empty for nil
// and absent credentials. Cooldown callers use the empty result to skip
// shared-team key construction.
func ProviderGrokTeamID(provider *Record) string {
	if provider == nil {
		return ""
	}
	return strings.TrimSpace(provider.GetCredential("team_id"))
}

// MarkGrokTeamModelRateLimit stores a model cooldown shared by a Grok OAuth
// team's providers. Missing identity, blank model, or zero deadline skips the
// update; an expired deadline selects ten minutes and a distant one caps at one
// hour.
//
// The store mutex covers comparison, installation, and pruning. A longer stored
// deadline wins over the new notice, while accepted installation removes
// expired entries from the map. Direct marking accepts any future deadline
// after the cap; callers using reset evidence can apply
// ResolveGrokTeamRateLimitUntil first for its additional threshold.
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
	// Expired nonzero notices still represent a requested team pause. Use
	// the
	// default duration before comparison with independently installed
	// longer
	// observations.
	if !until.After(now) {
		until = now.Add(grokTeamRateLimitDefaultTTL)
	}
	// Cap direct team isolation at its one-hour maximum.
	maxUntil := now.Add(grokTeamRateLimitMaxTTL)
	if until.After(maxUntil) {
		until = maxUntil
	}
	key := GrokTeamModelRateLimitKey(fp, model)
	// Compare and install under the same lock so another provider's longer
	// team pause survives this notice; prune expired entries before
	// releasing
	// shared map access.
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

// IsGrokTeamModelRateLimited reports whether this Grok OAuth team's model
// deadline is after now. It removes an expired matching entry under the store
// mutex; missing or invalid identity returns false.
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
	// Delete while holding the publication lock so this expired read cannot
	// erase a newer team deadline installed by another provider notice.
	if !cur.Until.After(now) {
		delete(globalGrokTeamModelRateLimits.items, key)
		return false
	}
	return true
}

// ResolveGrokTeamRateLimitUntil selects a reset beyond thirty seconds and caps
// it at one hour. Earlier, equal-threshold, or expired resets select ten
// minutes; direct marking applies a separate deadline rule.
func ResolveGrokTeamRateLimitUntil(resetAt, now time.Time) time.Time {
	// Resets within thirty seconds select the standard team-pause duration;
	// that resolution rule gives marking callers a different result from
	// supplying a short future deadline directly.
	if resetAt.After(now.Add(grokTeamRateLimitMinTTL)) {
		maxUntil := now.Add(grokTeamRateLimitMaxTTL)
		if resetAt.After(maxUntil) {
			return maxUntil
		}
		return resetAt
	}
	return now.Add(grokTeamRateLimitDefaultTTL)
}
