package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
)

// Client-format recognition applies a lightweight User-Agent path to model
// reads and stricter payload checks to ordinary Messages. These fixtures supply
// the required headers and body evidence, and verify that already parsed fields
// take precedence over unusable raw JSON.

// newClientContextFixture returns a Gin request context and its response recorder.
func newClientContextFixture(method, path string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, path, nil)
	return c, rec
}

// validClaudeCodeBodyJSON supplies the identity prompt and metadata used by strict validation.
func validClaudeCodeBodyJSON() []byte {
	return []byte(`{
		"model":"claude-3-5-sonnet-20241022",
		"system":[{"text":"You are Claude Code, Anthropic's official CLI for Claude."}],
		"metadata":{"user_id":"user_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa_account__session_aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}
	}`)
}

// TestSetClaudeCodeClientContext_FastPathAndStrictPath compares model reads with
// ordinary Messages under CLI and non-CLI headers. Recognition must accept the
// lightweight model path, require valid Messages evidence, and reject unrelated User-Agents.
func TestSetClaudeCodeClientContext_FastPathAndStrictPath(t *testing.T) {
	t.Run("non_cli_user_agent_sets_false", func(t *testing.T) {
		c, _ := newClientContextFixture(http.MethodPost, "/v1/messages")
		c.Request.Header.Set("User-Agent", "curl/8.6.0")
		SetClaudeCodeClientContext(c, validClaudeCodeBodyJSON(), nil)
		require.False(t, requeststate.IsClaudeCodeClient(c.Request.Context()))
	})

	t.Run("cli_non_messages_path_sets_true", func(t *testing.T) {
		c, _ := newClientContextFixture(http.MethodGet, "/v1/models")
		c.Request.Header.Set("User-Agent", "claude-cli/1.0.1")
		SetClaudeCodeClientContext(c, nil, nil)
		require.True(t, requeststate.IsClaudeCodeClient(c.Request.Context()))
	})

	t.Run("cli_messages_path_valid_body_sets_true", func(t *testing.T) {
		c, _ := newClientContextFixture(http.MethodPost, "/v1/messages")
		c.Request.Header.Set("User-Agent", "claude-cli/1.0.1")
		c.Request.Header.Set("X-App", "claude-code")
		c.Request.Header.Set("anthropic-beta", "message-batches-2024-09-24")
		c.Request.Header.Set("anthropic-version", "2023-06-01")
		SetClaudeCodeClientContext(c, validClaudeCodeBodyJSON(), nil)
		require.True(t, requeststate.IsClaudeCodeClient(c.Request.Context()))
	})

	t.Run("cli_messages_path_invalid_body_sets_false", func(t *testing.T) {
		c, _ := newClientContextFixture(http.MethodPost, "/v1/messages")
		c.Request.Header.Set("User-Agent", "claude-cli/1.0.1")
		// Required headers and body fields are missing for strict client validation.
		SetClaudeCodeClientContext(c, []byte(`{"model":"x"}`), nil)
		require.False(t, requeststate.IsClaudeCodeClient(c.Request.Context()))
	})
}

// TestSetClaudeCodeClientContext_ReuseParsedRequest supplies validated parsed fields
// alongside invalid raw JSON. Recognition must succeed from the parsed request,
// protecting callers that already decoded identity evidence from another body decode.
func TestSetClaudeCodeClientContext_ReuseParsedRequest(t *testing.T) {
	t.Run("reuse parsed request without body unmarshal", func(t *testing.T) {
		c, _ := newClientContextFixture(http.MethodPost, "/v1/messages")
		c.Request.Header.Set("User-Agent", "claude-cli/1.0.1")
		c.Request.Header.Set("X-App", "claude-code")
		c.Request.Header.Set("anthropic-beta", "message-batches-2024-09-24")
		c.Request.Header.Set("anthropic-version", "2023-06-01")

		parsedReq, err := requeststate.ParseGatewayRequest(requeststate.NewRequestBodyRef(validClaudeCodeBodyJSON()), "")
		require.NoError(t, err)
		// Supply invalid raw JSON after a valid parse. Successful recognition then
		// demonstrates that the supplied parsed fields take precedence over body decoding.
		SetClaudeCodeClientContext(c, []byte(`{invalid`), parsedReq)
		require.True(t, requeststate.IsClaudeCodeClient(c.Request.Context()))
	})
}
