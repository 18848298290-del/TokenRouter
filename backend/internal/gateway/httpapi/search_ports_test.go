package httpapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/searchtools"
	"github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	catalogprovider "github.com/TokenFlux/TokenRouter/internal/modelcatalog/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// searchFundingProbe counts accepted funding preflight calls during native
// search recording tests.
type searchFundingProbe struct {
	calls int // observed funding check count
}

// searchTargetProbe supplies one native provider and observes execution,
// completion reads and lease cleanup.
type searchTargetProbe struct {
	// calls, releases and snapshots track separate phases of the same
	// fixed-provider fixture. Tests invoke requests serially and compare each
	// counter after return to detect extra execution, completion reads or lease
	// cleanup.
	calls     int // observed native execution count
	releases  int // observed provider release count
	snapshots int // observed completion record reads
	// body retains an independent copy of the most recent Execute input. Later
	// assertions inspect its tool choice after synchronous HTTP handling has
	// returned.
	body []byte // copied last upstream request
}

// Check counts one funding preflight and permits the fixture request.
func (p *searchFundingProbe) Check(context.Context, billing.CheckInput) error { p.calls++; return nil }

// Select supplies this fixed provider with an already acquired slot for every
// fixture request. Group, model and excluded IDs do not affect selection; the
// returned release records cleanup when the request lease invokes it.
func (p *searchTargetProbe) Select(_ context.Context, group int64, _ string, _ map[int64]struct{}) (StandaloneSearchTarget, searchtools.Selection, bool, error) {
	// Count each cleanup invoked by the request lease, letting repeated requests
	// compare release cardinality independently of execution calls and
	// completion-record reads.
	return p, searchtools.Selection{ProviderID: 7, Acquired: true, Release: func() { p.releases++ }}, true, nil
}

// Execute counts one selected request, copies its body and returns one fixed
// native search source. The retained body has independent storage, so
// post-request tool assertions inspect this call's encoded input.
func (p *searchTargetProbe) Execute(_ context.Context, body []byte) ([]byte, error) {
	p.calls++
	p.body = append([]byte(nil), body...)
	return []byte(`{"output":[{"type":"web_search_call","action":{"sources":[{"url":"https://source.test","title":"source","snippet":"result"}]}}]}`), nil
}

// CompletionRecord counts one provider observation and returns a fresh Grok
// API Key record. The returned record identifies provider seven for
// production completion capture, independently of the fixture's
// execution-body storage.
func (p *searchTargetProbe) CompletionRecord() *provider.Record {
	p.snapshots++
	return &provider.Record{ID: 7, Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey}
}

// TestSearchNativePortsCompleteEachRequestOnce sends identical queries twice
// through real Web and X HTTP completion with synchronous worker submission.
// Every request performs one funding check, execution, provider observation,
// lease release, usage insertion and positive funds operation; repeated
// queries receive distinct request identities.
func TestSearchNativePortsCompleteEachRequestOnce(t *testing.T) {
	for _, isX := range []bool{false, true} {
		name := "web"
		if isX {
			name = "x"
		}
		t.Run(name, func(t *testing.T) {
			target := &searchTargetProbe{}
			checks := &searchFundingProbe{}
			logs := &testkit.UsageLogStore{Inserted: true}
			funds := &testkit.SettlementStore{}
			fixture := testkit.NewRecording(logs, funds, nil, false)
			// Use positive USD pricing per thousand searches so completion produces
			// observable billable funds.
			searchPrice := 5.0
			catalogue := catalogprovider.NewServiceFromSnapshot(catalogprovider.Options{}, nil, catalogprovider.Snapshot{
				Data:            map[string]*pricing.CatalogModelPricing{"grok-web-search": {}, "grok-x-search": {}},
				BillingDefaults: pricing.OperationPrices{SearchPricePer1k: &searchPrice},
			})
			fixture.Dependencies.Calculator = billing.NewCalculator(catalogue, billing.CalculatorOptions{})
			recorder := fixture.Core(nil, false)
			// Leave Workers nil so mandatory recording finishes inline before HTTP
			// returns; the following per-request counters can then observe completed
			// funds and usage writes.
			ports := SearchPorts{Selector: target, Funding: admission.NewFundingAdmission(checks, nil), Recorder: recorder}
			handler := NewSearchHandler(ports)
			var priorID string
			for i := 1; i <= 2; i++ {
				c, response := searchContext(`{"query":"same query","max_results":3}`)
				groupID := int64(2)
				key := &apikey.APIKey{ID: 3, UserID: 1, User: &identity.User{ID: 1}, GroupID: &groupID, Group: &routing.Group{ID: groupID, RateMultiplier: 1}}
				c.Set(string(keyhttp.ContextKeyAPIKey), key)
				if isX {
					handler.XSearch(c)
				} else {
					handler.WebSearch(c)
				}
				require.Equal(t, 200, response.Code, response.Body.String())
				require.Equal(t, "https://source.test", gjson.Get(response.Body.String(), "results.0.url").String())
				require.Equal(t, i, target.calls)
				require.Equal(t, i, target.snapshots)
				require.Equal(t, i, target.releases)
				require.Equal(t, i, checks.calls)
				require.Equal(t, i, funds.Calls)
				require.Positive(t, funds.LastCmd.BillableAmountUSD, "搜索按次费用通过实际完成计算器进入唯一资金操作")
				require.Equal(t, i, logs.Calls)
				require.NotNil(t, logs.LastLog)
				require.NotEqual(t, priorID, logs.LastLog.RequestID, "同查询仍按每次请求生成结算标识")
				priorID = logs.LastLog.RequestID
				tool := "web_search"
				if isX {
					tool = "x_search"
				}
				require.Equal(t, tool, gjson.GetBytes(target.body, "tools.0.type").String())
			}
		})
	}
}
