package middleware

import (
	"errors"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// recovery.go converts recoverable handler panics into an HTTP error response.
// A panic can occur before a response is committed or after a handler has
// already written it. Recovery uses Gin's panic diagnostics, then emits the
// public 500 envelope only for an unwritten response; broken connections and
// committed responses end the handler chain.

// Recovery returns Gin panic recovery with the project's public JSON error
// envelope for unwritten responses. Gin captures its diagnostic writer when
// middleware is constructed. The callback aborts broken-connection errors and
// responses Gin already marks written, keeping any sent body intact.
//
// A writable response receives the fixed unknown-error 500 details, then
// aborts. Broken errors handled by this callback are attached to Gin's error
// list; Gin can also handle directly recognized broken connections before
// invoking the callback.
func Recovery() gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(gin.DefaultErrorWriter, func(c *gin.Context, recovered any) {
		recoveredErr, _ := recovered.(error)

		// A recognized broken connection cannot receive a useful error envelope.
		// Record its error for request observation and abort further handling at that
		// failed connection.
		if isBrokenPipe(recoveredErr) {
			if recoveredErr != nil {
				_ = c.Error(recoveredErr)
			}
			c.Abort()
			return
		}

		// Gin marks a response written after committing headers or attempting a
		// hijack. Abort because that writer flag prevents ordinary recovery output,
		// including appending an incompatible envelope to already sent content.
		if c.Writer.Written() {
			c.Abort()
			return
		}

		response.ErrorWithDetails(
			c,
			http.StatusInternalServerError,
			apperror.UnknownMessage,
			apperror.UnknownReason,
			nil,
		)
		c.Abort()
	})
}

// isBrokenPipe reports a broken pipe or peer reset wrapped in net.OpError and
// os.SyscallError. It follows errors.As chains and checks case-insensitive
// syscall text; nil or unmatched error structures return false.
func isBrokenPipe(err error) bool {
	if err == nil {
		return false
	}

	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		return false
	}

	var syscallErr *os.SyscallError
	if !errors.As(opErr.Err, &syscallErr) {
		return false
	}

	msg := strings.ToLower(syscallErr.Error())
	return strings.Contains(msg, "broken pipe") || strings.Contains(msg, "connection reset by peer")
}
