package grok

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// quota.go parses Grok quota headers into request, token, retry, and
// subscription observations.
// Quota evidence can arrive as request limits, token limits, retry timing, or
// tier and entitlement headers independently. Passive parsing omits responses
// with none of those signals, while active observations retain status and
// probe time even when headers are absent. The raw header map contains a
// fixed allowlist of nonblank values.
//
// Window resets use one Unix-second representation after interpreting numeric
// magnitude, duration text, or RFC3339 timestamps. Relative values are
// anchored at parsing time, and positive subsecond durations are raised to
// one second before that conversion. Retry-After keeps numeric delays as
// supplied but converts HTTP dates to a nonnegative countdown. Observation
// timestamps mark header presence separately from a recorded probe attempt.

// GrokFreeRolling24hTokenLimit defines the nominal Free rolling-day amount for
// local soft gating; the classifier also accepts its configured alternate limits.
const GrokFreeRolling24hTokenLimit int64 = 500_000

// These values select accepted Free quota variants and response headers retained
// as evidence; unlisted headers never enter the snapshot's raw header map.
var (
	grokFreeRolling24hTokenLimits = map[int64]struct{}{
		GrokFreeRolling24hTokenLimit: {},

		// One-million and two-million values are accepted Free token limits.
		// Both variants are exercised by TestIsGrokFreeRolling24hTokenLimit.
		1_000_000: {},
		2_000_000: {},
	}

	quotaHeaderAllowlist = []string{
		"x-ratelimit-limit-requests",
		"x-ratelimit-remaining-requests",
		"x-ratelimit-reset-requests",
		"x-ratelimit-limit-tokens",
		"x-ratelimit-remaining-tokens",
		"x-ratelimit-reset-tokens",
		"x-rate-limit-limit-requests",
		"x-rate-limit-remaining-requests",
		"x-rate-limit-reset-requests",
		"x-rate-limit-limit-tokens",
		"x-rate-limit-remaining-tokens",
		"x-rate-limit-reset-tokens",
		"retry-after",
		"x-subscription-tier",
		"xai-subscription-tier",
		"x-xai-subscription-tier",
		"x-xai-user-tier",
		"xai-user-tier",
		"xai-tier",
		"x-user-tier",
		"x-plan-tier",
		"x-subscription-plan",
		"x-entitlement-status",
		"xai-entitlement-status",
		"x-xai-entitlement-status",
		"x-xai-user-entitlement-status",
		"x-user-entitlement-status",
	}
)

// QuotaWindow aliases the shared display representation of one parsed quota
// dimension.
type QuotaWindow = usageview.QuotaWindow

// QuotaSnapshot aliases the shared provider display value for Grok quota
// evidence.
type QuotaSnapshot = usageview.QuotaSnapshot

// IsGrokFreeRolling24hTokenLimit reports whether a limit matches a configured
// Free rolling-day variant.
func IsGrokFreeRolling24hTokenLimit(limit int64) bool {
	_, ok := grokFreeRolling24hTokenLimits[limit]
	return ok
}

// ParseQuotaHeaders constructs quota evidence from recognized response
// headers. Nil or headerless input returns nil, even for status 429; use
// ObserveQuotaHeaders or ParseQuotaObservation when a status-only observation
// needs to be retained.
func ParseQuotaHeaders(headers http.Header, statusCode int) *QuotaSnapshot {
	return parseQuotaHeaders(headers, statusCode, "", false)
}

// ObserveQuotaHeaders constructs an observation even when recognized headers
// are absent. A trimmed active_probe source stamps LastProbeAt; header
// evidence separately stamps LastHeadersSeenAt, allowing consumers to
// distinguish a probe attempt from usable quota data.
func ObserveQuotaHeaders(headers http.Header, statusCode int, source string) *QuotaSnapshot {
	return parseQuotaHeaders(headers, statusCode, source, true)
}

// parseQuotaHeaders builds request/token windows and metadata from
// allowlisted response values. keepEmpty retains a status-only snapshot;
// otherwise absent evidence returns nil. Tier and entitlement use ordered
// header aliases, with the first nonblank value selected before the raw
// values are copied.
//
// A present window, parsed retry delay, tier, entitlement, or allowlisted raw
// header marks headers observed. Raw nonblank but unparsable values therefore
// still record header presence. UpdatedAt is the current UTC time,
// LastProbeAt depends on the active_probe source, and LastHeadersSeenAt is
// set only when this evidence check succeeds.
func parseQuotaHeaders(headers http.Header, statusCode int, source string, keepEmpty bool) *QuotaSnapshot {
	if headers == nil && !keepEmpty {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	snapshot := &QuotaSnapshot{
		Requests: parseQuotaWindow(headers, "requests"),

		Tokens: parseQuotaWindow(headers, "tokens"),

		StatusCode: statusCode,

		Headers: make(map[string]string),

		ObservationSource: strings.TrimSpace(source),

		UpdatedAt: now,
	}
	if snapshot.ObservationSource == "active_probe" {
		snapshot.LastProbeAt = now
	}
	if retryAfter := parseRetryAfter(headers.Get("retry-after")); retryAfter != nil {
		snapshot.RetryAfterSeconds = retryAfter
	}
	snapshot.SubscriptionTier = firstHeader(headers,
		"xai-subscription-tier",
		"x-subscription-tier",
		"x-xai-subscription-tier",
		"x-xai-user-tier",
		"xai-user-tier",
		"xai-tier",
		"x-user-tier",
		"x-plan-tier",
		"x-subscription-plan",
	)
	snapshot.EntitlementStatus = firstHeader(headers,
		"xai-entitlement-status",
		"x-entitlement-status",
		"x-xai-entitlement-status",
		"x-xai-user-entitlement-status",
		"x-user-entitlement-status",
	)

	for _, name := range quotaHeaderAllowlist {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			snapshot.Headers[name] = value
		}
	}

	if snapshot.Requests == nil &&
		snapshot.Tokens == nil &&
		snapshot.RetryAfterSeconds == nil &&
		snapshot.SubscriptionTier == "" &&
		snapshot.EntitlementStatus == "" &&
		len(snapshot.Headers) == 0 {
		if keepEmpty {
			return snapshot
		}
		return nil
	}
	snapshot.HeadersObserved = true
	snapshot.LastHeadersSeenAt = now
	return snapshot
}

