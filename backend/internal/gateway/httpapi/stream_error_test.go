package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
)

// streamErrorBackendFixture supplies error observation methods; execution methods remain unset.
type streamErrorBackendFixture struct{ OpenAITextBackend }

func newGinContextForEndpoint(t *testing.T, endpoint string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, endpoint, nil)
	return c, w
}

// TestOpenAIHandleStreamingAwareError_ResponsesStreamingEmitsResponseFailed checks the terminal event after a
// Responses stream starts.
func TestOpenAIHandleStreamingAwareError_ResponsesStreamingEmitsResponseFailed(t *testing.T) {
	c, w := newGinContextForEndpoint(t, EndpointResponses)
	h := &OpenAITextHandler{backend: streamErrorBackendFixture{}}
	h.handleStreamingAwareError(c, http.StatusTooManyRequests, "rate_limit_error",
		"Concurrency limit exceeded for user, please retry later", true)

	resp, errObj := parseResponsesFailedSSE(t, w.Body.String())

	id, _ := resp["id"].(string)
	assert.True(t, strings.HasPrefix(id, "resp_"), "id should start with resp_, got %q", id)
	assert.Equal(t, "rate_limit_exceeded", errObj["code"])
	assert.Equal(t, "Concurrency limit exceeded for user, please retry later", errObj["message"])
}

// TestOpenAIHandleStreamingAwareError_ResponsesStreamingIncludesModel checks that the event uses the observed
// request model.
func TestOpenAIHandleStreamingAwareError_ResponsesStreamingIncludesModel(t *testing.T) {
	c, w := newGinContextForEndpoint(t, EndpointResponses)
	SetOpsRequestContext(c, "gpt-5.5", true)

	h := &OpenAITextHandler{backend: streamErrorBackendFixture{}}
	h.handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", "boom", true)

	resp, _ := parseResponsesFailedSSE(t, w.Body.String())
	assert.Equal(t, "gpt-5.5", resp["model"])
}

// TestOpenAIHandleStreamingAwareError_ResponsesStreamingOmitsEmptyModel checks that unknown models are omitted.
func TestOpenAIHandleStreamingAwareError_ResponsesStreamingOmitsEmptyModel(t *testing.T) {
	c, w := newGinContextForEndpoint(t, EndpointResponses)
	h := &OpenAITextHandler{backend: streamErrorBackendFixture{}}
	h.handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", "boom", true)

	resp, _ := parseResponsesFailedSSE(t, w.Body.String())
	_, hasModel := resp["model"]
	assert.False(t, hasModel, "model field must be omitted when unknown")
}

// TestOpenAIHandleStreamingAwareError_ResponsesStreamingReusesRequestID checks correlation with telemetry.RequestID.
func TestOpenAIHandleStreamingAwareError_ResponsesStreamingReusesRequestID(t *testing.T) {
	c, w := newGinContextForEndpoint(t, EndpointResponses)
	c.Request = c.Request.WithContext(
		context.WithValue(c.Request.Context(), telemetry.RequestID, "fd277bc5-ff7e-45d1-8aa9-f54e1df318f1"),
	)

	h := &OpenAITextHandler{backend: streamErrorBackendFixture{}}
	h.handleStreamingAwareError(c, http.StatusTooManyRequests, "rate_limit_error", "x", true)

	resp, _ := parseResponsesFailedSSE(t, w.Body.String())
	assert.Equal(t, "resp_fd277bc5ff7e45d18aa9f54e1df318f1", resp["id"])
}

