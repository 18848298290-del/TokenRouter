package httpapi

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

func (p *OpenAIResponseOutput) ImageStreamInterval() time.Duration {
	if p == nil || !p.Options.Configured || p.Options.ImageStreamDataIntervalTimeout <= 0 {
		return 0
	}
	return time.Duration(p.Options.ImageStreamDataIntervalTimeout) * time.Second
}

func (p *OpenAIResponseOutput) ImageKeepaliveInterval() time.Duration {
	if p == nil || !p.Options.Configured || p.Options.ImageStreamKeepaliveInterval <= 0 {
		return 0
	}
	return time.Duration(p.Options.ImageStreamKeepaliveInterval) * time.Second
}

func (p *OpenAIResponseOutput) ImageNoOutputSummary(body []byte) string {
	includeBody := true
	maxSnippet := 1024
	if p != nil && p.Options.Configured {
		includeBody = p.Options.LogUpstreamErrorBody
		if cfgMax := p.Options.LogUpstreamErrorBodyMaxBytes; cfgMax > 0 && cfgMax < maxSnippet {
			maxSnippet = cfgMax
		}
	}
	return openai.SummarizeOpenAIImagesNoOutputBodyWithSnippet(body, includeBody, maxSnippet)
}

func WriteOpenAIImagesUpstreamErrorResponse(c *gin.Context, err *openai.OpenAIImagesUpstreamError) bool {
	if err == nil {
		return false
	}
	return WriteImageError(c, &ImageErrorResponse{Status: err.ClientStatusCode(), Type: err.ClientErrorType(), Message: err.ClientMessage(), Code: err.Code, Param: err.Param}, func() int { return OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c) }, func() { StopOpenAIImagesJSONKeepaliveCommitted(c) })
}

// TextStreamInterval returns the maximum read interval for these conversion paths.
// A nonpositive gateway.stream_data_interval_timeout disables the limit.
func (p *OpenAIResponseOutput) TextStreamInterval() time.Duration {
	if p.Options.Configured && p.Options.StreamDataIntervalTimeout > 0 {
		return time.Duration(p.Options.StreamDataIntervalTimeout) * time.Second
	}
	return 0
}

func openAICompatFailedResponseMessage(resp *protocolopenai.ResponsesResponse) string {
	if resp == nil || resp.Error == nil {
		return ""
	}
	return strings.TrimSpace(resp.Error.Message)
}
