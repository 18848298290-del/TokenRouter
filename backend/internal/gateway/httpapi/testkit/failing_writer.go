package testkit

import (
	"errors"

	"github.com/gin-gonic/gin"
)

// FailingWriter simulates a client write failure while retaining the embedded writer's other capabilities.
type FailingWriter struct {
	gin.ResponseWriter
	FailAfter int
	writes    int
}

func (w *FailingWriter) Write(p []byte) (int, error) {
	if w.writes >= w.FailAfter {
		return 0, errors.New("write failed")
	}
	w.writes++
	return w.ResponseWriter.Write(p)
}
