package text

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// qoderCompatibleFixture supplies Qoder outcomes and records refresh, provider
// switching, admission and completion observations.
type qoderCompatibleFixture struct {
	// results retains the test's Qoder outcome sequence. Forward advances the
	// local slice header and requires enough entries for each acquisition that
	// reaches scripted forwarding.
	results                                                               []QoderCompatibleOutcome // retained ordered Qoder outcome script
	refresh                                                               QoderRefreshResult       // configured credential-refresh readiness result
	selects, forwards, refreshes, switched, partials, successes, acquires int                      // candidate selection calls, forwarding callback invocations, credential refresh calls, provider switch callbacks, partial-result handling calls, success handling calls and acquisition callback invocations
	knownError                                                            bool                     // fixture forces terminal error classification
	// exclusions holds a copied map for each Select call. Later loop mutations
	// leave those earlier snapshots available for exact per-selection assertions.
	exclusions      []map[int64]struct{} // copied exclusion sets by selection
	pending, failed bool                 // RefreshPending callback ran and terminal failure recorded
}

// Context returns a background context available to the Qoder fixture.
func (*qoderCompatibleFixture) Context() context.Context { return context.Background() }

// Select clones the exclusion set for later assertions and returns a numbered
// candidate. It observes exclusions without enforcing them, allowing tests to
// inspect the loop's pending-refresh and failure history.
func (p *qoderCompatibleFixture) Select(excluded map[int64]struct{}) (Selection, error) {
	// Retain each selection's exclusion evidence before the loop adds more IDs,
	// so later assertions inspect the state that this particular selection received.
	p.exclusions = append(p.exclusions, maps.Clone(excluded))
	p.selects++
	return Selection{Provider: provider.ProviderSnapshot{ID: int64(p.selects)}}, nil
}

// SelectionFailed records that Qoder fixture selection reached terminal
// handling.
func (p *qoderCompatibleFixture) SelectionFailed(error, bool, bool, error) { p.failed = true }

// Acquire counts admission and permits the Qoder fixture attempt to continue.
func (p *qoderCompatibleFixture) Acquire(bool) bool { p.acquires++; return true }

// Forward removes and returns the next supplied Qoder outcome while counting
// execution. Tests supply enough scripted results; it performs no network call,
// output write, or external resource cleanup.
func (p *qoderCompatibleFixture) Forward() QoderCompatibleOutcome {
	p.forwards++
	out := p.results[0]
	p.results = p.results[1:]
	return out
}

// Refresh counts credential-recovery requests and returns its configured
// readiness evidence.
func (p *qoderCompatibleFixture) Refresh() QoderRefreshResult { p.refreshes++; return p.refresh }

// RefreshPending records terminal handling for credentials still pending in
// this fixture.
func (p *qoderCompatibleFixture) RefreshPending(bool) { p.pending = true }

// Partial counts terminal served-partial handling requested by the Qoder loop.
func (p *qoderCompatibleFixture) Partial(QoderCompatibleOutcome) { p.partials++ }

// Canceled accepts Qoder cancellation handling without mutating fixture
// counters.
func (p *qoderCompatibleFixture) Canceled(bool, error) {}

// Failure reports whether knownError or supplied OutputChanged ends this
// scripted attempt. Either condition returns true; otherwise the fixture lets
// final unclassified-error selection continue and performs no output or resource
// work.
func (p *qoderCompatibleFixture) Failure(out QoderCompatibleOutcome) bool {
	return p.knownError || out.OutputChanged
}

// Exhausted records terminal ordinary failure after the provider allowance
// ends.
func (p *qoderCompatibleFixture) Exhausted(error) { p.failed = true }

// Success counts successful terminal handling for a Qoder fixture attempt.
func (p *qoderCompatibleFixture) Success() { p.successes++ }

// Switched counts an allowed move to another Qoder fixture candidate.
func (p *qoderCompatibleFixture) Switched() { p.switched++ }

// TestQoderCompatibleAttemptBoundaries combines partial output, changed output,
// unclassified failure, ready refresh and pending refresh. The assertions
// protect terminal handling, one refreshed reacquisition, and the partial-result
// or success callback counts reached by the actual scripts.
func TestQoderCompatibleAttemptBoundaries(t *testing.T) {
	failure := errors.New("upstream failed")
	t.Run("已观测部分服务只完成一次且不绑定成功", func(t *testing.T) {
		p := &qoderCompatibleFixture{results: []QoderCompatibleOutcome{{Err: failure, Partial: true, CanRefresh: true, CanFailover: true}}}
		RunQoderCompatible(p, 3)
		require.Equal(t, 1, p.partials)
		require.Zero(t, p.successes)
		require.Zero(t, p.refreshes)
		require.Equal(t, 1, p.forwards)
	})
	t.Run("任何已写字节禁止换号和刷新", func(t *testing.T) {
		p := &qoderCompatibleFixture{results: []QoderCompatibleOutcome{{Err: failure, OutputChanged: true, CanRefresh: true, CanFailover: true}}}
		RunQoderCompatible(p, 3)
		require.Equal(t, 1, p.selects)
		require.Zero(t, p.refreshes)
		require.Zero(t, p.switched)
	})
	t.Run("未分类无输出错误保持旧换号资格", func(t *testing.T) {
		p := &qoderCompatibleFixture{results: []QoderCompatibleOutcome{{Err: failure}, {}}}
		RunQoderCompatible(p, 3)
		require.Equal(t, 2, p.selects)
		require.Equal(t, 1, p.successes)
	})
	t.Run("同提供商刷新仍重新获取尝试资源", func(t *testing.T) {
		p := &qoderCompatibleFixture{results: []QoderCompatibleOutcome{{Err: failure, CanRefresh: true}, {}}, refresh: QoderRefreshResult{Ready: true}}
		RunQoderCompatible(p, 3)
		require.Equal(t, 1, p.selects)
		require.Equal(t, 2, p.acquires)
		require.Equal(t, 1, p.successes)
	})
	t.Run("刷新进行中到达提供商上限不再选择", func(t *testing.T) {
		p := &qoderCompatibleFixture{results: []QoderCompatibleOutcome{{Err: failure, CanRefresh: true}}, refresh: QoderRefreshResult{Pending: true}}
		RunQoderCompatible(p, 1)
		require.True(t, p.pending)
		require.Equal(t, 1, p.selects)
		require.Zero(t, p.successes)
	})
}

// TestQoderGatewayRefreshInProgressMarksProviderForFailoverUntilBudgetExhausted
// returns pending credentials for three scripted providers. Selection must
// accumulate their exclusions, observe two continuations, and stop without a
// success callback after its allowance ends.
func TestQoderGatewayRefreshInProgressMarksProviderForFailoverUntilBudgetExhausted(t *testing.T) {
	failure := errors.New("refresh pending")
	result := QoderCompatibleOutcome{Err: failure, CanRefresh: true}
	fixture := &qoderCompatibleFixture{results: []QoderCompatibleOutcome{result, result, result}, refresh: QoderRefreshResult{Pending: true}}
	RunQoderCompatible(fixture, 3)
	require.Equal(t, 3, fixture.selects)
	require.Equal(t, 2, fixture.switched)
	require.True(t, fixture.pending)
	require.Empty(t, fixture.exclusions[0])
	require.Contains(t, fixture.exclusions[1], int64(1))
	require.Contains(t, fixture.exclusions[2], int64(1))
	require.Contains(t, fixture.exclusions[2], int64(2))
	require.Zero(t, fixture.successes)
}
