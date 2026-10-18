package httpapi

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// openai_response_protocol_helpers.go supplies image response timing,
// diagnostic summaries and error delivery bindings.
// Image response readers use separately configured idle and heartbeat
// intervals, expressed as whole seconds. A no-output diagnostic can retain
// structured event information while omitting body excerpts, and image error
// delivery accounts for JSON heartbeat padding before deciding whether a
// response can be written.
//
// Streaming image waits share the SSE controller. Admission may already have
// committed an SSE comment before forwarding captures its retry baseline, so
// image output sizing also discounts the independent queue-heartbeat counter.
// Baselines and later observations use the same total, keeping comments from
// each stage distinct from delivered image content across provider attempts.

// OpenAIImagesAdjustedWrittenSize returns image output bytes after excluding
// the selected JSON or SSE heartbeat format. Callers use this measure both
// before and after forwarding, because admission comments can precede the
// attempt and waiting or body comments can arrive during it. A heartbeat-only
// response reports the unwritten sentinel so a permitted retry remains possible
// across attempts. Nonstreaming JSON padding uses its own controller; streamed
// output combines SSE controller, body-reader, and admission observations.
func OpenAIImagesAdjustedWrittenSize(c *gin.Context) int {
	if OpenAIImagesJSONKeepalivePresent(c) {
		return OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c)
	}
	size := OpenAICompactKeepaliveAdjustedWrittenSize(c)
	if size < 0 {
		return size
	}
	// Admission records comments before the image controller exists. Subtract
	// those bytes once as well so queued requests keep the same output baseline.
	if value, ok := c.Get(gatewayStreamHeartbeatBytesKey); ok {
		written, _ := value.(int)
		if written > 0 {
			size -= written
			if size <= 0 {
				return -1
			}
		}
	}
	return size
}

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
// heartbeat-adjusted output size and stops the active keepalive before taking
// over the response. A committed SSE heartbeat selects an in-stream error.
func WriteOpenAIImagesUpstreamErrorResponse(c *gin.Context, err *openai.OpenAIImagesUpstreamError) bool {
	if err == nil {
		return false
	}
	return WriteImageError(c, &ImageErrorResponse{Status: err.ClientStatusCode(), Type: err.ClientErrorType(), Message: err.ClientMessage(), Code: err.Code, Param: err.Param}, func() int { return OpenAIImagesAdjustedWrittenSize(c) }, func() { StopOpenAIImagesJSONKeepaliveCommitted(c) })
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
