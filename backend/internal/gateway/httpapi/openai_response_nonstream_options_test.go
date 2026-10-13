package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// TestHandleSSEToJSON_CompletedEventReturnsJSON supplies in-progress and
// completed SSE events with terminal usage. It checks final JSON content,
// removal of SSE framing and the exact input, output and cached-token counts.
func TestHandleSSEToJSON_CompletedEventReturnsJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{}})
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	body := []byte(strings.Join([]string{
		`data: {"type":"response.in_progress","response":{"id":"resp_2"}}`,
		`data: {"type":"response.completed","response":{"id":"resp_2","model":"gpt-4o","usage":{"input_tokens":7,"output_tokens":9,"input_tokens_details":{"cached_tokens":1}}}}`,
		`data: [DONE]`,
	}, "\n"))

	usage, err := openai.ReadSSEAsJSON(context.Background(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.NonStreamOptions(context.Background(), c, nil), body, "gpt-4o", "gpt-4o")
	require.NoError(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 7, usage.Usage.InputTokens)
	require.Equal(t, 9, usage.Usage.OutputTokens)
	require.Equal(t, 1, usage.Usage.CacheReadInputTokens)
	// Content-Type may come from upstream; the assertion protects conversion of the body to final JSON.
	require.NotContains(t, rec.Body.String(), "event:")
	require.Contains(t, rec.Body.String(), `"id":"resp_2"`)
	require.NotContains(t, rec.Body.String(), "data:")
}

// TestHandleSSEToJSON_ReconstructsImageGenerationOutputItemDone supplies an
// image item before an empty terminal output array. It checks restored image
// content and prompt, completed status and observed image-token usage.
func TestHandleSSEToJSON_ReconstructsImageGenerationOutputItemDone(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{}})
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	body := []byte(strings.Join([]string{
		`data: {"type":"response.output_item.done","item":{"id":"ig_123","type":"image_generation_call","status":"generating","result":"aGVsbG8=","revised_prompt":"draw a cat","output_format":"png"}}`,
		`data: {"type":"response.completed","response":{"id":"resp_img","model":"gpt-5.4","output":[],"usage":{"input_tokens":7,"output_tokens":9,"output_tokens_details":{"image_tokens":4}}}}`,
		`data: [DONE]`,
	}, "\n"))

	usage, err := openai.ReadSSEAsJSON(context.Background(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.NonStreamOptions(context.Background(), c, nil), body, "gpt-5.4", "gpt-5.4")
	require.NoError(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 4, usage.Usage.ImageOutputTokens)
	require.NotContains(t, rec.Body.String(), "data:")
	require.Equal(t, "image_generation_call", gjson.Get(rec.Body.String(), "output.0.type").String())
	require.Equal(t, "completed", gjson.Get(rec.Body.String(), "output.0.status").String())
	require.Equal(t, "aGVsbG8=", gjson.Get(rec.Body.String(), "output.0.result").String())
	require.Equal(t, "draw a cat", gjson.Get(rec.Body.String(), "output.0.revised_prompt").String())
}

// TestHandleSSEToJSON_NoFinalResponseKeepsSSEBody supplies an in-progress
// event followed by DONE with no final Response. It checks passthrough SSE
// content and a returned observation with zero input usage.
func TestHandleSSEToJSON_NoFinalResponseKeepsSSEBody(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{}})
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	body := []byte(`data: {"type":"response.in_progress","response":{"id":"resp_3"}}` + "\n" + `data: [DONE]`)

	usage, err := openai.ReadSSEAsJSON(context.Background(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.NonStreamOptions(context.Background(), c, nil), body, "gpt-4o", "gpt-4o")
	require.NoError(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 0, usage.Usage.InputTokens)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
	require.Contains(t, rec.Body.String(), `data: {"type":"response.in_progress"`)
}

// TestHandleSSEToJSON_ResponseFailedReturnsFailoverBeforeWrite supplies a
// generic failed terminal to an API-key provider. It checks no result, a typed
// 502 failover error and an untouched client writer.
func TestHandleSSEToJSON_ResponseFailedReturnsFailoverBeforeWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{}})
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	body := []byte(`data: {"type":"response.failed","error":{"message":"upstream rejected request"}}` + "\n" + `data: [DONE]`)

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Type: capability.ProviderTypeAPIKey, Platform: capability.PlatformOpenAI}}
	usage, err := openai.ReadSSEAsJSON(context.Background(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.NonStreamOptions(context.Background(), c, provider), body, "gpt-4o", "gpt-4o")
	require.Nil(t, usage)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.False(t, c.Writer.Written())
}

