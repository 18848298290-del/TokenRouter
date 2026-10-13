package httpapi

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Image response readers use separately configured idle and heartbeat
// intervals, expressed as whole seconds. A no-output diagnostic can retain
// structured event information while omitting body excerpts, and image error
// delivery accounts for JSON heartbeat padding before deciding whether a
// response can be written.

// ImageStreamInterval returns the configured maximum interval between image
// stream data as a duration. Nil output, unconfigured options or a nonpositive
// ImageStreamDataIntervalTimeout returns zero, letting the reader disable that
// timer.
func (p *OpenAIResponseOutput) ImageStreamInterval() time.Duration {
	if p == nil || !p.Options.Configured || p.Options.ImageStreamDataIntervalTimeout <= 0 {
		return 0
	}
	// Configuration stores whole seconds for the reader timer.
	return time.Duration(p.Options.ImageStreamDataIntervalTimeout) * time.Second
}

// ImageKeepaliveInterval returns the configured image-stream heartbeat
// interval as a duration. Nil output, unconfigured options or a nonpositive
// ImageStreamKeepaliveInterval returns zero, letting the reader disable that
// timer.
func (p *OpenAIResponseOutput) ImageKeepaliveInterval() time.Duration {
	if p == nil || !p.Options.Configured || p.Options.ImageStreamKeepaliveInterval <= 0 {
		return 0
	}
	// Configuration stores whole seconds for the reader timer.
	return time.Duration(p.Options.ImageStreamKeepaliveInterval) * time.Second
}

// ImageNoOutputSummary builds structured diagnostics for an image response
// with no image output. Unconfigured output permits a body excerpt up to 1024
// bytes; configured options choose body logging and can lower that maximum
// with a positive value.
func (p *OpenAIResponseOutput) ImageNoOutputSummary(body []byte) string {
	includeBody := true
	// Keep image no-output excerpts bounded for diagnostic messages.
	maxSnippet := 1024
	if p != nil && p.Options.Configured {
		includeBody = p.Options.LogUpstreamErrorBody
		if cfgMax := p.Options.LogUpstreamErrorBodyMaxBytes; cfgMax > 0 && cfgMax < maxSnippet {
			maxSnippet = cfgMax
		}
	}
	return openai.SummarizeOpenAIImagesNoOutputBodyWithSnippet(body, includeBody, maxSnippet)
}

// WriteOpenAIImagesUpstreamErrorResponse writes the upstream image error using
// its client classification. Nil errors return false; the image writer uses
// heartbeat-adjusted output size and stops JSON keepalives before taking over
// the response.
func WriteOpenAIImagesUpstreamErrorResponse(c *gin.Context, err *openai.OpenAIImagesUpstreamError) bool {
	if err == nil {
		return false
	}
	return WriteImageError(c, &ImageErrorResponse{Status: err.ClientStatusCode(), Type: err.ClientErrorType(), Message: err.ClientMessage(), Code: err.Code, Param: err.Param}, func() int { return OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c) }, func() { StopOpenAIImagesJSONKeepaliveCommitted(c) })
}

// TextStreamInterval returns the maximum read interval for text conversion
// readers. Configured positive seconds become a duration; unconfigured or
// nonpositive values return zero so the upstream reader can disable its idle
// timer.
func (p *OpenAIResponseOutput) TextStreamInterval() time.Duration {
	if p.Options.Configured && p.Options.StreamDataIntervalTimeout > 0 {
		// Convert stored whole seconds to the reader duration.
		return time.Duration(p.Options.StreamDataIntervalTimeout) * time.Second
	}
	return 0
}

// openAICompatFailedResponseMessage extracts and trims a terminal Response
// error message. A nil Response or missing Error returns an empty string,
// allowing each protocol failure handler to choose its own message.
func openAICompatFailedResponseMessage(resp *protocolopenai.ResponsesResponse) string {
	if resp == nil || resp.Error == nil {
		return ""
	}
	return strings.TrimSpace(resp.Error.Message)
}
