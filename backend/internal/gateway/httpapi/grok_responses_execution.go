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

// Grok Responses and Composer image descriptions share request preparation,
// transport policy and response observation. Each HTTP attempt keeps its own
// model observer, while image bridging delegates image replacement and usage
// aggregation to the Grok implementation using this provider's execution
// services.

// ForwardResponses starts a request-local upstream-model observation and
// delegates the selected provider to grokforward.Forward. The deferred capture
// stamps any returned result; the runtime decides which successful or handled
// error observations reach that result.
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

// BridgeComposerImages delegates Composer image replacement to Grok with a
// description callback for this provider and token. It returns the transformed
// body, aggregated description usage, applicability flag and any image-preflight error.
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

// DescribeComposerImage performs a single-image Responses description using
// the supplied provider token. It shares the executor's attribute, route,
// transport and observation services, returning description text and measured
// usage to Composer bridging.
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
