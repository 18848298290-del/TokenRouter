package grok

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// billing.go parses Grok billing amounts and merges weekly and monthly
// observations into a shared summary.
// Grok billing can report weekly credit progress and monthly included
// spending in the same configuration. Weekly reset dates remain separate from
// monthly billing dates so an exhausted weekly window is not assigned a
// month-end reset. Amount parsing accepts wrapped values, numbers, and
// numeric strings while retaining absence through nil pointers.
//
// Summary construction caps included monthly usage at a positive limit and
// converts monthly cents to display dollars. Probe merging replaces
// successful windows independently, retaining values and observation times
// for failed windows. Merges reuse input pointer and slice storage, while a
// new FailedWindows slice describes the latest probe outcome. Consumers need
// a separate copy before independently mutating retained amounts or product
// entries.

// Billing endpoint paths select weekly credits or monthly spending, while the
// fixed CLI identity and recognized cent limits define request and display policy.
const (
	// CLITokenAuthHeader names the fixed token identity header sent by billing probes.
	CLITokenAuthHeader     = "x-xai-token-auth"      // CLI token identity header
	CLITokenAuthValue      = "xai-grok-cli"          // CLI token identity value
	CLIClientVersionHeader = "x-grok-client-version" // CLI client version header
	// CLIClientVersion is the shared version used to construct Grok CLI request identities.
	// Billing request construction uses this value; CLI proxy transport can apply
	// a validated environment override before dispatch.
	CLIClientVersion = "1.0.67"
	// billingCLIUserAgent advertises the pinned pager and shell billing identity;
	// general forwarding obtains a separately resolved workspace User-Agent.
	billingCLIUserAgent = "grok-pager/" + CLIClientVersion + " grok-shell/" + CLIClientVersion + " (macos; aarch64)"

	BillingWeeklyPath  = "/billing?format=credits" // weekly credits request suffix
	BillingMonthlyPath = "/billing"                // monthly billing request suffix

	SuperGrokLimitCents      = 15_000  // $150.00
	SuperGrokHeavyLimitCents = 150_000 // $1,500.00
)

// BillingPeriod carries the upstream period label and its reported date
// strings.
type BillingPeriod struct {
	// Type, Start, and End describe one CurrentPeriod observation.
	// BuildBillingSummary uses these dates for weekly evidence independently of
	// BillingPeriodStart and BillingPeriodEnd, which represent the monthly
	// billing window.
	Type  string `json:"type,omitempty"`  // reported period type
	Start string `json:"start,omitempty"` // reported period start text
	End   string `json:"end,omitempty"`   // reported period end text
}

// BillingProductUsage represents one upstream product and its optional credit
// progress percentage.
type BillingProductUsage struct {
	Product string `json:"product,omitempty"` // upstream product display name
	// UsagePercent distinguishes an absent percentage from a reported zero.
	// BuildBillingSummary copies a present value when retaining this product.
	UsagePercent *float64 `json:"usagePercent,omitempty"` // reported credit progress percent
}

