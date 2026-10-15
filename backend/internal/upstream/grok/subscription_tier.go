package grok

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// subscription_tier.go resolves Grok plan labels from billing amounts,
// reported tiers, and fresh quota signals.
// A reported tier can be ambiguous even when billing and quota observations
// are available. Recognized monthly cent limits determine a plan first, while
// explicit Free and SuperGrok Lite, Plus, and Heavy tiers retain their own
// classifications. Other recognized paid labels can use fresh Grok 4.5
// Responses evidence to resolve Heavy; unsupported labels remain
// unclassified.
//
// Quota hints keep an observation timestamp separate from later snapshot
// writes. A Grok 4.5 limit observation records a local plan signal, while
// other models carry its prior time forward. Freshness prefers the last
// header time and allows bounded future skew, preventing a newer snapshot
// write from refreshing an old header observation. JWT decoding reads claims
// without signature verification, so these display signals do not
// authenticate a token.

// Plan inference limits the age of Grok 4.5 evidence and recognizes Heavy
// from either configured request-count or token-count thresholds for that model.
const (
	// GrokQuotaSignalMaxAge bounds the age of Grok 4.5 quota evidence used to infer a plan.
	GrokQuotaSignalMaxAge = 24 * time.Hour

	grok45ResponsesModel             = "grok-4.5" // quota inference model ID
	grokHeavyQuotaRequestLimit int64 = 8_300      // Heavy request limit threshold
	grokHeavyQuotaTokenLimit   int64 = 53_000_000 // Heavy token limit threshold
)

// MapJWTSubscriptionTier maps locally recognized numeric tier IDs to named
// labels. Unknown IDs are returned as decimal text, preserving an
// unrecognized claim for callers instead of assigning a known subscription
// category.
func MapJWTSubscriptionTier(tier uint64) string {
	switch tier {
	case 0:
		return "free"
	case 1:
		return "supergrok"
	case 2:
		return "x_basic"
	case 3:
		return "x_premium"
	case 4:
		return "x_premium_plus"
	case 5:
		return "supergrok_heavy"
	case 6:
		return "supergrok_lite"
	case 7:
		return "supergrok_plus"
	default:
		return strconv.FormatUint(tier, 10)
	}
}

// NormalizeSubscriptionTier trims and lowercases tier text, replaces hyphens,
// and joins whitespace with underscores before applying known aliases.
// Unrecognized values retain the resulting text; numeric text is not mapped
// through the JWT numeric tier table here.
func NormalizeSubscriptionTier(raw string) string {
	t := strings.ToLower(strings.TrimSpace(raw))
	t = strings.ReplaceAll(t, "-", "_")
	t = strings.Join(strings.Fields(t), "_")
	switch t {
	case "free", "grok_free", "grokfree", "free_tier", "freetier", "grok_basic", "grokbasic":
		return "free"
	case "supergrok", "grokpro":
		return "supergrok"
	case "supergrok_lite", "supergroklite":
		return "supergrok_lite"
	case "supergrok_heavy", "supergrokheavy":
		return "supergrok_heavy"
	case "supergrok_pro", "supergrokpro":
		return "supergrok_pro"
	case "supergrok_plus", "supergrokplus":
		return "supergrok_plus"
	case "x_basic", "xbasic", "basic":
		return "x_basic"
	case "x_premium", "xpremium":
		return "x_premium"
	case "x_premium_plus", "xpremiumplus", "x_premium+":
		return "x_premium_plus"
	default:
		return t
	}
}

// SubscriptionTierFromJWT decodes an unverified JWT tier claim into local
// tier text. Missing, malformed, unsupported, or negative float claims return
// empty. Numeric strings map through the numeric tier table; other strings
// use alias normalization.
//
// Decoded float64 claims convert to uint64 after the negative check, with
// fractional parts truncated for representable nonnegative values. The helper
// does not check the uint64 range. json.Number values use integer parsing
// when possible and otherwise normalize their text. The helper verifies no
// signature, issuer, expiry, or token authorization; callers use the result
// as metadata from their already chosen
// credential.
func SubscriptionTierFromJWT(jwt string) string {
	claims := DecodeJWTClaims(jwt)
	if claims == nil {
		return ""
	}
	raw, ok := claims["tier"]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case float64:
		if v < 0 {
			return ""
		}
		return MapJWTSubscriptionTier(uint64(v))
	case json.Number:
		n, err := v.Int64()
		if err != nil || n < 0 {
			return NormalizeSubscriptionTier(v.String())
		}
		return MapJWTSubscriptionTier(uint64(n))
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return ""
		}
		if n, err := strconv.ParseUint(trimmed, 10, 64); err == nil {
			return MapJWTSubscriptionTier(n)
		}
		return NormalizeSubscriptionTier(trimmed)
	default:
		return ""
	}
}

