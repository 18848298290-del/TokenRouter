package usageclient

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// CN providers expose balance amounts and reset times in several JSON
// representations. Numeric parsing accepts supported values before domain
// validation, while reset timestamps are normalized to UTC and percentage
// windows are kept even when their reset time cannot be parsed.
//
// Percentage limits use a fixed hundred-percent total and retain reported
// usage above that total. Remaining percentage is clamped at zero, so overuse
// is represented by its used value instead of a negative remainder. Endpoint
// construction uses a provider-configured host and a fixed adapter path,
// retaining only a requested coding prefix from the base path. Authentication,
// destination policy, and later normalized-value validation belong to the
// caller.

// CnParseF64 converts float64, float32, int, int64, json.Number, or trimmed
// numeric strings to float64. The boolean reports conversion success,
// including parseable NaN or infinity; unsupported types return false. Callers
// validate finiteness and domain ranges separately because integer conversion
// can lose precision.
func CnParseF64(raw any) (float64, bool) {
	// Numeric conversion accepts representable Go inputs before domain checks.
	// Callers apply finite/range validation because conversion alone can retain
	// nonfinite floats or round a large integer.
	switch v := raw.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// CnNormalizeResetTime formats supported timestamp strings or numeric epochs
// using UTC and the time.RFC3339 layout. Unrecognized strings, unsupported
// types, and nonpositive integral epochs return empty; float64 epochs pass
// through int64 conversion before seconds-or-milliseconds selection. Numeric
// conversion does not check RFC3339's year range, so an extreme epoch can
// produce text that CnUsageLimits later omits as an unparseable reset.
func CnNormalizeResetTime(raw any) string {
	switch v := raw.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return ""
		}
		// Reset text is optional observation data. Keep an unrecognized layout
		// absent so usage percentages remain available to the adapter and display.
		if ts, err := timezone.ParseFlexibleTimestamp(s); err == nil {
			return ts.UTC().Format(time.RFC3339)
		}
		return ""
	case float64:
		// Finite in-range epochs discard fractional units before magnitude selection.
		return CnMillisToRFC3339(int64(v))
	case int:
		return CnMillisToRFC3339(int64(v))
	case int64:
		return CnMillisToRFC3339(v)
	case json.Number:
		// JSON numbers require an in-range decimal Int64 spelling here. Omit
		// the optional reset when parsing fails, including fractional or exponent
		// notation, so usable percentage observations remain available.
		if n, err := v.Int64(); err == nil {
			return CnMillisToRFC3339(n)
		}
		return ""
	default:
		return ""
	}
}

// CnMillisToRFC3339 formats a positive epoch using UTC and the time.RFC3339
// layout. Values below one trillion represent seconds and larger values
// represent milliseconds; nonpositive values mean an absent timestamp.
// Extreme positive epochs can exceed the layout's four-digit year range,
// leaving strict RFC3339 parsing to reject the formatted reset text.
func CnMillisToRFC3339(n int64) string {
	if n <= 0 {
		return ""
	}
	var ms int64
	// Epoch magnitude selects seconds below the trillion-unit timestamp threshold.
	if n < 1_000_000_000_000 {
		ms = n * 1000
	} else {
		ms = n
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// CnUsageLimits builds normalized percentage limits from a nonempty list of
// provider windows. Blank names, negative usage, and nonfinite percentages
// reject the entire result. Reported usage can exceed one hundred percent and
// is retained beside a fixed one-hundred-percent limit.
//
// Remaining percentage is clamped at zero, while an invalid or zero reset time
// is omitted without discarding the window. Each numeric pointer receives
// separate storage. Window order is retained; duplicate-name validation and
// provider-label validation are left to the normalized-result consumer.
func CnUsageLimits(provider string, tiers []usageview.CNQuotaTier) (*usageview.UpstreamUsageInfo, error) {
	if len(tiers) == 0 {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	limits := make([]usageview.UpstreamUsageLimit, 0, len(tiers))
	for _, tier := range tiers {
		name := strings.TrimSpace(tier.Window)
		if name == "" || !usageview.ValidFiniteNumber(tier.UsedPercent) || tier.UsedPercent < 0 {
			return nil, usageview.ErrUpstreamUsageInvalidResponse
		}
		// Percentage windows have a hundred-percent total. Retain reported overuse
		// in Used while flooring the derived Remaining value for display and monitoring.
		remaining := 100 - tier.UsedPercent
		if remaining < 0 {
			remaining = 0
		}
		var resetAt *time.Time
		// Invalid reset timestamps do not discard otherwise usable usage data.
		// Store a reset only when the adapter supplied a nonzero RFC3339 instant.
		if parsed, err := time.Parse(time.RFC3339, tier.ResetAt); err == nil && !parsed.IsZero() {
			utc := parsed.UTC()
			resetAt = &utc
		}
		used := tier.UsedPercent
		limit := float64(100)
		limits = append(limits, usageview.UpstreamUsageLimit{Name: name, Used: &used, Limit: &limit, Remaining: &remaining, ResetAt: resetAt})
	}
	return &usageview.UpstreamUsageInfo{Provider: provider, Mode: "limits", Unit: "PERCENT", Limits: limits}, nil
}

// ValidateCNUsageStatus maps CN query HTTP status to a shared usage error,
// with 2xx returning nil. Authentication, rate limits, and missing endpoints
// receive specific categories; all other statuses mean invalid response.
func ValidateCNUsageStatus(status int) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == 401 || status == 403:
		return usageview.ErrUpstreamUsageAuthFailed
	case status == 429:
		return usageview.ErrUpstreamUsageRateLimited
	case status == 404 || status == 405:
		return usageview.ErrUpstreamUsageUnsupported
	default:
		return usageview.ErrUpstreamUsageInvalidResponse
	}
}

// CnUsageEndpoint builds a fixed adapter path on the trimmed provider base. It
// requires scheme and host; destination policy is validated elsewhere, and URL
// credentials, query contents, and fragments are discarded. ForceQuery remains,
// so a forced empty query can retain its bare question mark.
//
// When preserveCodingPrefix is true and the lowercase base path contains
// /coding anywhere, the replacement path receives exactly that prefix. All
// other base path components are discarded, and RawPath is cleared to match
// the rebuilt path. The supplied adapter path is trusted configuration from
// the caller.
func CnUsageEndpoint(base, path string, preserveCodingPrefix bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("invalid base URL")
	}
	prefix := ""
	// Coding endpoints use a fixed /coding prefix when the configured base
	// requests that mode; replace the rest of the path before appending adapter input.
	if preserveCodingPrefix && strings.Contains(strings.ToLower(parsed.Path), "/coding") {
		prefix = "/coding"
	}
	parsed.Path = strings.TrimRight(prefix, "/") + "/" + strings.TrimLeft(path, "/")
	// The new adapter path replaces the original escaped spelling. Clear that
	// spelling and non-path components before formatting the credential-bearing
	// request target.
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.User = nil
	return strings.TrimRight(parsed.String(), "/"), nil
}