// BillingConfig retains weekly credit and monthly spending fields decoded
// from a billing response.
type BillingConfig struct {
	// CurrentPeriod supplies primary period type and dates when present.
	// BuildBillingSummary keeps missing weekly dates empty even when monthly
	// dates exist.
	CurrentPeriod *BillingPeriod `json:"currentPeriod,omitempty"` // upstream primary period object
	// CreditUsagePercent being present establishes weekly evidence, including
	// zero. BuildBillingSummary keeps monthly included-credit progress in a
	// separate UsedPercent field.
	CreditUsagePercent *float64 `json:"creditUsagePercent,omitempty"` // reported weekly progress percent
	// ProductUsage also establishes weekly evidence when usable product names
	// remain. BuildBillingSummary skips blank names and copies retained
	// percentage pointers.
	ProductUsage []BillingProductUsage `json:"productUsage,omitempty"` // reported weekly product observations
	// MonthlyLimit and Used carry monthly cents in wrapped, numeric, or string
	// JSON form. BuildBillingSummary caps included usage at a positive limit and
	// also derives dollar-valued display amounts.
	MonthlyLimit json.RawMessage `json:"monthlyLimit,omitempty"` // cents
	Used         json.RawMessage `json:"used,omitempty"`         // cents
	// OnDemandCap, OnDemandUsed, and PrepaidBalance carry dollar amounts in the
	// same accepted numeric forms. A successfully parsed value establishes
	// weekly credit evidence; these amounts retain dollar units during display conversion.
	OnDemandCap    json.RawMessage `json:"onDemandCap,omitempty"`    // USD
	OnDemandUsed   json.RawMessage `json:"onDemandUsed,omitempty"`   // USD
	PrepaidBalance json.RawMessage `json:"prepaidBalance,omitempty"` // USD
	// IsUnifiedBillingUser records source membership, including its zero value.
	// Weekly merging accepts true and retains the previous membership when
	// this source reports false.
	IsUnifiedBillingUser bool `json:"isUnifiedBillingUser,omitempty"` // reported unified billing membership
	// TopUpMethod is trimmed during summary construction. Weekly merging replaces
	// retained method text only when the new summary supplies a nonblank value.
	TopUpMethod string `json:"topUpMethod,omitempty"` // reported top-up method text
	// BillingPeriodStart and BillingPeriodEnd describe monthly billing dates.
	// BuildBillingSummary copies them separately when monthly evidence exists,
	// preserving the distinction from the primary weekly period's Start and End.
	BillingPeriodStart string `json:"billingPeriodStart,omitempty"` // monthly billing start text
	BillingPeriodEnd   string `json:"billingPeriodEnd,omitempty"`   // monthly billing end text
}

// BillingPayload wraps the optional configuration returned by the billing
// endpoint.
type BillingPayload struct {
	// Config can be absent in successfully decoded JSON. BuildBillingSummary
	// returns nil for missing configuration, so callers do not infer a usage
	// window.
	Config *BillingConfig `json:"config,omitempty"` // optional upstream billing configuration
}

// BillingProductSummary aliases the product display value shared with
// provider usage consumers.
type BillingProductSummary = usageview.BillingProductSummary

// BillingSummary aliases the billing display value shared with provider usage
// consumers.
type BillingSummary = usageview.BillingSummary

// BuildBillingURL appends the weekly credits or monthly billing path to
// DefaultCLIBaseURL. The boolean selects the endpoint representation; this
// default-only helper does not consult runtime or saved provider base URLs.
func BuildBillingURL(formatCredits bool) string {
	base := strings.TrimRight(DefaultCLIBaseURL, "/")
	if formatCredits {
		return base + BillingWeeklyPath
	}
	return base + BillingMonthlyPath
}

// BuildBillingURLWithValidator appends the selected billing path after
// validating and normalizing baseURL. A nil validator uses platform base
// policy; callers pass their provider policy for custom endpoints and receive
// validation failures before a URL is built.
func BuildBillingURLWithValidator(baseURL string, formatCredits bool, validator BaseURLValidator) (string, error) {
	validatedBaseURL, err := validatedBaseURLWithValidator(baseURL, validator)
	if err != nil {
		return "", fmt.Errorf("invalid base url: %w", err)
	}
	if formatCredits {
		return validatedBaseURL + BillingWeeklyPath, nil
	}
	return validatedBaseURL + BillingMonthlyPath, nil
}

// ApplyCLIBillingHeaders sets JSON headers and the pinned billing CLI
// identity. A nonblank trimmed access token replaces Authorization; a blank
// token leaves any existing value. Nil requests are ignored and other
// requests need an allocated Header map.
func ApplyCLIBillingHeaders(req *http.Request, accessToken string) {
	if req == nil {
		return
	}
	token := strings.TrimSpace(accessToken)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(CLITokenAuthHeader, CLITokenAuthValue)
	req.Header.Set(CLIClientVersionHeader, CLIClientVersion)
	req.Header.Set("User-Agent", billingCLIUserAgent)
}

// ParseBillingPayload decodes the billing response into its optional config.
// Empty bytes or malformed JSON return an error; valid JSON with no config
// returns a payload whose Config remains nil for summary construction.
func ParseBillingPayload(body []byte) (*BillingPayload, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty billing body")
	}
	var payload BillingPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

