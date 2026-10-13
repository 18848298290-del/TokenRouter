package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
)

// TestWSLocalWarmupResultRoundTrip converts a local warmup through the WS and
// forwarding result types. The warmup flag and request ID survive, and the
// result remains excluded from scheduling success.
func TestWSLocalWarmupResultRoundTrip(t *testing.T) {
	original := &forward.OpenAIResult{LocalWarmup: true, RequestID: "resp_warmup", OpenAIWSMode: true, UpstreamTerminalEvent: "response.completed"}
	result := ForwardResultFromWS(ProjectWSResult(original))
	require.True(t, result.LocalWarmup)
	require.Equal(t, original.RequestID, result.RequestID)
	require.False(t, result.SucceededForScheduling())
}
