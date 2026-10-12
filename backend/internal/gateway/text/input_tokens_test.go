package text

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// inputTokenFixture supplies input-count outcomes and records selection,
// exclusions and terminal handling.
type inputTokenFixture struct {
	// ctx is returned for same-provider waiting. Tests supply a nonnil context,
	// including an already canceled one to stop before the next scripted count.
	ctx context.Context // context used for retry waiting
	// results retains the test's failure pointers. Forward advances its local
	// queue and requires enough entries for the requested number of count
	// invocations.
	results             []*AttemptFailure // ordered retained input-count failure outcomes
	selected, forwarded int               // selection callback invocations and count execution calls
	excluded            []bool            // provider-one exclusion observations by selection
	failed, exhausted   bool              // terminal failure recorded and exhaustion callback ran
}

// Context returns the fixture context used during same-provider count waiting.
func (p *inputTokenFixture) Context() context.Context { return p.ctx }

// Select returns provider one with one same-provider retry and records its
// exclusion. The fixture observes the supplied exclusion set instead of
// enforcing it, so tests inspect the loop's produced state.
func (p *inputTokenFixture) Select(ids map[int64]struct{}) (Selection, bool, error) {
	p.selected++
	_, yes := ids[1]
	p.excluded = append(p.excluded, yes)
	return Selection{Provider: provider.ProviderSnapshot{ID: 1}, RetryLimit: 1}, true, nil
}

// SelectionFailed records terminal input-count selection handling for fixture
// assertions.
func (p *inputTokenFixture) SelectionFailed(error, *AttemptFailure, bool) { p.failed = true }

// Forward counts execution and removes the next supplied input-count outcome.
// Tests provide enough queued outcomes for the loop; nil represents a
// successful count and resources are outside this fixture.
func (p *inputTokenFixture) Forward(Selection) *AttemptFailure {
	p.forwarded++
	value := p.results[0]
	p.results = p.results[1:]
	return value
}

// ForwardFailed records an ordinary terminal input-count error for assertions.
func (p *inputTokenFixture) ForwardFailed(Selection, error) { p.failed = true }

// Exhausted records classified input-count retry exhaustion for this fixture.
func (p *inputTokenFixture) Exhausted(*AttemptFailure) { p.exhausted = true }

// TestInputTokensRetryBoundaries supplies disallowed retries, zero switching
// allowance, canceled waiting and one allowed switch. The loop must stop at
// each stated boundary and produce exclusion evidence before the final
// successful count.
func TestInputTokensRetryBoundaries(t *testing.T) {
	failure := errors.New("failed")
	t.Run("不可重试错误立即展示", func(t *testing.T) {
		p := &inputTokenFixture{ctx: context.Background(), results: []*AttemptFailure{{Cause: failure, Policy: &failover.FailureInfo{}}}}
		RunInputTokens(p, 3)
		require.True(t, p.exhausted)
		require.Equal(t, 1, p.forwarded)
	})
	t.Run("换号预算零保持一次尝试", func(t *testing.T) {
		p := &inputTokenFixture{ctx: context.Background(), results: []*AttemptFailure{{Cause: failure, Policy: &failover.FailureInfo{RetryNext: true}}}}
		RunInputTokens(p, 0)
		require.True(t, p.exhausted)
		require.Equal(t, 1, p.selected)
	})
	t.Run("同提供商等待取消不追加执行", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		// Cancel before the scripted same-provider failure enters its delay. The
		// loop must stop waiting before it consumes another queued forwarding outcome.
		cancel()
		p := &inputTokenFixture{ctx: ctx, results: []*AttemptFailure{{Cause: failure, Policy: &failover.FailureInfo{RetryNext: true, RetryableOnSameProvider: true}}}}
		RunInputTokens(p, 3)
		require.Equal(t, 1, p.selected)
		require.Equal(t, 1, p.forwarded)
		require.False(t, p.exhausted)
	})
	t.Run("切换时排除失败提供商", func(t *testing.T) {
		p := &inputTokenFixture{ctx: context.Background(), results: []*AttemptFailure{{Cause: failure, Policy: &failover.FailureInfo{RetryNext: true}}, nil}}
		RunInputTokens(p, 1)
		require.Equal(t, []bool{false, true}, p.excluded)
		require.Equal(t, 2, p.forwarded)
	})
}
