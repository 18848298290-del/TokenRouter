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

const (
	StatusTimeout             = 2 * time.Second
	newAPIDefaultQuotaPerUnit = 500000.0
)

type Sub2APIUsageAdapter struct{}

type Sub2APIUsageResponse struct {
	Mode         string               `json:"mode"`
	IsValid      *bool                `json:"isValid"`
	Status       string               `json:"status"`
	PlanName     string               `json:"planName"`
	Unit         string               `json:"unit"`
	Remaining    *float64             `json:"remaining"`
	Balance      *float64             `json:"balance"`
	Quota        *Sub2APIQuota        `json:"quota"`
	RateLimits   []Sub2APIRateLimit   `json:"rate_limits"`
	Subscription *Sub2APISubscription `json:"subscription"`
	ExpiresAt    *time.Time           `json:"expires_at"`
}
type Sub2APIQuota struct {
	Limit     *float64 `json:"limit"`
	Used      *float64 `json:"used"`
	Remaining *float64 `json:"remaining"`
	Unit      string   `json:"unit"`
}
type Sub2APIRateLimit struct {
	Window      string          `json:"window"`
	Limit       *float64        `json:"limit"`
	Used        *float64        `json:"used"`
	Remaining   *float64        `json:"remaining"`
	WindowStart json.RawMessage `json:"window_start"`
	ResetAt     *time.Time      `json:"reset_at"`
}
type Sub2APISubscription struct {
	DailyUsageUSD      *float64   `json:"daily_usage_usd"`
	WeeklyUsageUSD     *float64   `json:"weekly_usage_usd"`
	MonthlyUsageUSD    *float64   `json:"monthly_usage_usd"`
	DailyLimitUSD      *float64   `json:"daily_limit_usd"`
	WeeklyLimitUSD     *float64   `json:"weekly_limit_usd"`
	MonthlyLimitUSD    *float64   `json:"monthly_limit_usd"`
	DailyResetAt       *time.Time `json:"daily_reset_at"`
	WeeklyResetAt      *time.Time `json:"weekly_reset_at"`
	MonthlyResetAt     *time.Time `json:"monthly_reset_at"`
	DailyWindowStart   *time.Time `json:"daily_window_start"`
	WeeklyWindowStart  *time.Time `json:"weekly_window_start"`
	MonthlyWindowStart *time.Time `json:"monthly_window_start"`
	Unlimited          *bool      `json:"unlimited"`
	ExpiresAt          *time.Time `json:"expires_at"`
}

// ZivvUsageAdapter queries the API key balance endpoint of the Zivv gateway.
// It requests /v1/user/balance from the site base URL;
// endpoint construction avoids duplicating an existing /v1 suffix.
type ZivvUsageAdapter struct{}

type ZivvUsageResponse struct {
	Balance     *float64 `json:"balance"`
	Currency    string   `json:"currency"`
	IsAvailable *bool    `json:"is_available"`
	KeyLimit    *float64 `json:"key_limit"`
	KeyUsed     *float64 `json:"key_used"`
	PlanName    string   `json:"plan_name"`
	TotalUsed   *float64 `json:"total_used"`
}

type NewAPIUsageAdapter struct{}

type NewAPITokenUsageResponse struct {
	Code    *bool  `json:"code"`
	Success *bool  `json:"success"`
	Message string `json:"message"`
	Data    *struct {
		Object             string          `json:"object"`
		Name               string          `json:"name"`
		TotalGranted       *float64        `json:"total_granted"`
		TotalUsed          *float64        `json:"total_used"`
		TotalAvailable     *float64        `json:"total_available"`
		UnlimitedQuota     *bool           `json:"unlimited_quota"`
		ExpiresAt          *int64          `json:"expires_at"`
		UserBalance        json.RawMessage `json:"user_balance"`
		UserBalanceDisplay json.RawMessage `json:"user_balance_display"`
		Currency           string          `json:"currency"`
	} `json:"data"`
}
type NewAPIWalletBalanceInfo struct {
	Currency     string          `json:"currency"`
	TotalBalance json.RawMessage `json:"total_balance"`
	Balance      json.RawMessage `json:"balance"`
	Remaining    json.RawMessage `json:"remaining"`
	Used         json.RawMessage `json:"used"`
	UsedBalance  json.RawMessage `json:"used_balance"`
	Total        json.RawMessage `json:"total"`
}
type NewAPIWalletData struct {
	ID           json.RawMessage           `json:"id"`
	Quota        json.RawMessage           `json:"quota"`
	UsedQuota    json.RawMessage           `json:"used_quota"`
	Balance      json.RawMessage           `json:"balance"`
	Remaining    json.RawMessage           `json:"remaining"`
	TotalBalance json.RawMessage           `json:"total_balance"`
	Used         json.RawMessage           `json:"used"`
	Total        json.RawMessage           `json:"total"`
	Currency     string                    `json:"currency"`
	BalanceInfos []NewAPIWalletBalanceInfo `json:"balance_infos"`
}
type NewAPIWalletBalanceResponse struct {
	Code         *bool                     `json:"code"`
	Success      *bool                     `json:"success"`
	Message      string                    `json:"message"`
	BalanceInfos []NewAPIWalletBalanceInfo `json:"balance_infos"`
	Currency     string                    `json:"currency"`
	Balance      json.RawMessage           `json:"balance"`
	Remaining    json.RawMessage           `json:"remaining"`
	TotalBalance json.RawMessage           `json:"total_balance"`
	Used         json.RawMessage           `json:"used"`
	UsedBalance  json.RawMessage           `json:"used_balance"`
	Total        json.RawMessage           `json:"total"`
	Data         *NewAPIWalletData         `json:"data"`
}
type NewAPIUsageDisplaySettings struct {
	Unit            string
	QuotaPerUnit    float64
	USDExchangeRate float64
}