// parseQuotaWindow parses request or token fields through preferred
// x-ratelimit names before x-rate-limit aliases. The first nonblank value
// wins for each field, even when it fails parsing; a later alias cannot
// replace that selection.
//
// Limit, remaining, and reset values stay independently optional, and the
// window is omitted only when all three parsed values are absent. A parsed
// reset receives Unix seconds and UTC RFC3339 text. parseQuotaHeaders can
// still retain unparsable source text in its raw header map when this helper
// returns no window.
func parseQuotaWindow(headers http.Header, dimension string) *QuotaWindow {
	limitHeader := firstHeader(headers,
		"x-ratelimit-limit-"+dimension,
		"x-rate-limit-limit-"+dimension,
	)
	remainingHeader := firstHeader(headers,
		"x-ratelimit-remaining-"+dimension,
		"x-rate-limit-remaining-"+dimension,
	)
	resetHeader := firstHeader(headers,
		"x-ratelimit-reset-"+dimension,
		"x-rate-limit-reset-"+dimension,
	)
	window := &QuotaWindow{
		Limit:     parseInt64Ptr(limitHeader),
		Remaining: parseInt64Ptr(remainingHeader),
	}
	if reset := parseResetHeader(resetHeader); reset != nil {
		window.ResetUnix = reset
		window.ResetAt = time.Unix(*reset, 0).UTC().Format(time.RFC3339)
	}
	if window.Limit == nil && window.Remaining == nil && window.ResetUnix == nil {
		return nil
	}
	return window
}

// parseResetHeader converts reset text to Unix seconds, returning nil when no
// accepted representation parses. Numeric values at least one trillion are
// millisecond epochs, values at least one billion are second epochs, and
// smaller values are relative seconds, including zero or negatives.
//
// Nonnumeric positive Go durations are added to the current time after
// raising durations below one second to one second. RFC3339 text supplies an
// absolute timestamp. Relative conversions read the wall clock here, so
// callers cannot inject their observation time through this parser.
func parseResetHeader(raw string) *int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// Interpret numeric, duration, and date forms in order. A parsed form
	// establishes a reset epoch; invalid text leaves other quota evidence usable without a reset.
	if value, err := strconv.ParseInt(raw, 10, 64); err == nil {
		// Numeric magnitude distinguishes absolute timestamps from relative delays.
		// Treating a value such as 60 as an epoch would give consumers an expired reset.
		switch {
		case value >= 1_000_000_000_000: // millisecond epoch threshold
			value /= 1000
		case value >= 1_000_000_000: // Unix-second epoch threshold
			// Absolute second timestamps keep their supplied epoch value.
		default: // seconds relative to parsing time
			value = time.Now().Unix() + value
		}
		return &value
	}
	if duration, err := time.ParseDuration(raw); err == nil && duration > 0 {
		// Raising subsecond resets to one second prevents Unix-second conversion from
		// collapsing their wait into the current second solely because of truncation.
		if duration < time.Second {
			duration = time.Second
		}
		value := time.Now().Add(duration).Unix()
		return &value
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		value := t.Unix()
		return &value
	}
	return nil
}

// parseRetryAfter returns a numeric delay unchanged, including negative
// values, or a nonnegative integer countdown from an HTTP date. Invalid text
// returns nil; date countdowns truncate fractional seconds before clamping
// past dates to zero.
func parseRetryAfter(raw string) *int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// Delay seconds retain their supplied sign before date interpretation. Invalid
	// timing text leaves other snapshot evidence available instead of creating a retry interval.
	if value, err := strconv.Atoi(raw); err == nil {
		return &value
	}
	if t, err := http.ParseTime(raw); err == nil {
		// HTTP dates become remaining whole seconds for consumers; elapsed reset dates
		// use zero so this representation cannot add a negative waiting interval.
		seconds := max(int(time.Until(t).Seconds()), 0)
		return &seconds
	}
	return nil
}

// parseInt64Ptr parses trimmed decimal text into an optional signed integer.
// Blank or invalid values return nil, while zero and negative values remain
// present for quota consumers to interpret.
func parseInt64Ptr(raw string) *int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// Malformed numeric headers leave this parsed dimension unset while snapshots
	// can still retain raw header evidence and other successfully parsed quota values.
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil
	}
	return &value
}

// firstHeader returns the first nonblank trimmed value among the supplied
// header names. An invalid nonblank value still wins; callers parse it
// afterward rather than trying a later alias on parse failure.
func firstHeader(headers http.Header, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

// ParseQuotaObservation returns parsed headers or a status-only snapshot for
// a headerless 429. The supplied now stamps that special snapshot only;
// recognized-header parsing uses its own wall clock, and other headerless
// statuses return nil.
func ParseQuotaObservation(headers http.Header, status int, now time.Time) *QuotaSnapshot {
	snapshot := ParseQuotaHeaders(headers, status)
	// A headerless 429 still records exhaustion status at the caller's observation
	// time, allowing health consumers to process throttling when window headers are absent.
	if snapshot == nil && status == http.StatusTooManyRequests {
		return &QuotaSnapshot{StatusCode: status, UpdatedAt: now.UTC().Format(time.RFC3339)}
	}
	return snapshot
}
