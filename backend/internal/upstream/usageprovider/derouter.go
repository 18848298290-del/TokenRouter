package usageprovider

import (
	"context"
	"encoding/json"

	"github.com/TokenFlux/TokenRouter/internal/upstream/internal/usageclient"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usagecontract"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// derouter.go queries derouter.ai account balances and normalizes spendable
// funds for usage display and reminder evaluation. Account keys authenticate a
// fixed management endpoint, whose root is supplied by provider configuration.
// The shared usage client applies transport policy and bounded response reads
// before this adapter interprets the account response.
//
// Account balance includes locked funds, while available identifies the amount
// the account can spend. All three observations must be present, finite, and
// consistent before available becomes the normalized remaining balance. Locked
// funds do not represent historical spending, so the result supplies neither a
// used amount nor a lifetime total. Decimal strings and JSON numbers share the
// same validation, including negative available balances representing debt.

// DerouterUsageAdapter reads spendable account funds using an Account Key.
type DerouterUsageAdapter struct{}

// derouterBalanceResponse holds the related account amounts returned by /balance.
type derouterBalanceResponse struct {
	// Balance, LockedBalance, and Available are required observations in USD.
	// Available must agree with Balance minus LockedBalance; missing or null
	// fields reject the response before display or reminder evaluation.
	Balance       *json.Number `json:"balance"`        // account funds including locks
	LockedBalance *json.Number `json:"locked_balance"` // currently locked account funds
	Available     *json.Number `json:"available"`      // currently spendable account funds
}

// Name returns the registry identifier for derouter.ai Account Key queries.
func (*DerouterUsageAdapter) Name() string { return usageview.UpstreamUsageAdapterDerouter }

// Query reads the fixed /balance management endpoint using the supplied Account
// Key. Callers provide a validated management root and stable transport callbacks;
// HTTP failures retain the shared usage error classification.
func (*DerouterUsageAdapter) Query(ctx context.Context, input *usagecontract.Request) (*usageview.UpstreamUsageInfo, error) {
	endpoint, err := usageclient.UpstreamUsageRootEndpoint(input.BaseURL, "/balance")
	if err != nil {
		return nil, usageview.ErrUpstreamUsageConfigInvalid.WithCause(err)
	}
	body, status, err := usageclient.New(input).GetURL(ctx, endpoint, true)
	if err != nil {
		return nil, err
	}
	if httpErr := usageclient.UpstreamUsageHTTPError(status, true); httpErr != nil {
		return nil, httpErr
	}
	return ParseDerouterUsage(body)
}

// ParseDerouterUsage validates account funds and returns available USD as the
// wallet remainder. Every amount must be present and finite, and locked funds
// must be nonnegative. Available must agree with balance minus locked funds
// under the shared decimal comparison tolerance.
//
// Negative spendable amounts remain debt, while missing values and inconsistent
// responses fail validation. Account totals and locked funds establish that
// consistency but cannot supply historical usage, so the normalized result
// carries only the observed remainder for display and balance reminders.
func ParseDerouterUsage(body []byte) (*usageview.UpstreamUsageInfo, error) {
	var response derouterBalanceResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse.WithCause(err)
	}
	if response.Balance == nil || response.LockedBalance == nil || response.Available == nil {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	balance, balanceErr := response.Balance.Float64()
	locked, lockedErr := response.LockedBalance.Float64()
	available, availableErr := response.Available.Float64()
	if balanceErr != nil || lockedErr != nil || availableErr != nil ||
		!usageview.ValidFiniteNumber(balance) || !usageview.ValidNonNegativeNumber(locked) ||
		!usageview.ValidFiniteNumber(available) {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	// The account endpoint reports all three amounts together. Reject mismatched
	// observations before reminders could treat inconsistent spendable funds as a low balance.
	remaining := balance - locked
	if !usageview.ValidFiniteNumber(remaining) || !CloseEnough(remaining, available) {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	return &usageview.UpstreamUsageInfo{
		Provider: usageview.UpstreamUsageAdapterDerouter,
		Mode:     "balance",
		Unit:     "USD",
		Balance:  &usageview.UpstreamUsageAmount{Remaining: &available},
	}, nil
}
