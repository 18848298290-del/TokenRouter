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

var _ forwardcore.Output = ForwardConversionOutput{}

// ForwardConversionOutput holds the current HTTP output and protocol conversion callbacks.
type ForwardConversionOutput struct {
	Context    *gin.Context
	Filter     *egress.CompiledHeaderFilter
	Responses  bool
	Reverse    func([]byte) []byte
	Commit     func()
	Diagnostic func(string, string, error, string, string)
}

func (o ForwardConversionOutput) CopyHeaders(headers map[string][]string) {
	if o.Filter != nil {
		egressprovider.WriteFilteredHeaders(o.Context.Writer.Header(), headers, o.Filter)
	}
}

func (o ForwardConversionOutput) BeginJSON() {
	o.Context.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
}

func (o ForwardConversionOutput) BeginStream() {
	h := o.Context.Writer.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	o.Context.Writer.WriteHeader(http.StatusOK)
}
func (o ForwardConversionOutput) ReverseTools(body []byte) []byte { return o.Reverse(body) }
func (o ForwardConversionOutput) JSONBytes(body []byte) {
	o.BeginJSON()
	o.Context.Writer.WriteHeader(http.StatusOK)
	if n, err := o.Context.Writer.Write(body); err != nil {
		_ = o.Context.Error(err)
	} else if n == len(body) {
		o.Commit()
	}
}

func (o ForwardConversionOutput) ResponsesJSON(value *protocolopenai.ResponsesResponse) {
	if body, err := json.Marshal(value); err == nil {
		o.JSONBytes(body)
	} else {
		o.Context.JSON(http.StatusOK, value)
	}
}

func (o ForwardConversionOutput) ChatJSON(value *protocolopenai.ChatCompletionsResponse) {
	if body, err := json.Marshal(value); err == nil {
		o.JSONBytes(body)
	} else {
		o.Context.JSON(http.StatusOK, value)
	}
}

func (o ForwardConversionOutput) Event(kind string, body []byte) (int, error) {
	var frame string
	if kind != "" {
		frame = fmt.Sprintf("event: %s\ndata: %s\n\n", kind, body)
	} else {
		frame = fmt.Sprintf("data: %s\n\n", body)
	}
	n, err := fmt.Fprint(o.Context.Writer, frame)
	// Mark delivery after a complete terminal frame; outer processing can still record read errors and usage.
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
func (o ForwardConversionOutput) Flush() { o.Context.Writer.Flush() }
func (o ForwardConversionOutput) Error(status int, kind, message string) {
	o.Commit()
	if o.Responses {
		o.Context.JSON(status, gin.H{"error": gin.H{"code": kind, "message": message}})
		return
	}
	o.Context.JSON(status, gin.H{"error": gin.H{"type": kind, "message": message}})
}

func (o ForwardConversionOutput) Observe(level, message string, err error, requestID, event string) {
	o.Diagnostic(level, message, err, requestID, event)
}

// WriteForwardMessageGenericError marks commitment and writes a generic Messages error.
func WriteForwardMessageGenericError(c *gin.Context, commit func()) {
	commit()
	c.JSON(http.StatusInternalServerError, gin.H{"type": "error", "error": gin.H{"type": "upstream_error", "message": "Upstream gateway error"}})
}

// WriteForwardCountSuccess uses the upstream Content-Type for passthrough and JSON otherwise.
func WriteForwardCountSuccess(c *gin.Context, status int, headers map[string][]string, body []byte, passthrough bool) {
	contentType := "application/json"
	if passthrough {
		if value := strings.TrimSpace(http.Header(headers).Get("Content-Type")); value != "" {
			contentType = value
		}
	}
	c.Data(status, contentType, body)
}

// WriteForwardCountError writes an Anthropic count error; the caller manages the commit marker.
func WriteForwardCountError(c *gin.Context, status int, kind, message string) {
	c.JSON(status, gin.H{"type": "error", "error": gin.H{"type": kind, "message": message}})
}

// WriteForwardGeminiErrorBody marks commitment and writes the supplied Gemini error.
func WriteForwardGeminiErrorBody(c *gin.Context, status int, contentType string, body []byte, commit func()) {
	commit()
	c.Data(status, contentType, body)
}
