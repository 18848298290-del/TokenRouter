package completion

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// recordStore counts settlement calls to detect duplicate settlement during log retries.
type recordStore struct {
	calls   int                          // financial apply invocation count
	err     error                        // configured settlement failure
	command *billing.UsageBillingCommand // last supplied command
	events  *[]string                    // shared serial event trace
}

// recordWriter retains attempted row pointers and write outcomes for ordering
// assertions. rows, contexts, and events are updated serially by the tests; events
// shares the same trace used by the funds and effects fixtures.
type recordWriter struct {
	rows []*usage.UsageLog // retained attempted row pointers
	// bestErr: best-effort configured error. errorSync: synchronous configured error.
	bestErr, errorSync error
	events             *[]string // shared write event trace
	contexts           []error   // observed write context errors
}

// recordEffects appends committed-effect observations to the shared serial test trace.
type recordEffects struct {
	events *[]string // shared committed event trace
}

// recordModels selects the supplied primary model for recorder pricing assertions.
type recordModels struct{}

// recordPriceCatalog supplies known prices for settlement and log-order tests.
type recordPriceCatalog struct{}

// Apply counts and retains one financial command before returning the configured
// fixture result. A cancelled context returns its error; otherwise the synthetic
// result carries Applied=true and the command's balance amount alongside the configured error.
func (s *recordStore) Apply(ctx context.Context, c *billing.UsageBillingCommand) (*billing.UsageBillingApplyResult, error) {
	s.calls++
	s.command = c
	*s.events = append(*s.events, "funds")
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &billing.UsageBillingApplyResult{Applied: true, BalanceAmountUSD: c.BillableAmountUSD}, s.err
}

// CreateBestEffort records the event, current context error, and supplied row
// pointer before returning its configured queue error. This serial fixture performs
// no persistence and lets tests inspect recorder ordering and retry inputs.
func (w *recordWriter) CreateBestEffort(ctx context.Context, r *usage.UsageLog) error {
	*w.events = append(*w.events, "best")
	w.contexts = append(w.contexts, ctx.Err())
	w.rows = append(w.rows, r)
	return w.bestErr
}

// Create records the synchronous event, context error, and supplied row pointer.
// It returns true with the configured error, allowing tests to verify that a failed
// analysis retry follows the funds result without another Apply invocation.
func (w *recordWriter) Create(ctx context.Context, r *usage.UsageLog) (bool, error) {
	*w.events = append(*w.events, "sync")
	w.contexts = append(w.contexts, ctx.Err())
	w.rows = append(w.rows, r)
	return true, w.errorSync
}

// ProviderUsed appends the provider-activity event to the serial shared trace.
func (e recordEffects) ProviderUsed(int64) { *e.events = append(*e.events, "used") }

// InvalidateAuth appends the authentication-expiry event to the serial shared trace.
func (e recordEffects) InvalidateAuth(context.Context, string) { *e.events = append(*e.events, "auth") }

// Settled appends the committed-effects event to the serial shared trace.
func (e recordEffects) Settled(SettlementInput, *billing.UsageBillingApplyResult) {
	*e.events = append(*e.events, "effects")
}

// Candidates returns the primary model as the recorder test pricing candidate.
func (recordModels) Candidates(model string, _ ...string) []string { return []string{model} }

// recordFixture builds serial funds, analysis, and effects spies around a fixed
// native usage input. The returned event slice is shared by those spies; callers
// configure failures and inspect completed recording calls through the retained objects.
func recordFixture() (*Recorder, *recordStore, *recordWriter, *Input, *[]string) {
	events := []string{}
	funds := &recordStore{events: &events}
	logs := &recordWriter{events: &events}
	calculator := billing.NewCalculator(recordPriceCatalog{}, billing.CalculatorOptions{})
	recorder := NewRecorder(Dependencies{Calculator: calculator, Funds: funds, Models: recordModels{}, Logs: logs, Effects: recordEffects{&events}}, RecorderOptions{DefaultMultiplier: 1})
	input := &Input{
		Result:    &Result{Model: "claude-sonnet-4", Usage: TokenUsage{InputTokens: 10, OutputTokens: 2, CacheReadInputTokens: 3, CacheCreationInputTokens: 1}},
		RequestID: "fixed",
		APIKey:    &KeySnapshot{ID: 2, BillingMode: billing.APIKeyBillingModeBalance, ActorUserID: 8},
		User:      &PayerSnapshot{ID: 1},
		Provider:  &ProviderSnapshot{ID: 3, RateMultiplier: 1},
	}
	return recorder, funds, logs, input, &events
}

// TestRecordSettlementFailureRetainsUnsettledFact records native and OpenAI usage
// from a cancelled source while funds returns an error. It checks one funds call,
// actor/payer attribution, protocol-specific input buckets, and a zero-ActualCost
// row after settlement.
func TestRecordSettlementFailureRetainsUnsettledFact(t *testing.T) {
	for _, openAI := range []bool{false, true} {
		t.Run(map[bool]string{false: "anthropic", true: "openai"}[openAI], func(t *testing.T) {
			core, funds, logs, in, events := recordFixture()
			funds.err = errors.New("settlement failed")
			// Cancel before recording to distinguish the live detached funds/write budgets
			// from the request context that has already ended in this failure scenario.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := core.Record(ctx, in, openAI)
			require.ErrorIs(t, err, funds.err)
			require.Equal(t, 1, funds.calls)
			require.Equal(t, []string{"funds", "best"}, *events)
			require.Greater(t, logs.rows[0].TotalCost, 0.0)
			require.Zero(t, logs.rows[0].ActualCost)
			require.Equal(t, int64(8), logs.rows[0].UserID)
			require.Equal(t, int64(1), logs.rows[0].BillingUserID)
			require.Equal(t, []error{nil}, logs.contexts)
			if openAI {
				require.Equal(t, 6, logs.rows[0].InputTokens)
			} else {
				require.Equal(t, 10, logs.rows[0].InputTokens)
			}
		})
	}
}

