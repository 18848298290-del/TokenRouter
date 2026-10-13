package httpapi

import (
	"strconv"

	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
)

// Invalid JSON can result from malformed values or a body cut off in transit.
// Parse diagnostics retain the byte length and bounded beginning and ending
// excerpts, escaping their contents so each field remains one log value.

// parseFailureSnippetLen limits each logged body excerpt in bytes, bounding
// the request data copied into parsing diagnostics for large invalid bodies.
const parseFailureSnippetLen = 256

// LogRequestBodyParseFailure logs parsing diagnostics with escaped body
// excerpts and total byte length. A nil logger skips reporting; a nil error
// asks requeststate to diagnose malformed JSON. Large bodies retain separate
// bounded beginning and ending excerpts.
func LogRequestBodyParseFailure(reqLog *zap.Logger, body []byte, err error) {
	if reqLog == nil {
		return
	}
	if err == nil {
		err = requeststate.DescribeInvalidJSON(body)
	}

	head := body
	var tail []byte
	// Keep both ends when the full body exceeds the excerpt budget; the length
	// field still reports the original byte count.
	if len(body) > parseFailureSnippetLen {
		head = body[:parseFailureSnippetLen]
		tail = body[len(body)-parseFailureSnippetLen:]
	}

	fields := []zap.Field{
		zap.Error(err),
		zap.Int("body_len", len(body)),
		zap.String("body_head", SanitizeBodySnippet(head)),
	}
	if len(tail) > 0 {
		fields = append(fields, zap.String("body_tail", SanitizeBodySnippet(tail)))
	}
	reqLog.Warn("parse request body failed", fields...)
}

// SanitizeBodySnippet returns a quoted string with escaped controls and invalid
// UTF-8.
func SanitizeBodySnippet(b []byte) string {
	return strconv.Quote(string(b))
}
