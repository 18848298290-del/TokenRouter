package httpapi

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/grokforward"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

func (s *GrokExecutor) ForwardResponses(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
	originalModel string,
	reqStream bool,
	startTime time.Time,
) (out *forwardcore.OpenAIResult, failure error) {
	BeginUpstreamResponseModelObservation(c)
	defer func() { captureResponseModel(c, out) }()
	adapter := &grokForwardAdapter{s: s, c: c, provider: provider}
	result, err := grokforward.Forward(ctx, adapter, adapter.options(), adapter.input(body, originalModel, reqStream, startTime))
	return result, err
}

func (s *GrokExecutor) BridgeComposerImages(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
	token string,
) ([]byte, openai.ForwardUsage, bool, error) {
	return grok.BridgeComposerImages(body, func(imageURL string, index int) (string, openai.ForwardUsage, error) {
		return s.DescribeComposerImage(ctx, c, provider, token, imageURL, index)
	})
}

// DescribeComposerImage runs a single-image preflight with Grok Responses configuration and shared provider
// observation and error handling.
func (s *GrokExecutor) DescribeComposerImage(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	token string,
	imageURL string,
	index int,
) (string, openai.ForwardUsage, error) {
	adapter := &grokForwardAdapter{s: s, c: c, provider: provider, token: token}
	return grokforward.DescribeImage(ctx, adapter, adapter.options(), adapter.input(nil, "", false, time.Time{}), imageURL, index)
}
