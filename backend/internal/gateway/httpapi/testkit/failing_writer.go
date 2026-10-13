package testkit

import (
	"errors"

	"github.com/gin-gonic/gin"
)

// Client write failures need to occur after a chosen number of output attempts
// in streaming tests. The wrapper counts Write calls and returns an injected
// error at its limit, while the embedded Gin writer continues supplying
// headers, flush and status inspection.

// FailingWriter simulates a client write failure while retaining the embedded writer's other capabilities.
type FailingWriter struct {
	gin.ResponseWriter // embedded HTTP writer capabilities
	// FailAfter limits delegated Write attempts; zero or negative values fail
	// immediately. Earlier delegated failures still consume an attempt before this
	// threshold.
	FailAfter int
	writes    int // delegated Write attempt count
}

// Write delegates at most FailAfter calls before returning a write failure. The
// attempt counter increments before each delegated write, including a delegated
// error; zero or negative limits fail the first call while the embedded writer
// supplies other capabilities.
func (w *FailingWriter) Write(p []byte) (int, error) {
	if w.writes >= w.FailAfter {
		return 0, errors.New("write failed")
	}
	w.writes++
	return w.ResponseWriter.Write(p)
}
