package usageprovider

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream/internal/usageclient"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usagecontract"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// adapters.go queries and normalizes Sub2API, New API, and Zivv usage
// responses.
// Gateway usage responses can describe a wallet, a key quota, or subscription
// windows using superficially similar amounts. Sub2API normalization selects
// the reported mode and checks agreement between redundant quota values, while
// Zivv keeps wallet money and key allowance in separate result fields. Finite
// negative wallet remainders remain debt; unlimited sentinels become
// subscription flags rather than displayed amounts.
//
// New API reads display settings before token and wallet data so raw quota
// units can be converted consistently. Wallet discovery prefers embedded
// values, then a configured user-token query, then the fixed compatibility
// endpoint, with authentication and timeout outcomes limiting further attempts.
// A merged result needs matching units and retains key limits independently of
// its wallet. Window reset derivation and amount validation retain missing
// observations as distinct from reported zero values.

// The optional New API display probe uses a short budget, while raw quota
// conversion uses its default divisor when no positive finite site factor is
// available.
const (
	// StatusTimeout: optional display settings budget.
	StatusTimeout             = 2 * time.Second
	newAPIDefaultQuotaPerUnit = 500000.0 // raw quota units per USD
)

// Sub2APIUsageAdapter queries the fixed usage endpoint and normalizes its
// reported mode.
type Sub2APIUsageAdapter struct{}

// Sub2APIUsageResponse is the wire envelope for wallet, quota, and subscription
// observations.
type Sub2APIUsageResponse struct {
	Mode string `json:"mode"` // reported upstream usage mode
	// IsValid distinguishes a missing validity observation from an explicit
	// rejected key. ParseSub2APIUsage reports different errors for those
	// cases.
	IsValid *bool `json:"isValid"`
	// Status selects accepted quota states in quota_limited mode: active,
	// quota_exhausted, or expired. Unrestricted normalization does not
	// consume it.
	Status   string `json:"status"`
	PlanName string `json:"planName"` // reported subscription plan name
	// Unit labels the top-level amounts. Unrestricted responses require
	// USD, and a present Quota requires agreement with its own USD unit.
	Unit string `json:"unit"`
	// Remaining accompanies quota or unrestricted amounts. Limited quota
	// requires agreement with Quota.Remaining; unrestricted subscriptions
	// can use minus one as their unlimited representation.
	Remaining *float64 `json:"remaining"`
	// Balance selects the unrestricted wallet path when Subscription is
	// absent. Its finite amount must agree with Remaining, including a
	// negative wallet remainder.
	Balance *float64 `json:"balance"`
	// Quota selects aggregate key-quota normalization. When it is absent,
	// quota_limited responses need rolling limits and omit top-level
	// Remaining and Unit.
	Quota *Sub2APIQuota `json:"quota"`
	// RateLimits: reported rolling allowance windows
	RateLimits []Sub2APIRateLimit `json:"rate_limits"`
	// Subscription selects unrestricted plan handling when Balance is
	// absent, and can accompany quota-limited results. Normalized window
	// amounts can retain its numeric storage.
	Subscription *Sub2APISubscription `json:"subscription"`
	// ExpiresAt is optional for aggregate quota or wallet results. A
	// supplied zero instant rejects normalization, while accepted instants
	// return as UTC copies.
	ExpiresAt *time.Time `json:"expires_at"`
}

// Sub2APIQuota holds one key's reported quota amounts and allowance unit.
type Sub2APIQuota struct {
	// Limit, Used, and Remaining are required finite nonnegative
	// observations on one USD scale. Limit is positive and Remaining agrees
	// with floored Limit minus Used; normalization retains these numeric
	// pointers.
	Limit *float64 `json:"limit"` // reported total key allowance
	Used  *float64 `json:"used"`  // reported consumed key allowance
	// Remaining: reported remaining key allowance
	Remaining *float64 `json:"remaining"`
	Unit      string   `json:"unit"` // USD
}

// Sub2APIRateLimit holds a rolling allowance and its reported window metadata.
type Sub2APIRateLimit struct {
	// Window identifies 5h, 1d, or 7d after trimming.
	// NormalizeSub2APIRateLimits rejects duplicate trimmed names so one
	// period has one reported allowance.
	Window string `json:"window"`
	// Limit, Used, and Remaining form one rolling allowance. Limit is
	// positive, all values are finite and nonnegative, and Remaining agrees
	// with floored Limit minus Used; normalized windows retain these
	// pointers. Limit: reported rolling allowance total
	Limit *float64 `json:"limit"`
	Used  *float64 `json:"used"` // reported rolling allowance consumption
	// Remaining: reported rolling allowance remainder
	Remaining *float64 `json:"remaining"`
	// WindowStart retains timestamp JSON or explicit null. Absence is
	// invalid, while null records unavailable start metadata without
	// inventing an instant.
	WindowStart json.RawMessage `json:"window_start"`
	// ResetAt is an optional rolling-window reset. A supplied zero instant
	// rejects normalization, while accepted times become UTC; WindowStart
	// is validated separately and does not derive this reset.
	ResetAt *time.Time `json:"reset_at"`
}

// Sub2APISubscription holds USD usage windows and unlimited-plan evidence from
// the response.
type Sub2APISubscription struct {
	// DailyUsageUSD, WeeklyUsageUSD, and MonthlyUsageUSD are required
	// finite nonnegative observations when a subscription is supplied.
	// Included limited windows retain the corresponding usage pointers in
	// their normalized data. DailyUsageUSD: reported daily USD usage
	DailyUsageUSD *float64 `json:"daily_usage_usd"`
	// WeeklyUsageUSD: reported weekly USD usage
	WeeklyUsageUSD *float64 `json:"weekly_usage_usd"`
	// MonthlyUsageUSD: reported monthly USD usage
	MonthlyUsageUSD *float64 `json:"monthly_usage_usd"`
	// DailyLimitUSD, WeeklyLimitUSD, and MonthlyLimitUSD enable their
	// respective finite windows when positive. Missing or zero limits omit
	// those windows; negative or nonfinite supplied limits are rejected.
	// DailyLimitUSD: reported daily USD allowance
	DailyLimitUSD *float64 `json:"daily_limit_usd"`
	// WeeklyLimitUSD: reported weekly USD allowance
	WeeklyLimitUSD *float64 `json:"weekly_limit_usd"`
	// MonthlyLimitUSD: reported monthly USD allowance
	MonthlyLimitUSD *float64 `json:"monthly_limit_usd"`
	// DailyResetAt, WeeklyResetAt, and MonthlyResetAt take precedence over
	// their corresponding window starts for included allowance windows. A
	// zero time encountered on that path rejects normalization; omitted
	// windows skip reset validation.
	DailyResetAt *time.Time `json:"daily_reset_at"` // daily reset instant
	// WeeklyResetAt: reported weekly reset timestamp
	WeeklyResetAt *time.Time `json:"weekly_reset_at"`
	// MonthlyResetAt: reported monthly reset timestamp
	MonthlyResetAt *time.Time `json:"monthly_reset_at"`
	// DailyWindowStart, WeeklyWindowStart, and MonthlyWindowStart derive
	// resets only when their corresponding reset is absent. The normalizer
	// adds fixed one-day, seven-day, or thirty-day durations to nonzero
	// starts. A supplied zero start rejects an included window when its
	// explicit reset is absent. DailyWindowStart: reported daily window start
	DailyWindowStart *time.Time `json:"daily_window_start"`
	// WeeklyWindowStart: reported weekly window start
	WeeklyWindowStart *time.Time `json:"weekly_window_start"`
	// MonthlyWindowStart: reported monthly window start
	MonthlyWindowStart *time.Time `json:"monthly_window_start"`
	// Unlimited controls plan representation when supplied. A missing flag
	// lets the first legacy minus-one remainder infer unlimited status;
	// supplied false retains limited handling.
	Unlimited *bool `json:"unlimited"`
	// ExpiresAt is required and nonzero for a supplied subscription. The
	// normalizer returns its UTC copy for plan display alongside window
	// observations.
	ExpiresAt *time.Time `json:"expires_at"`
}

// ZivvUsageAdapter queries the fixed Zivv wallet endpoint and retains key quota
// separately.
type ZivvUsageAdapter struct{}

// ZivvUsageResponse holds wallet amounts, key allowance, and availability from
// the response.
type ZivvUsageResponse struct {
	// Balance is the finite wallet remainder, including negative debt. The
	// normalizer combines it with TotalUsed and requires a nonnegative
	// wallet total.
	Balance *float64 `json:"balance"`
	// Currency selects USD, CNY, or TOKENS after trimming and uppercasing.
	// Other spellings reject normalization before any amount is published.
	Currency string `json:"currency"`
	// IsAvailable is required to distinguish missing response data from an
	// explicit unavailable key; false becomes an authentication failure.
	IsAvailable *bool `json:"is_available"`
	// KeyLimit and KeyUsed are finite nonnegative key observations. Zero
	// KeyLimit becomes unlimited; positive limits derive a floored
	// remainder separate from the reported wallet amount.
	KeyLimit *float64 `json:"key_limit"` // reported total key allowance
	KeyUsed  *float64 `json:"key_used"`  // reported consumed key allowance
	// PlanName labels the key subscription after trimming. An empty name
	// selects Zivv, ensuring the returned subscription has the display
	// label required by shared validation.
	PlanName string `json:"plan_name"`
	// TotalUsed is required as finite nonnegative cumulative wallet usage.
	// Adding it to Balance must produce a finite nonnegative wallet total.
	TotalUsed *float64 `json:"total_used"`
}

