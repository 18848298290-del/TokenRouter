package provider

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// TestClassifyOpenAIWSDialError supplies an unfinished-handshake failure and a
// context deadline error inside WSDialError. The classifier produces the
// specific handshake and deadline tokens used by diagnostics.
func TestClassifyOpenAIWSDialError(t *testing.T) {
	t.Run("handshake_not_finished", func(t *testing.T) {
		err := &openai.WSDialError{
			StatusCode: http.StatusBadGateway,
			Err:        errors.New("WebSocket protocol error: Handshake not finished"),
		}
		require.Equal(t, "handshake_not_finished", classifyOpenAIWSDialError(err))
	})

	t.Run("context_deadline", func(t *testing.T) {
		err := &openai.WSDialError{
			StatusCode: 0,
			Err:        context.DeadlineExceeded,
		}
		require.Equal(t, "ctx_deadline_exceeded", classifyOpenAIWSDialError(err))
	})
}

// TestSummarizeOpenAIWSDialError includes upstream routing headers on an
// unfinished handshake failure. The summary retains status, classifier,
// server, Via, CF-Ray, and request ID, with placeholders for missing close
// information.
func TestSummarizeOpenAIWSDialError(t *testing.T) {
	err := &openai.WSDialError{
		StatusCode: http.StatusBadGateway,
		ResponseHeaders: http.Header{
			"Server":       []string{"cloudflare"},
			"Via":          []string{"1.1 example"},
			"Cf-Ray":       []string{"abcd1234"},
			"X-Request-Id": []string{"req_123"},
		},
		Err: errors.New("WebSocket protocol error: Handshake not finished"),
	}

	status, class, closeStatus, closeReason, server, via, cfRay, reqID := SummarizeOpenAIWSDialError(err)
	require.Equal(t, http.StatusBadGateway, status)
	require.Equal(t, "handshake_not_finished", class)
	require.Equal(t, "-", closeStatus)
	require.Equal(t, "-", closeReason)
	require.Equal(t, "cloudflare", server)
	require.Equal(t, "1.1 example", via)
	require.Equal(t, "abcd1234", cfRay)
	require.Equal(t, "req_123", reqID)
}

// TestOpenAIWSErrorEventDiagnostics parses a structured WS error containing
// code, type, and message. Diagnostic summarization retains the code and
// message while normalizing the type into its bounded log token.
func TestOpenAIWSErrorEventDiagnostics(t *testing.T) {
	message := []byte(`{"type":"error","error":{"type":"invalid_request_error","code":"invalid_request","message":"invalid input"}}`)
	code, kind, msg := SummarizeOpenAIWSErrorEventFieldsFromRaw(protocolopenai.ParseWSErrorEventFields(message))
	require.Equal(t, "invalid_request", code)
	require.Equal(t, "invalid_request_err", kind)
	require.Equal(t, "invalid input", msg)
}
