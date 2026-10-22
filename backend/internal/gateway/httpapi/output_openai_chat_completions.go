package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// output_openai_chat_completions.go converts eligible buffered Chat read
// failures into classified failover errors.
// Buffered Chat conversion can lose its upstream stream before a complete
// client response exists. Only eligible transport interruptions enter stream
// policy: scanner-line limits, excluded body-size errors and request
// cancellation keep their existing error path. Request context therefore
// participates in deciding whether the read failure can trigger failover.
//
// Stream policy can replace a failure body with a generic envelope while
// recording health and retry evidence. The adapter restores the classified
// transport payload after that call so final error rules and client output can
// still identify the transport code after retry exhaustion.

// BufferedReadFailure converts eligible buffered-stream transport interruptions
// into policy failover evidence. It returns unrelated errors, scanner-line
// limits, request cancellation and excluded body-size errors unchanged; a
// missing request leaves classification without request-cancellation evidence.
//
// Response headers accompany the classified error when available. Stream
// policy performs its health and retry handling before the adapter restores
// the safe transport payload, because that policy can otherwise substitute a
// generic envelope that drops the transport code needed by final error rules.
func (p *OpenAIResponseOutput) BufferedReadFailure(
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	resp *http.Response,
	requestID string,
	err error,
) error {
	var readErr *openai.CompatBufferedReadError
	if !errors.As(err, &readErr) || readErr == nil || errors.Is(readErr.Unwrap(), bufio.ErrTooLong) {
		return err
	}
	var requestContext context.Context
	if c != nil && c.Request != nil {
		requestContext = c.Request.Context()
	}
	// Keep cancellation and excluded read limits in their existing failure path;
	// policy failover here is reserved for eligible upstream transport
	// interruptions.
	if !openai.ShouldClassifyUpstreamStreamReadError(readErr.Unwrap(), httpclient.ErrResponseBodyTooLarge, requestContext) {
		return err
	}
	classifiedErr := openai.NewUpstreamStreamReadError(readErr.Unwrap())
	code, message, ok := openai.OpenAIUpstreamStreamReadErrorDetails(classifiedErr)
	if !ok {
		return err
	}
	payload, _ := json.Marshal(gin.H{
		"error": gin.H{
			"type":    "upstream_error",
			"code":    code,
			"message": message,
		},
	})
	var responseHeaders http.Header
	if resp != nil {
		responseHeaders = resp.Header
	}
	failoverErr := p.NewStreamPolicyFailure(
		c, provider, false, requestID, responseHeaders, payload, message,
	)
	// Keep transport error codes stable after retry exhaustion so clients and passthrough rules can classify them.
	failoverErr.ResponseBody = payload
	return failoverErr
}
