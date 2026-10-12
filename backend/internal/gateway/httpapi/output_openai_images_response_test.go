package httpapi

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	upstreamopenai "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// TestExtractImagesUpstreamError_IncompleteIsRetryable supplies an incomplete
// response with max-output-token reason. Extraction returns a retryable 502,
// the incomplete code, and diagnostic text containing that reason.
func TestExtractImagesUpstreamError_IncompleteIsRetryable(t *testing.T) {
	body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
		"data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_1\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n"
	got := upstreamopenai.ExtractOpenAIImagesUpstreamError([]byte(body))
	if got == nil {
		t.Fatal("incomplete event should produce an upstream error, got nil")
		return
	}
	if got.StatusCode != http.StatusBadGateway {
		t.Fatalf("incomplete(max_output_tokens) should be 502 retryable, got %d", got.StatusCode)
	}
	if !upstreamopenai.IsOpenAIImagesRetryableUpstreamError(got) {
		t.Fatal("incomplete(max_output_tokens) should be retryable for failover")
	}
	if got.Code != "response_incomplete" {
		t.Fatalf("unexpected code %q", got.Code)
	}
	if !strings.Contains(got.Message, "max_output_tokens") {
		t.Fatalf("message should carry reason, got %q", got.Message)
	}
}

// TestExtractImagesUpstreamError_IncompleteContentFilterNotRetryable supplies
// an incomplete event attributed to content filtering. Extraction returns a
// terminal 400 classification that does not permit the tested image retry.
func TestExtractImagesUpstreamError_IncompleteContentFilterNotRetryable(t *testing.T) {
	body := "data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"r\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"content_filter\"}}}\n\n"
	got := upstreamopenai.ExtractOpenAIImagesUpstreamError([]byte(body))
	if got == nil {
		t.Fatal("content_filter incomplete should produce error")
		return
	}
	if got.StatusCode != http.StatusBadRequest {
		t.Fatalf("content_filter should be 400 (non-retryable), got %d", got.StatusCode)
	}
	if upstreamopenai.IsOpenAIImagesRetryableUpstreamError(got) {
		t.Fatal("content_filter must NOT be retryable")
	}
}

// TestExtractImagesUpstreamError_ErrorAndFailedUnchanged supplies a
// moderation-blocked error event in SSE data. The extracted image error keeps
// status 400 for client policy presentation.
func TestExtractImagesUpstreamError_ErrorAndFailedUnchanged(t *testing.T) {
	errBody := "data: {\"type\":\"error\",\"error\":{\"type\":\"image_generation_user_error\",\"code\":\"moderation_blocked\",\"message\":\"rejected\"}}\n\n"
	if got := upstreamopenai.ExtractOpenAIImagesUpstreamError([]byte(errBody)); got == nil || got.StatusCode != http.StatusBadRequest {
		t.Fatalf("moderation_blocked should still be 400, got %+v", got)
	}
}

// TestSummarizeNoOutputBody_ExtractsDiagnostics ends a stream after created
// and in-progress events with no image. The summary retains its no-output
// marker, final event type and observed response status.
func TestSummarizeNoOutputBody_ExtractsDiagnostics(t *testing.T) {
	body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\n" +
		"data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"r\",\"status\":\"in_progress\"}}\n\n"
	summary := upstreamopenai.SummarizeOpenAIImagesNoOutputBody([]byte(body))
	if !strings.HasPrefix(summary, "no_image_output") {
		t.Fatalf("summary should start with marker, got %q", summary)
	}
	if !strings.Contains(summary, "last_event=response.in_progress") {
		t.Fatalf("summary should capture last event type, got %q", summary)
	}
	if !strings.Contains(summary, "status=in_progress") {
		t.Fatalf("summary should capture response status, got %q", summary)
	}
}

// TestSummarizeNoOutputBody_IncompleteReasonAndTruncation supplies an
// incomplete event with a large extra field. The diagnostic keeps its
// incomplete reason and reports that the source body was truncated.
func TestSummarizeNoOutputBody_IncompleteReasonAndTruncation(t *testing.T) {
	// Oversized fixture text forces the summary to exercise its truncation marker.
	long := strings.Repeat("x", 2000)
	body := "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"junk\":\"" + long + "\"}}\n\n"
	summary := upstreamopenai.SummarizeOpenAIImagesNoOutputBody([]byte(body))
	if !strings.Contains(summary, "incomplete_reason=max_output_tokens") {
		t.Fatalf("should capture incomplete reason, got %q", summary[:120])
	}
	if !strings.Contains(summary, "truncated") {
		t.Fatalf("oversized body should be truncated, len=%d", len(summary))
	}
}

