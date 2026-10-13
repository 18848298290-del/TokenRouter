package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// TestBufferRawChatCompletions_RejectsOversizedResponse supplies a native Chat
// body larger than the configured read limit. It checks the body-limit error,
// absent result and HTTP 502 response.
func TestBufferRawChatCompletions_RejectsOversizedResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader("toolong")),
	}
	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions()})
	svc.Output.Options.ReadLimit = 3

	result, err := openai.ReadRawChatBuffered(upstreamcore.NewDeferredOutputContext(ResponseSink{Writer: c.Writer}), resp, svc.Output.RawOptions(c, resp, rawChatCompletionsTestProvider(), "gpt-5.4", "gpt-5.4", nil, WriteForwardChatError), "gpt-5.4", "gpt-5.4", nil, time.Now())
	require.ErrorIs(t, err, httpclient.ErrResponseBodyTooLarge)
	require.Nil(t, result)
	require.Equal(t, http.StatusBadGateway, rec.Code)
}
