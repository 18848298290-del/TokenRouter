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

const (
	// CLITokenAuthHeader carries the CLI identity required by cli-chat-proxy billing.
	CLITokenAuthHeader     = "x-xai-token-auth"
	CLITokenAuthValue      = "xai-grok-cli"
	CLIClientVersionHeader = "x-grok-client-version"
	// CLIClientVersion is the shared version used to construct Grok CLI request identities.
	// Update it with the supported release at https://x.ai/cli/stable.
	CLIClientVersion = "0.2.120"
	// billingCLIUserAgent uses the pager/shell identity for billing probes.
	// CLIUserAgent in cli_identity.go uses the workspace identity.
	billingCLIUserAgent = "grok-pager/" + CLIClientVersion + " grok-shell/" + CLIClientVersion + " (macos; aarch64)"

	BillingWeeklyPath  = "/billing?format=credits"
	BillingMonthlyPath = "/billing"

	SuperGrokLimitCents      = 15_000  // $150.00
	SuperGrokHeavyLimitCents = 150_000 // $1,500.00
)

// BillingPeriod describes a weekly or monthly usage window.
type BillingPeriod struct {
	Type  string `json:"type,omitempty"`
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
}

// BillingProductUsage records one product in a weekly credits window.
type BillingProductUsage struct {
	Product      string   `json:"product,omitempty"`
	UsagePercent *float64 `json:"usagePercent,omitempty"`
}

// BillingConfig is the nested config object from /v1/billing.
type BillingConfig struct {
	CurrentPeriod        *BillingPeriod        `json:"currentPeriod,omitempty"`
	CreditUsagePercent   *float64              `json:"creditUsagePercent,omitempty"`
	ProductUsage         []BillingProductUsage `json:"productUsage,omitempty"`
	MonthlyLimit         json.RawMessage       `json:"monthlyLimit,omitempty"`
	Used                 json.RawMessage       `json:"used,omitempty"`
	OnDemandCap          json.RawMessage       `json:"onDemandCap,omitempty"`
	OnDemandUsed         json.RawMessage       `json:"onDemandUsed,omitempty"`
	PrepaidBalance       json.RawMessage       `json:"prepaidBalance,omitempty"`
	IsUnifiedBillingUser bool                  `json:"isUnifiedBillingUser,omitempty"`
	TopUpMethod          string                `json:"topUpMethod,omitempty"`
	BillingPeriodStart   string                `json:"billingPeriodStart,omitempty"`
	BillingPeriodEnd     string                `json:"billingPeriodEnd,omitempty"`
}

// BillingPayload is the top-level /v1/billing response.
type BillingPayload struct {
	Config *BillingConfig `json:"config,omitempty"`
}

// BillingProductSummary is the shared display value for product usage.
type BillingProductSummary = usageview.BillingProductSummary

// BillingSummary is the shared display value for parsed billing data.
type BillingSummary = usageview.BillingSummary

// BuildBillingURL builds the CLI chat proxy weekly or monthly billing URL.
func BuildBillingURL(formatCredits bool) string {
	base := strings.TrimRight(DefaultCLIBaseURL, "/")
	if formatCredits {
		return base + BillingWeeklyPath
	}
	return base + BillingMonthlyPath
}

// BuildBillingURLWithValidator validates the supplied base URL before adding the billing path.
// Callers supply the forwarding base URL so custom upstreams also receive quota probes.
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

// ApplyCLIBillingHeaders sets bearer authentication and CLI identity headers for billing requests.
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

// ParseBillingPayload decodes a billing API response.
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

// BuildBillingSummary normalizes billing configuration for display.
func BuildBillingSummary(config *BillingConfig) *BillingSummary {
	if config == nil {
		return nil
	}
	summary := &BillingSummary{}
	period := config.CurrentPeriod
	periodType := resolvePeriodType(period)
	creditUsage := cloneFloat(config.CreditUsagePercent)

	// Keep weekly dates separate: substituting a monthly end date could block an exhausted weekly quota until month end.
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
	// Credits amounts are in dollars; monthly limit and used values are in cents.
	prepaid := parseCentValue(config.PrepaidBalance)
	onDemandCap := parseCentValue(config.OnDemandCap)
	onDemandUsed := parseCentValue(config.OnDemandUsed)
	billingStart := strings.TrimSpace(config.BillingPeriodStart)
	billingEnd := strings.TrimSpace(config.BillingPeriodEnd)

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
		// UsagePercent belongs to the weekly progress bar; monthly data uses UsedPercent.
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
	// Convert monthly cents to dollars for the displayed absolute amounts.
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

// MergeBillingProbeResult updates successful probe windows and retains previous values for failed windows.
func MergeBillingProbeResult(previous, weekly, monthly *BillingSummary, weeklyOK, monthlyOK bool) *BillingSummary {
	var out BillingSummary
	if previous != nil {
		out = *previous
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
		// The weekly credits response supplies prepaid and on-demand dollar amounts.
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
		// Use monthly on-demand values when no value is already available.
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

// StampBillingSummary sets fetch status, source, and timestamps in place.
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

func resolvePlan(monthlyLimitCents *float64) string {
	if monthlyLimitCents == nil {
		return ""
	}
	// Round away small floating-point errors before matching plan limits.
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

func parseCentValue(raw json.RawMessage) *float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	// Accept the wrapped form: {"val": 123}.
	var obj struct {
		Val any `json:"val"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Val != nil {
		return anyToFloat(obj.Val)
	}
	// Also accept a bare number or numeric string.
	var n any
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil
	}
	return anyToFloat(n)
}

func anyToFloat(v any) *float64 {
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

func cloneFloat(v *float64) *float64 {
	if v == nil {
		return nil
	}
	f := *v
	return &f
}