// TestImagesOAuthNonStreaming_CompletedNoImageTriggersSameProviderRetry
// reports successful completion with empty output and zero image-tool work.
// The nonstreaming reader returns a same-provider-retryable 502
// classification for the empty image result.
func TestImagesOAuthNonStreaming_CompletedNoImageTriggersSameProviderRetry(t *testing.T) {
	// Empty output and zero tool work reproduce the reader's empty-image path,
	// keeping it separate from text-derived unavailability and content refusal.
	upstreamSSE := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_x\",\"status\":\"in_progress\",\"model\":\"gpt-5.4-mini-2026-03-17\",\"output\":[]}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_x\",\"status\":\"completed\",\"model\":\"gpt-5.4-mini-2026-03-17\",\"output\":[],\"tool_usage\":{\"image_gen\":{\"output_tokens\":0}}}}\n\n"

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
	}

	svc := newImagesFixture(imagesFixtureInputs{})
	_, _, _, err := upstreamopenai.ReadImagesOAuthNonStreaming(resp, ResponseSink{Writer: c.Writer}, svc.Output.ImageOptions(c), "b64_json", "gpt-image-2")

	if err == nil {
		t.Fatal("completed-but-no-image should return an error")
	}
	var failoverErr *forwardcore.UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("expected *UpstreamFailoverError to trigger retry, got %T: %v", err, err)
	}
	if failoverErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", failoverErr.StatusCode)
	}
	if !failoverErr.RetryableOnSameProvider {
		t.Fatal("soft-failure should prefer same-provider retry (probabilistic upstream failure)")
	}
}

// TestImagesOAuthNonStreaming_ContentRefusalReturns400NoRetry supplies text
// and terminal output describing a content refusal. The buffered reader
// retains a client 400 message and returns no classified provider failover.
func TestImagesOAuthNonStreaming_ContentRefusalReturns400NoRetry(t *testing.T) {
	upstreamSSE := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"status\":\"in_progress\",\"model\":\"gpt-5.4-mini\",\"output\":[]}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"抱歉，这个请求因涉及违规内容被安全系统判定为不适合生成。\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"model\":\"gpt-5.4-mini\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"抱歉，这个请求因涉及违规内容被安全系统判定为不适合生成。\"}]}],\"tool_usage\":{\"image_gen\":{\"output_tokens\":0}}}}\n\n"

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(upstreamSSE))}

	svc := newImagesFixture(imagesFixtureInputs{})
	_, _, _, err := upstreamopenai.ReadImagesOAuthNonStreaming(resp, ResponseSink{Writer: c.Writer}, svc.Output.ImageOptions(c), "b64_json", "gpt-image-2")

	if err == nil {
		t.Fatal("content refusal should return an error")
	}
	if failoverErr, ok := errors.AsType[*forwardcore.UpstreamFailoverError](err); ok {
		t.Fatalf("content refusal must not be a retryable failover error, got %v", failoverErr)
	}
	var imgErr *upstreamopenai.OpenAIImagesUpstreamError
	if !errors.As(err, &imgErr) {
		t.Fatalf("expected *OpenAIImagesUpstreamError, got %T: %v", err, err)
	}
	if imgErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("content refusal should be 400, got %d", imgErr.StatusCode)
	}
	if !strings.Contains(imgErr.Message, "安全系统") && !strings.Contains(imgErr.Message, "违规") {
		t.Fatalf("refusal message should carry model reason, got %q", imgErr.Message)
	}
}