// NewAPIUsageAdapter combines validated token allowance with a separately
// queried user wallet.
type NewAPIUsageAdapter struct{}

// NewAPITokenUsageResponse holds token quota and optional embedded wallet
// observations from JSON.
type NewAPITokenUsageResponse struct {
	// Code and Success distinguish an explicit rejection from malformed
	// token data: either supplied false rejects authentication, while
	// Code=true is required and Success can be absent.
	Code    *bool  `json:"code"`    // required token success marker
	Success *bool  `json:"success"` // optional response success marker
	Message string `json:"message"` // reported upstream diagnostic text
	// Data is required after successful flags. Its token counters and
	// expiry are validated before normalization, while embedded wallet
	// fields stay raw for separate source selection.
	Data *struct {
		Object string `json:"object"` // token_usage response kind
		// Name labels the normalized key subscription after trimming. A
		// blank value selects New API so shared subscription validation
		// receives a nonempty name.
		Name string `json:"name"`
		// TotalGranted, TotalUsed, and TotalAvailable are required
		// finite nonnegative counters for limited keys. Normalization
		// compares TotalGranted against the sum of the other two using
		// CloseEnough, whose infinite-operand limitation is documented
		// there. Unlimited keys can omit counters or supply finite
		// negative values, which normalization excludes. TotalGranted:
		// reported total raw quota
		TotalGranted *float64 `json:"total_granted"`
		// TotalUsed: reported consumed raw quota
		TotalUsed *float64 `json:"total_used"`
		// TotalAvailable: reported remaining raw quota
		TotalAvailable *float64 `json:"total_available"`
		// UnlimitedQuota is required to select the key's
		// representation. True omits numeric quota limits, while false
		// selects conversion of validated finite counters.
		UnlimitedQuota *bool `json:"unlimited_quota"`
		// ExpiresAt is required and at least minus one in parsed token
		// responses. Nonpositive values mean no expiry; positive
		// seconds or accepted milliseconds become a UTC instant.
		ExpiresAt *int64 `json:"expires_at"`
		// UserBalance and UserBalanceDisplay are alternate embedded
		// wallet sources. UserBalanceDisplay takes precedence and uses
		// display amounts; UserBalance uses raw quota conversion. A
		// malformed present preferred field ends selection.
		// UserBalance: raw embedded wallet quota
		UserBalance json.RawMessage `json:"user_balance"`
		// UserBalanceDisplay: embedded displayed wallet remainder
		UserBalanceDisplay json.RawMessage `json:"user_balance_display"`
		// Currency labels embedded display amounts when recognized.
		// Unrecognized text uses the site unit, which Query later
		// checks against the key's unit.
		Currency string `json:"currency"`
	} `json:"data"`
}

// NewAPIWalletBalanceInfo holds one currency's wallet fields in supported JSON
// number or string forms.
type NewAPIWalletBalanceInfo struct {
	// Currency determines whether listed amounts are displayed or raw
	// quota. Recognized units and an absent label mark display values;
	// other text uses the site's quota conversion.
	Currency string `json:"currency"`
	// TotalBalance, Balance, and Remaining are alternate remainder
	// spellings. Selection takes the first nonempty JSON value in that
	// order, and a malformed selected value rejects this wallet entry.
	// TotalBalance: preferred reported wallet remainder
	TotalBalance json.RawMessage `json:"total_balance"`
	// Balance: alternate reported wallet remainder
	Balance json.RawMessage `json:"balance"`
	// Remaining: alternate remaining wallet amount
	Remaining json.RawMessage `json:"remaining"`
	// Used and UsedBalance are optional wallet-usage spellings. UsedBalance
	// is preferred when present; a malformed preferred value rejects the
	// selected entry before shared amount validation. Used: alternate
	// reported wallet consumption
	Used json.RawMessage `json:"used"`
	// UsedBalance: preferred reported wallet consumption
	UsedBalance json.RawMessage `json:"used_balance"`
	Total       json.RawMessage `json:"total"` // optional reported wallet total
}

// NewAPIWalletData holds a nested wallet or user-self response, retaining raw
// numeric representations for normalization.
type NewAPIWalletData struct {
	// ID identifies the self-query wallet owner when an expected user ID is
	// configured. That path requires a present matching positive integral
	// value before consuming Quota.
	ID json.RawMessage `json:"id"`
	// Quota supplies the self-query remainder and the last generic nested
	// remainder source. Its presence also keeps unlabeled generic amounts
	// on the raw conversion path.
	Quota json.RawMessage `json:"quota"`
	// UsedQuota is a lifetime usage counter. The self-query parser leaves
	// it outside wallet totals, while generic nested parsing uses it when
	// Used is absent.
	UsedQuota json.RawMessage `json:"used_quota"`
	// Balance, Remaining, and TotalBalance are nested remainder
	// alternatives. Generic selection prefers TotalBalance, then Balance,
	// then Remaining, before considering Quota as the final source.
	// Balance: alternate nested wallet remainder
	Balance json.RawMessage `json:"balance"`
	// Remaining: alternate nested remaining amount
	Remaining json.RawMessage `json:"remaining"`
	// TotalBalance: preferred nested wallet remainder
	TotalBalance json.RawMessage `json:"total_balance"`
	// Used: optional reported wallet consumption
	Used  json.RawMessage `json:"used"`
	Total json.RawMessage `json:"total"` // optional reported wallet total
	// Currency marks recognized nested values as display amounts. An absent
	// label also marks display when Quota is absent; otherwise site quota
	// factors apply.
	Currency string `json:"currency"`
	// BalanceInfos replaces the root list when this nested list is
	// nonempty. The selected list is searched for the site's currency
	// before another available entry is considered.
	BalanceInfos []NewAPIWalletBalanceInfo `json:"balance_infos"`
}

// NewAPIWalletBalanceResponse holds wallet fields at the response root or
// inside a nested data object.
type NewAPIWalletBalanceResponse struct {
	// Code and Success are optional response flags. Either supplied false
	// selects authentication failure before field parsing; absent flags
	// leave wallet shape validation to its selected representation. Code:
	// optional response validity marker
	Code    *bool  `json:"code"`
	Success *bool  `json:"success"` // optional response success marker
	Message string `json:"message"` // reported upstream diagnostic text
	// BalanceInfos: root currency balance entries
	BalanceInfos []NewAPIWalletBalanceInfo `json:"balance_infos"`
	// Currency controls root amount conversion when no list or Data object
	// supplies the wallet. Recognized units or absent text mark display
	// values; other text uses site settings.
	Currency string `json:"currency"`
	// Balance, Remaining, and TotalBalance are root remainder alternatives.
	// TotalBalance takes precedence, then Balance and Remaining, with
	// absent or null JSON skipped before numeric parsing. Balance:
	// alternate root wallet remainder
	Balance json.RawMessage `json:"balance"`
	// Remaining: alternate root remaining amount
	Remaining json.RawMessage `json:"remaining"`
	// TotalBalance: preferred root wallet remainder
	TotalBalance json.RawMessage `json:"total_balance"`
	// Used and UsedBalance are optional root usage observations, with
	// UsedBalance selected first. Optional Total is passed independently to
	// shared amount validation when present. Used: alternate root wallet
	// consumption
	Used json.RawMessage `json:"used"`
	// UsedBalance: preferred root wallet consumption
	UsedBalance json.RawMessage `json:"used_balance"`
	Total       json.RawMessage `json:"total"` // optional root wallet total
	// Data selects nested wallet fields when a list has not supplied an
	// amount. A present empty object therefore needs its own usable
	// remainder; root amounts do not replace that selected shape.
	Data *NewAPIWalletData `json:"data"`
}

// NewAPIUsageDisplaySettings supplies the site's display unit and raw quota
// conversion factors.
type NewAPIUsageDisplaySettings struct {
	// Unit is the site display unit used by token conversion and wallet
	// matching. TOKENS leaves quota unchanged; CNY selects the additional
	// exchange factor after money conversion.
	Unit string
	// QuotaPerUnit divides raw quota into money units. Nonpositive or
	// nonfinite factors select the shared default before conversion, while
	// TOKENS bypasses the divisor.
	QuotaPerUnit float64
	// USDExchangeRate scales converted money when Unit is CNY. Invalid or
	// nonpositive factors become one; other display units leave this factor
	// unused.
	USDExchangeRate float64
}

