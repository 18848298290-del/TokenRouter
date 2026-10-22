package textattempt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// TestGenericResponsesCarriesSelectedDiagnosticPolicy selects two providers through
// the actual bridge before forwarding. Each callback receives its own header
// policy and provider identity instead of the context retained before selection.
func TestGenericResponsesCarriesSelectedDiagnosticPolicy(t *testing.T) {
	for _, platform := range []string{"gemini", "antigravity", "anthropic"} {
		t.Run(platform, func(t *testing.T) {
			var saved telemetry.RequestRecord
			root := telemetry.WithRequestCapture(t.Context(), telemetry.RequestRecord{RequestID: "local"}, func(r telemetry.RequestRecord) { saved = r })
			root, _ = timing.WithStages(root, time.Now(), nil)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(root)
			target := gatewaycapture.NewExecutionProvider(&provider.Record{ID: 7, Platform: platform, Type: "oauth", Extra: map[string]any{provider.ProviderExtraUpstreamRequestIDHeader: "X-Client-Request-ID"}})
			deps := &messageExecutionDependencies{
				geminiAvailable: true, antigravityAvailable: true,
				selectProvider: func(context.Context, *int64, string, string, map[int64]struct{}) (*gatewaycapture.SelectionResult, error) {
					return &gatewaycapture.SelectionResult{Provider: target}, nil
				},
				reportSchedule: func(*gatewaycapture.SelectionResult, int64, bool, *forwardcore.MessagesResult) {},
			}
			forward := func(ctx context.Context, _ *gin.Context, _ *gatewaycapture.ExecutionProvider, _ []byte, _ *requeststate.ParsedRequest) (*forwardcore.MessagesResult, error) {
				exchange := timing.BeginHTTPTrace(ctx)
				timing.ObserveResponse(exchange, &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": {"default"}, "X-Client-Request-Id": {"custom"}}})
				return nil, nil
			}
			deps.forwardGeminiResponses, deps.forwardAntigravityResponses, deps.forwardResponses = forward, forward, forward
			bridge := &genericResponsesAttemptBridge{messageAttemptBridge: messageAttemptBridge{c: c, fixed: deps, apiKey: &apikey.APIKey{}}, requestCtx: root}
			_, err := bridge.Select(nil)
			require.NoError(t, err)
			bridge.Forward(textflow.AttemptState{})
			target = gatewaycapture.NewExecutionProvider(&provider.Record{ID: 8, Platform: platform, Type: "oauth"})
			_, err = bridge.Select(nil)
			require.NoError(t, err)
			bridge.Forward(textflow.AttemptState{})
			require.Len(t, saved.Attempts, 2)
			require.Equal(t, int64(7), saved.Attempts[0].ProviderID)
			require.Equal(t, "custom", saved.Attempts[0].RequestID)
			require.Equal(t, int64(8), saved.Attempts[1].ProviderID)
			require.Equal(t, "default", saved.Attempts[1].RequestID)
		})
	}
}
