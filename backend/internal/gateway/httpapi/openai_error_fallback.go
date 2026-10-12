package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
)

func (h OpenAIErrorOutput) EnsureResponse(c *gin.Context, streamStarted bool, err error) bool {
	if c == nil || c.Writer == nil {
		return false
	}
	// Stop both heartbeat writers before inspecting Writer state.
	compactKeepaliveCommitted := StopOpenAICompactSSEKeepaliveCommitted(c)
	if compactKeepaliveCommitted {
		streamStarted = true
	}
	imageKeepalivePresent := OpenAIImagesJSONKeepalivePresent(c)
	StopOpenAIImagesJSONKeepaliveCommitted(c)
	imageKeepalivePaddingOnly := false
	imageKeepaliveResponseWritten := false
	if imageKeepalivePresent {
		adjustedSize := OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c)
		imageKeepalivePaddingOnly = adjustedSize < 0
		imageKeepaliveResponseWritten = adjustedSize >= 0
	}
	compactKeepaliveHasMeaningfulOutput := compactKeepaliveCommitted && OpenAICompactKeepaliveAdjustedWrittenSize(c) > 0
	// A Compact heartbeat may have committed HTTP 200 while a terminal protocol event is still needed.
	if (IsResponseCommitted(c) && (!compactKeepaliveCommitted || compactKeepaliveHasMeaningfulOutput)) ||
		(!compactKeepaliveCommitted && imageKeepaliveResponseWritten) {
		return false
	}
	errType := "upstream_error"
	message := "Upstream request failed"
	status := http.StatusBadGateway
	if warning, ok := forwardcore.WarningFromError(err); ok && gatewayprovider.IsOpenAICyberWarningPayload(warning.ResponseBody, warning.Message) {
		errType = "invalid_request_error"
		message = gatewayprovider.ExtractOpenAICyberWarningMessage(warning.ResponseBody, warning.Message)
		if warning.StatusCode >= 400 && warning.StatusCode <= 599 {
			status = warning.StatusCode
		}
	}
	// SSE heartbeats require a terminal event; image JSON padding can still be followed by a JSON error.
	if c.Writer.Written() && !imageKeepalivePaddingOnly {
		streamStarted = true
	}
	h.StreamError(c, status, errType, message, streamStarted)
	return true
}

func ShouldLogOpenAIForwardFailureAsWarn(c *gin.Context, wroteFallback bool) bool {
	if wroteFallback {
		return false
	}
	if c == nil || c.Writer == nil {
		return false
	}
	return c.Writer.Written()
}

// OpenAIForwardErrorAlreadyCommunicated reports whether forwarding already delivered the upstream error.
//
// HTTP 200 streams can carry terminal failures such as policy refusals.
// Forwarding delivers the event before returning an error for logging and metrics.
// Appending a generic failure afterward would give strict clients duplicate terminal events.
func OpenAIForwardErrorAlreadyCommunicated(c *gin.Context, writerSizeBeforeForward int, err error) bool {
	if err == nil || c == nil || c.Writer == nil {
		return false
	}
	// Exclude heartbeat bytes when checking whether response output started (#3887).
	if OpenAICompactKeepaliveAdjustedWrittenSize(c) == writerSizeBeforeForward || OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c) == writerSizeBeforeForward {
		return false
	}
	if GetOpsCyberPolicy(c) != nil {
		return true
	}

	msg := strings.TrimSpace(err.Error())
	for _, prefix := range []string{
		"upstream response failed:",
		"non-streaming openai protocol error:",
	} {
		if strings.HasPrefix(msg, prefix) {
			return true
		}
	}
	return false
}
