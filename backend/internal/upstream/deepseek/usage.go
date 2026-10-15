package deepseek

import (
	"context"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/upstream/internal/usageclient"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usagecontract"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// usage.go queries DeepSeek balances and maps accepted currency entries into
// usage observations.
// DeepSeek balance responses can contain several currencies, each with a
// separately reported remaining amount. Every accepted entry stays in its
// response order, while the first accepted entry also supplies the primary
// Balance and Unit that single-balance consumers display. Invalid numeric
// entries are skipped individually; an absent array or an array with no usable
// amounts rejects the query.
//
// Availability is a separate observation and defaults to true when is_available
// is absent. A missing currency label becomes CNY, and finite negative balances
// remain available as wallet debt. The query uses a fixed management path and
// the supplied transport snapshot, so provider orchestration determines the
// destination policy and later monitoring decisions.

// DefaultTestModel selects the DeepSeek model used by provider connection
// probes. The probe exercises model access independently of balance queries.
const DefaultTestModel = "deepseek-chat"

// DeepseekBalanceUsageAdapter reads DeepSeek wallet balances through the
// configured usage-query transport.
type DeepseekBalanceUsageAdapter struct{}

// Name returns the registry identifier for DeepSeek wallet-balance queries.
func (*DeepseekBalanceUsageAdapter) Name() string {
	return usageview.UpstreamUsageAdapterDeepseekBalance
}

// Query reads the fixed /user/balance endpoint and returns all entries with
// parseable finite amounts. It requires a stable nonnil request with usable
// transport and header callbacks; shared usageclient methods bound and close
// the response.
//
// The first accepted entry determines the primary amount and unit, while the
// complete accepted list remains available for multi-currency monitoring.
// Missing currency text becomes CNY, and missing is_available becomes true. An
// invalid entry is skipped, but an absent array or wholly unusable array
// returns the invalid-response error.
func (*DeepseekBalanceUsageAdapter) Query(ctx context.Context, input *usagecontract.Request) (*usageview.UpstreamUsageInfo, error) {
	client := usageclient.New(input)
	endpoint, err := usageclient.CnUsageEndpoint(client.BaseURL, "/user/balance", false)
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
	var balances []usageview.UpstreamUsageBalanceEntry
	available := true
	if raw := gjson.GetBytes(body, "is_available"); raw.Exists() {
		available = raw.Bool()
	}
	balanceInfos := gjson.GetBytes(body, "balance_infos")
	if !balanceInfos.Exists() || !balanceInfos.IsArray() {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	balanceInfos.ForEach(func(_, item gjson.Result) bool {
		currency := strings.ToUpper(strings.TrimSpace(item.Get("currency").String()))
		totalBalance := item.Get("total_balance")
		value, ok := usageclient.CnParseF64(totalBalance.Value())
		// A malformed currency entry cannot contribute a usable amount.
		// Continue collecting other currencies so one damaged entry
		// does not discard their reported balances.
		if !totalBalance.Exists() || !ok || !usageview.ValidFiniteNumber(value) {
			return true
		}
		// Use CNY for unlabeled entries so the primary balance and
		// retained currency list have a concrete unit for display and
		// monitoring.
		if currency == "" {
			currency = "CNY"
		}
		balances = append(balances, usageview.UpstreamUsageBalanceEntry{Currency: currency, Remaining: value})
		return true
	})
	if len(balances) == 0 {
		return nil, usageview.ErrUpstreamUsageInvalidResponse
	}
	primary := balances[0].Remaining
	return &usageview.UpstreamUsageInfo{
		Provider:  "deepseek",
		Mode:      "balance",
		Unit:      balances[0].Currency,
		Balance:   &usageview.UpstreamUsageAmount{Remaining: &primary},
		Balances:  balances,
		Available: &available,
	}, nil
}
