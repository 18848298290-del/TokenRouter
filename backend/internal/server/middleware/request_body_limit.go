package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Request body limits take effect when a downstream reader consumes bytes.
// Wrapping the body before route parsing lets each handler map an excessive
// read to its own error response, while closing the wrapper reaches the
// underlying request body.

// RequestBodyLimit wraps the incoming body with http.MaxBytesReader before
// downstream parsing. The limit is in bytes, and negative limits behave as
// zero. Readers receive a MaxBytesError when they exceed it; handlers choose
// the response status. Closing the wrapper closes the underlying body.
func RequestBodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}
