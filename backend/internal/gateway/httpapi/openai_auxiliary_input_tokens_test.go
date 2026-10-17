package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// delayedCountFailure pauses its one failing read until another count completes.
type delayedCountFailure struct {
	started chan struct{} // body read entered
	release chan struct{} // delayed failure release
	once    sync.Once     // read notification guard
}

// Native Responses preflight uses local counting when a selected target cannot
// provide the official count endpoint. The fixtures compare a custom relay,
// Grok, and an official API-key target returning 404, checking positive
// estimates and whether a network request was attempted.

// TestForwardResponsesInputTokensCustomRelayUsesLocalEstimate supplies a custom
// OpenAI relay with text and tools. It checks positive response.input_tokens
// output and confirms the recording transport receives no upstream request.
func TestForwardResponsesInputTokensCustomRelayUsesLocalEstimate(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil)

	upstream := &auxiliaryHTTPRecorder{}
	svc := newAuxiliaryFixture(auxiliaryFixtureInputs{
		transport: upstream,
	})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 159, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Concurrency: 1,
			Credentials: map[string]any{"api_key": "relay-key", "base_url": "https://relay.example/v1"},
		},
	}
	body := []byte(`{"model":"gpt-5.4","instructions":"Be concise.","input":"hello world","tools":[{"type":"function","name":"lookup","description":"Look up a value","parameters":{"type":"object"}}]}`)

	err := svc.ForwardResponsesInputTokens(context.Background(), c, provider, body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "response.input_tokens", gjson.Get(recorder.Body.String(), "object").String())
	require.Positive(t, gjson.Get(recorder.Body.String(), "input_tokens").Int())
	require.Nil(t, upstream.lastReq)
}

// TestForwardResponsesInputTokensGrokUsesLocalEstimate supplies Grok OAuth
// input to native preflight. It checks a successful response.input_tokens
// object and positive local count despite no configured upstream transport for
// this fixture.
func TestForwardResponsesInputTokensGrokUsesLocalEstimate(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil)
	svc := newAuxiliaryFixture(auxiliaryFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 160, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}

	err := svc.ForwardResponsesInputTokens(context.Background(), c, provider, []byte(`{"model":"grok-4.1","input":"hello world"}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "response.input_tokens", gjson.Get(recorder.Body.String(), "object").String())
	require.Positive(t, gjson.Get(recorder.Body.String(), "input_tokens").Int())
}

// TestForwardResponsesInputTokensUpstream404FallsBackLocally returns a generic
// 404 from the official counting target. It checks an upstream request occurred
// and the client receives a successful native object with a positive local
// estimate.
func TestForwardResponsesInputTokensUpstream404FallsBackLocally(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil)
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"Invalid URL"}}`)),
	}}
	svc := newAuxiliaryFixture(auxiliaryFixtureInputs{
		transport: upstream,
	})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 171, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Concurrency: 1,
			Credentials: map[string]any{"api_key": "official-key", "base_url": "https://api.openai.com/v1"},
		},
	}
	body := []byte(`{"model":"gpt-5.4","instructions":"Be concise.","input":"hello world"}`)

	err := svc.ForwardResponsesInputTokens(context.Background(), c, provider, body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "response.input_tokens", gjson.Get(recorder.Body.String(), "object").String())
	require.Positive(t, gjson.Get(recorder.Body.String(), "input_tokens").Int())
	require.NotNil(t, upstream.lastReq)
}

