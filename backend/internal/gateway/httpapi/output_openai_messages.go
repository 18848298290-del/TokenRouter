package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Buffered Messages compatibility reads collect terminal response data before
// producing a unary client result. HTTP options connect tool-name restoration
// and model/tier observation to request state, while stream failures retain
// sanitized diagnostics associated with the actual selected provider.

// RecordMessagesStreamError stores sanitized Messages stream failure evidence
// in request Ops. Nil context skips observation; a selected provider supplies
// its platform and identity, while missing provider metadata uses the OpenAI
// platform default.
func (p *OpenAIResponseOutput) RecordMessagesStreamError(c *gin.Context, provider *gatewayprovider.ExecutionProvider, upstreamRequestID, kind, message string) {
	if c == nil {
		return
	}
	message = logredact.SanitizeUpstreamQueries(message)
	SetOpsUpstreamError(c, http.StatusBadGateway, message, "")
	event := ops.OpsUpstreamErrorEvent{
		Platform:           capability.PlatformOpenAI,
		UpstreamStatusCode: http.StatusBadGateway,
		UpstreamRequestID:  strings.TrimSpace(upstreamRequestID),
		Kind:               kind,
		Message:            message,
	}
	if provider != nil {
		event.Platform = provider.Record.Platform
		event.ProviderID = provider.Record.ID
		event.ProviderName = provider.Record.Name
	}
	AppendOpsUpstreamError(c, event)
}

// ReadBufferedTerminal delegates buffered Responses terminal collection with
// HTTP-bound options. It returns the decoded terminal value, usage and
// accumulator from the shared reader; callers retain response cleanup and
// handle its terminal or read failure.
func (p *OpenAIResponseOutput) ReadBufferedTerminal(
	resp *http.Response,
	c *gin.Context,
	logPrefix string,
	requestID string,
) (*protocolopenai.ResponsesResponse, protocolopenai.ForwardUsage, *bridge.BufferedResponseAccumulator, error) {
	return openai.ReadCompatBufferedTerminal(resp, p.BufferedOptions(c, logPrefix, requestID))
}

// BufferedOptions builds the callbacks and scanner limit used by buffered
// terminal reading. MaxLineSize is chosen during construction; its interval
// callback reads options when invoked. Tool restoration and observation
// callbacks read current request state, and logPrefix plus requestID are
// retained for diagnostics.
func (p *OpenAIResponseOutput) BufferedOptions(c *gin.Context, logPrefix, requestID string) openai.CompatBufferedOptions {
	maxLineSize := OpenAIResponseDefaultMaxLineSize
	if p.Options.Configured && p.Options.MaxLineSize > 0 {
		maxLineSize = p.Options.MaxLineSize
	}
	return openai.CompatBufferedOptions{
		MaxLineSize: maxLineSize,
		StreamInterval: func() time.Duration {
			if p.Options.Configured && p.Options.StreamDataIntervalTimeout > 0 {
				// Convert the configured inactivity budget from seconds for the upstream
				// buffered reader.
				return time.Duration(p.Options.StreamDataIntervalTimeout) * time.Second
			}
			return 0
		},
		RestoreToolNames: func(body []byte) []byte { return RestoreCodexToolNamesFromContext(c, body) },
		Observe:          func(body []byte, event string) { ObserveOpenAIServiceTierInContext(c, body, event) },
		Log: func(message string, err error, interval time.Duration) {
			if err != nil {
				logging.L().Warn(logPrefix+": "+message, zap.Error(err), zap.String("request_id", requestID))
				return
			}
			logging.L().Warn(logPrefix+": "+message, zap.String("request_id", requestID), zap.Duration("interval", interval))
		},
	}
}