// TestRecordLogFailureDoesNotResettle makes both analysis write attempts fail
// after an applied funds result. It checks the funds/effects/write event order,
// one financial invocation, and a retained positive charge in the attempted retry row.
func TestRecordLogFailureDoesNotResettle(t *testing.T) {
	core, funds, logs, in, events := recordFixture()
	logs.bestErr = errors.New("queue full")
	logs.errorSync = errors.New("database unavailable")
	require.NoError(t, core.Record(context.Background(), in, true))
	require.Equal(t, 1, funds.calls)
	require.Equal(t, []string{"funds", "effects", "best", "sync"}, *events)
	require.Greater(t, logs.rows[1].ActualCost, 0.0)
}

// TestSnapshotIsolatesQueueInputs captures mutable scalar pointers, image
// collections, notification data, and plan rates before changing their sources.
// It checks the submitted values and fixed pricing instant retained by the snapshot.
func TestSnapshotIsolatesQueueInputs(t *testing.T) {
	threshold := 2.0
	group := int64(7)
	tier := "priority"
	at := time.Date(2026, 9, 16, 3, 4, 5, 0, time.UTC)
	in := &Input{
		PricingAt:    at,
		APIKey:       &KeySnapshot{GroupID: &group, Group: &GroupSnapshot{AudioPrice: &pricing.AudioPriceConfig{RealtimePerMin: &threshold}}},
		User:         &PayerSnapshot{Notification: &billing.UserSummary{BalanceNotifyThreshold: &threshold}},
		Result:       &Result{ServiceTier: &tier, ImageOutputSizes: []string{"1K"}, ImageSizeBreakdown: map[string]int{"1K": 1}},
		Subscription: &billing.UserSubscription{ID: 1, Plan: &billing.SubscriptionPlan{GroupIDs: []int64{7}, GroupRateMultipliers: map[int64]float64{7: 2}}},
	}
	// Mutate shared scalar fixtures, image collections, and the plan-rate map after
	// Snapshot to expose storage sharing in the fields asserted below.
	frozen := Snapshot(in)
	threshold = 99
	group = 8
	tier = "flex"
	in.Result.ImageOutputSizes[0] = "4K"
	in.Result.ImageSizeBreakdown["1K"] = 9
	in.Subscription.Plan.GroupRateMultipliers[7] = 8
	require.Equal(t, at, frozen.PricingAt)
	require.Equal(t, int64(7), *frozen.APIKey.GroupID)
	require.Equal(t, "priority", *frozen.Result.ServiceTier)
	require.Equal(t, 2.0, *frozen.User.Notification.BalanceNotifyThreshold)
	require.Equal(t, 2.0, *frozen.APIKey.Group.AudioPrice.RealtimePerMin)
	require.Equal(t, []string{"1K"}, frozen.Result.ImageOutputSizes)
	require.Equal(t, 1, frozen.Result.ImageSizeBreakdown["1K"])
	require.Equal(t, 2.0, frozen.Subscription.Plan.GroupRateMultipliers[7])
}

// GetModelPricing returns fixed input/output token prices for claude-sonnet-4.
// Other names return nil, giving ordering tests a stable priced model and a
// clear missing-price result without consulting a live catalogue.
func (recordPriceCatalog) GetModelPricing(model string) *pricing.CatalogModelPricing {
	if model != "claude-sonnet-4" {
		return nil
	}
	return &pricing.CatalogModelPricing{InputCostPerToken: 3e-6, OutputCostPerToken: 15e-6}
}

// ForceUpdate accepts the catalogue refresh interface while leaving fixed fixture prices unchanged.
func (recordPriceCatalog) ForceUpdate() error { return nil }

// TestRecordCyberRetainsFailureFactAndDoesNotResettleLogFailure records Cyber
// classification with either failed funds or failed analysis writes. It checks one
// financial call, retained rows, zero charge on settlement failure, and the unchanged source flag.
func TestRecordCyberRetainsFailureFactAndDoesNotResettleLogFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		core, funds, logs, in, _ := recordFixture()
		if failed {
			funds.err = errors.New("settlement failed")
		} else {
			logs.bestErr = errors.New("queue full")
			logs.errorSync = errors.New("write failed")
		}
		core.RecordCyber(context.Background(), in)
		require.Equal(t, 1, funds.calls)
		require.NotEmpty(t, logs.rows)
		for _, row := range logs.rows {
			require.Equal(t, RequestTypeCyberBlocked, row.RequestType)
			if failed {
				require.Zero(t, row.ActualCost)
			}
		}
		require.False(t, in.CyberBlocked)
	}
}

// TestRecordCyberPreservesZeroUsage supplies an eligible model with empty measured
// usage to the Cyber recorder. It checks a zero financial command, a retained
// zero-cost analysis row, and the Cyber request classification.
func TestRecordCyberPreservesZeroUsage(t *testing.T) {
	core, funds, logs, in, _ := recordFixture()
	in.Result.Usage = TokenUsage{}
	core.RecordCyber(context.Background(), in)
	require.Equal(t, 1, funds.calls)
	require.Zero(t, funds.command.BillableAmountUSD)
	require.NotEmpty(t, logs.rows)
	require.Zero(t, logs.rows[0].ActualCost)
	require.Equal(t, RequestTypeCyberBlocked, logs.rows[0].RequestType)
}
