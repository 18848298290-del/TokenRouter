package kimi

import (
	"context"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/upstream/internal/usageclient"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usagecontract"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// Kimi coding usage supplies a short window through the first limits entry with
// a detail field and a weekly window through usage. The parser keeps those
// windows in that order and derives each percentage from limit and remaining.
// It floors negative derived usage at zero and reports zero utilization for a
// nonpositive limit, while a reported deficit larger than the limit can exceed
// one hundred percent.
//
// The coding adapter converts these observations to shared percentage limits,
// whose consumer validates finite nonnegative usage. The balance adapter
// instead reads available_balance as a finite CNY amount, including wallet
// debt. Both queries use fixed paths on the supplied base and delegate bounded
// response handling to usageclient; optional reset text is normalized
// separately from the observed amounts.

// DefaultTestModel selects the Kimi model used for provider connection probes.
// Model probes run independently of coding and balance queries.
const DefaultTestModel = "kimi-k2.5"

// KimiCodingUsageAdapter reads coding-plan windows as normalized percentage
// limits for provider queries.
type KimiCodingUsageAdapter struct{}

// KimiBalanceUsageAdapter reads the reported CNY wallet balance for provider
// queries.
type KimiBalanceUsageAdapter struct{}

// ParseKimiUsageTiers derives a five-hour observation from the first limits
// entry whose detail field exists, then adds a weekly observation when usage
// exists. Numeric conversion success flags are ignored: unsupported fields
// become zero, while a parsed nonfinite result can enter the calculation.
// Callers validate the derived percentage observations separately.
//
// Derived usage is limit minus remaining, floored at zero. A positive limit
// converts it to percent, while a nonpositive limit reports zero utilization.
// The parser retains over-one-hundred percentages and optional normalized reset
// text, and later details cannot add duplicate five-hour windows.
func ParseKimiUsageTiers(body []byte) []usageview.CNQuotaTier {
	var tiers []usageview.CNQuotaTier

	if limits := gjson.GetBytes(body, "limits"); limits.IsArray() {
		limits.ForEach(func(_, item gjson.Result) bool {
			detail := item.Get("detail")
			if !detail.Exists() {
				return true
			}
			// Numeric conversion success flags are ignored.
			// Unsupported fields become zero, while returned
			// nonfinite values can reach the calculation; callers
			// validate the derived percentage separately.
			limit, _ := usageclient.CnParseF64(detail.Get("limit").Value())
			remaining, _ := usageclient.CnParseF64(detail.Get("remaining").Value())
			used := limit - remaining
			// A remaining amount above the limit would imply
			// negative usage. Floor that derived amount so the
			// percentage remains usable by shared quota consumers.
			if used < 0 {
				used = 0
			}
			var util float64
			if limit > 0 {
				// Convert the observed fraction to the shared
				// percentage unit.
				util = used / limit * 100
			}
			tiers = append(tiers, usageview.CNQuotaTier{
				Window:      "5h",
				UsedPercent: util,
				ResetAt:     usageclient.CnNormalizeResetTime(detail.Get("resetTime").Value()),
			})
			// Stop at the selected detail so subsequent limit
			// entries cannot add another five-hour window to the
			// normalized result.
			return false
		})
	}

	if usage := gjson.GetBytes(body, "usage"); usage.Exists() {
		// Weekly conversion also uses returned numbers without their
		// success flags. Keep window metadata while shared limit
		// normalization checks the derived percentage, including any
		// nonfinite calculation result.
		limit, _ := usageclient.CnParseF64(usage.Get("limit").Value())
		remaining, _ := usageclient.CnParseF64(usage.Get("remaining").Value())
		used := limit - remaining
		// Weekly remaining can exceed the limit too. Floor derived
		// usage so this observation uses the same nonnegative
		// percentage convention.
		if used < 0 {
			used = 0
		}
		var util float64
		if limit > 0 {
			// Express weekly usage in the shared percentage unit.
			util = used / limit * 100
		}
		tiers = append(tiers, usageview.CNQuotaTier{
			Window:      "7d",
			UsedPercent: util,
			ResetAt:     usageclient.CnNormalizeResetTime(usage.Get("resetTime").Value()),
		})
	}

	return tiers
}

// Name returns the registry identifier for Kimi coding-plan usage queries.
func (*KimiCodingUsageAdapter) Name() string { return usageview.UpstreamUsageAdapterKimiCoding }

// Query reads the fixed coding usage endpoint and returns normalized percentage
// windows. It requires a stable request with usable callbacks; endpoint
// construction retains a requested coding prefix, and unusable windows or
// unsuccessful HTTP status return shared usage errors.
func (*KimiCodingUsageAdapter) Query(ctx context.Context, input *usagecontract.Request) (*usageview.UpstreamUsageInfo, error) {
	client := usageclient.New(input)
	endpoint, err := usageclient.CnUsageEndpoint(client.BaseURL, "/v1/usages", true)
	if err != nil {
		return nil, usageview.ErrUpstreamUsageConfigInvalid.WithCause(err)
	}
	body, status, err := client.GetURL(ctx, endpoint, true)
	if err != nil {
		return nil, err
	}
	if err := usageclient.ValidateCNUsageStatus(status); err != nil {
		return nil, err
	}
	tiers := ParseKimiUsageTiers(body)
	return usageclient.CnUsageLimits("kimi", tiers)
}

// Name returns the registry identifier for Kimi CNY wallet-balance queries.
func (*KimiBalanceUsageAdapter) Name() string { return usageview.UpstreamUsageAdapterKimiBalance }

// Query reads the fixed balance endpoint and returns a finite CNY remainder,
// including negative balances. It requires a stable request with usable
// callbacks and rejects a nonzero gjson integer interpretation of code, or an
// absent, unparseable, or nonfinite available_balance.
func (*KimiBalanceUsageAdapter) Query(ctx context.Context, input *usagecontract.Request) (*usageview.UpstreamUsageInfo, error) {
	client := usageclient.New(input)
	endpoint, err := usageclient.CnUsageEndpoint(client.BaseURL, "/v1/users/me/balance", false)
	if err != nil {
		return nil, usageview.ErrUpstreamUsageConfigInvalid.WithCause(err)
	}
	body, status, err := client.GetURL(ctx, endpoint, true)
	if err != nil {
		return nil, err
	}
	if err := usageclient.ValidateCNUsageStatus(status); err != nil {
		return nil, err
	}
	if code := gjson.GetBytes(body, "code"); code.Exists() && code.Int() != 0 {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	value, ok := usageclient.CnParseF64(gjson.GetBytes(body, "data.available_balance").Value())
	if !ok || !usageview.ValidFiniteNumber(value) {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	return &usageview.UpstreamUsageInfo{
		Provider: "kimi",
		Mode:     "balance",
		Unit:     "CNY",
		Balance:  &usageview.UpstreamUsageAmount{Remaining: &value},
	}, nil
}
