package pricingcontract

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

func TestQoderNativeChainCompletionFailures(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		billingFail, logFail bool
	}{{name: "settlement-failure", billingFail: true}, {name: "record-failure", logFail: true}} {
		t.Run(tc.name, func(t *testing.T) {
			provider, platform, client := gatewaytestkit.NewDefaultQoderFixture()
			client.Body = qoderWrappedSSELineForTest(t, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "served"}}}}) + qoderWrappedSSELineForTest(t, map[string]any{"usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 3}}) + qoderWrappedErrorSSELineForTest(t, 502, map[string]any{"code": "500", "message": "fixture failure"})
			usageRepo := &gatewaytestkit.UsageLogStore{}
			billingRepo := &gatewaytestkit.SettlementStore{}
			if tc.billingFail {
				billingRepo.Err = errors.New("fixture billing unavailable")
			}
			if tc.logFail {
				usageRepo.Err = usage.MarkUsageLogCreateNotPersisted(errors.New("fixture usage unavailable"))
			}
			completion := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &gatewaytestkit.UserStore{}, &gatewaytestkit.SubscriptionStore{})
			rec := httptest.NewRecorder()
			body := []byte(`{"model":"auto","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
			// Feed the provider and upstream outcome into completion to check usage recording after a stream failure.
			executor, input := platform.Runtime.PrepareQoderTarget(gatewayhttp.QoderRequestMetadata(nil), provider, body, protocol.ProtocolOpenAIChatCompletions, "auto")
			completed, released, bound := 0, 0, 0
			var completionErr error
			ports := gateway.RequestPorts{CanFailover: func(error) bool { return true }, Select: func(context.Context, map[int64]struct{}) (*gateway.Selection, error) {
				return &gateway.Selection{Acquired: true, Release: func() { released++ }, Executor: executor, Input: input, Bind: func(context.Context, upstream.AttemptResult) { bound++ }, Complete: func(ctx context.Context, r upstream.AttemptResult) {
					completed++
					completionErr = completion.RecordMessages(ctx, &gatewaycapture.MessagesCapture{Result: forwardcore.MessagesFromAttempt(r), APIKey: &apikey.APIKey{ID: 505}, User: &identity.User{ID: 605}, Provider: gatewaycapture.ExecutionCompletionRecord(gatewaycapture.NewExecutionProvider(provider))})
				}}, nil
			}}
			err := gateway.NewQoderUseCase(3, time.Second).Run(context.Background(), gateway.Request{Stream: true}, ports, &gateway.OutputTracker{Sink: gatewayhttp.ResponseSink{Writer: rec}})
			require.Error(t, err)
			require.Contains(t, rec.Body.String(), "served")
			require.Len(t, client.Requests, 1)
			require.Equal(t, 1, completed)
			require.Equal(t, 1, released)
			require.Zero(t, bound)
			require.Equal(t, 1, billingRepo.Calls)
			require.Equal(t, 1, usageRepo.Calls)
			require.Equal(t, 12, usageRepo.LastLog.InputTokens)
			require.Equal(t, 3, usageRepo.LastLog.OutputTokens)
			if tc.billingFail {
				require.ErrorIs(t, completionErr, billingRepo.Err)
				require.Zero(t, usageRepo.LastLog.ActualCost)
			} else {
				require.NoError(t, completionErr)
			}
		})
	}
}

func qoderWrappedSSELineForTest(t *testing.T, inner map[string]any) string {
	t.Helper()
	body, err := json.Marshal(inner)
	require.NoError(t, err)
	wrapper, err := json.Marshal(map[string]string{"body": string(body)})
	require.NoError(t, err)
	return "data: " + string(wrapper) + "\n\n"
}

func qoderWrappedErrorSSELineForTest(t *testing.T, statusCode int, inner map[string]any) string {
	t.Helper()
	body, err := json.Marshal(inner)
	require.NoError(t, err)
	wrapper, err := json.Marshal(map[string]any{
		"body":            string(body),
		"statusCodeValue": statusCode,
	})
	require.NoError(t, err)
	return "data: " + string(wrapper) + "\n\n"
}
