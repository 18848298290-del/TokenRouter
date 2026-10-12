package kimi

import (
	"context"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/upstream/internal/usageclient"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usagecontract"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// DefaultTestModel is the model used for Kimi provider probes.
const DefaultTestModel = "kimi-k2.5"

type KimiCodingUsageAdapter struct{}

type KimiBalanceUsageAdapter struct{}

// ParseKimiUsageTiers parses the Kimi For Coding /usages response.
//
//   - The first limits[].detail supplies the five-hour window.
//   - usage supplies the weekly window.
//
// Utilization is max(limit-remaining, 0)/limit*100, or zero for a nonpositive limit.
func ParseKimiUsageTiers(body []byte) []usageview.CNQuotaTier {
	var tiers []usageview.CNQuotaTier

	if limits := gjson.GetBytes(body, "limits"); limits.IsArray() {
		limits.ForEach(func(_, item gjson.Result) bool {
			detail := item.Get("detail")
			if !detail.Exists() {
				return true
			}
			limit, _ := usageclient.CnParseF64(detail.Get("limit").Value())
			remaining, _ := usageclient.CnParseF64(detail.Get("remaining").Value())
			used := limit - remaining
			if used < 0 {
				used = 0
			}
			var util float64
			if limit > 0 {
				util = used / limit * 100
			}
			tiers = append(tiers, usageview.CNQuotaTier{
				Window:      "5h",
				UsedPercent: util,
				ResetAt:     usageclient.CnNormalizeResetTime(detail.Get("resetTime").Value()),
			})
			return false // The first detail supplies the five-hour window.
		})
	}

	if usage := gjson.GetBytes(body, "usage"); usage.Exists() {
		limit, _ := usageclient.CnParseF64(usage.Get("limit").Value())
		remaining, _ := usageclient.CnParseF64(usage.Get("remaining").Value())
		used := limit - remaining
		if used < 0 {
			used = 0
		}
		var util float64
		if limit > 0 {
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

func (*KimiCodingUsageAdapter) Name() string { return usageview.UpstreamUsageAdapterKimiCoding }
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

func (*KimiBalanceUsageAdapter) Name() string { return usageview.UpstreamUsageAdapterKimiBalance }
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