// Name returns the registry identifier for Sub2API upstream usage queries.
func (*Sub2APIUsageAdapter) Name() string { return usageview.UpstreamUsageAdapterSub2API }

// Query reads /v1/usage with the API key and normalizes its reported mode. It
// requires a stable request snapshot with usable transport, context, header,
// and endpoint callbacks; unsuccessful statuses use shared usage errors.
func (a *Sub2APIUsageAdapter) Query(ctx context.Context, input *usagecontract.Request) (*usageview.UpstreamUsageInfo, error) {
	client := usageclient.New(input)
	body, status, err := client.Get(ctx, "/v1/usage", true)
	if err != nil {
		return nil, err
	}
	if httpErr := usageclient.UpstreamUsageHTTPError(status, true); httpErr != nil {
		return nil, httpErr
	}
	return ParseSub2APIUsage(body)
}

// ParseSub2APIUsage decodes and normalizes a Sub2API usage response. A missing
// isValid field is malformed, false is an authentication failure, and supported
// mode values select quota-limited or unrestricted handling; other modes are
// rejected.
func ParseSub2APIUsage(body []byte) (*usageview.UpstreamUsageInfo, error) {
	var response Sub2APIUsageResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse.WithCause(err)
	}
	if response.IsValid == nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	if !*response.IsValid {
		return nil, usageview.ErrUpstreamUsageAuthFailed
	}
	switch response.Mode {
	case "quota_limited":
		return NormalizeSub2APIQuotaLimited(&response)
	case "unrestricted":
		return NormalizeSub2APIUnrestricted(&response)
	default:
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
}