// CanonicalGrokPlan resolves local plan text from monthly amounts, reported
// tier, and quota metadata. A recognized rounded monthly limit wins before
// any tier signal. Free and x_basic become free; explicit SuperGrok Heavy,
// Lite, and Plus remain their named classifications.
//
// Ambiguous paid labels consult Grok45ResponsesPlanHint and otherwise become
// supergrok. Unknown labels return empty, so an unrelated tier cannot gain
// paid classification from quota alone. Quota inference uses fresh retained
// hints or Grok 4.5 model observations; a zero time passed to that helper
// uses the current wall clock. Callers select which credential tier reaches
// this resolver.
func CanonicalGrokPlan(monthlyLimitCents *float64, subscriptionTier string, quota *QuotaSnapshot) string {
	// Recognized monthly included amounts outrank reported tier and quota hints,
	// giving callers one consistent plan when those independent observations disagree.
	if plan := resolvePlan(monthlyLimitCents); plan != "" {
		return NormalizeSubscriptionTier(plan)
	}

	normalized := NormalizeSubscriptionTier(subscriptionTier)
	switch normalized {
	case "free", "x_basic":
		return "free"
	case "supergrok_heavy":
		return "supergrok_heavy"
	case "supergrok_lite":
		return "supergrok_lite"
	case "supergrok_plus":
		return "supergrok_plus"
	}

	// Quota evidence refines recognized ambiguous paid labels only; clear free
	// or named paid tiers have already returned, and unknown labels remain unclassified.
	if isAmbiguousGrokPaidPlan(normalized) {
		if hint := Grok45ResponsesPlanHint(quota, time.Time{}); hint != "" {
			return hint
		}
		return "supergrok"
	}
	return ""
}

// isAmbiguousGrokPaidPlan reports whether normalized text is a recognized
// ambiguous paid label.
func isAmbiguousGrokPaidPlan(normalized string) bool {
	switch normalized {
	case "supergrok", "supergrok_pro", "paid", "pro":
		return true
	default:
		return false
	}
}

// IsGrok45ResponsesQuotaModel reports whether a trimmed, lowercased model is
// grok-4.5 or has its hyphenated suffix. Vendor-qualified IDs do not match
// this local evidence rule; callers pass the model that produced the headers.
func IsGrok45ResponsesQuotaModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return m == grok45ResponsesModel || strings.HasPrefix(m, grok45ResponsesModel+"-")
}

// Grok45ResponsesPlanHint returns a fresh carried supergrok or
// supergrok_heavy hint before inspecting the current model. A carried hint
// retains its own timestamp, so snapshots from another model can reuse it
// without making its observation newer.
//
// Otherwise the snapshot must identify Grok 4.5 and have fresh observation
// time; a Heavy-sized request or token limit then yields supergrok_heavy.
// Smaller windows return no direct hint in this path. Nil snapshots or
// unusable times return empty, and zero now selects the current wall clock.
func Grok45ResponsesPlanHint(quota *QuotaSnapshot, now time.Time) string {
	if quota == nil {
		return ""
	}
	// A carried hint keeps the originating model's timestamp, allowing reuse after
	// other-model snapshots while preventing their later writes from renewing that evidence.
	if plan := NormalizeSubscriptionTier(quota.PlanFrom45Responses); plan == "supergrok" || plan == "supergrok_heavy" {
		if isQuotaTimestampFresh(quota.PlanFrom45ResponsesAt, now) {
			return plan
		}
	}
	if !IsGrok45ResponsesQuotaModel(quota.Model) || !IsQuotaSnapshotFresh(quota, now) {
		return ""
	}
	if quotaLooksLikeGrokHeavy(quota) {
		return "supergrok_heavy"
	}
	return ""
}

// ApplyGrok45ResponsesPlanSignal mutates a snapshot's retained plan hint. A
// Grok 4.5 observation with at least one limit records Heavy when either
// local threshold is met and supergrok otherwise. Its timestamp comes from
// the first nonblank LastHeadersSeenAt or UpdatedAt.
//
// A snapshot without that model/limit evidence carries a previous nonblank
// hint and its original timestamp forward. This path does not refresh or
// validate the carried hint's age; readers apply freshness later. Nil target
// snapshots are ignored, and callers serialize mutation when snapshots are
// shared.
func ApplyGrok45ResponsesPlanSignal(s, prev *QuotaSnapshot) {
	if s == nil {
		return
	}
	observedAt := firstNonEmptyQuotaTime(s.LastHeadersSeenAt, s.UpdatedAt)
	if IsGrok45ResponsesQuotaModel(s.Model) && quotaHasLimitWindow(s) {
		if quotaLooksLikeGrokHeavy(s) {
			s.PlanFrom45Responses = "supergrok_heavy"
			s.PlanFrom45ResponsesAt = observedAt
			return
		}
		s.PlanFrom45Responses = "supergrok"
		s.PlanFrom45ResponsesAt = observedAt
		return
	}
	// Carry the previous timestamp with its hint when this observation lacks a
	// Grok 4.5 limit, so the new snapshot cannot extend the hint's freshness lifetime.
	if prev != nil && strings.TrimSpace(prev.PlanFrom45Responses) != "" {
		s.PlanFrom45Responses = prev.PlanFrom45Responses
		s.PlanFrom45ResponsesAt = prev.PlanFrom45ResponsesAt
	}
}