// TestNonStreamingSSEToJSON_CapacityFailedEventFailsOver supplies a capacity
// rejection inside a failed SSE terminal. It checks HTTP 400 classification,
// same-provider retry eligibility and retained error body before any client
// output.
func TestNonStreamingSSEToJSON_CapacityFailedEventFailsOver(t *testing.T) {
	c, rec := newNonStreamingFailoverContext(t)
	svc := newNonStreamingFailoverService()
	body := sseTerminalBody("response.failed",
		`{"type":"response.failed","error":{"message":"Selected model is at capacity. Please try a different model.","type":"invalid_request_error"}}`)

	result, err := openai.ReadSSEAsJSON(context.Background(), newNonStreamingSSEResponse(), upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.NonStreamOptions(context.Background(), c, newNonStreamingFailoverProvider()), body, "model", "model")

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	// invalid_request_error maps to 400 and uses the streaming failover decision.
	require.Equal(t, http.StatusBadRequest, failoverErr.StatusCode)
	// Capacity shedding gets bounded retries on the same provider through the shared streaming policy.
	require.True(t, failoverErr.RetryableOnSameProvider)
	require.Contains(t, string(failoverErr.ResponseBody), "Selected model is at capacity")
	// Failover requires that no response bytes have been written.
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

// TestNonStreamingSSEToJSON_UnclassifiedFailedEventFailsOver supplies a
// generic failure accepted by the stream classifier. It checks a typed
// failover result with no written response or returned usage object.
func TestNonStreamingSSEToJSON_UnclassifiedFailedEventFailsOver(t *testing.T) {
	c, rec := newNonStreamingFailoverContext(t)
	svc := newNonStreamingFailoverService()
	payload := []byte(`{"type":"response.failed","error":{"message":"upstream rejected request"}}`)
	body := sseTerminalBody("response.failed", string(payload))

	// The same frame must be classified as retryable by the streaming classifier.
	require.True(t, openai.OpenAIStreamFailedEventShouldFailover(payload, "upstream rejected request"))

	result, err := openai.ReadSSEAsJSON(context.Background(), newNonStreamingSSEResponse(), upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.NonStreamOptions(context.Background(), c, newNonStreamingFailoverProvider()), body, "model", "model")

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

// TestNonStreamingSSEToJSON_NonRetryableFailedEventStillWritesProtocolError
// enumerates parameter, context-window and content-policy failures. It checks
// JSON 502 protocol errors containing each message and no provider failover
// wrapper.
func TestNonStreamingSSEToJSON_NonRetryableFailedEventStillWritesProtocolError(t *testing.T) {
	cases := []struct {
		name    string // fixture case name
		data    string // upstream terminal payload
		wantMsg string // expected client message
	}{
		{
			name:    "invalid_request",
			data:    `{"type":"response.failed","error":{"type":"invalid_request_error","code":"invalid_request","message":"unknown parameter foo"}}`,
			wantMsg: "unknown parameter foo",
		},
		{
			name:    "context_window",
			data:    `{"type":"response.failed","response":{"id":"resp_failed","status":"failed","output":[],"error":{"code":"upstream_error","message":"input exceeds the context window"}}}`,
			wantMsg: "input exceeds the context window",
		},
		{
			name:    "content_policy",
			data:    `{"type":"response.failed","error":{"type":"content_policy_violation","message":"blocked by our content policy"}}`,
			wantMsg: "blocked by our content policy",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newNonStreamingFailoverContext(t)
			svc := newNonStreamingFailoverService()

			result, err := openai.ReadSSEAsJSON(context.Background(), newNonStreamingSSEResponse(), upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.NonStreamOptions(context.Background(), c, newNonStreamingFailoverProvider()), sseTerminalBody("response.failed", tc.data), "model", "model")

			require.Nil(t, result)
			require.Error(t, err)
			var failoverErr *forwardcore.UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr), "不可重试的上游错误不得换号")
			require.Equal(t, http.StatusBadGateway, rec.Code)
			require.Contains(t, rec.Body.String(), tc.wantMsg)
			require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
		})
	}
}

