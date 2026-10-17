package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// forward_conversion_output.go delivers converted JSON and SSE responses and
// records handling of terminal output.
// Protocol conversion can finish with a complete JSON result, a terminal SSE
// frame or an explicit error path. A handled-output marker lets the outer
// forwarder know when this adapter has completed the relevant output step. For
// successful byte output, the marker follows a complete write; explicit JSON
// error output marks handling before attempting delivery.
//
// Terminal recognition follows the client protocol: Responses interprets the
// effective event type, while Chat recognizes DONE and error objects. Writer
// errors and partial writes retain their returned evidence. Header filtering
// and tool reversal are supplied callbacks, leaving the protocol converter
// responsible for their content and the HTTP adapter responsible for
// synchronous response delivery. Converted upstream events carry timing source
// observation through the active writer chain. Locally generated stream errors
// use LocalEvent so they cannot establish upstream delivery milestones.

// The interface assertion verifies that conversion output implements the shared
// synchronous header, body, event and diagnostic callbacks used by forwarders.
var _ forwardcore.Output = ForwardConversionOutput{}

// ForwardConversionOutput holds the current HTTP output and protocol conversion callbacks.
type ForwardConversionOutput struct {
	Context *gin.Context // required synchronous HTTP request context
	// Filter controls allowed upstream header copying; nil skips that step. A
	// present filter is read when CopyHeaders is invoked for this response.
	Filter *egress.CompiledHeaderFilter
	// Responses selects terminal event recognition and error-envelope fields;
	// false uses Chat termination markers and its error type field for conversion
	// output.
	Responses bool
	Reverse   func([]byte) []byte // required tool-name restoration callback
	// Commit is required for handled-output marking. Complete successful writes
	// call it afterward; explicit error paths call it before attempting their JSON
	// response.
	Commit func()
	// required conversion diagnostic callback
	Diagnostic func(string, string, error, string, string)
}

// CopyHeaders transfers allowed upstream values through the configured egress
// filter. A nil filter skips copying; a present filter requires the bound
// request writer and applies its configured header policy synchronously.
func (o ForwardConversionOutput) CopyHeaders(headers map[string][]string) {
	if o.Filter != nil {
		egressprovider.WriteFilteredHeaders(o.Context.Writer.Header(), headers, o.Filter)
	}
}

// BeginJSON sets the client content type for converted JSON output.
func (o ForwardConversionOutput) BeginJSON() {
	o.Context.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
}

// BeginStream installs SSE transport headers and writes the successful status
// line. It requires the bound request writer; callers then emit events and
// separately mark handled output when their terminal or error path qualifies.
func (o ForwardConversionOutput) BeginStream() {
	h := o.Context.Writer.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	o.Context.Writer.WriteHeader(http.StatusOK)
}

// ReverseTools delegates restoration through the required callback bound to
// conversion. The callback determines whether bytes are reused or replaced, so
// callers use the returned body rather than assuming in-place mutation.
func (o ForwardConversionOutput) ReverseTools(body []byte) []byte { return o.Reverse(body) }

// JSONBytes writes a successful converted JSON body and marks a complete write
// handled. Write errors attach to Gin; short writes with no error leave Commit
// uncalled. The request writer and Commit callback are required.
func (o ForwardConversionOutput) JSONBytes(body []byte) {
	o.BeginJSON()
	o.Context.Writer.WriteHeader(http.StatusOK)
	if n, err := o.Context.Writer.Write(body); err != nil {
		_ = o.Context.Error(err)
	} else if n == len(body) {
		o.Commit()
	}
}

// ResponsesJSON encodes a converted response before using checked byte output.
// Encoding failure delegates to Gin JSON rendering; that fallback owns its
// rendering error behavior and does not invoke this adapter's Commit callback.
func (o ForwardConversionOutput) ResponsesJSON(value *protocolopenai.ResponsesResponse) {
	if body, err := json.Marshal(value); err == nil {
		o.JSONBytes(body)
	} else {
		// Delegate rendering after encoding failure, leaving Gin to attach its
		// rendering error while this adapter retains no complete-byte delivery
		// marker.
		o.Context.JSON(http.StatusOK, value)
	}
}

// ChatJSON encodes a converted Chat response before using checked byte output.
// Encoding failure delegates to Gin JSON rendering, whose rendering path does
// not invoke this adapter's explicit handled-output callback.
func (o ForwardConversionOutput) ChatJSON(value *protocolopenai.ChatCompletionsResponse) {
	if body, err := json.Marshal(value); err == nil {
		o.JSONBytes(body)
	} else {
		// Delegate rendering after encoding failure, leaving Gin to attach its
		// rendering error while this adapter retains no complete-byte delivery
		// marker.
		o.Context.JSON(http.StatusOK, value)
	}
}