// NormalizeSub2APIQuotaLimited returns key quota or rolling limits from a
// nonnil response with an accepted quota status. An absent Quota requires
// nonempty rolling limits and absent top-level remaining and unit; an optional
// subscription establishes USD for that limits result.
//
// A present Quota requires positive finite USD allowance and consistent
// nonnegative used and remaining values, including agreement with top-level
// Remaining after flooring overuse at zero. Normalized quota and rolling-limit
// amounts retain the response's numeric pointers, so callers keep that storage
// stable while the result is consumed. Expiry values are normalized to UTC.
func NormalizeSub2APIQuotaLimited(response *Sub2APIUsageResponse) (*usageview.UpstreamUsageInfo, error) {
	if response.Status != "active" && response.Status != "quota_exhausted" && response.Status != "expired" {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	limits, err := NormalizeSub2APIRateLimits(response.RateLimits)
	if err != nil {
		return nil, err
	}
	subscription, err := NormalizeSub2APISubscription(response.PlanName, response.Subscription)
	if err != nil {
		return nil, err
	}
	// A limits-only response has no total key quota to compare. Require
	// rolling windows and absent aggregate fields so that representation
	// cannot mix incompatible allowance sources.
	if response.Quota == nil {
		if len(limits) == 0 || response.Remaining != nil || strings.TrimSpace(response.Unit) != "" {
			return nil, usageview.ErrUpstreamUsageInvalidResponse
		}
		expiresAt, err := NormalizeTime(response.ExpiresAt)
		if err != nil {
			return nil, err
		}
		unit := ""
		if subscription != nil {
			unit = "USD"
		}
		return &usageview.UpstreamUsageInfo{Provider: usageview.UpstreamUsageAdapterSub2API, Mode: "limits", Unit: unit, Limits: limits, Subscription: subscription, ExpiresAt: expiresAt}, nil
	}
	quota := response.Quota
	// Compare an overused allowance against zero remaining while retaining
	// reported Used. This lets the result describe exhaustion without
	// treating spent capacity as negative available quota.
	if quota.Limit == nil || quota.Used == nil || quota.Remaining == nil || response.Remaining == nil ||
		quota.Unit != "USD" || response.Unit != quota.Unit || *quota.Limit <= 0 ||
		!usageview.ValidNonNegativeNumber(*quota.Limit) || !usageview.ValidNonNegativeNumber(*quota.Used) || !usageview.ValidNonNegativeNumber(*quota.Remaining) ||
		!usageview.ValidNonNegativeNumber(*response.Remaining) || !CloseEnough(*quota.Remaining, math.Max(0, *quota.Limit-*quota.Used)) ||
		!CloseEnough(*quota.Remaining, *response.Remaining) {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	expiresAt, err := NormalizeTime(response.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return &usageview.UpstreamUsageInfo{
		Provider: usageview.UpstreamUsageAdapterSub2API,
		Mode:     "quota",
		Unit:     quota.Unit,
		Balance:  &usageview.UpstreamUsageAmount{Used: quota.Used, Total: quota.Limit, Remaining: quota.Remaining},
		Limits:   limits, Subscription: subscription, ExpiresAt: expiresAt,
	}, nil
}

// NormalizeSub2APIUnrestricted returns either a wallet or subscription from a
// nonnil USD response with a plan name and finite Remaining. Exactly one of
// Balance and Subscription must be supplied, distinguishing the meaning of that
// top-level remaining value.
//
// Wallet amounts retain finite negative debt when Balance agrees with
// Remaining, and the returned amount borrows Balance's numeric storage.
// Subscriptions convert the unlimited representation to a flag with absent
// numeric remaining; limited subscriptions require their smallest window
// remainder to agree with the top-level amount. This prevents a wallet amount
// and subscription sentinel from sharing one display representation.
func NormalizeSub2APIUnrestricted(response *Sub2APIUsageResponse) (*usageview.UpstreamUsageInfo, error) {
	if response.Unit != "USD" || strings.TrimSpace(response.PlanName) == "" || response.Remaining == nil || !usageview.ValidFiniteNumber(*response.Remaining) {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	expiresAt, err := NormalizeTime(response.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if (response.Subscription == nil) == (response.Balance == nil) {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	if response.Balance != nil {
		if !usageview.ValidFiniteNumber(*response.Balance) || !CloseEnough(*response.Balance, *response.Remaining) {
			return nil, usageview.ErrUpstreamUsageInvalidResponse
		}
		return &usageview.UpstreamUsageInfo{
			Provider:  usageview.UpstreamUsageAdapterSub2API,
			Mode:      "balance",
			Unit:      response.Unit,
			Balance:   &usageview.UpstreamUsageAmount{Remaining: response.Balance},
			ExpiresAt: expiresAt,
		}, nil
	}
	subscription, err := NormalizeSub2APISubscription(response.PlanName, response.Subscription, response.Remaining)
	if err != nil || subscription == nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	// Treat -1 as an unlimited subscription's wire sentinel, then return a
	// flag-based result so downstream amount readers cannot display that
	// sentinel as funds.
	if subscription.Unlimited {
		if *response.Remaining != -1 {
			return nil, usageview.ErrUpstreamUsageInvalidResponse
		}
		return &usageview.UpstreamUsageInfo{Provider: usageview.UpstreamUsageAdapterSub2API, Mode: "subscription", Unit: response.Unit, Subscription: subscription, ExpiresAt: expiresAt}, nil
	}
	if !usageview.ValidNonNegativeNumber(*response.Remaining) || subscription.Remaining == nil || !CloseEnough(*response.Remaining, *subscription.Remaining) {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	return &usageview.UpstreamUsageInfo{Provider: usageview.UpstreamUsageAdapterSub2API, Mode: "subscription", Unit: response.Unit, Subscription: subscription, ExpiresAt: expiresAt}, nil
}

// NormalizeSub2APISubscription builds a plan from usage windows, or returns nil
// when raw is nil. Supplied plans require a name, all three nonnegative usage
// observations, and a nonzero expiry. Only the first optional legacyRemaining
// argument participates in unlimited inference.
//
// A supplied Unlimited flag takes precedence over the legacy -1 remainder;
// unlimited plans reject any finite window limits. Limited plans require at
// least one window and expose the smallest derived remainder across them.
// Window Used and Limit pointers borrow raw storage, while the selected
// Remaining and normalized times use newly computed values.
func NormalizeSub2APISubscription(planName string, raw *Sub2APISubscription, legacyRemaining ...*float64) (*usageview.UpstreamUsageSubscription, error) {
	if raw == nil {
		return nil, nil
	}
	if strings.TrimSpace(planName) == "" || raw.DailyUsageUSD == nil || raw.WeeklyUsageUSD == nil || raw.MonthlyUsageUSD == nil ||
		!usageview.ValidNonNegativeNumber(*raw.DailyUsageUSD) || !usageview.ValidNonNegativeNumber(*raw.WeeklyUsageUSD) || !usageview.ValidNonNegativeNumber(*raw.MonthlyUsageUSD) {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	expiresAt, err := NormalizeTime(raw.ExpiresAt)
	if err != nil || expiresAt == nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	var remainingSentinel *float64
	if len(legacyRemaining) > 0 {
		remainingSentinel = legacyRemaining[0]
	}
	unlimited := raw.Unlimited != nil && *raw.Unlimited
	// The legacy remainder can establish unlimited status when the
	// dedicated flag is absent. Keep an explicitly supplied false flag
	// authoritative when selecting this representation.
	if raw.Unlimited == nil && remainingSentinel != nil && *remainingSentinel == -1 {
		unlimited = true
	}
	limits, err := NormalizeSub2APISubscriptionLimits(raw)
	if err != nil {
		return nil, err
	}
	if unlimited {
		if len(limits) != 0 {
			return nil, usageview.ErrUpstreamUsageInvalidResponse
		}
		return &usageview.UpstreamUsageSubscription{PlanName: strings.TrimSpace(planName), Unlimited: true, ExpiresAt: expiresAt}, nil
	}
	if len(limits) == 0 {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	// The smallest allowance remainder constrains the subscription display
	// when several windows apply, so a larger daily or monthly remainder
	// cannot replace an exhausted window.
	minimum := *limits[0].Remaining
	for _, limit := range limits[1:] {
		if limit.Remaining != nil && *limit.Remaining < minimum {
			minimum = *limit.Remaining
		}
	}
	return &usageview.UpstreamUsageSubscription{PlanName: strings.TrimSpace(planName), Remaining: &minimum, ExpiresAt: expiresAt, Limits: limits}, nil
}

// NormalizeSub2APISubscriptionLimits builds daily, weekly, and monthly limits
// from a nonnil subscription in that order. Missing or zero limits omit their
// windows; included windows require finite nonnegative usage and a positive
// finite limit, with remaining floored at zero.
//
// For an included window, a supplied reset takes precedence over its start.
// When reset is absent, a usable start adds the fixed daily, weekly, or
// thirty-day duration. Invalid times encountered on that included path reject
// normalization. Returned Used and Limit pointers retain the source fields, so
// callers keep that storage stable while consuming the derived windows.
func NormalizeSub2APISubscriptionLimits(raw *Sub2APISubscription) ([]usageview.UpstreamUsageLimit, error) {
	// subscriptionInput pairs one subscription window's allowance and reset
	// sources for normalization.
	type subscriptionInput struct {
		name string // normalized subscription window label
		// used and limit retain the subscription's numeric pointers for
		// this window. A missing or zero limit omits it; included
		// windows require usable finite nonnegative amounts and
		// positive allowance.
		used  *float64 // reported window usage amount
		limit *float64 // reported window allowance total
		// resetAt is the preferred source for an included window. A
		// zero instant encountered there rejects normalization, while
		// absence allows start and duration to derive its reset.
		resetAt *time.Time
		// start supplies a reset when resetAt is absent for an included
		// window. A supplied zero time rejects that derivation.
		start *time.Time
		// duration is added to start only when resetAt is absent.
		// Monthly windows use thirty days as the fixed approximation
		// for this derived reset.
		duration time.Duration
	}
	// Reset derivation uses fixed window durations, including thirty days
	// for monthly allowance, so a missing reset receives an approximation
	// from its reported start.
	inputs := []subscriptionInput{
		{name: "daily", used: raw.DailyUsageUSD, limit: raw.DailyLimitUSD, resetAt: raw.DailyResetAt, start: raw.DailyWindowStart, duration: 24 * time.Hour},
		{name: "weekly", used: raw.WeeklyUsageUSD, limit: raw.WeeklyLimitUSD, resetAt: raw.WeeklyResetAt, start: raw.WeeklyWindowStart, duration: 7 * 24 * time.Hour},
		{name: "monthly", used: raw.MonthlyUsageUSD, limit: raw.MonthlyLimitUSD, resetAt: raw.MonthlyResetAt, start: raw.MonthlyWindowStart, duration: 30 * 24 * time.Hour},
	}
	limits := make([]usageview.UpstreamUsageLimit, 0, len(inputs))
	for _, input := range inputs {
		if input.limit == nil || *input.limit == 0 {
			continue
		}
		if input.used == nil || *input.limit < 0 || !usageview.ValidNonNegativeNumber(*input.limit) || !usageview.ValidNonNegativeNumber(*input.used) {
			return nil, usageview.ErrUpstreamUsageInvalidResponse
		}
		// Keep reported overuse in Used while flooring Remaining at
		// zero, allowing finite subscription windows to show exhaustion
		// without a negative allowance.
		remaining := math.Max(0, *input.limit-*input.used)
		resetAt, err := NormalizeTime(input.resetAt)
		if err != nil {
			return nil, err
		}
		// Derive a reset from the window start only when no reset was
		// supplied. An explicit reset retains precedence over the fixed
		// duration approximation.
		if resetAt == nil && input.start != nil {
			start, startErr := NormalizeTime(input.start)
			if startErr != nil {
				return nil, startErr
			}
			if start != nil {
				value := start.Add(input.duration)
				resetAt = &value
			}
		}
		limits = append(limits, usageview.UpstreamUsageLimit{Name: input.name, Used: input.used, Limit: input.limit, Remaining: &remaining, ResetAt: resetAt})
	}
	return limits, nil
}

// NormalizeSub2APIRateLimits validates and returns 5h, 1d, or 7d allowance
// windows in response order. Trimmed names must be unique, and each window
// requires positive finite Limit plus nonnegative finite Used and Remaining.
//
// Remaining must agree with limit minus used after flooring overuse at zero.
// WindowStart is required as valid timestamp JSON or explicit null, and an
// optional nonzero ResetAt is normalized to UTC. Amount pointers borrow each
// source entry, allowing validation to retain reported values while computed
// checks confirm their consistency.
func NormalizeSub2APIRateLimits(raw []Sub2APIRateLimit) ([]usageview.UpstreamUsageLimit, error) {
	limits := make([]usageview.UpstreamUsageLimit, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		name := strings.TrimSpace(item.Window)
		if name == "" || (name != "5h" && name != "1d" && name != "7d") {
			return nil, usageview.ErrUpstreamUsageInvalidResponse
		}
		if _, exists := seen[name]; exists {
			return nil, usageview.ErrUpstreamUsageInvalidResponse
		}
		seen[name] = struct{}{}
		// Keep the reported overuse visible in Used, but compare its
		// remainder against exhausted capacity of zero so rolling
		// allowances cannot expose negative available quota.
		if item.Limit == nil || item.Used == nil || item.Remaining == nil || *item.Limit <= 0 || !usageview.ValidNonNegativeNumber(*item.Limit) ||
			!usageview.ValidNonNegativeNumber(*item.Used) || !usageview.ValidNonNegativeNumber(*item.Remaining) ||
			!CloseEnough(*item.Remaining, math.Max(0, *item.Limit-*item.Used)) {
			return nil, usageview.ErrUpstreamUsageInvalidResponse
		}
		if err := ValidateSub2APIWindowStart(item.WindowStart); err != nil {
			return nil, err
		}
		resetAt, err := NormalizeTime(item.ResetAt)
		if err != nil {
			return nil, err
		}
		limits = append(limits, usageview.UpstreamUsageLimit{Name: name, Used: item.Used, Limit: item.Limit, Remaining: item.Remaining, ResetAt: resetAt})
	}
	return limits, nil
}

// ValidateSub2APIWindowStart accepts an explicit JSON null or a nonzero
// time.Time JSON representation. Absent, blank, malformed, and zero timestamp
// values return the invalid-response error, keeping missing metadata distinct
// from a reported unavailable start.
func ValidateSub2APIWindowStart(raw json.RawMessage) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		// The required window_start spelling distinguishes unavailable
		// metadata (explicit null) from a response that omitted the
		// field, which cannot satisfy this adapter's shape validation.
		return usageview.ErrUpstreamUsageInvalidResponse
	}
	if trimmed == "null" {
		return nil
	}
	var value time.Time
	if err := json.Unmarshal(raw, &value); err != nil || value.IsZero() {
		return usageview.ErrUpstreamUsageInvalidResponse
	}
	return nil
}

// Name returns the registry identifier for Zivv wallet and quota queries.
func (*ZivvUsageAdapter) Name() string { return usageview.UpstreamUsageAdapterZivv }

// Query reads /v1/user/balance with the API key and normalizes wallet and key
// allowance. It requires a stable request snapshot with usable callbacks;
// shared endpoint construction handles versioned bases, and unsuccessful
// statuses return usage errors.
func (a *ZivvUsageAdapter) Query(ctx context.Context, input *usagecontract.Request) (*usageview.UpstreamUsageInfo, error) {
	client := usageclient.New(input)
	body, status, err := client.Get(ctx, "/v1/user/balance", true)
	if err != nil {
		return nil, err
	}
	if httpErr := usageclient.UpstreamUsageHTTPError(status, true); httpErr != nil {
		return nil, httpErr
	}
	return ParseZivvUsage(body)
}

// ParseZivvUsage returns wallet amounts alongside key quota from a complete
// available response. Wallet remaining can be finite and negative, while used
// amounts and key counters require finite nonnegative values; wallet total is
// the remaining amount plus cumulative usage and must be nonnegative.
//
// Zero key limit becomes an unlimited subscription flag. Positive key limits
// produce a separately floored remainder for Limits and Subscription, keeping
// key capacity distinct from wallet money. Currency must identify USD, CNY, or
// TOKENS, and blank plan names use Zivv.
func ParseZivvUsage(body []byte) (*usageview.UpstreamUsageInfo, error) {
	var response ZivvUsageResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse.WithCause(err)
	}
	if response.IsAvailable == nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	if !*response.IsAvailable {
		return nil, usageview.ErrUpstreamUsageAuthFailed
	}
	if response.Balance == nil || response.TotalUsed == nil || response.KeyLimit == nil || response.KeyUsed == nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	if !usageview.ValidFiniteNumber(*response.Balance) || !usageview.ValidNonNegativeNumber(*response.TotalUsed) ||
		!usageview.ValidNonNegativeNumber(*response.KeyLimit) || !usageview.ValidNonNegativeNumber(*response.KeyUsed) {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	unit := strings.ToUpper(strings.TrimSpace(response.Currency))
	if unit != "USD" && unit != "CNY" && unit != "TOKENS" {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	planName := strings.TrimSpace(response.PlanName)
	if planName == "" {
		planName = "Zivv"
	}
	total := *response.Balance + *response.TotalUsed
	if !usageview.ValidNonNegativeNumber(total) {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	usage := &usageview.UpstreamUsageInfo{
		Provider: usageview.UpstreamUsageAdapterZivv,
		Mode:     "balance",
		Unit:     unit,
		Balance:  &usageview.UpstreamUsageAmount{Used: response.TotalUsed, Total: &total, Remaining: response.Balance},
	}
	if *response.KeyLimit <= 0 {
		// Zero denotes unlimited key allowance. Use the subscription
		// flag so display consumers do not interpret an unlimited key
		// as a zero-capacity quota window.
		usage.Subscription = &usageview.UpstreamUsageSubscription{PlanName: planName, Unlimited: true}
		return usage, nil
	}
	// Key allowance exhaustion is separate from wallet debt. Floor the
	// derived key remainder while retaining the finite wallet amount
	// already recorded above.
	keyRemaining := math.Max(0, *response.KeyLimit-*response.KeyUsed)
	usage.Limits = []usageview.UpstreamUsageLimit{{
		Name: "key_quota", Used: response.KeyUsed, Limit: response.KeyLimit, Remaining: &keyRemaining,
	}}
	usage.Subscription = &usageview.UpstreamUsageSubscription{PlanName: planName, Remaining: &keyRemaining}
	return usage, nil
}

// Name returns the registry identifier for combined New API usage queries.
func (*NewAPIUsageAdapter) Name() string { return usageview.UpstreamUsageAdapterNewAPI }

// Query combines a New API user wallet with separately normalized key quota and
// subscription information. It requires a stable request snapshot with usable
// callbacks, and first probes display settings under a shorter budget before
// token and wallet queries use the original context. Supply BaseURL without
// a forced empty query marker so the shared token builder's appended slash
// belongs to the endpoint path.
//
// Wallet discovery follows the fixed source order described by queryWallet. A
// wallet is required even for unlimited keys, because a key's quota cannot
// supply its owner's remaining funds. Nonempty token and wallet units must
// agree before the wallet amount and unit are merged into the result, while key
// limits and subscription observations retain their separate fields.
func (a *NewAPIUsageAdapter) Query(ctx context.Context, input *usagecontract.Request) (*usageview.UpstreamUsageInfo, error) {
	client := usageclient.New(input)
	// Give the optional unit probe its own short budget, then release that
	// timer before required token and wallet requests continue under the
	// query context.
	displayCtx, cancelDisplay := context.WithTimeout(ctx, StatusTimeout)
	display := a.queryDisplaySettings(displayCtx, client)
	cancelDisplay()
	// Return to the query context after the optional display probe, so its
	// short timeout cannot terminate the required token and wallet
	// requests.
	tokenUsage, tokenResponse, tokenErr := a.queryTokenUsage(ctx, client, display)
	if tokenErr != nil {
		return nil, tokenErr
	}
	wallet, walletErr := a.queryWallet(ctx, client, display, tokenResponse)
	if walletErr != nil {
		return nil, walletErr
	}
	if tokenUsage == nil || wallet == nil || wallet.Balance == nil {
		return nil, usageview.ErrUpstreamUsageWalletUnavailable
	}
	if tokenUsage.Unit != "" && wallet.Unit != "" && tokenUsage.Unit != wallet.Unit {
		// A result has one unit. Reject mismatched currencies because
		// labeling key quota with the wallet currency would misstate
		// the amount.
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	// The display has one wallet amount alongside key-specific limits and
	// subscription observations. Merge wallet fields after unit agreement
	// so neither amount is relabeled with another currency.
	tokenUsage.Mode = "balance"
	tokenUsage.Unit = wallet.Unit
	tokenUsage.Balance = wallet.Balance
	return tokenUsage, nil
}

// queryDisplaySettings reads optional site conversion settings, defaulting to
// USD, the default quota divisor, and exchange rate one. Endpoint, request,
// status, or required-shape failures retain those defaults so Query can continue
// its required token and wallet reads.
//
// The response needs success=true and a present QuotaDisplayType even when it
// supplies valid numeric factors. An unrecognized label retains USD while
// positive finite factors can replace defaults. TOKENS retains those factors,
// but quota conversion later bypasses them.
func (a *NewAPIUsageAdapter) queryDisplaySettings(ctx context.Context, client *usageclient.Client) NewAPIUsageDisplaySettings {
	settings := NewAPIUsageDisplaySettings{Unit: "USD", QuotaPerUnit: newAPIDefaultQuotaPerUnit, USDExchangeRate: 1}
	endpoint, err := usageclient.UpstreamUsageStatusEndpoint(client.BaseURL)
	if err != nil {
		// The display probe is optional. Retain the conversion defaults
		// when its endpoint cannot be built so required token and
		// wallet requests can proceed.
		return settings
	}
	body, status, err := client.GetURL(ctx, endpoint, false)
	// Failed or unsuccessful display probing leaves the established
	// defaults available, allowing required usage queries to continue
	// despite missing site presentation settings.
	if err != nil || status < http.StatusOK || status >= http.StatusMultipleChoices {
		return settings
	}
	var response struct {
		// Success is required and true before this optional probe's
		// values replace conversion defaults; missing or rejected
		// responses retain those defaults.
		Success *bool `json:"success"`
		// Data supplies the site's unit and optional conversion
		// factors. Absence keeps all defaults because no settings
		// payload was observed.
		Data *struct {
			// QuotaDisplayType is required for a usable settings
			// shape. Recognized USD, CNY, or TOKENS text selects
			// the unit; other values retain USD while valid numeric
			// factors can still apply.
			QuotaDisplayType *string `json:"quota_display_type"`
			// QuotaPerUnit replaces the default divisor when it is
			// present, positive, and finite, before later raw quota
			// amounts are converted.
			QuotaPerUnit *float64 `json:"quota_per_unit"`
			// USDExchangeRate replaces factor one when present,
			// positive, and finite. Quota conversion applies this
			// retained factor when the selected display unit is
			// CNY.
			USDExchangeRate *float64 `json:"usd_exchange_rate"`
		} `json:"data"`
	}
	// Unusable display response shape cannot establish site factors. Keep
	// the defaults rather than deriving quota units from a partial or
	// rejected settings object.
	if err := json.Unmarshal(body, &response); err != nil || response.Success == nil || !*response.Success || response.Data == nil || response.Data.QuotaDisplayType == nil {
		return settings
	}
	switch strings.ToUpper(strings.TrimSpace(*response.Data.QuotaDisplayType)) {
	case "USD", "CNY", "TOKENS":
		settings.Unit = strings.ToUpper(strings.TrimSpace(*response.Data.QuotaDisplayType))
	}
	if response.Data.QuotaPerUnit != nil && usageview.ValidPositiveNumber(*response.Data.QuotaPerUnit) {
		settings.QuotaPerUnit = *response.Data.QuotaPerUnit
	}
	if response.Data.USDExchangeRate != nil && usageview.ValidPositiveNumber(*response.Data.USDExchangeRate) {
		settings.USDExchangeRate = *response.Data.USDExchangeRate
	}
	return settings
}

// queryTokenUsage reads the fixed token path through the shared management
// endpoint builder and returns normalized allowance with its decoded wire
// response. Display settings control conversion, and transport, HTTP, parsing,
// or normalization errors stop execution before wallet discovery proceeds.
func (a *NewAPIUsageAdapter) queryTokenUsage(ctx context.Context, client *usageclient.Client, settings NewAPIUsageDisplaySettings) (*usageview.UpstreamUsageInfo, *NewAPITokenUsageResponse, error) {
	endpoint, err := usageclient.UpstreamUsageTokenEndpoint(client.BaseURL)
	if err != nil {
		return nil, nil, err
	}
	body, status, err := client.GetURL(ctx, endpoint, true)
	if err != nil {
		return nil, nil, err
	}
	if httpErr := usageclient.UpstreamUsageHTTPError(status, true); httpErr != nil {
		return nil, nil, httpErr
	}
	response, err := ParseNewAPITokenUsage(body)
	if err != nil {
		return nil, nil, err
	}
	usage, err := NormalizeNewAPITokenUsage(response, settings)
	if err != nil {
		return nil, nil, err
	}
	return usage, response, nil
}

// queryWallet selects a wallet from embedded token fields, a configured
// user-token self query, or the fixed API-key compatibility endpoint, in that
// order. A present embedded field ends selection even when malformed, keeping
// an advertised wallet amount's validation failure visible.
//
// A configured wallet token enables /api/user/self. Authentication failures,
// rate limiting, and deadline failures stop the query; other unsuccessful self
// results allow /user/balance. A transport failure from self is retained and
// can be returned when the compatibility transport also fails. The configured
// user ID is validated before either request and checked against a successful
// self response.
//
// Compatibility success must parse as wallet data; other compatibility statuses
// or shapes return wallet unavailable, with rate limits and deadlines retaining
// their specific errors. Callers do not synthesize wallet funds from the token
// allowance when no wallet source succeeds.
func (a *NewAPIUsageAdapter) queryWallet(
	ctx context.Context,
	client *usageclient.Client,
	settings NewAPIUsageDisplaySettings,
	tokenResponse *NewAPITokenUsageResponse,
) (*usageview.UpstreamUsageInfo, error) {
	// A present embedded amount owns the selected wallet source. Return its
	// validation error directly so a malformed preferred value cannot
	// silently select another account endpoint.
	if embedded, present, err := NormalizeNewAPITokenWallet(tokenResponse, settings); present {
		if err != nil {
			return nil, err
		}
		return embedded, nil
	}

	userToken := strings.TrimSpace(client.WalletToken)
	userID, err := ConfiguredNewAPIUserID(client.WalletUserID)
	if err != nil {
		return nil, err
	}
	var lastRequestErr error
	// The configured user credential selects the self-query route first.
	// Authentication and deadline failures end this path, while other
	// failed self results permit the fixed compatibility request.
	if userToken != "" {
		endpoint, endpointErr := usageclient.UpstreamUsageUserSelfEndpoint(client.BaseURL)
		if endpointErr != nil {
			return nil, endpointErr
		}
		body, status, requestErr := client.GetURLWithBearer(ctx, endpoint, userToken, userID)
		if requestErr == nil {
			if status == http.StatusUnauthorized || status == http.StatusForbidden {
				return nil, usageview.ErrUpstreamUsageWalletAuthFailed
			}
			if status == http.StatusTooManyRequests {
				return nil, usageview.ErrUpstreamUsageRateLimited
			}
			if status >= http.StatusOK && status < http.StatusMultipleChoices {
				wallet, parseErr := ParseNewAPIUserSelfWallet(body, settings, userID)
				if parseErr == nil {
					return wallet, nil
				}
				if errors.Is(parseErr, usageview.ErrUpstreamUsageAuthFailed) || errors.Is(parseErr, usageview.ErrUpstreamUsageWalletAuthFailed) {
					return nil, usageview.ErrUpstreamUsageWalletAuthFailed
				}
			}
		} else if errors.Is(requestErr, usageview.ErrUpstreamUsageTimeout) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, requestErr
		} else if requestErr != nil {
			// Retain a non-deadline self-query transport failure
			// while trying the next wallet route, so a second
			// transport failure can report the earlier request's
			// evidence.
			lastRequestErr = requestErr
		}
	}

	// Try the fixed API-key wallet route after self-query did not supply
	// usable data. A missing route or HTML body cannot establish remaining
	// funds and is classified as wallet unavailable.
	endpoint, err := usageclient.UpstreamUsageWalletEndpoint(client.BaseURL)
	if err != nil {
		return nil, err
	}
	body, status, requestErr := client.GetURLWithBearer(ctx, endpoint, client.APIKey, "")
	if requestErr != nil {
		if errors.Is(requestErr, usageview.ErrUpstreamUsageTimeout) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, requestErr
		}
		// If both wallet transports failed, expose the retained
		// self-query failure. The compatibility failure otherwise
		// becomes wallet unavailable when no usable amount was
		// observed.
		if lastRequestErr != nil {
			return nil, lastRequestErr
		}
		return nil, usageview.ErrUpstreamUsageWalletUnavailable
	}
	if status == http.StatusTooManyRequests {
		return nil, usageview.ErrUpstreamUsageRateLimited
	}
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		wallet, parseErr := ParseNewAPIWalletBalance(body, settings)
		if parseErr == nil {
			return wallet, nil
		}
	}
	return nil, usageview.ErrUpstreamUsageWalletUnavailable
}

// NormalizeNewAPITokenWallet reads an embedded wallet and reports whether a
// field was present, even when that field is invalid. Missing response data or
// both absent/null wallet fields return present=false, allowing queryWallet to
// try a separate request.
//
// UserBalanceDisplay takes precedence and already uses a display amount;
// UserBalance instead converts raw quota using site settings. A malformed
// preferred value rejects the response rather than selecting its lower-priority
// counterpart. Normalized wallet validation retains finite negative debt while
// rejecting unusable numbers.
func NormalizeNewAPITokenWallet(response *NewAPITokenUsageResponse, settings NewAPIUsageDisplaySettings) (*usageview.UpstreamUsageInfo, bool, error) {
	if response == nil || response.Data == nil {
		return nil, false, nil
	}
	data := response.Data
	if raw := NonEmptyJSON(data.UserBalanceDisplay); raw != nil {
		value, err := ParseNewAPINumber(raw)
		if err != nil || value == nil {
			return nil, true, usageview.ErrUpstreamUsageInvalidResponse
		}
		unit, _ := NormalizeNewAPIWalletUnit(data.Currency, settings.Unit)
		usage, usageErr := NewAPIWalletAmountUsage(unit, nil, nil, value)
		return usage, true, usageErr
	}
	if raw := NonEmptyJSON(data.UserBalance); raw != nil {
		value, err := ParseNewAPINumber(raw)
		if err != nil || value == nil || !usageview.ValidFiniteNumber(*value) {
			return nil, true, usageview.ErrUpstreamUsageInvalidResponse
		}
		remaining := NormalizeNewAPIQuota(*value, settings)
		usage, usageErr := NewAPIWalletAmountUsage(settings.Unit, nil, nil, &remaining)
		return usage, true, usageErr
	}
	return nil, false, nil
}

// ParseNewAPIWalletBalance selects and normalizes one wallet amount from a JSON
// envelope. A nonempty nested balance_infos list replaces the root list;
// multiple entries first prefer the site's unit, then use the first available
// entry when no preferred unit was selected.
//
// Within an entry, TotalBalance precedes Balance and Remaining. With no list, a
// supplied Data object selects nested fields, otherwise root fields are used.
// Nested Quota is the last remaining source. Recognized currencies mark display
// amounts; absent currency also marks list and root values as display, and
// nested values as display when Quota is absent.
//
// Raw quota amounts are converted with site settings. Required remaining and
// optional used/total values must parse and pass shared amount validation;
// malformed preferred sources reject the selected wallet. Present false Code or
// Success flags produce an authentication error.
func ParseNewAPIWalletBalance(body []byte, settings NewAPIUsageDisplaySettings) (*usageview.UpstreamUsageInfo, error) {
	var response NewAPIWalletBalanceResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse.WithCause(err)
	}
	if response.Code != nil && !*response.Code || response.Success != nil && !*response.Success {
		return nil, usageview.ErrUpstreamUsageAuthFailed
	}
	infos := response.BalanceInfos
	if response.Data != nil && len(response.Data.BalanceInfos) > 0 {
		infos = response.Data.BalanceInfos
	}
	// Prefer the site's currency across wallet entries before accepting
	// another unit, keeping a later matching entry from being hidden by
	// response order.
	for pass := range 2 {
		for _, info := range infos {
			unit, displayValue := NormalizeNewAPIWalletUnit(info.Currency, settings.Unit)
			// balance_infos amounts already use display units when
			// currency is absent. Dividing again by quota_per_unit
			// would understate the wallet balance.
			if strings.TrimSpace(info.Currency) == "" {
				displayValue = true
			}
			if len(infos) > 1 && pass == 0 && settings.Unit != "" && unit != settings.Unit {
				continue
			}
			remainingRaw := FirstNewAPIRaw(info.TotalBalance, info.Balance, info.Remaining)
			remaining, err := ParseNewAPINumber(remainingRaw)
			if err != nil || remaining == nil {
				return nil, usageview.ErrUpstreamUsageInvalidResponse
			}
			if !displayValue {
				value := NormalizeNewAPIQuota(*remaining, settings)
				remaining = &value
			}
			used, err := ParseNewAPINumber(FirstNewAPIRaw(info.UsedBalance, info.Used))
			if err != nil {
				return nil, usageview.ErrUpstreamUsageInvalidResponse
			}
			total, err := ParseNewAPINumber(info.Total)
			if err != nil {
				return nil, usageview.ErrUpstreamUsageInvalidResponse
			}
			if displayValue {
				if used != nil {
					usedValue := *used
					used = &usedValue
				}
				if total != nil {
					totalValue := *total
					total = &totalValue
				}
			} else {
				if used != nil {
					usedValue := NormalizeNewAPIQuota(*used, settings)
					used = &usedValue
				}
				if total != nil {
					totalValue := NormalizeNewAPIQuota(*total, settings)
					total = &totalValue
				}
			}
			return NewAPIWalletAmountUsage(unit, used, total, remaining)
		}
	}
	// When no nested data object is supplied, select root wallet fields
	// using the same amount precedence as list entries; a supplied object
	// selects nested handling.
	if response.Data == nil {
		unit, displayValue := NormalizeNewAPIWalletUnit(response.Currency, settings.Unit)
		remainingRaw := FirstNewAPIRaw(response.TotalBalance, response.Balance, response.Remaining)
		// Root wallet amounts with no currency label are treated as
		// display values, so an already-displayed balance is not
		// divided by the raw quota factor.
		if strings.TrimSpace(response.Currency) == "" {
			displayValue = true
		}
		remaining, err := ParseNewAPINumber(remainingRaw)
		if err != nil || remaining == nil {
			return nil, usageview.ErrUpstreamUsageInvalidResponse
		}
		used, err := ParseNewAPINumber(FirstNewAPIRaw(response.UsedBalance, response.Used))
		if err != nil {
			return nil, usageview.ErrUpstreamUsageInvalidResponse
		}
		total, err := ParseNewAPINumber(response.Total)
		if err != nil {
			return nil, usageview.ErrUpstreamUsageInvalidResponse
		}
		if !displayValue {
			remainingValue := NormalizeNewAPIQuota(*remaining, settings)
			remaining = &remainingValue
			if used != nil {
				usedValue := NormalizeNewAPIQuota(*used, settings)
				used = &usedValue
			}
			if total != nil {
				totalValue := NormalizeNewAPIQuota(*total, settings)
				total = &totalValue
			}
		}
		return NewAPIWalletAmountUsage(unit, used, total, remaining)
	}
	data := response.Data
	unit, displayValue := NormalizeNewAPIWalletUnit(data.Currency, settings.Unit)
	remainingRaw := FirstNewAPIRaw(data.TotalBalance, data.Balance, data.Remaining, data.Quota)
	// A nested amount without currency or Quota is a display value.
	// Presence of Quota keeps the raw conversion path selected even if
	// another amount field wins precedence.
	if strings.TrimSpace(data.Currency) == "" && NonEmptyJSON(data.Quota) == nil {
		displayValue = true
	}
	remaining, err := ParseNewAPINumber(remainingRaw)
	if err != nil || remaining == nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	used, err := ParseNewAPINumber(FirstNewAPIRaw(data.Used, data.UsedQuota))
	if err != nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	total, err := ParseNewAPINumber(data.Total)
	if err != nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	if !displayValue {
		remainingValue := NormalizeNewAPIQuota(*remaining, settings)
		remaining = &remainingValue
		if used != nil {
			usedValue := NormalizeNewAPIQuota(*used, settings)
			used = &usedValue
		}
		if total != nil {
			totalValue := NormalizeNewAPIQuota(*total, settings)
			total = &totalValue
		}
	}
	return NewAPIWalletAmountUsage(unit, used, total, remaining)
}

// ParseNewAPIUserSelfWallet validates a nested user-self wallet and returns its
// quota-derived remainder. False success/code flags reject authentication, and
// a nonempty expectedUserID requires a present ID whose normalized integer text
// matches that supplied identity.
//
// Quota is the required amount, converted unless a recognized currency marks it
// as display units. Lifetime UsedQuota is left outside the wallet amount, so
// the result contains remaining funds without inventing a total from an
// unrelated lifetime counter. Shared validation permits finite negative
// remaining values.
func ParseNewAPIUserSelfWallet(body []byte, settings NewAPIUsageDisplaySettings, expectedUserID string) (*usageview.UpstreamUsageInfo, error) {
	var response NewAPIWalletBalanceResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse.WithCause(err)
	}
	if response.Success != nil && !*response.Success || response.Code != nil && !*response.Code {
		return nil, usageview.ErrUpstreamUsageAuthFailed
	}
	if response.Data == nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	if expectedUserID != "" {
		if NonEmptyJSON(response.Data.ID) == nil {
			return nil, usageview.ErrUpstreamUsageWalletAuthFailed
		}
		actual, err := ParseNewAPIInteger(response.Data.ID)
		if err != nil || actual != expectedUserID {
			return nil, usageview.ErrUpstreamUsageWalletAuthFailed
		}
	}
	remaining, err := ParseNewAPINumber(response.Data.Quota)
	if err != nil || remaining == nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	unit, displayValue := NormalizeNewAPIWalletUnit(response.Data.Currency, settings.Unit)
	if !displayValue {
		remainingValue := NormalizeNewAPIQuota(*remaining, settings)
		remaining = &remainingValue
	}
	// The user-self lifetime counter has no matching wallet period total.
	// Pass only the observed remainder so the display cannot combine
	// lifetime usage with current funds into a synthetic wallet capacity.
	return NewAPIWalletAmountUsage(unit, nil, nil, remaining)
}

// NewAPIWalletAmountUsage validates observed wallet components and retains the
// supplied numeric pointers in a balance result. At least one component must be
// present; callers keep pointed values stable while consuming the result and
// validate the display unit separately.
func NewAPIWalletAmountUsage(unit string, used, total, remaining *float64) (*usageview.UpstreamUsageInfo, error) {
	amount := &usageview.UpstreamUsageAmount{Used: used, Total: total, Remaining: remaining}
	if err := usageview.ValidateUsageAmount(amount); err != nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse.WithCause(err)
	}
	return &usageview.UpstreamUsageInfo{Provider: usageview.UpstreamUsageAdapterNewAPI, Mode: "balance", Unit: unit, Balance: amount}, nil
}

// ConfiguredNewAPIUserID trims and canonicalizes a positive decimal int64
// identifier, or returns empty for an unconfigured value. Invalid, overflowing,
// and nonpositive values return a configuration error before user-specific
// wallet requests are sent.
func ConfiguredNewAPIUserID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return "", usageview.ErrUpstreamUsageConfigInvalid
	}
	return strconv.FormatInt(value, 10), nil
}

// ParseNewAPINumber decodes a finite JSON number or numeric string into
// float64, returning nil for missing or null input. Strings permit trimmed
// whitespace, commas, and leading dollar or yuan symbols; malformed or
// nonfinite values return an error.
func ParseNewAPINumber(raw json.RawMessage) (*float64, error) {
	raw = NonEmptyJSON(raw)
	if raw == nil {
		return nil, nil
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err == nil {
		if !usageview.ValidFiniteNumber(value) {
			return nil, errors.New("invalid numeric value")
		}
		return &value, nil
	}
	var textValue string
	if err := json.Unmarshal(raw, &textValue); err != nil {
		return nil, err
	}
	textValue = strings.TrimSpace(textValue)
	// Normalize supported display-number spellings before float parsing,
	// letting numeric wallet strings include grouping separators and
	// leading currency symbols used by these response formats.
	textValue = strings.ReplaceAll(textValue, ",", "")
	textValue = strings.TrimPrefix(textValue, "$")
	textValue = strings.TrimPrefix(textValue, "¥")
	value, err := strconv.ParseFloat(textValue, 64)
	if err != nil || !usageview.ValidFiniteNumber(value) {
		return nil, errors.New("invalid numeric value")
	}
	return &value, nil
}

// ParseNewAPIInteger formats the int64 conversion of a positive integral
// float64 decoded by ParseNewAPINumber. Missing, nonpositive, and nonintegral
// decoded values fail. Float64 rounding can make a fractional input integral
// before this check, and the conversion does not validate the int64 range.
func ParseNewAPIInteger(raw json.RawMessage) (string, error) {
	value, err := ParseNewAPINumber(raw)
	if err != nil || value == nil || *value <= 0 || math.Trunc(*value) != *value {
		return "", errors.New("invalid user id")
	}
	// Formatting follows a float64-to-int64 conversion after integrality
	// checks; precision loss and out-of-range conversion remain possible
	// for very large IDs.
	return strconv.FormatInt(int64(*value), 10), nil
}

// NonEmptyJSON returns raw unless it is absent, whitespace-only, or JSON null.
// Returned bytes borrow the supplied storage, and other JSON spellings remain
// available for the selected field's parser.
func NonEmptyJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" || strings.TrimSpace(string(raw)) == "null" {
		return nil
	}
	return raw
}

// FirstNewAPIRaw returns the first supplied value that NonEmptyJSON retains, or
// nil when all are absent. The bytes borrow that value's storage; callers
// validate its content after source selection.
func FirstNewAPIRaw(values ...json.RawMessage) json.RawMessage {
	for _, value := range values {
		if raw := NonEmptyJSON(value); raw != nil {
			return raw
		}
	}
	return nil
}

// NormalizeNewAPIWalletUnit recognizes supported currency aliases and reports
// whether amounts already use display units. Unrecognized text selects
// fallback, or USD when fallback is empty, with false directing callers to
// quota conversion.
func NormalizeNewAPIWalletUnit(raw, fallback string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "USD", "$", "US$":
		return "USD", true
	case "CNY", "RMB", "¥", "￥":
		return "CNY", true
	case "TOKENS", "TOKEN":
		return "TOKENS", true
	}
	if fallback == "" {
		return "USD", false
	}
	return fallback, false
}

