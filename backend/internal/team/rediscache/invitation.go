package rediscache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/team"
)

// invitation.go enforces recipient cooldowns and team invitation limits
// through Redis admission checks.
// Invitation admission shares a per-team hourly counter and a per-recipient
// cooldown. The recipient key hashes lowercased trimmed email text, keeping
// equivalent case/spacing submissions in the same cooldown while distinct
// mailboxes compete for the team counter.
//
// One Redis script checks recipient cooldown before the hourly ceiling, then
// writes the recipient marker before incrementing the team counter. Completed
// admission records both controls; a later script-command failure can retain
// an earlier write. The hourly expiry starts with its first accepted increment
// and is not extended by later requests.
// Denial returns the applicable TTL, with a full hourly-window retry hint
// when a saturated counter lacks usable TTL. Redis or response-decoding
// failure remains an error for TeamService's unavailable decision; the
// retained client stays owned by application assembly.

// Invitation admission uses a recipient cooldown and a fixed hourly team
// window; the hourly ceiling applies across all recipient keys belonging to
// the same team.
const (
	// TeamInvitationRecipientCooldown sets the accepted team/mailbox cooldown
	// length before another send attempt.
	TeamInvitationRecipientCooldown = time.Minute
	// TeamInvitationHourlyWindow sets the counter lifetime from its first
	// accepted team attempt.
	TeamInvitationHourlyWindow = time.Hour
	// TeamInvitationHourlyLimit caps accepted attempts for all recipients in one
	// team window.
	TeamInvitationHourlyLimit = 20
)

// TeamInvitationRateScript checks recipient cooldown before hourly occupancy
// and records accepted attempts in one Redis execution. Denial branches retain
// existing counters, while a command error after the first write can leave a
// recipient marker before returning failure. A saturated counter with unusable
// TTL reports a full window hint.
var TeamInvitationRateScript = redis.NewScript(`
local cooldown_ttl = redis.call('PTTL', KEYS[1])
if cooldown_ttl > 0 then
  return {0, cooldown_ttl}
end
local hourly_count = tonumber(redis.call('GET', KEYS[2]) or '0')
if hourly_count >= tonumber(ARGV[3]) then
  local hourly_ttl = redis.call('PTTL', KEYS[2])
  if hourly_ttl < 1 then hourly_ttl = tonumber(ARGV[2]) end
  return {0, hourly_ttl}
end
redis.call('SET', KEYS[1], '1', 'PX', ARGV[1])
hourly_count = redis.call('INCR', KEYS[2])
if hourly_count == 1 then redis.call('PEXPIRE', KEYS[2], ARGV[2]) end
return {1, 0}
`)

// TeamInvitationLimiter checks shared Redis invitation controls and returns
// admission or retry duration.
type TeamInvitationLimiter struct {
	// redis is the retained shared client used for atomic admission scripts.
	// Assembly owns client closure after invitation callers stop; nil client
	// makes checks fail.
	redis *redis.Client
}

// NewTeamInvitationLimiter retains the supplied Redis client for shared
// invitation admission. It starts no worker and does not close the client; a
// nil client produces an error when checked.
func NewTeamInvitationLimiter(redisClient *redis.Client) team.TeamInvitationLimiter {
	return &TeamInvitationLimiter{redis: redisClient}
}

// CheckAndRecord attempts shared invitation admission for a team and
// normalized mailbox, returning its retry duration when denied. Nil
// receiver/client and Redis or malformed-response failures return errors,
// leaving TeamService to reject unavailable admission.
//
// The script first checks recipient cooldown, then the team hourly count, and
// records an accepted attempt before downstream token generation or mail
// preparation begins. Recipient hashes normalize case/spacing only, and the
// hourly window expires from its first accepted increment. Response parts
// must decode as two supported integers; retry milliseconds become a duration
// and only admission value one is accepted.
func (l *TeamInvitationLimiter) CheckAndRecord(ctx context.Context, teamID int64, email string) (bool, time.Duration, error) {
	if l == nil || l.redis == nil {
		return false, 0, fmt.Errorf("team invitation Redis is not configured")
	}
	// Hash lowercased trimmed mailbox text for a team-specific recipient key,
	// sharing cooldown across case/spacing variants while the separate hourly
	// key collects every recipient in that team.
	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	sum := sha256.Sum256([]byte(normalizedEmail))
	recipientKey := fmt.Sprintf("team:invite:%d:recipient:%s", teamID, hex.EncodeToString(sum[:]))
	hourlyKey := fmt.Sprintf("team:invite:%d:hourly", teamID)

	// Redis serializes the checks and writes, but a runtime error can follow an
	// applied recipient marker. Return that error for unavailable admission; the
	// library retries a missing script digest through EVAL.
	value, err := TeamInvitationRateScript.Run(ctx, l.redis, []string{recipientKey, hourlyKey},
		TeamInvitationRecipientCooldown.Milliseconds(), TeamInvitationHourlyWindow.Milliseconds(), TeamInvitationHourlyLimit).Result()
	if err != nil {
		return false, 0, err
	}
	// Require the Lua admission flag and retry-millisecond pair before decoding.
	parts, ok := value.([]any)
	if !ok || len(parts) != 2 {
		return false, 0, fmt.Errorf("invalid team invitation rate limit result")
	}
	allowed, err := RedisInt64(parts[0])
	if err != nil {
		return false, 0, err
	}
	retryMillis, err := RedisInt64(parts[1])
	if err != nil {
		return false, 0, err
	}
	// Convert Redis retry milliseconds for the service's duration-based hint.
	return allowed == 1, time.Duration(retryMillis) * time.Millisecond, nil
}

// RedisInt64 returns a supplied int64 or scans one integer from string input.
// Scanning can leave trailing text unconsumed; other dynamic types return an
// error, and the caller rejects script replies whose part cannot be decoded.
func RedisInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case string:
		var parsed int64
		// Decode a string reply part with the scanner's first-integer rule,
		// allowing trailing text while returning any parse failure for the caller
		// to reject.
		_, err := fmt.Sscan(typed, &parsed)
		return parsed, err
	default:
		return 0, fmt.Errorf("invalid Redis integer type: %T", value)
	}
}
