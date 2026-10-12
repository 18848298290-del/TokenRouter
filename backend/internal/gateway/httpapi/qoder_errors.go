package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// Qoder requests can write keepalive bytes before an error occurs, including
// whitespace for a buffered JSON client. The output path therefore checks
// captured client stream intent as well as physical writer state before
// choosing JSON or SSE, preserving buffered clients’ parseable response
// format.
//
// Streaming Responses errors first attempt response.failed, Chat errors use an
// OpenAI error followed by DONE, and Messages errors use the Anthropic error
// shape. If the Responses terminal helper declines output, the current code
// falls through to the generic Anthropic SSE error path. A true helper result
// means handled even when it recorded a serialization or write error; callers
// cannot infer accepted bytes from that flag. Writers quote dynamic
// fields when constructing frames, and callers retain error classification and
// semantic commitment responsibilities.

// WriteQoderStreamError presents an endpoint-aware failure using observed
// output and captured client stream intent. Callers supply a usable
// context/writer and Ops stream flag; already-written headers or a true
// started flag enter the compatibility branch, but buffered requests still
// receive JSON.
//
// Streaming Responses first attempts response.failed and returns when the
// helper reports handled, including its serialization/write-error cases. A
// missing Flusher produces false and falls through to the Anthropic SSE path.
// Chat instead writes its error and DONE, while Messages uses an Anthropic
// error event. Before output starts the method uses plain endpoint JSON,
// leaving semantic commit and classification decisions to its caller.
func WriteQoderStreamError(c *gin.Context, status int, errType, message string, streamStarted bool, endpoint QoderEndpoint) {
	if streamStarted || c.Writer.Written() {
		// Buffered requests can already have keepalive whitespace; retain JSON in
		// that case so physical writes do not accidentally select an SSE envelope.
		if !qoderRequestIsStream(c) {
			WriteQoderError(c, status, errType, message, endpoint)
			return
		}
		if endpoint == QoderResponses {
			if WriteResponsesFailedSSE(c, errType, "", message, ErrorRequestID(c), ErrorRequestModel(c)) {
				return
			}
		}
		if endpoint == QoderChat {
			WriteQoderChatErrorSSE(c, errType, message)
			return
		}
		// A false Responses-helper result reaches this Anthropic error path because
		// that helper found no Flusher; true already ends this path even after a write error.
		errorEvent := `data: {"type":"error","error":{"type":` + strconv.Quote(errType) + `,"message":` + strconv.Quote(message) + `}}` + "\n\n"
		// Presentation cannot return write errors through this interface; the caller
		// retains execution failure while this path makes a best-effort client write.
		_, _ = c.Writer.WriteString(errorEvent)
		if flusher, ok := c.Writer.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}
	WriteQoderError(c, status, errType, message, endpoint)
}

// WriteQoderChatErrorSSE writes an escaped OpenAI Chat error followed by DONE.
// Callers supply a usable streaming writer; write errors are discarded here,
// and an available HTTP flusher is invoked after the frames.
func WriteQoderChatErrorSSE(c *gin.Context, errType, message string) {
	errorEvent := `data: {"error":{"type":` + strconv.Quote(errType) + `,"message":` + strconv.Quote(message) + `}}` + "\n\n" + "data: [DONE]\n\n"
	// Keep this terminal attempt best-effort; upstream failure remains the caller’s
	// error even when the client writer cannot accept the error or DONE frames.
	_, _ = c.Writer.WriteString(errorEvent)
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

// WriteQoderError writes a plain client-protocol JSON error with the selected
// status. Messages adds the Anthropic top-level error type; Chat, Responses
// and other endpoint values use the OpenAI error-object envelope.
func WriteQoderError(c *gin.Context, status int, errType, message string, endpoint QoderEndpoint) {
	if endpoint == QoderMessages {
		c.JSON(status, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    errType,
				"message": message,
			},
		})
		return
	}
	c.JSON(status, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

// qoderRequestIsStream reads the boolean stream intent stored during Ops
// observation. Nil context, absent state or a differently typed value returns
// false, allowing buffered keepalive output to retain JSON error handling.
func qoderRequestIsStream(c *gin.Context) bool {
	if c == nil {
		return false
	}
	v, _ := c.Get("ops_stream")
	stream, _ := v.(bool)
	return stream
}
