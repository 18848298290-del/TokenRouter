package httpapi

import (
	"strconv"

	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
)

// parseFailureSnippetLen bounds each head and tail snippet to 256 bytes.
// Bodies longer than this limit are logged as snippets rather than in full.
const parseFailureSnippetLen = 256

// LogRequestBodyParseFailure logs a JSON parsing or validation error with body diagnostics.
// The error, byte length, and escaped head and tail help diagnose invalid or truncated input.
// Callers using gjson.ValidBytes can pass nil to derive an error from the body.
func LogRequestBodyParseFailure(reqLog *zap.Logger, body []byte, err error) {
	if reqLog == nil {
		return
	}
	if err == nil {
		err = requeststate.DescribeInvalidJSON(body)
	}

	head := body
	var tail []byte
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

// SanitizeBodySnippet quotes control characters and invalid UTF-8 for a single-line log value.
func SanitizeBodySnippet(b []byte) string {
	return strconv.Quote(string(b))
}
