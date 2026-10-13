package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestResolveOpenAIWSSessionHeadersPrefersCodexHyphenHeader supplies both
// header spellings and a prompt cache key. The hyphenated value must win and
// retain its matching diagnostic source label.
func TestResolveOpenAIWSSessionHeadersPrefersCodexHyphenHeader(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request.Header.Set("session-id", "codex-session")
	c.Request.Header.Set("session_id", "legacy-session")

	resolution := ResolveOpenAIWSSessionHeaders(c, "prompt-cache")

	require.Equal(t, "codex-session", resolution.SessionID)
	require.Equal(t, "header_session-id", resolution.SessionSource)
}

// TestResolveOpenAIWSSessionHeadersFallsBackToLegacyHeader supplies the legacy
// header and a prompt cache key. The header value must win and identify the
// legacy header as its source.
func TestResolveOpenAIWSSessionHeadersFallsBackToLegacyHeader(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request.Header.Set("session_id", "legacy-session")

	resolution := ResolveOpenAIWSSessionHeaders(c, "prompt-cache")

	require.Equal(t, "legacy-session", resolution.SessionID)
	require.Equal(t, "header_session_id", resolution.SessionSource)
}