// TestImagesOAuthNonStreaming_TextFallbackReturnsCapabilityError supplies
// ordinary text output instead of images. The buffered reader reports
// image-generation unavailable with the tested retryable server-error status.
func TestImagesOAuthNonStreaming_TextFallbackReturnsCapabilityError(t *testing.T) {
	upstreamSSE := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"status\":\"in_progress\",\"model\":\"gpt-5.4-mini\",\"output\":[]}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Here's a polished image prompt for your request.\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"model\":\"gpt-5.4-mini\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"Here's a polished image prompt for your request.\"}]}],\"tool_usage\":{\"image_gen\":{\"output_tokens\":0}}}}\n\n"

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(upstreamSSE))}

	svc := newImagesFixture(imagesFixtureInputs{})
	_, _, _, err := upstreamopenai.ReadImagesOAuthNonStreaming(resp, ResponseSink{Writer: c.Writer}, svc.Output.ImageOptions(c), "b64_json", "gpt-image-2")

	var imgErr *upstreamopenai.OpenAIImagesUpstreamError
	if !errors.As(err, &imgErr) {
		t.Fatalf("expected *OpenAIImagesUpstreamError, got %T: %v", err, err)
	}
	if imgErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("text fallback should be retryable 502, got %d", imgErr.StatusCode)
	}
	if imgErr.Code != "image_generation_unavailable" {
		t.Fatalf("text fallback should identify missing image execution, got %q", imgErr.Code)
	}
}

// TestImagesOAuthStreaming_TextFallbackReturnsCapabilityError supplies text
// deltas and empty image completion. The streaming reader returns its
// unavailable-image 502 while leaving the tested error event unflushed for
// recovery.
func TestImagesOAuthStreaming_TextFallbackReturnsCapabilityError(t *testing.T) {
	upstreamSSE := "event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Here's a polished image prompt for your request.\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"model\":\"gpt-5.4-mini\",\"output\":[]}}\n\n"

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(upstreamSSE))}

	svc := newImagesFixture(imagesFixtureInputs{})
	_, _, _, _, err := upstreamopenai.ReadImagesOAuthStreaming(resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.ImageOptions(c), time.Now(), "b64_json", "image_generation", "gpt-image-2")

	var imgErr *upstreamopenai.OpenAIImagesUpstreamError
	if !errors.As(err, &imgErr) {
		t.Fatalf("expected *OpenAIImagesUpstreamError, got %T: %v", err, err)
	}
	if imgErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("streaming text fallback should be retryable 502, got %d", imgErr.StatusCode)
	}
	if imgErr.Code != "image_generation_unavailable" {
		t.Fatalf("streaming text fallback should identify missing image execution, got %q", imgErr.Code)
	}
	if strings.Contains(rec.Body.String(), "event: error") {
		t.Fatal("retryable text fallback must remain unflushed for failover")
	}
}

// TestImagesOAuthStreaming_SplitSafetyRefusalReturns400 splits refusal text
// across two deltas before empty terminal output. The streaming reader
// recognizes the combined policy refusal and presents its tested 400 error
// event.
func TestImagesOAuthStreaming_SplitSafetyRefusalReturns400(t *testing.T) {
	upstreamSSE := "event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"安全系\"}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"统拒绝生成\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[]}}\n\n"

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(upstreamSSE))}

	svc := newImagesFixture(imagesFixtureInputs{})
	_, _, _, _, err := upstreamopenai.ReadImagesOAuthStreaming(resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.ImageOptions(c), time.Now(), "b64_json", "image_generation", "gpt-image-2")

	var imgErr *upstreamopenai.OpenAIImagesUpstreamError
	if !errors.As(err, &imgErr) {
		t.Fatalf("expected *OpenAIImagesUpstreamError, got %T: %v", err, err)
	}
	if imgErr.StatusCode != http.StatusBadRequest || imgErr.Code != "content_policy_violation" {
		t.Fatalf("split safety refusal should remain a content-policy 400, got status=%d code=%q", imgErr.StatusCode, imgErr.Code)
	}
	if !strings.Contains(rec.Body.String(), "event: error") {
		t.Fatal("content-policy refusal must reach the streaming client")
	}
}

// TestExtractModelRefusal_EmptyWhenNoText supplies empty output with zero
// image-tool tokens. Refusal extraction returns an empty message, leaving the
// missing-image case distinct from observed refusal text.
func TestExtractModelRefusal_EmptyWhenNoText(t *testing.T) {
	body := "data: {\"type\":\"response.completed\",\"response\":{\"output\":[],\"tool_usage\":{\"image_gen\":{\"output_tokens\":0}}}}\n\n"
	if refusal := upstreamopenai.ExtractOpenAIImagesModelRefusal([]byte(body)); refusal != "" {
		t.Fatalf("empty response should yield no refusal, got %q", refusal)
	}
}

