package textattempt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway/execution"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Opening Messages execution must reuse fixed service bindings while
// allocating fresh request state. The observation fixture mutates source
// metadata after an errored result is converted, protecting partial usage and
// the specific timing, header and size copies consumed by the text loop.

// TestFixedMessagesOpenUsesBoundDependenciesAndExplicitState opens two
// requests on one runtime. Both bridges must share the bound dependency
// pointer while keeping distinct state and preserving the first request’s
// model after the second opens.
func TestFixedMessagesOpenUsesBoundDependenciesAndExplicitState(t *testing.T) {
	dependencies := &messageExecutionDependencies{}
	runtime := &Runtime{dependencies: dependencies}
	var previous *messageAttemptBridge
	for _, model := range []string{"first", "second"} {
		writer := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(writer)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		started := false
		sink := &gatewayhttp.MessagesOutput{ResponseSink: gatewayhttp.ResponseSink{Writer: c.Writer}, HTTP: c, Log: zap.NewNop(), StreamStarted: &started}
		input := execution.Request{Model: model, Body: []byte(model), UserID: 5, Funding: execution.FundingState{Key: &apikey.APIKey{ID: 9}}}
		ports, err := runtime.Open(context.Background(), input, sink)
		require.NoError(t, err)
		state, ok := ports.(*messageAttemptBridge)
		require.True(t, ok)
		require.Same(t, dependencies, state.fixed)
		require.Equal(t, model, state.reqModel)
		require.Equal(t, int64(9), state.apiKey.ID)
		if previous != nil {
			require.NotSame(t, previous, state)
			require.Equal(t, "first", previous.reqModel)
		}
		previous = state
	}
}

// TestMessageObservedAttemptRetainsPartialUsageAndCopiesMetadata converts
// usage with a cancellation error, then mutates original timing, headers and
// output sizes. The attempt must remain Served/HasUsage and keep the captured
// metadata values.
func TestMessageObservedAttemptRetainsPartialUsageAndCopiesMetadata(t *testing.T) {
	first := 12
	result := &forwardcore.MessagesResult{Model: "model", Usage: upstream.TokenUsage{OutputTokens: 2}, UpstreamHeaders: http.Header{"X-Request-Id": []string{"upstream"}}, FirstTokenMs: &first, ImageOutputSizes: []string{"1K"}}
	observed := messageObservedAttempt(result, context.Canceled)
	first = 99
	http.Header(result.UpstreamHeaders).Set("X-Request-Id", "changed")
	result.ImageOutputSizes[0] = "4K"
	require.True(t, observed.Cancelled)
	require.True(t, observed.HasUsage)
	require.True(t, observed.Served)
	require.Equal(t, 2, observed.Usage.OutputTokens)
	require.Equal(t, 12, *observed.FirstTokenMs)
	require.Equal(t, "upstream", observed.UpstreamHeaders.Get("X-Request-Id"))
	require.Equal(t, []string{"1K"}, observed.ImageOutputSizes)
}
