package httpapi

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// ForwardQoderAttempt executes and writes one request; the caller owns provider failover.
// An error returns partial results only when the executor reports both Served and HasUsage.
// First-output timing and usage are not synthesized here.
func ForwardQoderAttempt(ctx context.Context, c *gin.Context, runtime *gatewayadapter.QoderRuntime, value *provider.Record, body []byte, wire protocol.ProtocolID, responseModels ...string) (*forward.MessagesResult, error) {
	responseModel := qoder.FirstNonEmptyQoder(responseModels...)
	if responseModel == "" {
		responseModel = strings.TrimSpace(qoder.GjsonString(body, "model"))
	}
	executor, input := runtime.PrepareQoderTarget(QoderRequestMetadata(c), value, body, wire, responseModel)
	result, err := executor.Execute(ctx, input, ResponseSink{Writer: c.Writer})
	if err != nil {
		runtime.ObserveQoderFailure(ctx, value, err)
		if !result.Served || !result.HasUsage {
			return nil, err
		}
	}
	return &forward.MessagesResult{RequestID: result.RequestID, Model: result.Model, UpstreamModel: result.UpstreamModel, Usage: result.Usage, Stream: result.Stream, Duration: result.Duration, ClientDisconnect: result.ClientDisconnect}, err
}

// QoderRequestMetadata clones request headers and includes the client identity for one platform attempt.
func QoderRequestMetadata(c *gin.Context) qoder.RequestMetadata {
	result := qoder.RequestMetadata{APIKeyID: APIKeyIDFromContext(c)}
	if c != nil && c.Request != nil {
		result.Headers = c.Request.Header.Clone()
		result.ClaudeCode = requeststate.IsClaudeCodeClient(c.Request.Context())
	}
	return result
}