// BuildBillingSummary derives a display summary from optional weekly and
// monthly billing evidence. Nil configuration or no recognized billing
// evidence returns nil. Parsed numeric pointers are copied, and product
// entries with blank names are skipped. Monthly UsedCents remains the
// reported total, while IncludedUsedCents is capped at a positive monthly
// limit before deriving its usage percentage.
//
// Weekly evidence selects CurrentPeriod as the primary date/type source and
// the reported credit percentage; otherwise monthly billing dates become
// primary. Unknown primary type becomes weekly when weekly evidence exists,
// while a recognized CurrentPeriod label is retained. Monthly billing dates
// remain separate from those primary dates. Credit balances retain reported
// dollars, and monthly cent amounts receive dollar display fields. A
// recognized monthly limit supplies the display plan.
func BuildBillingSummary(config *BillingConfig) *BillingSummary {
	if config == nil {
		return nil
	}
	summary := &BillingSummary{}
	period := config.CurrentPeriod
	periodType := resolvePeriodType(period)
	creditUsage := cloneFloat(config.CreditUsagePercent)

	// A weekly reset belongs to CurrentPeriod even when its dates are missing.
	// Copying monthly billing dates here would give consumers the wrong window endpoint.
	periodStart := ""
	periodEnd := ""
	if period != nil {
		periodStart = strings.TrimSpace(period.Start)
		periodEnd = strings.TrimSpace(period.End)
	}

	products := make([]BillingProductSummary, 0, len(config.ProductUsage))
	for _, item := range config.ProductUsage {
		product := strings.TrimSpace(item.Product)
		if product == "" {
			continue
		}
		products = append(products, BillingProductSummary{
			Product:      product,
			UsagePercent: cloneFloat(item.UsagePercent),
		})
	}

	monthlyLimit := parseCentValue(config.MonthlyLimit)
	used := parseCentValue(config.Used)
	// Credit balances share the numeric parser with monthly cents but retain dollar
	// units, so only monthly limit and usage receive the later division by one hundred.
	prepaid := parseCentValue(config.PrepaidBalance)
	onDemandCap := parseCentValue(config.OnDemandCap)
	onDemandUsed := parseCentValue(config.OnDemandUsed)
	billingStart := strings.TrimSpace(config.BillingPeriodStart)
	billingEnd := strings.TrimSpace(config.BillingPeriodEnd)

	// Keep reported total usage separately while capping the included-credit
	// amount, so display consumers can distinguish the quantities when total
	// spending exceeds the included limit.
	var includedUsed *float64
	if used != nil {
		if monthlyLimit != nil && *monthlyLimit > 0 {
			v := math.Min(*used, *monthlyLimit)
			includedUsed = &v
		} else {
			includedUsed = cloneFloat(used)
		}
	}

	var usedPercent *float64
	// Monthly included-credit progress uses percent, keeping the weekly credit
	// UsagePercent independent even when both windows appear in the same summary.
	if monthlyLimit != nil && *monthlyLimit > 0 && includedUsed != nil {
		v := (*includedUsed / *monthlyLimit) * 100
		usedPercent = &v
	}

	hasWeekly := creditUsage != nil || periodType == "weekly" || len(products) > 0 || prepaid != nil || onDemandCap != nil || onDemandUsed != nil
	hasMonthly := monthlyLimit != nil || used != nil || (!hasWeekly && billingEnd != "")
	if !hasWeekly && !hasMonthly {
		return nil
	}

	if hasWeekly {
		if periodType == "unknown" {
			periodType = "weekly"
		}
		summary.PeriodType = periodType
		summary.UsagePercent = creditUsage
		summary.PeriodStart = periodStart
		summary.PeriodEnd = periodEnd
	} else {
		// A monthly-only summary exposes included-credit progress through UsedPercent;
		// leaving UsagePercent absent keeps consumers from displaying it as weekly credit usage.
		summary.PeriodType = "monthly"
		summary.PeriodStart = billingStart
		summary.PeriodEnd = billingEnd
	}
	summary.ProductUsage = products
	summary.MonthlyLimitCents = monthlyLimit
	summary.UsedCents = used
	summary.IncludedUsedCents = includedUsed
	if hasMonthly {
		summary.BillingPeriodStart = billingStart
		summary.BillingPeriodEnd = billingEnd
	}
	summary.UsedPercent = usedPercent
	summary.PrepaidBalance = prepaid
	if onDemandCap != nil {
		summary.OnDemandCap = onDemandCap
	}
	if onDemandUsed != nil {
		summary.OnDemandUsed = onDemandUsed
	}
	// Display monthly absolute amounts in dollars while retaining the cent-valued
	// fields used for plan recognition and included-credit percentage calculation.
	if monthlyLimit != nil {
		v := *monthlyLimit / 100
		summary.MonthlyLimit = &v
	}
	if used != nil {
		v := *used / 100
		summary.MonthlyUsed = &v
	}
	summary.TopUpMethod = strings.TrimSpace(config.TopUpMethod)
	summary.IsUnifiedBillingUser = config.IsUnifiedBillingUser
	summary.Plan = resolvePlan(monthlyLimit)
	return summary
}