// TestOpenAIHandleStreamingAwareError_ResponsesStreamingJSONEscaping checks JSON encoding of message characters.
func TestOpenAIHandleStreamingAwareError_ResponsesStreamingJSONEscaping(t *testing.T) {
	cases := []struct {
		name    string
		errType string
		message string
	}{
		{"双引号", "server_error", `upstream returned "invalid" response`},
		{"反斜杠", "server_error", `path C:\Users\test\file.txt not found`},
		{"双引号+反斜杠", "upstream_error", `error parsing "key\value": unexpected token`},
		{"换行与制表", "server_error", "line1\nline2\ttab"},
		{"普通", "upstream_error", "Upstream service temporarily unavailable"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, w := newGinContextForEndpoint(t, EndpointResponses)
			h := &OpenAITextHandler{backend: streamErrorBackendFixture{}}
			h.handleStreamingAwareError(c, http.StatusBadGateway, tc.errType, tc.message, true)

			_, errObj := parseResponsesFailedSSE(t, w.Body.String())
			assert.Equal(t, tc.message, errObj["message"], "message 必须被原样还原")
		})
	}
}

// TestOpenAIHandleStreamingAwareError_ChatCompletionsStreamingKeepsLegacy checks Chat's event:error format.
func TestOpenAIHandleStreamingAwareError_ChatCompletionsStreamingKeepsLegacy(t *testing.T) {
	c, w := newGinContextForEndpoint(t, EndpointChatCompletions)
	h := &OpenAITextHandler{backend: streamErrorBackendFixture{}}
	h.handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", "boom", true)

	body := w.Body.String()
	assert.True(t, strings.HasPrefix(body, "event: error\n"), "got: %q", body)
}

// TestGatewayHandleStreamingAwareError_ResponsesStreamingEmitsResponseFailed checks Responses errors from the
// Anthropic writer.
func TestGatewayHandleStreamingAwareError_ResponsesStreamingEmitsResponseFailed(t *testing.T) {
	c, w := newGinContextForEndpoint(t, EndpointResponses)
	WriteAnthropicStreamError(c, http.StatusBadGateway, "upstream_error", "", "upstream gone", true, MarkOpsStreamError)

	_, errObj := parseResponsesFailedSSE(t, w.Body.String())
	assert.Equal(t, "upstream_error", errObj["code"])
	assert.Equal(t, "upstream gone", errObj["message"])
}

// TestGatewayHandleStreamingAwareError_MessagesStreamingKeepsLegacy checks the Messages data:{type:error,...} format.
func TestGatewayHandleStreamingAwareError_MessagesStreamingKeepsLegacy(t *testing.T) {
	c, w := newGinContextForEndpoint(t, EndpointMessages)
	WriteAnthropicStreamError(c, http.StatusBadGateway, "upstream_error", "", "boom", true, MarkOpsStreamError)

	body := w.Body.String()
	assert.True(t, strings.HasPrefix(body, `data: {"type":"error"`), "got: %q", body)
}

func TestGatewayAdmissionErrorPreservesGatewayCode(t *testing.T) {
	c, w := newGinContextForEndpoint(t, EndpointMessages)
	WriteAnthropicStreamError(c, http.StatusTooManyRequests, "rate_limit_error", GatewayQueueFullCode, "Too many pending requests, please retry later", false, MarkOpsStreamError)

	assert.Equal(t, GatewayQueueFullCode, gjson.GetBytes(w.Body.Bytes(), "error.code").String())
}

func TestOpenAIAdmissionErrorPreservesGatewayCodeAcrossResponsesSSE(t *testing.T) {
	c, w := newGinContextForEndpoint(t, EndpointResponses)
	h := &OpenAITextHandler{backend: streamErrorBackendFixture{}}
	h.WriteStreamingErrorWithCode(c, http.StatusTooManyRequests, "rate_limit_error", GatewayConcurrencyLimitCode, "Concurrency limit exceeded for provider, please retry later", true, false)

	_, errObj := parseResponsesFailedSSE(t, w.Body.String())
	assert.Equal(t, GatewayConcurrencyLimitCode, errObj["code"])
}

