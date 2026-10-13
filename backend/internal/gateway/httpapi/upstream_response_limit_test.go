package httpapi

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
)

// TestReadUpstreamResponseBody exercises valid bodies, overflow and a separate
// read failure. It requires the overflow callback only for the size sentinel,
// while every failed read returns no body.
func TestReadUpstreamResponseBody(t *testing.T) {
	t.Run("within limit", func(t *testing.T) {
		body, err := ReadUpstreamResponseBody(bytes.NewReader([]byte("ok")), httpclient.DefaultResponseReadMaxBytes, nil, nil)
		require.NoError(t, err)
		require.Equal(t, []byte("ok"), body)
	})

	t.Run("exceeds limit calls onTooLarge", func(t *testing.T) {
		maxBytes := int64(3)

		called := false
		onTooLarge := func(_ *gin.Context) { called = true }

		body, err := ReadUpstreamResponseBody(bytes.NewReader([]byte("toolong")), maxBytes, nil, onTooLarge)
		require.Nil(t, body)
		require.True(t, errors.Is(err, httpclient.ErrResponseBodyTooLarge))
		require.True(t, called)
	})

	t.Run("nil onTooLarge does not panic", func(t *testing.T) {
		maxBytes := int64(3)

		body, err := ReadUpstreamResponseBody(bytes.NewReader([]byte("toolong")), maxBytes, nil, nil)
		require.Nil(t, body)
		require.True(t, errors.Is(err, httpclient.ErrResponseBodyTooLarge))
	})

	t.Run("io error does not call onTooLarge", func(t *testing.T) {
		called := false
		onTooLarge := func(_ *gin.Context) { called = true }

		body, err := ReadUpstreamResponseBody(iotest.ErrReader(errors.New("disk failure")), httpclient.DefaultResponseReadMaxBytes, nil, onTooLarge)
		require.Nil(t, body)
		require.Error(t, err)
		require.False(t, errors.Is(err, httpclient.ErrResponseBodyTooLarge))
		require.False(t, called)
	})
}

// TestUpstreamResponseLimitPreservesProtocolEnvelope overflows a small buffer
// with Anthropic and OpenAI writers. It requires the size sentinel plus each
// protocol's 502 envelope and safe response-size message.
func TestUpstreamResponseLimitPreservesProtocolEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name  string         // inbound protocol label
		write TooLargeWriter // protocol overflow output callback
		body  string         // expected JSON error envelope
	}{
		{"anthropic", AnthropicResponseTooLarge, `{"type":"error","error":{"type":"upstream_error","message":"Upstream response too large"}}`},
		{"openai", OpenAIResponseTooLarge, `{"error":{"type":"upstream_error","message":"Upstream response too large"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(response)
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
			body, err := ReadUpstreamResponseBody(strings.NewReader("too long"), 3, c, tc.write)
			require.Nil(t, body)
			require.ErrorIs(t, err, httpclient.ErrResponseBodyTooLarge)
			require.Equal(t, http.StatusBadGateway, response.Code)
			require.JSONEq(t, tc.body, response.Body.String())
		})
	}
}
