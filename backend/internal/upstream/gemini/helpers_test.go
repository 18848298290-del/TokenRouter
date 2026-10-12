package gemini

import (
	"io"
	"sync/atomic"
)

// executeBody counts calls that close the upstream response body.
type executeBody struct {
	io.ReadCloser
	closes *atomic.Int32
}

func (b *executeBody) Close() error { b.closes.Add(1); return b.ReadCloser.Close() }