// TestInboundIsResponses_CoversAllRoutes checks versioned, bare, and Codex Responses paths, including compact.
// These paths select response.failed for stream errors.
func TestInboundIsResponses_CoversAllRoutes(t *testing.T) {
	cases := []struct {
		route string
		want  bool
	}{
		{"/v1/responses", true},
		{"/v1/responses/compact", true},
		{"/responses", true}, // Bare Responses alias.
		{"/responses/compact", true},
		{"/backend-api/codex/responses", true},
		{"/backend-api/codex/responses/compact", true},
		{"/v1/chat/completions", false},
		{"/v1/messages", false},
		{"/", false},
		{"/responses-fake", false},
	}
	for _, tc := range cases {
		t.Run(tc.route, func(t *testing.T) {
			c, _ := newGinContextForEndpoint(t, tc.route)
			assert.Equal(t, tc.want, InboundIsResponses(c), "route=%q", tc.route)
		})
	}
}

// TestInboundIsResponses_FallsBackToURLPath checks routing when Gin has no matched path.
func TestInboundIsResponses_FallsBackToURLPath(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/responses", nil)
	// This context has a URL path but no registered route.
	assert.True(t, InboundIsResponses(c), "URL.Path fallback must work when FullPath is empty")
}

// TestOpenAIHandleStreamingAwareError_BareResponsesRouteEmitsResponseFailed checks the bare Responses alias.
func TestOpenAIHandleStreamingAwareError_BareResponsesRouteEmitsResponseFailed(t *testing.T) {
	c, w := newGinContextForEndpoint(t, "/responses")
	h := &OpenAITextHandler{backend: streamErrorBackendFixture{}}
	h.handleStreamingAwareError(c, http.StatusTooManyRequests, "rate_limit_error",
		"Concurrency limit exceeded for user, please retry later", true)

	resp, errObj := parseResponsesFailedSSE(t, w.Body.String())
	id, _ := resp["id"].(string)
	assert.True(t, strings.HasPrefix(id, "resp_"))
	assert.Equal(t, "rate_limit_exceeded", errObj["code"])
}

// TestOpenAIHandleStreamingAwareError_ResponsesStreamingCarriesCreatedAt checks timestamps and generated IDs.
// A failed response includes created_at for clients that require that field (#5601).
func TestOpenAIHandleStreamingAwareError_ResponsesStreamingCarriesCreatedAt(t *testing.T) {
	c, w := newGinContextForEndpoint(t, EndpointResponses)
	h := &OpenAITextHandler{backend: streamErrorBackendFixture{}}
	h.handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", "boom", true)

	resp, _ := parseResponsesFailedSSE(t, w.Body.String())
	raw, ok := resp["created_at"]
	assert.True(t, ok, "response.failed 必须带 created_at")
	createdAt, ok := raw.(float64)
	assert.True(t, ok, "created_at 必须是数字，得到 %T", raw)
	assert.Greater(t, int64(createdAt), int64(0), "created_at 必须是有效的 unix 时间戳")
}

func TestSynthesizeResponseID_FallbackUUID(t *testing.T) {
	c, _ := newGinContextForEndpoint(t, EndpointResponses)
	id := SynthesizeResponseID(ErrorRequestID(c))
	assert.True(t, strings.HasPrefix(id, "resp_"))
	// A UUID without hyphens has 32 hex characters; the resp_ prefix adds five.
	assert.Len(t, id, 37)
}

func TestMapResponsesErrorCode(t *testing.T) {
	cases := []struct{ in, out string }{
		{"rate_limit_error", "rate_limit_exceeded"},
		{"invalid_request_error", "invalid_request"},
		{"permission_error", "permission_denied"},
		{"authentication_error", "authentication_failed"},
		{"upstream_error", "upstream_error"},
		{"server_error", "server_error"},
		{"api_error", "server_error"},
		{"", "server_error"},
		{"custom_thing", "custom_thing"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.out, MapResponsesErrorCode(tc.in), "in=%q", tc.in)
	}
}

func (streamErrorBackendFixture) StopCompact(c *gin.Context) bool {
	return StopOpenAICompactSSEKeepaliveCommitted(c)
}

func (streamErrorBackendFixture) MarkStream(c *gin.Context, kind, message string, status int) {
	MarkOpsStreamError(c, kind, message, status)
}

func (streamErrorBackendFixture) ErrorMetadata(c *gin.Context) (string, string) {
	return ErrorRequestID(c), ErrorRequestModel(c)
}
