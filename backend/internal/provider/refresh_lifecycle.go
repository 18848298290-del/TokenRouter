package provider

import (
	"context"
	"errors"
)

// A refresh coordinator can be stopped while callers are exchanging credentials
// or waiting for the same provider lock. Registration connects each operation to
// shutdown cancellation, and a second stop-state check after lock acquisition
// prevents a waiter from starting an exchange before its cancellation signal has
// arrived.
//
// The shared activity state tracks actual completion rather than context
// delivery, so a callback that ignores cancellation keeps shutdown unfinished.
// beginRefresh returns a finish callback that removes its registration, while
// StopContext cancels active work and records its first completed result. A
// timeout stays recorded after late completion. This wrapper uses the same
// activity implementation as provider maintenance services and checks its
// stopped flag under that implementation's mutex.

// ErrRefreshStopped identifies rejected new work after the OAuth coordinator or
// related refresh service has entered shutdown.
var ErrRefreshStopped = errors.New("oauth refresh coordinator is stopped")

// refreshActivity shares provider operation registration and retained shutdown
// results with operationActivity.
type refreshActivity = operationActivity

// beginRefresh registers refresh work with parent cancellation and shutdown,
// requiring a nonnil parent. The caller invokes the returned finish callback
// exactly once after all exchange and cleanup work has returned.
func (api *OAuthRefreshAPI) beginRefresh(parent context.Context) (context.Context, func(), error) {
	return api.activity.begin(parent, ErrRefreshStopped)
}

// checkRefreshActive checks the stopped flag under the activity mutex after a
// refresh lock is acquired. It returns context.Canceled for stopped state even
// before this waiter's context receives cancellation, otherwise returning
// ctx.Err().
func (api *OAuthRefreshAPI) checkRefreshActive(ctx context.Context) error {
	api.activity.mu.Lock()
	defer api.activity.mu.Unlock()
	if api.activity.stopped {
		return context.Canceled
	}
	return ctx.Err()
}

// StopContext rejects new registrations, cancels active exchanges and lock
// waiters, and waits for their finish callbacks. Nil receiver succeeds; reached
// shutdown requires a nonnil context.
//
// The shared activity implementation retains the first completed result,
// including a timeout when callbacks ignore cancellation. Later calls return
// that result after completion, while a concurrent waiting caller may exhaust
// its own context. Already registered work remains tracked until its actual
// return, and coordinator exchange paths separately discard credentials observed
// after their cancellation check.
func (api *OAuthRefreshAPI) StopContext(ctx context.Context) error {
	if api == nil {
		return nil
	}
	return api.activity.stop(ctx, "oauth refresh")
}