// Event writes converted upstream output with its timing source attached.
// Callers emit synchronously and use LocalEvent for converter-generated errors.
// The source scope ends on every return, including short writes and failures.
func (o ForwardConversionOutput) Event(kind string, body []byte) (int, error) {
	sink := NewResponseSink(o.Context)
	if sink.timing != nil {
		// Scope the existing writer chain so conversion keeps its exact write
		// count and error while timing observes only successful upstream bytes.
		sink.timing.upstreamWrites.Add(1)
		defer sink.timing.upstreamWrites.Add(-1)
	}
	return o.writeEvent(kind, body)
}

// LocalEvent writes a converter-generated SSE frame with local provenance.
// It retains terminal commitment and write results; the caller flushes the
// existing stream after handling the result.
func (o ForwardConversionOutput) LocalEvent(kind string, body []byte) (int, error) {
	return o.writeEvent(kind, body)
}

// writeEvent writes one SSE frame and returns its byte count and error.
// A nonempty kind adds an event line; otherwise it emits a data-only frame.
//
// Handled output is recorded only after the complete frame is written with no
// error. Responses uses the effective event type for terminal detection, while
// Chat accepts DONE or an error object. This keeps a partial terminal write
// from qualifying as delivered completion while outer processing can still
// retain usage and read failure evidence.
func (o ForwardConversionOutput) writeEvent(kind string, body []byte) (int, error) {
	var frame string
	if kind != "" {
		frame = fmt.Sprintf("event: %s\ndata: %s\n\n", kind, body)
	} else {
		frame = fmt.Sprintf("data: %s\n\n", body)
	}
	n, err := fmt.Fprint(o.Context.Writer, frame)
	// Require a complete terminal write before reporting handled stream output,
	// so partial delivery leaves outer processing able to retain its failure and usage evidence.
	if err == nil && n == len(frame) {
		terminal := false
		if o.Responses {
			terminal = protocolopenai.OpenAIStreamEventTypeIsTerminal(protocolopenai.EffectiveOpenAISSEEventType(body, kind))
		} else {
			terminal = strings.TrimSpace(string(body)) == "[DONE]" || gjson.GetBytes(body, "error").IsObject()
		}
		if terminal {
			o.Commit()
		}
	}
	return n, err
}

// Flush sends buffered conversion bytes through the bound Gin response writer.
func (o ForwardConversionOutput) Flush() { o.Context.Writer.Flush() }

// Error marks this conversion error handled before writing its JSON envelope.
// Responses puts kind in code and Chat puts it in type; the marker describes
// error handling, since later delivery failure can still occur.
func (o ForwardConversionOutput) Error(status int, kind, message string) {
	o.Commit()
	if o.Responses {
		o.Context.JSON(status, gin.H{"error": gin.H{"code": kind, "message": message}})
		return
	}
	o.Context.JSON(status, gin.H{"error": gin.H{"type": kind, "message": message}})
}

// Observe forwards conversion diagnostics through the required callback with
// its supplied level, message and event. The callback receives the upstream
// request ID and optional error as separate values for request association.
func (o ForwardConversionOutput) Observe(level, message string, err error, requestID, event string) {
	o.Diagnostic(level, message, err, requestID, event)
}

// WriteForwardMessageGenericError marks a handled Messages failure before
// writing generic 500 output. The callback and Gin context are required;
// marking precedes delivery so outer error handling can see that this path has
// attempted its response.
func WriteForwardMessageGenericError(c *gin.Context, commit func()) {
	commit()
	c.JSON(http.StatusInternalServerError, gin.H{"type": "error", "error": gin.H{"type": "upstream_error", "message": "Upstream gateway error"}})
}

// WriteForwardCountSuccess writes counted output with the supplied status and
// body. Passthrough uses a nonblank trimmed upstream Content-Type; other cases
// select application/json, allowing ordinary count results to use their
// expected decoder.
func WriteForwardCountSuccess(c *gin.Context, status int, headers map[string][]string, body []byte, passthrough bool) {
	contentType := "application/json"
	if passthrough {
		if value := strings.TrimSpace(http.Header(headers).Get("Content-Type")); value != "" {
			contentType = value
		}
	}
	c.Data(status, contentType, body)
}

// WriteForwardCountError writes an Anthropic count failure with the supplied
// status. The caller owns separate handled-output marking; this helper emits
// the protocol envelope using the provided error kind and message.
func WriteForwardCountError(c *gin.Context, status int, kind, message string) {
	c.JSON(status, gin.H{"type": "error", "error": gin.H{"type": kind, "message": message}})
}

// WriteForwardGeminiErrorBody marks a handled Gemini error before writing its
// supplied bytes. The callback is required and runs before delivery; status and
// Content-Type are provided by the platform error adapter.
func WriteForwardGeminiErrorBody(c *gin.Context, status int, contentType string, body []byte, commit func()) {
	commit()
	c.Data(status, contentType, body)
}
