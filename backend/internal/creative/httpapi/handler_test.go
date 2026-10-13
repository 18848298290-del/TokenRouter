package httpapi

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/creative"
)

// Multipart limits are checked while file parts are consumed, before creative
// submission begins. Handler scope tests separately exercise missing and
// malformed workspace headers and confirm that an accepted UUID is normalized
// while retaining the supplied user identifier.

// TestParseCreativeCreateRunMultipartStopsAtTotalLimit uploads two file parts
// whose combined bytes exceed the parser total limit. Parsing returns no
// request and CREATIVE_INPUT_TOO_LARGE before domain submission occurs.
func TestParseCreativeCreateRunMultipartStopsAtTotalLimit(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("group_id", "12"))
	part, err := writer.CreateFormFile("source_images[]", "source.png")
	require.NoError(t, err)
	_, err = part.Write([]byte("123456"))
	require.NoError(t, err)
	part, err = writer.CreateFormFile("source_images[]", "source-2.png")
	require.NoError(t, err)
	_, err = part.Write([]byte("7890"))
	require.NoError(t, err)
	// Finish the multipart boundary so this case reaches the cumulative file-byte
	// limit rather than failing because its synthetic HTTP form is incomplete.
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/creative/runs", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = req

	parsed, err := parseCreativeCreateRunMultipart(ctx, 32, 8)
	require.Nil(t, parsed)
	require.ErrorIs(t, err, creative.ErrCreativeInputTooLarge)
}

// TestCreativeRunScopeFromRequest supplies absent, invalid, and uppercase
// workspace headers to the scope helper. It checks the distinct errors and the
// normalized UUID paired with user seven.
func TestCreativeRunScopeFromRequest(t *testing.T) {
	t.Run("缺失 header", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		_, err := creativeRunScopeFromRequest(c, 7)
		require.ErrorIs(t, err, creative.ErrCreativeWorkspaceRequired)
	})

	t.Run("非法 UUID", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = &http.Request{Header: make(http.Header)}
		c.Request.Header.Set(creative.CreativeWorkspaceHeader, "invalid")
		_, err := creativeRunScopeFromRequest(c, 7)
		require.ErrorIs(t, err, creative.ErrCreativeWorkspaceInvalid)
	})

	t.Run("规范化 UUID", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = &http.Request{Header: make(http.Header)}
		c.Request.Header.Set(creative.CreativeWorkspaceHeader, "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA")
		scope, err := creativeRunScopeFromRequest(c, 7)
		require.NoError(t, err)
		require.Equal(t, creative.CreativeRunScope{UserID: 7, WorkspaceID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}, scope)
	})
}
