package media

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// alphaPortsStub scripts search outcomes while counting adapter callback
// invocations.
type alphaPortsStub struct {
	// outcomes is indexed by the one-based selection count. Each test supplies
	// enough entries and keeps referenced results stable while the loop
	// executes serially.
	outcomes []AlphaOutcome // shared ordered search outcome script
	// selected advances the scripted outcome index; released, reported,
	// completed and switched count their respective callbacks. Serial tests
	// compare these observations to distinguish retry, switch and completion
	// paths.
	selected, released, reported, completed, switched int // selection callback invocations, release callback invocations, scheduling feedback calls, successful result submissions and adapter switch calls
	// exclusions stores a new map per SelectAlpha call so later loop mutations
	// cannot rewrite the exclusion evidence asserted for an earlier selection.
	exclusions []map[int64]struct{} // owned exclusion-map snapshots per selection
	// cancel is invoked only when ObserveAlpha sees a retry event. Tests use
	// that synchronous event before the delay to cancel waiting
	// deterministically.
	cancel context.CancelFunc // optional retry-observer cancellation callback
}

// SelectAlpha records a copied exclusion set and returns provider one with
// retry limit one. It observes exclusions for assertions but keeps choosing
// that provider, using selected to advance the outcome script.
func (p *alphaPortsStub) SelectAlpha(_ context.Context, excluded map[int64]struct{}) (AlphaSelection, bool, error) {
	p.selected++
	snapshot := make(map[int64]struct{}, len(excluded))
	for id := range excluded {
		snapshot[id] = struct{}{}
	}
	p.exclusions = append(p.exclusions, snapshot)
	return AlphaSelection{Provider: provider.ProviderSnapshot{ID: 1}, RetryLimit: 1}, true, nil
}

// AcquireAlpha returns a fixture release callback and reports successful
// admission. Calling release increments the observation count; this stub
// acquires no external provider resource and runs under serial tests.
func (p *alphaPortsStub) AcquireAlpha(context.Context, AlphaSelection) (func(), bool) {
	return func() { p.released++ }, true
}

// ForwardAlpha returns the outcome indexed by the preceding SelectAlpha
// call. The test supplies enough scripted entries, and the result retains
// each entry's pointer fields for later loop handling.
func (p *alphaPortsStub) ForwardAlpha(context.Context, AlphaSelection, []byte) AlphaOutcome {
	return p.outcomes[p.selected-1]
}

// ReportAlpha counts scheduling feedback calls made by the search loop.
func (p *alphaPortsStub) ReportAlpha(context.Context, AlphaSelection, *AlphaResult, bool, error) {
	p.reported++
}

// CompleteAlpha counts successful nonnil search results submitted for
// fixture completion.
func (p *alphaPortsStub) CompleteAlpha(context.Context, AlphaSelection, *AlphaResult) { p.completed++ }

// SwitchAlpha counts adapter switch bookkeeping calls during search
// recovery decisions.
func (p *alphaPortsStub) SwitchAlpha(AlphaSelection) { p.switched++ }

// StopAlpha429 returns false for every scripted status and switch count.
// These tests therefore observe budget exhaustion and output handling
// without a status-specific adapter stop changing the loop path.
func (p *alphaPortsStub) StopAlpha429(AlphaSelection, int, int) bool { return false }

// ObserveAlpha invokes an optional cancellation callback when retry is
// observed. The loop emits that event before waiting, so the fixture can
// test canceled delay without relying on wall-clock sleep.
func (p *alphaPortsStub) ObserveAlpha(e AlphaEvent) {
	// Cancel at synchronous retry observation before the loop enters its wait,
	// making the canceled-delay assertion independent of scheduler timing.
	if e.Kind == "retry" && p.cancel != nil {
		p.cancel()
	}
}

// AlphaClientGone keeps the fixture client available regardless of context
// cancellation. Tests can therefore isolate cancellation in the retry delay
// from adapter-reported client departure paths.
func (p *alphaPortsStub) AlphaClientGone() bool { return false }

// TestAlphaSearchSameProviderBudgetAndCanceledDelay first recovers on the
// same provider without excluding it. A second run cancels at retry
// observation and checks that the acquired slot ends without another
// selection or completion.
func TestAlphaSearchSameProviderBudgetAndCanceledDelay(t *testing.T) {
	// Use a retryable failure with a short configured wait, so the observer
	// can exercise cancellation at the same recovery phase as ordinary
	// same-provider success.
	retry := AlphaOutcome{Err: errors.New("retry"), Failure: &failover.FailureInfo{RetryableOnSameProvider: true, SameProviderRetryDelay: time.Millisecond}}
	p := &alphaPortsStub{outcomes: []AlphaOutcome{retry, {Result: &AlphaResult{Calls: 1}}}}
	require.Nil(t, RunAlphaSearch(context.Background(), nil, 3, p))
	require.Equal(t, 2, p.selected)
	require.Equal(t, 2, p.released)
	require.Equal(t, 1, p.completed)
	require.Zero(t, p.switched)
	require.Empty(t, p.exclusions[1], "同提供商恢复不能排除刚才候选")
	ctx, cancel := context.WithCancel(context.Background())
	// Release the test context registration on every return path, while the
	// retry observer can independently cancel it before the loop waits.
	defer cancel()
	p = &alphaPortsStub{outcomes: []AlphaOutcome{retry}, cancel: cancel}
	require.Nil(t, RunAlphaSearch(ctx, nil, 3, p))
	require.Equal(t, 1, p.selected)
	require.Equal(t, 1, p.released)
	require.Zero(t, p.completed)
}

// TestAlphaSearchOutputFailureAndUnbillableResponse returns a classified
// error after output changes, then a nil-result success. It checks failure
// reporting together with exhaustion and suppresses completion for successful
// forwarding without search observations.
func TestAlphaSearchOutputFailureAndUnbillableResponse(t *testing.T) {
	p := &alphaPortsStub{outcomes: []AlphaOutcome{{Err: errors.New("after output"), Failure: &failover.FailureInfo{}, OutputChanged: true}}}
	failure := RunAlphaSearch(context.Background(), nil, 3, p)
	require.Equal(t, "exhausted", failure.Stage)
	require.Equal(t, 1, p.reported)
	require.Zero(t, p.switched)
	require.Zero(t, p.completed)
	p = &alphaPortsStub{outcomes: []AlphaOutcome{{}}}
	require.Nil(t, RunAlphaSearch(context.Background(), nil, 3, p))
	require.Equal(t, 1, p.reported)
	require.Zero(t, p.completed, "成功透传但无搜索计量不生成完成任务")
}
