package httpapi

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	upstreamopenai "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// output_openai_images_response.go binds image response readers to HTTP
// limits, diagnostics and output observations.
// Image response readers need HTTP-specific body limits, error presentation
// and output observations while parsing stays in the native image
// implementation. Bound callbacks retain this request's writer and context,
// including heartbeat-adjusted byte observations used to decide response
// commitment and safe retry across header waits and response reading.

// ImageOptions binds image readers to a usable HTTP request and
// response-output instance. Callbacks retain Gin and its writer, so callers
// use them within this request's synchronous response handling. Configured
// disabled response-header handling preserves the upstream content type where
// the native reader supports that option.
//
// ReadBody applies the shared bounded response reader. ClassifyReadError reads
// c.Request.Context() when invoked, so current HTTP cancellation is distinguished
// from transport failure. Replacing c.Request changes the context consulted by
// later classification. EmptyOutput supplies a same-provider-retryable 502
// classification for missing images. Callers bind Backfill separately before
// using the direct API Key nonstream reader, which invokes it
// unconditionally. Header filtering, image error writing, timing settings and
// diagnostic summaries remain bound to their shared output services.
func (p *OpenAIResponseOutput) ImageOptions(c *gin.Context) upstreamopenai.ImageResponseOptions {
	return upstreamopenai.ImageResponseOptions{
		StartKeepalive: func() func() { return StartOpenAISSEKeepalive(c, p.ImageKeepaliveInterval()) },

		PreserveContentType: p.Options.Configured && !p.Options.ResponseHeadersEnabled,

		ReadLimit: func() int64 { return p.Options.ReadLimit },

		ReadBody: func(r io.Reader) ([]byte, error) {
			// Buffered image overflow can follow an SSE heartbeat. Use image-aware
			// presentation so the size-limit failure ends the already selected format.
			return ReadUpstreamResponseBody(r, p.Options.ReadLimit, c, func(c *gin.Context) {
				WriteOpenAIImagesUpstreamErrorResponse(c, &upstreamopenai.OpenAIImagesUpstreamError{
					StatusCode: http.StatusBadGateway,
					ErrorType:  "upstream_error",
					Message:    "Upstream response too large",
				})
			})
		},

		ClassifyReadError: func(err error) error {
			// Consult the current HTTP request context even when upstream execution is
			// detached, so that request's cancellation prevents treating its read failure
			// as recoverable transport evidence.
			if upstreamopenai.ShouldClassifyUpstreamStreamReadError(err, httpclient.ErrResponseBodyTooLarge, c.Request.Context()) {
				return upstreamopenai.NewUpstreamStreamReadError(err)
			}
			return err
		},

		ResponseHeaders: func(dst, src http.Header) { egressadapter.WriteFilteredHeaders(dst, src, p.Headers) },

		ObserveError: func(status int, message, detail string) { SetOpsUpstreamError(c, status, message, detail) },

		ObserveStreamError: func(err *upstreamopenai.OpenAIImagesUpstreamError) {
			// A client departure retains cancellation as the lifecycle outcome; other
			// committed stream failures publish their intended status for Ops monitoring.
			if c.Request != nil && c.Request.Context().Err() != nil {
				markOpsStreamError(c, OpsStreamError{ErrType: err.ClientErrorType(), Code: err.Code, Message: err.ClientMessage(), IntendedStatus: err.ClientStatusCode()})
			} else {
				MarkOpsStreamFailure(c, err.ClientErrorType(), err.Code, err.ClientMessage(), err.ClientStatusCode())
			}
		},

		WriteHTTPError: func(err *upstreamopenai.OpenAIImagesUpstreamError) bool {
			return WriteOpenAIImagesUpstreamErrorResponse(c, err)
		},

		EmptyOutput: func(body []byte) error {
			return &forwardcore.UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseBody: body, RetryableOnSameProvider: true}
		},

		Summary: p.ImageNoOutputSummary,

		AdjustedWrittenSize: func() int { return OpenAIImagesAdjustedWrittenSize(c) },

		RecordKeepalive: func(n int) { RecordOpenAIStreamKeepaliveBytes(c, n) },

		StreamInterval: p.ImageStreamInterval,

		KeepaliveInterval: p.ImageKeepaliveInterval,

		Logf: func(format string, args ...any) {
			logging.LegacyPrintf("service.openai_gateway", format, args...)
		},
	}
}