func (*Sub2APIUsageAdapter) Name() string { return usageview.UpstreamUsageAdapterSub2API }

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
	minimum := *limits[0].Remaining
	for _, limit := range limits[1:] {
		if limit.Remaining != nil && *limit.Remaining < minimum {
			minimum = *limit.Remaining
		}
	}
	return &usageview.UpstreamUsageSubscription{PlanName: strings.TrimSpace(planName), Remaining: &minimum, ExpiresAt: expiresAt, Limits: limits}, nil
}

func NormalizeSub2APISubscriptionLimits(raw *Sub2APISubscription) ([]usageview.UpstreamUsageLimit, error) {
	type subscriptionInput struct {
		name     string
		used     *float64
		limit    *float64
		resetAt  *time.Time
		start    *time.Time
		duration time.Duration
	}
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
		remaining := math.Max(0, *input.limit-*input.used)
		resetAt, err := NormalizeTime(input.resetAt)
		if err != nil {
			return nil, err
		}
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

func ValidateSub2APIWindowStart(raw json.RawMessage) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		// /v1/usage requires the field to be present.
		// JSON null denotes an unavailable window start.
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

func (*ZivvUsageAdapter) Name() string { return usageview.UpstreamUsageAdapterZivv }

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
		// A zero key limit means unlimited; represent that with the subscription flag.
		usage.Subscription = &usageview.UpstreamUsageSubscription{PlanName: planName, Unlimited: true}
		return usage, nil
	}
	keyRemaining := math.Max(0, *response.KeyLimit-*response.KeyUsed)
	usage.Limits = []usageview.UpstreamUsageLimit{{
		Name: "key_quota", Used: response.KeyUsed, Limit: response.KeyLimit, Remaining: &keyRemaining,
	}}
	usage.Subscription = &usageview.UpstreamUsageSubscription{PlanName: planName, Remaining: &keyRemaining}
	return usage, nil
}

func (*NewAPIUsageAdapter) Name() string { return usageview.UpstreamUsageAdapterNewAPI }

func (a *NewAPIUsageAdapter) Query(ctx context.Context, input *usagecontract.Request) (*usageview.UpstreamUsageInfo, error) {
	client := usageclient.New(input)
	displayCtx, cancelDisplay := context.WithTimeout(ctx, StatusTimeout)
	display := a.queryDisplaySettings(displayCtx, client)
	cancelDisplay()
	// The optional unit probe has a shorter timeout.
	// Token quota requests use the full query deadline so slow instances can still respond.
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
		// A result has one unit. Reject mismatched currencies because labeling
		// key quota with the wallet currency would misstate the amount.
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	// Balance is the user wallet; keep API key quota in Limits and Subscription
	// so the frontend can display both independently.
	tokenUsage.Mode = "balance"
	tokenUsage.Unit = wallet.Unit
	tokenUsage.Balance = wallet.Balance
	return tokenUsage, nil
}

func (a *NewAPIUsageAdapter) queryDisplaySettings(ctx context.Context, client *usageclient.Client) NewAPIUsageDisplaySettings {
	settings := NewAPIUsageDisplaySettings{Unit: "USD", QuotaPerUnit: newAPIDefaultQuotaPerUnit, USDExchangeRate: 1}
	endpoint, err := usageclient.UpstreamUsageStatusEndpoint(client.BaseURL)
	if err != nil {
		return settings
	}
	body, status, err := client.GetURL(ctx, endpoint, false)
	if err != nil || status < http.StatusOK || status >= http.StatusMultipleChoices {
		return settings
	}
	var response struct {
		Success *bool `json:"success"`
		Data    *struct {
			QuotaDisplayType *string  `json:"quota_display_type"`
			QuotaPerUnit     *float64 `json:"quota_per_unit"`
			USDExchangeRate  *float64 `json:"usd_exchange_rate"`
		} `json:"data"`
	}
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

// queryWallet first uses any balance embedded in the token response.
// Otherwise it tries /api/user/self with a configured PAT, then /user/balance with the API key.
// Both endpoint paths are fixed by the adapter.
func (a *NewAPIUsageAdapter) queryWallet(
	ctx context.Context,
	client *usageclient.Client,
	settings NewAPIUsageDisplaySettings,
	tokenResponse *NewAPITokenUsageResponse,
) (*usageview.UpstreamUsageInfo, error) {
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
	// A configured PAT enables the official self-query endpoint before trying
	// the compatibility endpoint, which may serve frontend HTML.
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
			lastRequestErr = requestErr
		}
	}

	// Some forks support API keys on /user/balance. A missing route or SPA HTML
	// response is reported as unavailable wallet data.
	endpoint, err := usageclient.UpstreamUsageWalletEndpoint(client.BaseURL)
	if err != nil {
		return nil, err
	}
	body, status, requestErr := client.GetURLWithBearer(ctx, endpoint, client.APIKey, "")
	if requestErr != nil {
		if errors.Is(requestErr, usageview.ErrUpstreamUsageTimeout) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, requestErr
		}
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

