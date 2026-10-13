package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// A body-size wrapper reports an oversized request through the reader's error
// result. The handler in this case deliberately retains a successful status
// after observing that error, distinguishing the read limit from automatic HTTP
// rejection.

// TestRequestBodyLimit_LimitsBody sends five bytes through a four-byte
// request-body limit. The downstream reader receives an error while the handler
// keeps control of the final response status.
func TestRequestBodyLimit_LimitsBody(t *testing.T) {
	r := gin.New()
	r.Use(RequestBodyLimit(4))
	r.POST("/t", func(c *gin.Context) {
		_, err := io.ReadAll(c.Request.Body)
		require.Error(t, err)
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/t", bytes.NewBufferString("12345"))
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}