// QuotaSnapshotObservedAt parses LastHeadersSeenAt before UpdatedAt. The
// first nonblank timestamp wins, including when it is malformed; nil
// snapshots or parse failure return false so callers do not refresh evidence
// from another timestamp.
//
// Choosing header time keeps a later snapshot write from renewing an older
// header observation. The parsed time can still be stale, and readers apply
// freshness separately. Source selection precedes parsing, so a malformed
// header time does not fall through to an otherwise valid update timestamp.
func QuotaSnapshotObservedAt(snapshot *QuotaSnapshot) (time.Time, bool) {
	if snapshot == nil {
		return time.Time{}, false
	}
	return parseQuotaTimestamp(firstNonEmptyQuotaTime(snapshot.LastHeadersSeenAt, snapshot.UpdatedAt))
}

// IsQuotaSnapshotFresh reports whether the chosen quota observation time is
// within GrokQuotaSignalMaxAge and at most five minutes ahead of now. Missing
// or invalid observation times return false; zero now uses the current wall
// clock.
func IsQuotaSnapshotFresh(snapshot *QuotaSnapshot, now time.Time) bool {
	observedAt, ok := QuotaSnapshotObservedAt(snapshot)
	if !ok {
		return false
	}
	return isTimeFresh(observedAt, now)
}

// isQuotaTimestampFresh parses one RFC3339 timestamp before applying the
// quota freshness rule. Blank or invalid input returns false, and zero now
// uses the current wall clock through isTimeFresh.
func isQuotaTimestampFresh(raw string, now time.Time) bool {
	parsed, ok := parseQuotaTimestamp(raw)
	if !ok {
		return false
	}
	return isTimeFresh(parsed, now)
}

// parseQuotaTimestamp parses trimmed RFC3339 observation text into a time.
// Blank or malformed text returns false, preserving missing evidence rather
// than substituting the current time or another source timestamp.
func parseQuotaTimestamp(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	// Invalid observation text cannot establish freshness. Return no parsed time
	// so plan inference cannot treat a malformed timestamp as current evidence.
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

// isTimeFresh accepts observations no older than GrokQuotaSignalMaxAge and no
// more than five minutes in the future. Zero now reads the wall clock;
// callers parse or select the observation timestamp before applying it.
func isTimeFresh(observedAt, now time.Time) bool {
	if now.IsZero() {
		now = time.Now()
	}
	// Limited future skew tolerates nearby observation clocks while rejecting distant
	// future timestamps that would otherwise keep a hint fresh for an excessive period.
	age := now.Sub(observedAt)
	return age <= GrokQuotaSignalMaxAge && age >= -5*time.Minute
}

// quotaHasLimitWindow reports whether request or token limit pointers are
// present. Nil snapshots or missing limits return false; a pointed zero still
// counts as observed limit evidence for plan-signal recording.
func quotaHasLimitWindow(quota *QuotaSnapshot) bool {
	if quota == nil {
		return false
	}
	if quota.Requests != nil && quota.Requests.Limit != nil {
		return true
	}
	return quota.Tokens != nil && quota.Tokens.Limit != nil
}

// quotaLooksLikeGrokHeavy reports whether either retained limit meets its
// local Heavy threshold. Missing request and token limits contribute zero,
// and nil snapshots return false. A request limit can qualify by itself, as
// can a token limit, so a smaller or missing companion window does not cancel
// a qualifying observation.
//
// Model eligibility and observation age belong to Grok45ResponsesPlanHint
// and ApplyGrok45ResponsesPlanSignal. This helper evaluates the numeric
// windows supplied by those callers.
func quotaLooksLikeGrokHeavy(quota *QuotaSnapshot) bool {
	if quota == nil {
		return false
	}
	var requestLimit, tokenLimit int64
	if quota.Requests != nil && quota.Requests.Limit != nil {
		requestLimit = *quota.Requests.Limit
	}
	if quota.Tokens != nil && quota.Tokens.Limit != nil {
		tokenLimit = *quota.Tokens.Limit
	}
	return requestLimit >= grokHeavyQuotaRequestLimit || tokenLimit >= grokHeavyQuotaTokenLimit
}

// firstNonEmptyQuotaTime returns the first nonblank trimmed timestamp text.
// It does not validate that text, so a malformed earlier value can suppress a
// later valid timestamp; all blank values return empty.
func firstNonEmptyQuotaTime(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