// TestParseOpenAIImagesSSEUsageBytes_ToolUsagePrecedenceAndFallback combines
// valid, absent and malformed image-tool usage with top-level observations.
// Valid tool data wins as one source; invalid tool fields retain the complete
// fallback usage.
func TestParseOpenAIImagesSSEUsageBytes_ToolUsagePrecedenceAndFallback(t *testing.T) {
	fallback := openai.ForwardUsage{InputTokens: 3, ImageInputTokens: 2, OutputTokens: 4, ImageOutputTokens: 2}
	tests := []struct {
		name      string              // usage precedence case label
		toolUsage string              // injected image-tool usage JSON
		want      openai.ForwardUsage // expected retained token observations
	}{
		{
			name:      "valid tool usage takes atomic precedence without mixing top-level image input",
			toolUsage: `{"input_tokens":4.6e1,"output_tokens":2459e0,"output_tokens_details":{"image_tokens":24590e-1}}`,
			want:      openai.ForwardUsage{InputTokens: 46, OutputTokens: 2459, ImageOutputTokens: 2459},
		},
		{
			name:      "valid tool image input takes precedence",
			toolUsage: `{"input_tokens":46,"input_tokens_details":{"image_tokens":35},"output_tokens":2459,"output_tokens_details":{"image_tokens":2459}}`,
			want:      openai.ForwardUsage{InputTokens: 46, ImageInputTokens: 35, OutputTokens: 2459, ImageOutputTokens: 2459},
		},
		{name: "absent", want: fallback},
		{name: "malformed field", toolUsage: `{"input_tokens":"46","output_tokens":2459,"output_tokens_details":{"image_tokens":2459}}`, want: fallback},
		{name: "fractional field", toolUsage: `{"input_tokens":46,"output_tokens":2459.5,"output_tokens_details":{"image_tokens":2459}}`, want: fallback},
		{name: "negative field", toolUsage: `{"input_tokens":46,"output_tokens":2459,"output_tokens_details":{"image_tokens":-1}}`, want: fallback},
		{name: "overflow field", toolUsage: `{"input_tokens":46,"output_tokens":9223372036854775808,"output_tokens_details":{"image_tokens":2459}}`, want: fallback},
		{name: "incomplete object", toolUsage: `{"input_tokens":46,"output_tokens":2459}`, want: fallback},
		{name: "malformed image input detail", toolUsage: `{"input_tokens":46,"input_tokens_details":{"image_tokens":1.5},"output_tokens":2459,"output_tokens_details":{"image_tokens":2459}}`, want: fallback},
		{name: "hostile huge exponent", toolUsage: `{"input_tokens":1e1000000000,"output_tokens":2459,"output_tokens_details":{"image_tokens":2459}}`, want: fallback},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolUsageField := ""
			if tt.toolUsage != "" {
				toolUsageField = `,"tool_usage":{"image_gen":` + tt.toolUsage + `}`
			}
			payload := []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":3,"input_tokens_details":{"image_tokens":2},"output_tokens":4,"output_tokens_details":{"image_tokens":2}}` + toolUsageField + `}}`)
			var got openai.ForwardUsage
			upstreamopenai.ParseOpenAIImagesSSEUsageBytes(payload, &got)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestParseOpenAIImagesSSEUsageBytes_MalformedCompletedDoesNotOverrideUsage
// records valid usage and then supplies a terminal payload with invalid
// trailing text. Parsing keeps the prior token observations instead of
// accepting the malformed replacement.
func TestParseOpenAIImagesSSEUsageBytes_MalformedCompletedDoesNotOverrideUsage(t *testing.T) {
	var usage openai.ForwardUsage

	upstreamopenai.ParseOpenAIImagesSSEUsageBytes([]byte(`{"type":"response.output_item.done","item":{"type":"image_generation_call","result":"aW1hZ2U="}}`), &usage)
	upstreamopenai.ParseOpenAIImagesSSEUsageBytes([]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":4,"output_tokens_details":{"image_tokens":2}}}}`), &usage)
	upstreamopenai.ParseOpenAIImagesSSEUsageBytes([]byte(`{"type":"response.completed","response":{"tool_usage":{"image_gen":{"input_tokens":46,"output_tokens":2459,"output_tokens_details":{"image_tokens":2459}}}}} trailing`), &usage)

	require.Equal(t, openai.ForwardUsage{InputTokens: 3, OutputTokens: 4, ImageOutputTokens: 2}, usage)
}

// TestExtractOpenAIImagesBillableCountFromJSONBytes_CompletedEvent feeds an
// image-specific completion event to the output counter. Its encoded image is
// counted once with the supplied terminal event shape.
func TestExtractOpenAIImagesBillableCountFromJSONBytes_CompletedEvent(t *testing.T) {
	body := []byte(`{"type":"image_generation.completed","b64_json":"ZmluYWw=","usage":{"input_tokens":10,"output_tokens":18}}`)

	counter := openai.NewOpenAIImageOutputCounter()
	counter.AddSSEData(body)
	require.Equal(t, 1, counter.Count())
}

// TestOpenAIImagesSSEErrorStatus compares image-stream error categories and
// codes for standard failure classes. The fixture checks their mapped HTTP
// status for rate, authentication, permission, missing model, moderation and
// server cases.
func TestOpenAIImagesSSEErrorStatus(t *testing.T) {
	tests := []struct {
		name    string // status mapping case label
		errType string // supplied upstream error category
		code    string // supplied upstream error code
		want    int    // expected HTTP error status
	}{
		{name: "rate limit", errType: "rate_limit_error", want: http.StatusTooManyRequests},
		{name: "auth", code: "invalid_api_key", want: http.StatusUnauthorized},
		{name: "permission", errType: "permission_error", want: http.StatusForbidden},
		{name: "not found", code: "model_not_found", want: http.StatusNotFound},
		{name: "moderation", errType: "image_generation_user_error", code: "moderation_blocked", want: http.StatusBadRequest},
		{name: "server", errType: "server_error", code: "server_error", want: http.StatusBadGateway},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, upstreamopenai.OpenAIImagesSSEErrorStatus(tt.errType, tt.code))
		})
	}
}

