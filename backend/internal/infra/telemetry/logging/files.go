package logging

import (
	"errors"
	"io"
	"sync"
)

// fileWriters retains ownership of file outputs across logger reconfiguration.
var fileWriters struct {
	sync.Mutex
	values []io.Closer
}

func ownFileWriter(writer io.Closer) {
	fileWriters.Lock()
	fileWriters.values = append(fileWriters.values, writer)
	fileWriters.Unlock()
}

// CloseFiles closes all registered file outputs. Call it after log producers stop
// and Sync completes. It does not wait for lumberjack's maintenance goroutine.
func CloseFiles() error {
	fileWriters.Lock()
	writers := fileWriters.values
	fileWriters.values = nil
	fileWriters.Unlock()
	var errs []error
	for _, writer := range writers {
		errs = append(errs, writer.Close())
	}
	return errors.Join(errs...)
}
