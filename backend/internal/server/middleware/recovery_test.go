package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Panic recovery has separate diagnostic and public-response effects. These
// cases inspect Gin's captured panic output and compare the ordinary JSON
// envelope with a response already committed before the panic, keeping its sent
// body intact.

// TestRecovery_PanicLogContainsInfo raises a named panic under the project
// recovery middleware. The response returns 500 and Gin's captured diagnostics
// contain both the panic message and a stack-file reference.
func TestRecovery_PanicLogContainsInfo(t *testing.T) {
	// Replace Gin's process-wide diagnostic writer before constructing Recovery,
	// which captures that writer. Restore it at cleanup so later serial tests use
	// their own destination.
	var buf bytes.Buffer
	originalWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &buf
	t.Cleanup(func() {
		gin.DefaultErrorWriter = originalWriter
	})

	r := gin.New()
	r.Use(Recovery())
	r.GET("/panic", func(c *gin.Context) {
		panic("custom panic message for test")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)

	logOutput := buf.String()
	require.Contains(t, logOutput, "custom panic message for test", "日志应包含 panic 信息")
	require.Contains(t, logOutput, "recovery_test.go", "日志应包含堆栈跟踪文件名")
}

// TestRecovery compares an unwritten panic, successful handling, and a panic
// after a committed JSON response. Recovery supplies the public 500 envelope
// for the first case and keeps the already sent success response intact.
func TestRecovery(t *testing.T) {
	tests := []struct {
		name         string            // recovery scenario label
		handler      gin.HandlerFunc   // downstream panic or response action
		wantHTTPCode int               // expected final HTTP status
		wantBody     response.Response // expected decoded public envelope
	}{
		{
			name: "panic_returns_standard_json_500",
			handler: func(c *gin.Context) {
				panic("boom")
			},
			wantHTTPCode: http.StatusInternalServerError,
			wantBody: response.Response{
				Code:    http.StatusInternalServerError,
				Message: apperror.UnknownMessage,
				Reason:  "HTTP_500",
			},
		},
		{
			name: "no_panic_passthrough",
			handler: func(c *gin.Context) {
				response.Success(c, gin.H{"ok": true})
			},
			wantHTTPCode: http.StatusOK,
			wantBody: response.Response{
				Code:    0,
				Message: "success",
				Data:    map[string]any{"ok": true},
			},
		},
		{
			name: "panic_after_write_does_not_override_body",
			handler: func(c *gin.Context) {
				response.Success(c, gin.H{"ok": true})
				panic("boom")
			},
			wantHTTPCode: http.StatusOK,
			wantBody: response.Response{
				Code:    0,
				Message: "success",
				Data:    map[string]any{"ok": true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := gin.New()
			r.Use(Recovery())
			r.GET("/t", tt.handler)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/t", nil)
			r.ServeHTTP(w, req)

			require.Equal(t, tt.wantHTTPCode, w.Code)

			var got response.Response
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			require.Equal(t, tt.wantBody, got)
		})
	}
}
