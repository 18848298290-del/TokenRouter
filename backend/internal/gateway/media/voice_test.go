package media

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// voicePortsStub scripts voice outcomes and counts selection, release and
// completion.
type voicePortsStub struct {
	// selected determines the candidate ID and outcome position. released
	// counts each fixture release callback, while completed records nil-error
	// completion; serial tests relate these observations to the four-selection
	// limit.
	selected, released, completed int // candidate sequence number, release callback invocations and successful completion calls
	// outcomes retains one entry for each selected candidate. The test
	// supplies sufficient entries, and ForwardVoice returns their result
	// references without copying them.
	outcomes []VoiceOutcome // shared ordered voice outcome script
}

// SelectVoice advances selection count and returns that count as provider
// ID. It ignores exclusions and context so the fixture isolates attempt
// accounting from real routing and cancellation behavior.
func (p *voicePortsStub) SelectVoice(context.Context, map[int64]struct{}) (provider.ProviderSnapshot, bool, error) {
	p.selected++
	return provider.ProviderSnapshot{ID: int64(p.selected)}, true, nil
}

// AcquireVoice returns a callable release that increments the fixture
// observation count. Admission succeeds for each candidate, and the
// callback owns no real provider resource in these serial tests.
func (p *voicePortsStub) AcquireVoice(context.Context, provider.ProviderSnapshot) (func(), bool) {
	return func() { p.released++ }, true
}

// ForwardVoice returns the scripted outcome for the preceding selection
// count. The caller supplies a long enough script, and this fixture ignores
// request content because the test measures attempt accounting.
func (p *voicePortsStub) ForwardVoice(context.Context, provider.ProviderSnapshot, VoiceRequest) VoiceOutcome {
	return p.outcomes[p.selected-1]
}

// CompleteVoice increments the fixture observation count for successful
// voice completion.
func (p *voicePortsStub) CompleteVoice(context.Context, provider.ProviderSnapshot, VoiceRequest, *VoiceResult) {
	p.completed++
}

// TestVoiceRetryUsesOriginalFourAttemptBudget exhausts four retryable
// outcomes and compares selection and release totals. A second script
// succeeds after one failure and checks two releases with a single
// completion.
func TestVoiceRetryUsesOriginalFourAttemptBudget(t *testing.T) {
	failure := VoiceOutcome{Err: errors.New("upstream"), RetryNext: true}
	ports := &voicePortsStub{outcomes: []VoiceOutcome{failure, failure, failure, failure}}
	result := RunVoice(context.Background(), VoiceRequest{Endpoint: "tts"}, ports)
	require.NotNil(t, result)
	require.ErrorIs(t, result.Last, failure.Err)
	require.Equal(t, 4, ports.selected)
	require.Equal(t, 4, ports.released)
	require.Zero(t, ports.completed)
	success := &voicePortsStub{outcomes: []VoiceOutcome{failure, {Result: &VoiceResult{RequestID: "audio"}}}}
	require.Nil(t, RunVoice(context.Background(), VoiceRequest{Endpoint: "tts"}, success))
	require.Equal(t, 2, success.released)
	require.Equal(t, 1, success.completed)
}
