package gemini

import (
	"io"
	"sync/atomic"
)

// executeBody wraps a response stream and counts explicit fixture close
// invocations.
type executeBody struct {
	io.ReadCloser // wrapped upstream response body
	// closes points to the nonnil shared atomic counter supplied by the fixture;
	// every Close invocation updates it before the wrapped body is closed.
	closes *atomic.Int32 // shared close invocation counter
}

// Close increments the shared close counter before delegating to the wrapped
// body. It counts every invocation, including repeated closes, and returns the
// wrapped close error to the fixture caller.
func (b *executeBody) Close() error { b.closes.Add(1); return b.ReadCloser.Close() }
