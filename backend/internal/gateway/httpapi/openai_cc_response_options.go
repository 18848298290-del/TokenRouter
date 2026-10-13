package httpapi

import (
	"bufio"
	"io"

	"github.com/gin-gonic/gin"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Chat Completion readers observe service tiers while decoding chunks and
// bounded JSON bodies. Their callbacks retain the active HTTP request so model
// observations, body-limit diagnostics and protocol errors reach the same
// request that receives output.

// CCOptions binds Chat Completion observations and bounded body reading to c.
// Keep c and writeError available until the reader finishes; chunk tier state
// belongs to this option instance, while JSON observations update request
// state.
func (p *OpenAIResponseOutput) CCOptions(c *gin.Context, writeError func(*gin.Context, int, string, string)) openai.CCResponseOptions {
	tierObserver := &forwardcore.ResponseObserver{}
	maxLineSize := OpenAIResponseDefaultMaxLineSize
	if p.Options.Configured && p.Options.MaxLineSize > 0 {
		maxLineSize = p.Options.MaxLineSize
	}
	return openai.CCResponseOptions{
		MaxLineSize: maxLineSize,
		ObserveChunk: func(body []byte) {
			tierObserver.ObserveOpenAI(body, wire.OpenAIChatCompletionServiceTierEventType(body))
			if observer := UpstreamResponseModelObserverFromContext(c); observer != nil {
				observer.ObserveOpenAI(body, "")
			}
		},
		ObserveJSON: func(body []byte) {
			ObserveOpenAIServiceTierInContext(c, body, "response.completed")
			if observer := UpstreamResponseModelObserverFromContext(c); observer != nil {
				observer.ObserveOpenAI(body, "")
			}
		},
		ServiceTier: tierObserver.ServiceTier,
		ReadBody: func(r io.Reader) ([]byte, error) {
			return ReadUpstreamResponseBody(r, p.Options.ReadLimit, c, OpenAIResponseTooLarge)
		},
		BodyLimitError: httpclient.ErrResponseBodyTooLarge,
		WriteError:     func(status int, kind, message string) { writeError(c, status, kind, message) },
	}
}

// Scanner creates a compatibility SSE scanner with the configured line-size
// limit. A missing configuration or nonpositive MaxLineSize selects
// OpenAIResponseDefaultMaxLineSize; callers supply and retain ownership of the
// reader being scanned.
func (p *OpenAIResponseOutput) Scanner(r io.Reader) *bufio.Scanner {
	maxLineSize := OpenAIResponseDefaultMaxLineSize
	if p.Options.Configured && p.Options.MaxLineSize > 0 {
		maxLineSize = p.Options.MaxLineSize
	}
	return openai.NewCompatSSEScanner(r, maxLineSize)
}
