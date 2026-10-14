package backup

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// runtimeSettings supplies stored fixture values and an optional blocked-read cancellation gate.
type runtimeSettings struct {
	// mu protects values during ordinary fixture reads and writes. Tests configure
	// the other controls before operation work starts or after earlier work has completed.
	mu     sync.Mutex        // stored fixture value lock
	values map[string]string // fixture JSON setting values
	// entered and once announce the first blocked read. A configured entered channel
	// makes reads wait for their context; cancelled records that observed cancellation atomically.
	entered chan struct{} // blocked settings read notification
	once    sync.Once     // one read-entry notification guard
	// fail is configured before the tested write path starts. Set returns an
	// injected persistence error before changing values whenever this flag is true.
	fail      bool
	cancelled atomic.Bool // blocked read observed cancellation
}

// runtimeArchive supplies inert archive work and counts requested restore executions.
type runtimeArchive struct {
	calls atomic.Int32 /* observed restore operation count */
}

// GetValue returns a configured setting under the fixture lock, or blocks when
// entered is configured. The blocking path announces its first read, waits for
// context cancellation, marks that observation, and returns the context error.
func (r *runtimeSettings) GetValue(ctx context.Context, key string) (string, error) {
	if r.entered != nil {
		// Announce the settings read before waiting so shutdown tests can cancel
		// service warmup at an observed blocking point instead of relying on a startup delay.
		r.once.Do(func() { close(r.entered) })
		<-ctx.Done()
		r.cancelled.Store(true)
		return "", ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.values[key], nil
}

// Set stores fixture text under its value lock unless fail requests an injected
// persistence error. Nil maps are initialized before the first successful write;
// failure returns before mutation so tests can check mandatory record-registration handling.
func (r *runtimeSettings) Set(ctx context.Context, key, value string) error {
	// Serialize fixture writes with ordinary value reads so worker finalization
	// and request-side setup cannot expose a partially initialized settings map to each other.
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("planned record write failure")
	}
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[key] = value
	return nil
}

// Write returns successful empty archive output for lifecycle-only backup scenarios.
func (a *runtimeArchive) Write(context.Context, *BackupRecord, BackupObjectStore, *BackupS3Config, BackupDumpOptions, func(context.Context, *BackupRecord) error, func(time.Duration) (context.Context, context.CancelFunc)) (int64, error) {
	return 0, nil
}

// Restore counts the requested database operation and returns fixture success.
func (a *runtimeArchive) Restore(context.Context, *BackupRecord, BackupObjectStore) error {
	a.calls.Add(1)
	return nil
}

// runtimeBackup constructs an inactive service using the supplied settings and
// inert archive fixtures. Storage and encryption are omitted because these
// lifecycle scenarios operate on controlled settings and use New's no-op maintenance claimant.
func runtimeBackup(repo *runtimeSettings, archive *runtimeArchive) *BackupService {
	return New(repo, Options{Now: time.Now}, nil, nil, nil, archive, nil)
}
