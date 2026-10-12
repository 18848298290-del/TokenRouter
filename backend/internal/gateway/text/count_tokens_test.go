package text

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// countFixture supplies ordered count outcomes and records selection,
// preparation and failed-session cleanup.
type countFixture struct {
	// ctx is returned to failover decisions. Tests supply an active or canceled
	// nonnil context before the scripted failure enters cancellation or retry
	// handling.
	ctx context.Context // context used for retry decisions
	// outcomes retains failure pointers supplied by the test. Forward advances
	// the local slice header and requires another queued entry for every
	// invocation.
	outcomes                     []*AttemptFailure // ordered retained count failure outcomes
	selected, prepared, released int               // selection callback invocations, payload preparation calls and failed-session cleanup calls
	excluded                     []bool            // provider-one exclusion observations by selection
	canceled, exhausted, failed  bool              // cancellation callback ran, exhaustion callback ran and terminal failure recorded
}

// Context returns the fixture context used by count retry decisions.
func (p *countFixture) Context() context.Context { return p.ctx }

// Select returns a numbered candidate and records whether provider one was
// excluded. The fixture observes the supplied set rather than enforcing it,
// letting assertions inspect loop-produced exclusions after failed forwarding.
func (p *countFixture) Select(ids map[int64]struct{}) (Selection, error) {
	p.selected++
	_, excluded := ids[1]
	p.excluded = append(p.excluded, excluded)
	return Selection{Provider: provider.ProviderSnapshot{ID: int64(p.selected), Platform: "anthropic"}}, nil
}

// SelectionFailed records terminal count selection failure for fixture
// assertions.
func (p *countFixture) SelectionFailed(error, *AttemptFailure) { p.failed = true }

// Prepare counts fresh payload preparation and permits fixture forwarding to
// continue.
func (p *countFixture) Prepare(Selection) bool { p.prepared++; return true }

// Forward removes and returns the next supplied count failure outcome. Callers
// provide enough outcomes for selected attempts; nil represents success and the
// fixture performs no network or resource work.
func (p *countFixture) Forward(Selection) *AttemptFailure {
	out := p.outcomes[0]
	p.outcomes = p.outcomes[1:]
	return out
}

// ForwardFailed records an ordinary terminal count error for fixture
// assertions.
func (p *countFixture) ForwardFailed(Selection, error) { p.failed = true }

// ReleaseSession counts failed-session cleanup calls made by the count loop.
func (p *countFixture) ReleaseSession(Selection) { p.released++ }

// Exhausted records that classified count failure reached its retry allowance.
func (p *countFixture) Exhausted(Selection, *AttemptFailure) { p.exhausted = true }

// Canceled records cancellation handling reached after failed-session fixture
// cleanup.
func (p *countFixture) Canceled() { p.canceled = true }

// TempUnscheduleRetryableError accepts fixture retry exclusions without
// changing provider state.
func (*countFixture) TempUnscheduleRetryableError(context.Context, int64, *AttemptFailure) {}

// TestCountTokensAttemptBoundaries supplies classified, canceled and ordinary
// count failures through a recording fixture. Assertions protect fresh
// preparation, failed-session release, produced exclusion evidence and
// termination before another attempt.
func TestCountTokensAttemptBoundaries(t *testing.T) {
	t.Run("失败提供商排除且每次重新准备", func(t *testing.T) {
		p := &countFixture{ctx: context.Background(), outcomes: []*AttemptFailure{{Cause: errors.New("retry"), Policy: &failover.FailureInfo{RetryNext: true}}, nil}}
		RunCountTokens(p, 2, nil)
		require.Equal(t, 2, p.selected)
		require.Equal(t, 2, p.prepared)
		require.Equal(t, 1, p.released)
		require.Equal(t, []bool{false, true}, p.excluded)
		require.False(t, p.exhausted)
	})
	t.Run("取消只释放已尝试会话", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		// Cancel before the scripted failure reaches failover so its terminal
		// decision must still release the failed session before observing cancellation.
		cancel()
		p := &countFixture{ctx: ctx, outcomes: []*AttemptFailure{{Cause: context.Canceled, Policy: &failover.FailureInfo{RetryNext: true}}}}
		RunCountTokens(p, 2, nil)
		require.True(t, p.canceled)
		require.Equal(t, 1, p.selected)
		require.Equal(t, 1, p.released)
	})
	t.Run("普通错误不再次选号", func(t *testing.T) {
		p := &countFixture{ctx: context.Background(), outcomes: []*AttemptFailure{{Cause: errors.New("read failed")}}}
		RunCountTokens(p, 2, nil)
		require.True(t, p.failed)
		require.Equal(t, 1, p.selected)
		require.Equal(t, 1, p.released)
	})
}
