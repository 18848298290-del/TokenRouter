package scheduler

import (
	"context"
	"errors"
	"sync"
)

// A bucket rebuild holds a storage lock until its work finishes. BucketLease
// retains the release result so cleanup paths can share one callback,
// including a lost-lock error returned by an ownership-aware storage adapter.

// ErrBucketLeaseLost tells a bucket rebuild that its lock owner is absent or
// has been replaced during cleanup.
var ErrBucketLeaseLost = errors.New("scheduler bucket lease lost")

// BucketLease runs its lock-release callback once and shares that result
// across callers.
type BucketLease struct {
	release func(context.Context) error // storage ownership cleanup callback
	once    sync.Once                   // serializes one cleanup execution
	// err is written inside once.Do and returned after that call
	// completes, so concurrent Release callers observe the same first
	// cleanup result.
	err error
}

// NewBucketLease retains release for the lock owner's cleanup paths. A nil
// callback creates a lease whose Release succeeds; construction performs no
// storage operation or background work.
func NewBucketLease(release func(context.Context) error) *BucketLease {
	return &BucketLease{release: release}
}

// Release invokes the cleanup callback once and returns its saved error.
// Concurrent callers wait for that execution; the first caller supplies the
// callback context, and nil leases succeed.
func (l *BucketLease) Release(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.release != nil {
			l.err = l.release(ctx)
		}
	})
	return l.err
}
