package logging

import (
	"errors"
	"io"
	"sync"
)

// Logger reconfiguration can replace output cores while earlier rotating file
// writers remain owned by the process. Registration retains those closers
// across replacements, and shutdown closes the accumulated outputs after
// producers have stopped and the current logger has been synchronized.
// Lumberjack can keep a separate rotation-maintenance goroutine beyond the
// file close itself.
//
// CloseFiles detaches the current registry under its mutex before closing any
// writer. Repeated or concurrent close calls therefore receive separate
// lists, while a registration arriving after detachment stays available for a
// later cleanup. Closing happens outside the registry lock so writer I/O
// cannot block later ownership registration.

// fileWriters keeps rotating file outputs owned across logger reconfiguration
// until shutdown detaches and closes the retained list after producers stop.
var fileWriters struct {
	// Mutex protects values while writers register and CloseFiles detaches the
	// current list. Cleanup retains the detached values and closes after
	// unlocking, allowing registration to publish another list for later cleanup.
	sync.Mutex             // writer registration mutex
	values     []io.Closer // owned rotating file closers
}

// ownFileWriter registers writer for process-level file cleanup under the
// registry mutex. Callers supply a non-nil closer once per owned output;
// later logger reconfiguration leaves registered writers available to
// CloseFiles.
func ownFileWriter(writer io.Closer) {
	fileWriters.Lock()
	fileWriters.values = append(fileWriters.values, writer)
	fileWriters.Unlock()
}

// CloseFiles detaches the registered writer list and closes every detached
// writer, joining failures after all attempts. Call after log producers stop
// and Sync completes; lumberjack maintenance goroutines are not joined.
//
// Detachment occurs under the registry mutex before any Close call, so
// concurrent or repeated cleanup does not claim this same list twice. A later
// registration remains in the registry for a later close call. Callers
// register each owned writer once and serialize shutdown with new logger
// output creation.
func CloseFiles() error {
	fileWriters.Lock()
	// Detach ownership before closing so concurrent or repeated cleanup cannot
	// close this same list twice, while later registrations remain available for another call.
	writers := fileWriters.values
	fileWriters.values = nil
	fileWriters.Unlock()
	var errs []error
	for _, writer := range writers {
		errs = append(errs, writer.Close())
	}
	return errors.Join(errs...)
}