// MergeBillingProbeResult combines weekly and monthly outcomes with a
// previous summary. Successful nonnil windows replace their own fields and
// advance their UTC timestamps; failed windows retain previous data. Missing
// previous window timestamps can use prior UpdatedAt, then FetchedAt, when
// recognized window fields exist. Successful flags with nil summaries affect
// Partial and FailedWindows but install no values. Both failed probes with no
// previous summary return nil.
//
// The merge shallow-copies the previous struct and assigns pointer and slice
// fields from supplied summaries. Treat retained fields as immutable unless
// copied separately. Weekly credit amounts update when present; monthly on-
// demand values fill empty fields. FailedWindows is rebuilt for this attempt
// so its slice does not overwrite the previous summary's failure list.
func MergeBillingProbeResult(previous, weekly, monthly *BillingSummary, weeklyOK, monthlyOK bool) *BillingSummary {
	var out BillingSummary
	if previous != nil {
		out = *previous
		// A supplied summary can lack per-window timestamps. Reuse its retained
		// overall observation time only for recognized window fields, keeping
		// missing evidence from gaining a timestamp.
		previousUpdatedAt := previous.UpdatedAt
		if previousUpdatedAt == "" {
			previousUpdatedAt = previous.FetchedAt
		}
		if out.WeeklyUpdatedAt == "" && (out.UsagePercent != nil || len(out.ProductUsage) > 0) {
			out.WeeklyUpdatedAt = previousUpdatedAt
		}
		if out.MonthlyUpdatedAt == "" && (out.MonthlyLimitCents != nil || out.UsedPercent != nil) {
			out.MonthlyUpdatedAt = previousUpdatedAt
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)

	if weeklyOK && weekly != nil {
		out.PeriodType = weekly.PeriodType
		out.UsagePercent = weekly.UsagePercent
		out.PeriodStart = weekly.PeriodStart
		out.PeriodEnd = weekly.PeriodEnd
		out.ProductUsage = weekly.ProductUsage
		// Present weekly credit amounts replace retained values independently, allowing
		// missing balance fields in an otherwise successful response to keep earlier evidence.
		if weekly.PrepaidBalance != nil {
			out.PrepaidBalance = weekly.PrepaidBalance
		}
		if weekly.OnDemandCap != nil {
			out.OnDemandCap = weekly.OnDemandCap
		}
		if weekly.OnDemandUsed != nil {
			out.OnDemandUsed = weekly.OnDemandUsed
		}
		if weekly.TopUpMethod != "" {
			out.TopUpMethod = weekly.TopUpMethod
		}
		if weekly.IsUnifiedBillingUser {
			out.IsUnifiedBillingUser = true
		}
		out.WeeklyUpdatedAt = now
	}
	if monthlyOK && monthly != nil {
		if out.PeriodType == "" {
			out.PeriodType = "monthly"
		}
		out.MonthlyLimitCents = monthly.MonthlyLimitCents
		out.UsedCents = monthly.UsedCents
		out.IncludedUsedCents = monthly.IncludedUsedCents
		out.BillingPeriodStart = monthly.BillingPeriodStart
		out.BillingPeriodEnd = monthly.BillingPeriodEnd
		out.UsedPercent = monthly.UsedPercent
		out.MonthlyLimit = monthly.MonthlyLimit
		out.MonthlyUsed = monthly.MonthlyUsed
		// Monthly on-demand amounts fill empty output fields, allowing weekly or
		// previously retained credit values to keep precedence when both sources expose them.
		if monthly.OnDemandCap != nil && out.OnDemandCap == nil {
			out.OnDemandCap = monthly.OnDemandCap
		}
		if monthly.OnDemandUsed != nil && out.OnDemandUsed == nil {
			out.OnDemandUsed = monthly.OnDemandUsed
		}
		out.Plan = monthly.Plan
		out.MonthlyUpdatedAt = now
	}

	out.Partial = !weeklyOK || !monthlyOK
	out.FailedWindows = nil
	if !weeklyOK {
		out.FailedWindows = append(out.FailedWindows, "weekly")
	}
	if !monthlyOK {
		out.FailedWindows = append(out.FailedWindows, "monthly")
	}
	if !weeklyOK && !monthlyOK && previous == nil {
		return nil
	}
	return &out
}

// StampBillingSummary mutates a summary's status, source, FetchedAt, and
// UpdatedAt using the current UTC RFC3339 time. Nil input returns nil;
// callers serialize mutation of shared summaries and retain independently
// tracked window timestamps.
func StampBillingSummary(summary *BillingSummary, statusCode int, source string) *BillingSummary {
	if summary == nil {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	summary.StatusCode = statusCode
	summary.Source = source
	summary.FetchedAt = now
	summary.UpdatedAt = now
	return summary
}

// resolvePeriodType returns weekly, monthly, or unknown from the upstream
// period label. Nil or unmatched labels are unknown, and the lowercased
// weekly substring is checked before monthly when a label contains both.
func resolvePeriodType(period *BillingPeriod) string {
	if period == nil {
		return "unknown"
	}
	raw := strings.ToLower(strings.TrimSpace(period.Type))
	if strings.Contains(raw, "weekly") {
		return "weekly"
	}
	if strings.Contains(raw, "monthly") {
		return "monthly"
	}
	return "unknown"
}

// resolvePlan matches a rounded monthly cent limit to the configured plan
// amounts. Missing or unmatched limits return empty; rounding allows nearby
// floating-point representations to match the local display-plan thresholds.
func resolvePlan(monthlyLimitCents *float64) string {
	if monthlyLimitCents == nil {
		return ""
	}
	// Rounded cents allow nearby numeric representations to match plan thresholds.
	limit := math.Round(*monthlyLimitCents)
	switch limit {
	case SuperGrokLimitCents:
		return "SuperGrok"
	case SuperGrokHeavyLimitCents:
		return "SuperGrok Heavy"
	default:
		return ""
	}
}

// parseCentValue decodes a wrapped val, bare number, or numeric string into a
// numeric pointer. Empty, null, malformed, or unsupported input returns nil.
// Despite its name, callers also use it for dollar-valued credit fields.
func parseCentValue(raw json.RawMessage) *float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	// Billing values can wrap the same numeric payload under val. Decode that
	// representation first, then let bare values use the shared conversion path.
	var obj struct {
		Val any `json:"val"` // wrapped numeric billing value
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Val != nil {
		return anyToFloat(obj.Val)
	}
	// Without a successfully decoded, nonnil wrapped val, try the bare JSON
	// form so numeric and string payloads use the shared amount conversion.
	var n any
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil
	}
	return anyToFloat(n)
}

// anyToFloat converts supported numeric Go values or trimmed numeric strings
// to a float64 pointer. Blank, unparseable, or unsupported values return nil;
// negative and nonfinite values are not rejected by this helper.
func anyToFloat(v any) *float64 {
	// Conversion failure is absent billing evidence for this value. Summary
	// construction can retain other valid amounts or windows instead of rejecting the whole configuration.
	switch n := v.(type) {
	case float64:
		return &n
	case float32:
		f := float64(n)
		return &f
	case int:
		f := float64(n)
		return &f
	case int64:
		f := float64(n)
		return &f
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return nil
		}
		return &f
	case string:
		s := strings.TrimSpace(n)
		if s == "" {
			return nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil
		}
		return &f
	default:
		return nil
	}
}

// cloneFloat copies a present float into independently allocated pointer
// storage. Nil input remains nil, allowing billing consumers to distinguish
// missing observations from reported zero amounts.
func cloneFloat(v *float64) *float64 {
	if v == nil {
		return nil
	}
	f := *v
	return &f
}
