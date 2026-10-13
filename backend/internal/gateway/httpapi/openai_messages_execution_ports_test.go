package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestOpenAIMessagesExecutionAdapterPreservesNilFailover classifies a
// model-not-found HTTP 400 through the Messages adapter. It checks a nil error
// interface and that classification leaves the client writer untouched.
func TestOpenAIMessagesExecutionAdapterPreservesNilFailover(t *testing.T) {
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	p := &openAIMessagesExecutionAdapter{s: &OpenAITextExecutor{Output: &OpenAIResponseOutput{Health: &provideradapter.OpenAIResponseHealth{}}}, c: c, provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI}}}
	body := []byte(`{"error":{"type":"invalid_request_error","message":"model not found"}}`)
	err := p.FailoverHTTP(context.Background(), &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header)}, body, "model not found", "gpt6")
	require.NoError(t, err)
	require.False(t, c.Writer.Written(), "端口只分类，客户端错误仍由后续协议适配写出")
}