// TestNativeInputTokensFinalizesObservedExchange uses the official native counting
// path through real HTTP observation. Validation, read, cancellation, and fallback
// outcomes keep the upstream attempt terminal without creating a usage record.
func TestNativeInputTokensFinalizesObservedExchange(t *testing.T) {
	for _, mode := range []string{"success", "missing_count", "read_failure", "canceled", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			var saved telemetry.RequestRecord
			ctx := telemetry.WithRequestCapture(t.Context(), telemetry.RequestRecord{RequestID: "count"}, func(r telemetry.RequestRecord) { saved = r })
			payload := `{"object":"response.input_tokens","input_tokens":42}`
			status := 200
			if mode == "missing_count" {
				payload = `{}`
			}
			if mode == "unsupported" {
				status = 404
				payload = `{"error":{"message":"not found"}}`
			}
			response := &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"selected"}}, Body: io.NopCloser(strings.NewReader(payload))}
			if mode == "read_failure" {
				response.Body = &openAIImagesReadErrorBody{err: io.ErrUnexpectedEOF}
			}
			if mode == "canceled" {
				response.Body = &openAIImagesReadErrorBody{err: context.Canceled}
			}
			transport := &diagnosticHTTPTransport{pool: httpclient.NewUpstreamPool(), responses: []*http.Response{response}}
			svc := newAuxiliaryFixture(auxiliaryFixtureInputs{transport: transport})
			target := gatewayprovider.NewExecutionProvider(&providercore.Record{ID: 7, Platform: "openai", Type: "apikey", Credentials: map[string]any{"api_key": "sk-test"}})
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil).WithContext(ctx)
			SetOpsSelectedProvider(c, target.Record.RoutingSnapshot())
			err := svc.ForwardResponsesInputTokens(c.Request.Context(), c, target, []byte(`{"model":"gpt-5","input":"hello"}`))
			expected := "failed"
			if mode == "success" {
				expected = "completed"
				require.NoError(t, err)
			}
			if mode == "unsupported" {
				require.NoError(t, err)
				require.Equal(t, 200, c.Writer.Status())
			}
			require.Len(t, saved.Attempts, 1)
			require.Equal(t, expected, saved.Attempts[0].Outcome)
			require.Equal(t, "selected", saved.Attempts[0].RequestID)
			require.Empty(t, saved.UsageWriteState)
		})
	}
}

// Read blocks until release is closed, then returns cancellation without consuming
// bytes. The caller must eventually close release; closing this response body
// alone does not unblock its controlled read.
func (r *delayedCountFailure) Read([]byte) (int, error) {
	// Notify only once so the test can start replacement work after the original
	// response reaches body processing, then release this older failure afterward.
	r.once.Do(func() { close(r.started) })
	<-r.release
	return 0, context.Canceled
}

// Close releases no external resources owned by this controlled body fixture.
func (*delayedCountFailure) Close() error { return nil }

// TestNativeInputTokensDelayedFailureKeepsAttemptOwnership completes a later count
// before an older canceled body returns. Same-provider and cross-provider cases
// must retain both selected IDs and leave the newer successful attempt completed.
func TestNativeInputTokensDelayedFailureKeepsAttemptOwnership(t *testing.T) {
	for _, nextProvider := range []int64{7, 8} {
		var saved telemetry.RequestRecord
		root := telemetry.WithRequestCapture(t.Context(), telemetry.RequestRecord{RequestID: "count"}, func(r telemetry.RequestRecord) { saved = r })
		started := make(chan struct{})
		release := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		transport := &diagnosticHTTPTransport{pool: httpclient.NewUpstreamPool(), responses: []*http.Response{
			{StatusCode: 200, Header: http.Header{"X-Request-Id": {"first"}}, Body: &delayedCountFailure{started: started, release: release}},
			{StatusCode: 200, Header: http.Header{"X-Request-Id": {"second"}}, Body: io.NopCloser(strings.NewReader(`{"input_tokens":42}`))},
		}}
		svc := newAuxiliaryFixture(auxiliaryFixtureInputs{transport: transport})
		run := func(id int64) error {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil).WithContext(root)
			target := gatewayprovider.NewExecutionProvider(&providercore.Record{ID: id, Platform: "openai", Type: "apikey", Credentials: map[string]any{"api_key": "sk-test"}})
			SetOpsSelectedProvider(c, target.Record.RoutingSnapshot())
			return svc.ForwardResponsesInputTokens(c.Request.Context(), c, target, []byte(`{"model":"gpt-5","input":"hello"}`))
		}
		done := make(chan error, 1)
		// Pause the first body after its dispatch reference is captured, allowing
		// replacement work to finish before the old error reaches its deferred observer.
		go func() { done <- run(7) }()
		<-started
		require.NoError(t, run(nextProvider))
		unblock()
		require.Error(t, <-done)
		require.Len(t, saved.Attempts, 2)
		require.Equal(t, "failed", saved.Attempts[0].Outcome)
		require.Equal(t, "completed", saved.Attempts[1].Outcome)
		require.Equal(t, "first", saved.Attempts[0].RequestID)
		require.Equal(t, "second", saved.Attempts[1].RequestID)
		require.Equal(t, nextProvider, saved.Attempts[1].ProviderID)
	}
}