// TestNonStreamingSSEToJSON_BareErrorEventUsesConservativeClassifier compares
// generic and recognized transient bare error frames. It checks a final
// protocol error for the generic case and pre-output failover for the
// transient case.
func TestNonStreamingSSEToJSON_BareErrorEventUsesConservativeClassifier(t *testing.T) {
	t.Run("non_transient_stays_protocol_error", func(t *testing.T) {
		c, rec := newNonStreamingFailoverContext(t)
		svc := newNonStreamingFailoverService()
		data := `{"type":"error","error":{"message":"upstream rejected request"}}`

		// The same message can trigger failover in response.failed and terminate a bare error event.
		require.True(t, openai.OpenAIStreamFailedEventShouldFailover([]byte(data), "upstream rejected request"))
		require.False(t, openai.OpenAIStreamErrorEventShouldFailover([]byte(data), "upstream rejected request"))

		result, err := openai.ReadSSEAsJSON(context.Background(), newNonStreamingSSEResponse(), upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.NonStreamOptions(context.Background(), c, newNonStreamingFailoverProvider()), sseTerminalBody("error", data), "model", "model")

		require.Nil(t, result)
		require.Error(t, err)
		var failoverErr *forwardcore.UpstreamFailoverError
		require.False(t, errors.As(err, &failoverErr))
		require.Equal(t, http.StatusBadGateway, rec.Code)
	})

	t.Run("transient_fails_over", func(t *testing.T) {
		c, _ := newNonStreamingFailoverContext(t)
		svc := newNonStreamingFailoverService()
		data := `{"type":"error","error":{"message":"Temporary upstream failure, please retry"}}`

		result, err := openai.ReadSSEAsJSON(context.Background(), newNonStreamingSSEResponse(), upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.NonStreamOptions(context.Background(), c, newNonStreamingFailoverProvider()), sseTerminalBody("error", data), "model", "model")

		require.Nil(t, result)
		var failoverErr *forwardcore.UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.False(t, c.Writer.Written())
	})
}

// TestNonStreamingSSEToJSON_MatchesStreamingClassifierVerdict runs the listed
// failed payloads through buffered conversion and the stream predicate. It
// checks agreement on the presence of a typed failover error for those
// fixtures.
func TestNonStreamingSSEToJSON_MatchesStreamingClassifierVerdict(t *testing.T) {
	payloads := []string{
		`{"type":"response.failed","error":{"message":"Selected model is at capacity. Please try a different model.","type":"invalid_request_error"}}`,
		`{"type":"response.failed","error":{"message":"upstream rejected request"}}`,
		`{"type":"response.failed","error":{"type":"invalid_request_error","code":"invalid_request","message":"unknown parameter foo"}}`,
		`{"type":"response.failed","response":{"id":"r","status":"failed","output":[],"error":{"code":"upstream_error","message":"input exceeds the context window"}}}`,
		`{"type":"response.failed","error":{"type":"content_policy_violation","message":"blocked by our content policy"}}`,
	}

	for _, data := range payloads {
		// Bound the case label while keeping the full payload in each assertion.
		t.Run(data[:min(len(data), 60)], func(t *testing.T) {
			payload := []byte(data)
			want := openai.OpenAIStreamFailedEventShouldFailover(payload, openai.ExtractOpenAISSEErrorMessage(payload))

			c, _ := newNonStreamingFailoverContext(t)
			svc := newNonStreamingFailoverService()
			_, err := openai.ReadSSEAsJSON(context.Background(), newNonStreamingSSEResponse(), upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.NonStreamOptions(context.Background(), c, newNonStreamingFailoverProvider()), sseTerminalBody("response.failed", data), "model", "model")

			var failoverErr *forwardcore.UpstreamFailoverError
			require.Equal(t, want, errors.As(err, &failoverErr),
				"非流式裁决与流式分类器不一致：%s", data)
		})
	}
}

// TestNonStreamingSSEToJSON_CommittedResponseKeepsProtocolError marks response
// commitment before a capacity failed terminal. It checks a JSON 502 error and
// absence of provider failover under that marker.
func TestNonStreamingSSEToJSON_CommittedResponseKeepsProtocolError(t *testing.T) {
	c, rec := newNonStreamingFailoverContext(t)
	svc := newNonStreamingFailoverService()
	MarkResponseCommitted(c)
	body := sseTerminalBody("response.failed",
		`{"type":"response.failed","error":{"message":"Selected model is at capacity. Please try a different model.","type":"invalid_request_error"}}`)

	result, err := openai.ReadSSEAsJSON(context.Background(), newNonStreamingSSEResponse(), upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.NonStreamOptions(context.Background(), c, newNonStreamingFailoverProvider()), body, "model", "model")

	require.Nil(t, result)
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.Equal(t, http.StatusBadGateway, rec.Code)
}
