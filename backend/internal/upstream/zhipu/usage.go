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

// Zhipu token windows carry unit identifiers that can establish five-hour and
// weekly identity independently of reset chronology. The parser reserves those
// identified slots first, then uses remaining entries to fill vacant slots.
// Entries marked without reset evidence precede those marked with it, and
// the integer reset value orders each group. Positive numeric resets count
// as present before shared normalization checks the formatted date; normalized
// string resets retain their text and leave the numeric sort field at zero.
//
// Token and credit allowances feed different source lists. Credit entries use
// reset-based candidate ordering only when the response has no token entry,
// even when some identified token slots would otherwise remain vacant. The
// query adds a raw API key and optional team identity to its fixed management
// endpoint, then
// passes the selected observations through shared percentage-limit
// normalization for display and monitoring.

// Window tags reserve known coding slots before reset-based classification; the
// probe model separately selects the model used by provider connectivity
// checks.
const (
	// CnZhipuWindowUnknown: classify from remaining reset evidence.
	CnZhipuWindowUnknown CnZhipuWindow = iota
	CnZhipuWindow5h                    // five-hour usage observation slot
	CnZhipuWindowWeekly                // weekly usage observation slot

	// DefaultTestModel selects the model used by Zhipu connection probes,
	// independently of coding quota-window queries.
	DefaultTestModel = "glm-4.7"
)

// CnZhipuWindow identifies the observation slot selected by a coding quota
// unit.
type CnZhipuWindow int

// ZhipuCodingUsageAdapter reads coding quota observations through the
// configured usage-query transport.
type ZhipuCodingUsageAdapter struct{}

// ClassifyZhipuWindowUnit maps unit 3 to the five-hour slot and unit 6 to the
// weekly slot. Other units return CnZhipuWindowUnknown, leaving
// ParseZhipuTokenTiers to classify a remaining observation using reset
// metadata.
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

// ParseZhipuTokenTiers returns up to two percentage observations from
// TOKENS_LIMIT entries, or CREDIT_LIMIT entries when token entries are absent.
// Token unit 3 reserves the five-hour slot and unit 6 reserves the weekly slot;
// later entries for an occupied unit join the unclassified candidates. Credit
// entries use reset ordering regardless of their unit fields.
//
// Vacant slots are filled five-hour first after a stable ordering of
// candidates: missing resets precede present resets, then gjson integer reset
// values sort ascending. Parsed string resets retain a zero numeric sort value,
// so the ordering is a heuristic rather than a comparison of every normalized
// date. Explicit unit identities therefore survive a weekly reset preceding a
// short window's reset.
//
// Missing or unparseable percentage values become zero. The returned values and
// optional reset text are passed to CnUsageLimits by Query, which rejects
// negative or nonfinite percentages, retains values above one hundred, and
// omits unparseable reset text.
func ParseZhipuTokenTiers(data gjson.Result) []usageview.CNQuotaTier {
	// entry keeps one allowance's percentage and separate
	// reset-classification evidence.
	type entry struct {
		// resetMs and resetISO retain integer ordering evidence and
		// formatted text. Positive resetMs values and nonempty string
		// resets set hasReset before CnUsageLimits checks the date text.
		// String parsing leaves resetMs zero, so its displayed reset and
		// its ordering value remain separate observations.
		resetMs    int64   // sortable numeric reset value
		hasReset   bool    // parser reset presence marker
		percentage float64 // selected used percentage
		resetISO   string  // normalized reset time text
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
		// An absent or unreadable percentage contributes zero here.
		// CnUsageLimits later validates retained numeric observations,
		// while this parser retains the candidate's window metadata.
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
				// Normalize numeric epoch text for the usage
				// result while retaining its integer reset value
				// as the reset heuristic's ordering key.
				resetISO = usageclient.CnMillisToRFC3339(resetMs)
			case gjson.String:
				// A string timestamp can establish reset
				// presence and display text even though it
				// contributes no parsed epoch to numeric
				// candidate ordering.
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

	// Credit-only responses still supply percentage observations to the
	// shared normalizer. Keep them out of slot selection when any token
	// entry has established that token allowances are present.
	if !hasTokensLimit {
		unclassified = append(unclassified, creditFallback...)
	}

	// Reserve identified unit slots before considering these candidates.
	// Missing-reset entries fill vacant slots first, and equal numeric keys
	// retain response order through the stable sort.
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

// Name returns the registry identifier for Zhipu coding quota queries.
func (*ZhipuCodingUsageAdapter) Name() string { return usageview.UpstreamUsageAdapterZhipuCoding }

// Query reads the fixed coding quota endpoint with the raw API key and returns
// normalized percentage windows. It requires a stable request with usable
// callbacks; a nonempty organization selects type=2 and team headers, with an
// optional project included only for that team request.
func (*ZhipuCodingUsageAdapter) Query(ctx context.Context, input *usagecontract.Request) (*usageview.UpstreamUsageInfo, error) {
	client := usageclient.New(input)
	endpoint, err := usageclient.CnUsageEndpoint(client.BaseURL, "/api/monitor/usage/quota/limit", false)
	if err != nil {
		return nil, usageview.ErrUpstreamUsageConfigInvalid.WithCause(err)
	}
	// Install the coding query's raw-key header after provider overrides
	// through GetURLWithHeaders, so forwarding Bearer formatting cannot
	// select a different credential representation.
	teamHeaders := map[string]string{"Authorization": client.APIKey}
	organization := strings.TrimSpace(client.ZhipuOrganization)
	if organization != "" {
		// Reject line breaks before inserting organization identity
		// into HTTP headers, so the selected team value cannot
		// introduce another header line.
		if strings.ContainsAny(organization, "\r\n") {
			return nil, usageview.ErrUpstreamUsageConfigInvalid
		}
		// Couple team mode to the organization identity in both the URL
		// and headers. This keeps the selected quota scope aligned with
		// the supplied team credentials.
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
			// Validate the optional team project before it becomes
			// a header value, using the same line-break restriction
			// as the organization identity.
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
