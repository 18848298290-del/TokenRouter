package httpapi

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
)

// TestOpenAIFirstOutputTimeoutForReasoningEffort configures base and
// high-effort first-output budgets. It checks low, high, xhigh and max
// selections, including max extracted from a compatible request body.
func TestOpenAIFirstOutputTimeoutForReasoningEffort(t *testing.T) {
	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{Response: OpenAIResponseOptions{OpenAIFirstOutputTimeoutSeconds: 120, OpenAIHighEffortFirstOutputTimeoutSeconds: 300}}})

	require.Equal(t, 120*time.Second, svc.Output.FirstOutputTimeout("low"))
	require.Equal(t, 300*time.Second, svc.Output.FirstOutputTimeout("high"))
	require.Equal(t, 300*time.Second, svc.Output.FirstOutputTimeout("xhigh"))
	require.Equal(t, 300*time.Second, svc.Output.FirstOutputTimeout("max"))

	// Extract max from a compatible request body before checking its timeout
	// class; this fixture observes budget selection and performs no upstream forwarding.
	effort := requeststate.ExtractOpenAIReasoningEffortFromBody(
		[]byte(`{"model":"deepseek-v4-flash","reasoning":{"effort":"max"}}`))
	require.NotNil(t, effort)
	require.Equal(t, 300*time.Second, svc.Output.FirstOutputTimeout(*effort))
}