// NormalizeNewAPITokenWallet reads wallet balances embedded by some forks in token responses.
// Display values already use the site unit; raw user_balance uses the /api/status conversion.
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
	for pass := range 2 {
		for _, info := range infos {
			unit, displayValue := NormalizeNewAPIWalletUnit(info.Currency, settings.Unit)
			// balance_infos amounts already use display units when currency is absent.
			// Dividing again by quota_per_unit would understate the wallet balance.
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
	if response.Data == nil {
		unit, displayValue := NormalizeNewAPIWalletUnit(response.Currency, settings.Unit)
		remainingRaw := FirstNewAPIRaw(response.TotalBalance, response.Balance, response.Remaining)
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
	// used_quota is lifetime usage, not usage within a wallet period.
	// Return remaining balance without deriving a wallet total from that counter.
	return NewAPIWalletAmountUsage(unit, nil, nil, remaining)
}

func NewAPIWalletAmountUsage(unit string, used, total, remaining *float64) (*usageview.UpstreamUsageInfo, error) {
	amount := &usageview.UpstreamUsageAmount{Used: used, Total: total, Remaining: remaining}
	if err := usageview.ValidateUsageAmount(amount); err != nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse.WithCause(err)
	}
	return &usageview.UpstreamUsageInfo{Provider: usageview.UpstreamUsageAdapterNewAPI, Mode: "balance", Unit: unit, Balance: amount}, nil
}

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
	textValue = strings.ReplaceAll(textValue, ",", "")
	textValue = strings.TrimPrefix(textValue, "$")
	textValue = strings.TrimPrefix(textValue, "¥")
	value, err := strconv.ParseFloat(textValue, 64)
	if err != nil || !usageview.ValidFiniteNumber(value) {
		return nil, errors.New("invalid numeric value")
	}
	return &value, nil
}

func ParseNewAPIInteger(raw json.RawMessage) (string, error) {
	value, err := ParseNewAPINumber(raw)
	if err != nil || value == nil || *value <= 0 || math.Trunc(*value) != *value {
		return "", errors.New("invalid user id")
	}
	return strconv.FormatInt(int64(*value), 10), nil
}

func NonEmptyJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" || strings.TrimSpace(string(raw)) == "null" {
		return nil
	}
	return raw
}

func FirstNewAPIRaw(values ...json.RawMessage) json.RawMessage {
	for _, value := range values {
		if raw := NonEmptyJSON(value); raw != nil {
			return raw
		}
	}
	return nil
}

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

func ParseNewAPITokenUsage(body []byte) (*NewAPITokenUsageResponse, error) {
	var response NewAPITokenUsageResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse.WithCause(err)
	}
	// Some New API versions reject tokens with HTTP 200 and success/code=false.
	// Classify that as an authentication failure before validating the response shape.
	if response.Code != nil && !*response.Code || response.Success != nil && !*response.Success {
		return nil, usageview.ErrUpstreamUsageAuthFailed
	}
	if response.Code == nil || !*response.Code || response.Data == nil ||
		response.Data.Object != "token_usage" || response.Data.UnlimitedQuota == nil || response.Data.ExpiresAt == nil ||
		*response.Data.ExpiresAt < -1 {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	if *response.Data.UnlimitedQuota {
		// Unlimited token counters can be negative after upstream 32-bit overflow.
		// These counters are omitted from normalized unlimited results,
		// so require finite values when present but allow negative counters.
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

func NormalizeNewAPIQuota(value float64, settings NewAPIUsageDisplaySettings) float64 {
	if settings.Unit == "TOKENS" {
		return value
	}
	divisor := settings.QuotaPerUnit
	if !usageview.ValidPositiveNumber(divisor) {
		divisor = newAPIDefaultQuotaPerUnit
	}
	converted := value / divisor
	if settings.Unit == "CNY" {
		rate := settings.USDExchangeRate
		if !usageview.ValidPositiveNumber(rate) {
			rate = 1
		}
		converted *= rate
	}
	return converted
}

func NormalizeNewAPITime(value int64) (*time.Time, error) {
	if value <= 0 {
		return nil, nil
	}
	// Accept Unix seconds and the millisecond values returned by some deployments.
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

func CloseEnough(left, right float64) bool {
	return math.Abs(left-right) <= math.Max(0.000001, math.Max(math.Abs(left), math.Abs(right))*0.00001)
}
