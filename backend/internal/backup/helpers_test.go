package backup

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// runtimeSettings signals when a settings read starts, then waits for context cancellation.
type runtimeSettings struct {
	mu        sync.Mutex
	values    map[string]string
	entered   chan struct{}
	once      sync.Once
	fail      bool
	cancelled atomic.Bool
}

type runtimeArchive struct{ calls atomic.Int32 }

func (r *runtimeSettings) GetValue(ctx context.Context, key string) (string, error) {
	if r.entered != nil {
		r.once.Do(func() { close(r.entered) })
		<-ctx.Done()
		r.cancelled.Store(true)
		return "", ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.values[key], nil
}

func (r *runtimeSettings) Set(ctx context.Context, key, value string) error {
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

func (a *runtimeArchive) Write(context.Context, *BackupRecord, BackupObjectStore, *BackupS3Config, BackupDumpOptions, func(context.Context, *BackupRecord) error, func(time.Duration) (context.Context, context.CancelFunc)) (int64, error) {
	return 0, nil
}

func (a *runtimeArchive) Restore(context.Context, *BackupRecord, BackupObjectStore) error {
	a.calls.Add(1)
	return nil
}

func runtimeBackup(repo *runtimeSettings, archive *runtimeArchive) *BackupService {
	return New(repo, Options{DatabaseName: "test", Now: time.Now}, nil, nil, nil, archive, nil)
}
