package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	upstreamopenai "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// messagesBufferedReadErrorFixture returns a configured read error.
type messagesBufferedReadErrorFixture struct{ err error }

func TestAnthropicBufferedResponsesReadErrorKeepsExistingBehavior(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: &messagesBufferedReadErrorFixture{err: io.ErrUnexpectedEOF}}
	output := &OpenAIResponseOutput{Options: OpenAIResponseOptions{ReadLimit: 128 * 1024 * 1024}}
	result, err := upstreamopenai.ReadMessagesBuffered(
		resp, upstream.NewDeferredOutputContext(ResponseSink{Writer: c.Writer}), output.MessagesOptions(c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 40, Name: "openai-oauth", Platform: capability.PlatformOpenAI}}, resp,
			"gpt-5.6-sol", "gpt-5.6-sol", "gpt-5.6-sol"), "gpt-5.6-sol", "gpt-5.6-sol", time.Now(),
	)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Equal(t, io.ErrUnexpectedEOF, err)
	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.NotErrorAs(t, err, &failoverErr)
}

func (r *messagesBufferedReadErrorFixture) Read([]byte) (int, error) { return 0, r.err }

func (r *messagesBufferedReadErrorFixture) Close() error { return nil }

func TestHandleAnthropicBufferedStreamingResponse_OverridesUpstreamContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			`data: {"type":"response.completed","response":{"id":"resp_buffered_json","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":15,"output_tokens":6,"total_tokens":21,"input_tokens_details":{"cached_tokens":5}}}}` + "\n\n",
		)),
	}
	output := &OpenAIResponseOutput{Options: OpenAIResponseOptions{Configured: true, ReadLimit: 128 * 1024 * 1024}, Headers: egress.CompileHeaderFilter(egress.ResponseHeaderOptions{})}

	result, err := upstreamopenai.ReadMessagesBuffered(
		resp, upstream.NewDeferredOutputContext(ResponseSink{Writer: c.Writer}), output.MessagesOptions(c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation}}, resp, "claude-sonnet-4-5", "gpt-5.4", "gpt-5.4"), "claude-sonnet-4-5", "gpt-5.4", time.Now(),
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	require.NotContains(t, rec.Header().Get("Content-Type"), "text/event-stream")

	var message protocolanthropic.AnthropicResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &message))
	require.Equal(t, "message", message.Type)
	require.Equal(t, "resp_buffered_json", message.ID)
	require.Equal(t, 10, message.Usage.InputTokens)
	require.Equal(t, 6, message.Usage.OutputTokens)
	require.Equal(t, 5, message.Usage.CacheReadInputTokens)
}
