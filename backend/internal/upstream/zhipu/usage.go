package zhipu

import (
	"context"
	"net/url"
	"sort"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/upstream/internal/usageclient"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usagecontract"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

const (
	CnZhipuWindowUnknown CnZhipuWindow = iota
	CnZhipuWindow5h
	CnZhipuWindowWeekly

	// DefaultTestModel is the default Zhipu probe model.
	DefaultTestModel = "glm-4.7"
)

// CnZhipuWindow identifies the window of a Zhipu TOKENS_LIMIT entry.
type CnZhipuWindow int

type ZhipuCodingUsageAdapter struct{}

// ClassifyZhipuWindowUnit maps unit 3 to five hours and unit 6 to seven days.
// Unknown or missing units return Unknown for reset-based classification by the caller.
func ClassifyZhipuWindowUnit(unit int64) CnZhipuWindow {
	switch unit {
	case 3:
		return CnZhipuWindow5h
	case 6:
		return CnZhipuWindowWeekly
	default:
		return CnZhipuWindowUnknown
	}
}

// ParseZhipuTokenTiers maps data.limits to five-hour and weekly windows.
//
// Classification follows cc-switch parse_zhipu_token_tiers (issue #3036):
//  1. Use unit 3 for five hours and unit 6 for weekly. Near a period end, the
//     weekly reset can precede the five-hour reset, so reset order alone is insufficient.
//  2. For unclassified entries, fill vacant slots with missing resets first,
//     then numeric resets in ascending order. A zero-use five-hour entry may lack a reset.
//
// CREDIT_LIMIT and TOKENS_LIMIT measure different allowances. When token entries
// exist, exclude credit entries so credit percentages cannot affect token-window
// scheduling thresholds. Use credit entries for display only when token entries are absent.
// Older plans may return a single five-hour entry; newer plans can return both windows.
func ParseZhipuTokenTiers(data gjson.Result) []usageview.CNQuotaTier {
	type entry struct {
		resetMs    int64
		hasReset   bool
		percentage float64
		resetISO   string
	}
	var (
		fiveHour     entry
		fiveHourSet  bool
		weekly       entry
		weeklySet    bool
		unclassified []entry
	)

	classify := func(item gjson.Result, e entry) {
		switch ClassifyZhipuWindowUnit(item.Get("unit").Int()) {
		case CnZhipuWindow5h:
			if !fiveHourSet {
				fiveHour, fiveHourSet = e, true
			} else {
				unclassified = append(unclassified, e)
			}
		case CnZhipuWindowWeekly:
			if !weeklySet {
				weekly, weeklySet = e, true
			} else {
				unclassified = append(unclassified, e)
			}
		default:
			unclassified = append(unclassified, e)
		}
	}
	var creditFallback []entry
	hasTokensLimit := false

	data.Get("limits").ForEach(func(_, item gjson.Result) bool {
		limitType := strings.ToUpper(strings.TrimSpace(item.Get("type").String()))
		if limitType != "TOKENS_LIMIT" && limitType != "CREDIT_LIMIT" {
			return true
		}
		percentage := 0.0
		if p, ok := usageclient.CnParseF64(item.Get("percentage").Value()); ok {
			percentage = p
		}
		var (
			resetMs  int64
			hasReset bool
			resetISO string
		)
		if nr := item.Get("nextResetTime"); nr.Exists() {
			switch nr.Type {
			case gjson.Number:
				resetMs = nr.Int()
				hasReset = resetMs > 0
				resetISO = usageclient.CnMillisToRFC3339(resetMs)
			case gjson.String:
				resetISO = usageclient.CnNormalizeResetTime(nr.String())
				hasReset = resetISO != ""
			}
		}
		e := entry{resetMs: resetMs, hasReset: hasReset, percentage: percentage, resetISO: resetISO}
		if limitType == "TOKENS_LIMIT" {
			hasTokensLimit = true
			classify(item, e)
		} else {
			creditFallback = append(creditFallback, e)
		}
		return true
	})

	// Some plans report credit allowances only; use those for display when token windows are absent.
	if !hasTokensLimit {
		unclassified = append(unclassified, creditFallback...)
	}

	// Fill vacant slots with missing resets first, then by numeric reset time.
	sort.SliceStable(unclassified, func(i, j int) bool {
		if unclassified[i].hasReset != unclassified[j].hasReset {
			return !unclassified[i].hasReset
		}
		return unclassified[i].resetMs < unclassified[j].resetMs
	})
	for _, e := range unclassified {
		switch {
		case !fiveHourSet:
			fiveHour, fiveHourSet = e, true
		case !weeklySet:
			weekly, weeklySet = e, true
		}
	}

	var tiers []usageview.CNQuotaTier
	if fiveHourSet {
		tiers = append(tiers, usageview.CNQuotaTier{Window: "5h", UsedPercent: fiveHour.percentage, ResetAt: fiveHour.resetISO})
	}
	if weeklySet {
		tiers = append(tiers, usageview.CNQuotaTier{Window: "7d", UsedPercent: weekly.percentage, ResetAt: weekly.resetISO})
	}
	return tiers
}

func (*ZhipuCodingUsageAdapter) Name() string { return usageview.UpstreamUsageAdapterZhipuCoding }
func (*ZhipuCodingUsageAdapter) Query(ctx context.Context, input *usagecontract.Request) (*usageview.UpstreamUsageInfo, error) {
	client := usageclient.New(input)
	endpoint, err := usageclient.CnUsageEndpoint(client.BaseURL, "/api/monitor/usage/quota/limit", false)
	if err != nil {
		return nil, usageview.ErrUpstreamUsageConfigInvalid.WithCause(err)
	}
	// The Coding Plan endpoint expects the raw API key in Authorization.
	teamHeaders := map[string]string{"Authorization": client.APIKey}
	organization := strings.TrimSpace(client.ZhipuOrganization)
	if organization != "" {
		if strings.ContainsAny(organization, "\r\n") {
			return nil, usageview.ErrUpstreamUsageConfigInvalid
		}
		// Team requests use type=2 and an organization header.
		parsed, parseErr := url.Parse(endpoint)
		if parseErr != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, usageview.ErrUpstreamUsageConfigInvalid
		}
		query := parsed.Query()
		query.Set("type", "2")
		parsed.RawQuery = query.Encode()
		endpoint = parsed.String()
		teamHeaders["bigmodel-organization"] = organization
		if project := strings.TrimSpace(client.ZhipuProject); project != "" {
			if strings.ContainsAny(project, "\r\n") {
				return nil, usageview.ErrUpstreamUsageConfigInvalid
			}
			teamHeaders["bigmodel-project"] = project
		}
	}
	body, status, err := client.GetURLWithHeaders(ctx, endpoint, teamHeaders)
	if err != nil {
		return nil, err
	}
	if err := usageclient.ValidateCNUsageStatus(status); err != nil {
		return nil, err
	}
	if success := gjson.GetBytes(body, "success"); success.Exists() && !success.Bool() {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	tiers := ParseZhipuTokenTiers(gjson.GetBytes(body, "data"))
	return usageclient.CnUsageLimits("zhipu", tiers)
}