// TestCollectOpenAIImagesFromResponsesBody_FallsBackToOutputItemDone carries
// image data in output-item completion while final output is empty.
// Collection retains one image, its prompt and format, creation time and
// terminal tool usage.
func TestCollectOpenAIImagesFromResponsesBody_FallsBackToOutputItemDone(t *testing.T) {
	body := []byte(
		"data: {\"type\":\"response.created\",\"response\":{\"created_at\":1710000004}}\n\n" +
			"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"ig_123\",\"type\":\"image_generation_call\",\"result\":\"aGVsbG8=\",\"revised_prompt\":\"draw a cat\",\"output_format\":\"png\",\"quality\":\"high\"}}\n\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000004,\"tool_usage\":{\"image_gen\":{\"images\":1}},\"output\":[]}}\n\n" +
			"data: [DONE]\n\n",
	)

	results, createdAt, usageRaw, firstMeta, foundFinal, err := upstreamopenai.CollectOpenAIImagesFromResponsesBody(body, time.Now)
	require.NoError(t, err)
	require.True(t, foundFinal)
	require.Equal(t, int64(1710000004), createdAt)
	require.Len(t, results, 1)
	require.Equal(t, "aGVsbG8=", results[0].Result)
	require.Equal(t, "draw a cat", results[0].RevisedPrompt)
	require.Equal(t, "png", firstMeta.OutputFormat)
	require.JSONEq(t, `{"images":1}`, string(usageRaw))
}

// TestCollectOpenAIImagesFromResponsesBody_MultilineSSE splits one completed
// response across consecutive SSE data lines. Collection joins that payload
// and retains the terminal image, timestamp, format and image-tool usage.
func TestCollectOpenAIImagesFromResponsesBody_MultilineSSE(t *testing.T) {
	body := []byte(
		"data: {\"type\":\"response.completed\",\n" +
			"data: \"response\":{\"created_at\":1710000010,\"usage\":{\"input_tokens\":5,\"output_tokens\":9,\"output_tokens_details\":{\"image_tokens\":4}},\"tool_usage\":{\"image_gen\":{\"images\":1}},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"ZmluYWw=\",\"output_format\":\"png\"}]}}\n\n" +
			"data: [DONE]\n\n",
	)

	results, createdAt, usageRaw, firstMeta, foundFinal, err := upstreamopenai.CollectOpenAIImagesFromResponsesBody(body, time.Now)
	require.NoError(t, err)
	require.True(t, foundFinal)
	require.Equal(t, int64(1710000010), createdAt)
	require.Len(t, results, 1)
	require.Equal(t, "ZmluYWw=", results[0].Result)
	require.Equal(t, "png", firstMeta.OutputFormat)
	require.JSONEq(t, `{"images":1}`, string(usageRaw))
}