// ParseNewAPITokenUsage decodes the token envelope and distinguishes a rejected
// key from malformed usage. Present false Code or Success flags fail
// authentication first, while accepted data requires Code=true, token_usage
// identity, an unlimited flag, and an expiry of at least minus one.
//
// Unlimited keys allow absent or finite negative counters because normalization
// omits quota amounts for that representation. Limited keys require all three
// finite nonnegative counters; NormalizeNewAPITokenUsage subsequently checks
// their consistency with CloseEnough before displaying key capacity; that
// comparison can accept a sum that overflowed to infinity. Wallet fields stay
// raw for separate source selection.
func ParseNewAPITokenUsage(body []byte) (*NewAPITokenUsageResponse, error) {
	var response NewAPITokenUsageResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse.WithCause(err)
	}
	// An explicit response-level rejection determines authentication
	// failure before shape checks, so a rejected key cannot become a
	// misleading missing-field diagnostic.
	if response.Code != nil && !*response.Code || response.Success != nil && !*response.Success {
		return nil, usageview.ErrUpstreamUsageAuthFailed
	}
	if response.Code == nil || !*response.Code || response.Data == nil ||
		response.Data.Object != "token_usage" || response.Data.UnlimitedQuota == nil || response.Data.ExpiresAt == nil ||
		*response.Data.ExpiresAt < -1 {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	if *response.Data.UnlimitedQuota {
		// Unlimited normalization omits quota counters. Accept finite
		// negative values in this representation so counter sentinel
		// values cannot turn an otherwise valid unlimited key into
		// displayed negative capacity.
		for _, value := range []*float64{response.Data.TotalGranted, response.Data.TotalUsed, response.Data.TotalAvailable} {
			if value != nil && !usageview.ValidFiniteNumber(*value) {
				return nil, usageview.ErrUpstreamUsageInvalidResponse
			}
		}
		return &response, nil
	}
	if response.Data.TotalGranted == nil || response.Data.TotalUsed == nil || response.Data.TotalAvailable == nil ||
		!usageview.ValidNonNegativeNumber(*response.Data.TotalGranted) || !usageview.ValidNonNegativeNumber(*response.Data.TotalUsed) ||
		!usageview.ValidNonNegativeNumber(*response.Data.TotalAvailable) {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	return &response, nil
}

// NormalizeNewAPITokenUsage converts a parsed token response into key limits or
// an unlimited subscription. Callers supply the required pointers and finite
// counters established by ParseNewAPITokenUsage; nil response data is rejected,
// and expiry is normalized separately.
//
// Unlimited quota omits numeric capacity observations. Limited quota requires
// TotalGranted to pass CloseEnough against TotalUsed plus TotalAvailable,
// then converts all three values with the same display settings. Finite source
// counters do not establish arithmetic agreement when that sum overflows and
// the comparison accepts it. The token_quota limit and subscription remainder
// describe key allowance; Query adds an independently selected wallet later.
func NormalizeNewAPITokenUsage(response *NewAPITokenUsageResponse, settings NewAPIUsageDisplaySettings) (*usageview.UpstreamUsageInfo, error) {
	if response == nil || response.Data == nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	expiresAt, err := NormalizeNewAPITime(*response.Data.ExpiresAt)
	if err != nil {
		return nil, err
	}
	planName := strings.TrimSpace(response.Data.Name)
	if planName == "" {
		planName = "New API"
	}
	// Represent unlimited key allowance with its flag and expiry, omitting
	// numeric counters so negative sentinels cannot enter displayed token
	// quota or wallet funds.
	if *response.Data.UnlimitedQuota {
		return &usageview.UpstreamUsageInfo{
			Provider: usageview.UpstreamUsageAdapterNewAPI,
			Mode:     "subscription",
			Unit:     settings.Unit,
			Subscription: &usageview.UpstreamUsageSubscription{
				PlanName:  planName,
				Unlimited: true,
				ExpiresAt: expiresAt,
			},
			ExpiresAt: expiresAt,
		}, nil
	}
	// Finite source counters can still overflow when added. This comparison
	// passes the derived sum directly to CloseEnough, so an infinite sum can
	// pass its tolerance check.
	if !CloseEnough(*response.Data.TotalGranted, *response.Data.TotalUsed+*response.Data.TotalAvailable) {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	used := NormalizeNewAPIQuota(*response.Data.TotalUsed, settings)
	total := NormalizeNewAPIQuota(*response.Data.TotalGranted, settings)
	remaining := NormalizeNewAPIQuota(*response.Data.TotalAvailable, settings)
	limit := usageview.UpstreamUsageLimit{Name: "token_quota", Used: &used, Limit: &total, Remaining: &remaining}
	return &usageview.UpstreamUsageInfo{
		Provider: usageview.UpstreamUsageAdapterNewAPI,
		Mode:     "limits",
		Unit:     settings.Unit,
		Limits:   []usageview.UpstreamUsageLimit{limit},
		Subscription: &usageview.UpstreamUsageSubscription{
			PlanName:  planName,
			Remaining: &remaining,
			ExpiresAt: expiresAt,
		},
		ExpiresAt: expiresAt,
	}, nil
}

// NormalizeNewAPIQuota converts raw quota to the site's display unit, leaving
// TOKENS unchanged. Money units divide by a positive finite quota factor, with
// unusable factors selecting the shared default.
//
// CNY additionally multiplies by a positive finite exchange rate, or one when
// that factor is unusable. The input itself is not validated here, and extreme
// factors can make a finite input produce a nonfinite result. Callers validate
// derived wallet or token quantities before publishing them.
func NormalizeNewAPIQuota(value float64, settings NewAPIUsageDisplaySettings) float64 {
	if settings.Unit == "TOKENS" {
		return value
	}
	divisor := settings.QuotaPerUnit
	if !usageview.ValidPositiveNumber(divisor) {
		divisor = newAPIDefaultQuotaPerUnit
	}
	// Scale raw quota to the site's money display unit.
	converted := value / divisor
	if settings.Unit == "CNY" {
		rate := settings.USDExchangeRate
		if !usageview.ValidPositiveNumber(rate) {
			rate = 1
		}
		// Apply the configured exchange factor to the CNY display
		// amount.
		converted *= rate
	}
	return converted
}

// NormalizeNewAPITime returns a UTC expiry from a positive epoch, with
// nonpositive values treated as absent. Values beyond the final second of year
// 9999 are interpreted as milliseconds up to the corresponding ceiling,
// truncating subsecond precision; larger values are rejected.
func NormalizeNewAPITime(value int64) (*time.Time, error) {
	if value <= 0 {
		return nil, nil
	}
	// Values above the last valid year-9999 second use the millisecond
	// path. Check its corresponding ceiling before truncating to seconds
	// for the displayed expiry.
	if value > 253402300799 {
		if value > 253402300799000 {
			return nil, usageview.ErrUpstreamUsageInvalidResponse
		}
		value /= 1000
	}
	result := time.Unix(value, 0).UTC()
	if result.IsZero() {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	return &result, nil
}

// NormalizeTime returns a copied UTC timestamp, nil for an absent value, and an
// invalid-response error for a supplied zero time. The source pointer and its
// stored timezone remain unchanged.
func NormalizeTime(value *time.Time) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	if value.IsZero() {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	normalized := value.UTC()
	return &normalized, nil
}

// CloseEnough compares an amount difference with the larger of a 0.000001
// absolute tolerance and 0.00001 times the larger operand magnitude. The
// absolute floor controls small amounts, while the relative tolerance scales
// the allowance for rounding at larger magnitudes. Usage normalizers apply this
// comparison to reported amounts and derived totals or remainders.
//
// Callers needing arithmetic consistency validate both operands, including any
// computed sum, as finite first. The helper performs no finiteness check and can
// accept an infinite operand.
func CloseEnough(left, right float64) bool {
	// An infinite operand makes the scaled bound infinite. An infinite
	// absolute difference then passes the comparison, so this expression
	// alone cannot reject an overflowed sum.
	return math.Abs(left-right) <= math.Max(0.000001, math.Max(math.Abs(left), math.Abs(right))*0.00001)
}
