package provider

import (
	"strings"
	"time"
)

// gemini_usage_rules.go groups Gemini usage by model class and calculates
// local daily quota windows.
// Gemini local quota views group model usage into Pro and Flash classes. Flash
// or lite substrings select Flash, and remaining model names select Pro,
// allowing quota readers to sum requests, tokens, and provider costs under
// those two categories.
//
// Daily windows begin at midnight in the supplied location. Reset calculation
// adds a fixed twenty-four-hour duration, then another if the first result is
// not future; it therefore follows elapsed hours rather than next calendar
// midnight at daylight-saving changes. Callers choose the quota location and
// use the returned start and reset consistently. Shared and per-class quota
// fields remain separate so precheck and display can apply the selected
// provider tier.

// Gemini model classes select the local request, token, and cost buckets used
// by quota display and precheck readers.
const (
	GeminiModelPro   GeminiModelClass = "pro"   // default local Pro usage bucket
	GeminiModelFlash GeminiModelClass = "flash" // flash or lite usage bucket
)

// GeminiQuota holds shared and class-specific request limits for quota
// evaluation.
type GeminiQuota struct {
	// SharedRPD, ProRPD, and FlashRPD measure requests per day; SharedRPM, ProRPM,
	// and FlashRPM measure requests per minute. Prechecks independently prefer a
	// positive shared limit for each duration and otherwise select the model
	// class limit. A nonpositive selected value disables that local check.
	SharedRPD int64 `json:"shared_rpd,omitempty"` // shared daily request limit
	SharedRPM int64 `json:"shared_rpm,omitempty"` // shared minute request limit
	ProRPD    int64 `json:"pro_rpd,omitempty"`    // Pro daily request limit
	ProRPM    int64 `json:"pro_rpm,omitempty"`    // Pro minute request limit
	FlashRPD  int64 `json:"flash_rpd,omitempty"`  // Flash daily request limit
	FlashRPM  int64 `json:"flash_rpm,omitempty"`  // Flash minute request limit
}

// GeminiUsageTotals aggregates request counts, token counts, and provider
// costs by class.
type GeminiUsageTotals struct {
	// ProRequests: Pro request count; FlashRequests: Flash request count.
	// ProTokens: Pro token count; FlashTokens: Flash token count.
	ProRequests, FlashRequests, ProTokens, FlashTokens int64
	// ProCost: accumulated Pro provider cost; FlashCost: accumulated Flash
	// provider cost.
	ProCost, FlashCost float64
}

// GeminiModelUsage carries local statistics for one observed model identity.
type GeminiModelUsage struct {
	Model string // observed usage model identity
	// Requests: observed request count; TotalTokens: observed total token count.
	Requests, TotalTokens int64
	ProviderCost          float64 // observed local provider cost
}

// GeminiModelClass identifies the Pro or Flash local usage bucket.
type GeminiModelClass string

// GeminiModelClassFromName classifies trimmed case-insensitive names
// containing flash or lite as Flash. Every other name, including empty text,
// selects Pro, so local quota aggregation uses substring evidence without
// validating the model family.
func GeminiModelClassFromName(model string) GeminiModelClass {
	name := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(name, "flash") || strings.Contains(name, "lite") {
		return GeminiModelFlash
	}
	return GeminiModelPro
}

// AggregateGeminiUsage sums model request, token, and cost values into class
// totals. Input values are retained as supplied, including negative values;
// nil input yields zero totals.
func AggregateGeminiUsage(stats []GeminiModelUsage) GeminiUsageTotals {
	var totals GeminiUsageTotals
	for _, stat := range stats {
		switch GeminiModelClassFromName(stat.Model) {
		case GeminiModelFlash:
			totals.FlashRequests += stat.Requests
			totals.FlashTokens += stat.TotalTokens
			totals.FlashCost += stat.ProviderCost
		default:
			totals.ProRequests += stat.Requests
			totals.ProTokens += stat.TotalTokens
			totals.ProCost += stat.ProviderCost
		}
	}
	return totals
}

// GeminiDailyWindowStart returns midnight of now in the supplied quota
// location. Callers provide a nonnil location, and the returned time keeps
// that zone for usage-window queries.
func GeminiDailyWindowStart(now time.Time, loc *time.Location) time.Time {
	localNow := now.In(loc)
	return time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
}

// GeminiDailyResetTime returns a future reset using fixed twenty-four-hour
// steps from local midnight. Supply a nonnil location; daylight-saving changes
// can make this differ from next calendar midnight.
//
// The first step normally provides the reset. If the current local day is long
// enough for that time to be at or before now, a second step supplies a future
// deadline. The calculation uses elapsed duration, so callers apply the same
// chosen location and interval rule when interpreting this local quota
// estimate.
func GeminiDailyResetTime(now time.Time, loc *time.Location) time.Time {
	localNow := now.In(loc)
	start := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
	// Add elapsed hours rather than a calendar day, retaining this fixed-duration
	// reset rule even when the supplied zone crosses a daylight-saving transition.
	reset := start.Add(24 * time.Hour)
	// A long local day can leave the first elapsed-day reset behind now. Advance
	// another fixed interval so precheck callers receive a future recovery point.
	if !reset.After(localNow) {
		reset = reset.Add(24 * time.Hour)
	}
	return reset
}
