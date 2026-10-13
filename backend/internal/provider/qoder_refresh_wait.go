package provider

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// A Qoder request can encounter a refresh lock owned by another worker before
// that worker commits new credentials. An immediate reread followed by short
// polls distinguishes an observed credential change from merely waiting until
// the request's refresh budget ends.
//
// Changed observations win at each callback result, while read failures leave
// time for subsequent polls. A three-second child deadline also follows parent
// cancellation; expiry returns the in-progress sentinel with the latest read or
// context error as text. The request-side caller can then report unresolved
// refresh work instead of returning the failed credentials as a successful
// replacement. Timer cleanup is bounded by the callback honoring its supplied
// context.

// qoderRefreshLockWait bounds request-side observation of another refresh
// worker, while qoderRefreshLockPoll spaces repeated credential rereads within
// that budget.
const (
	qoderRefreshLockWait = 3 * time.Second        // maximum credential observation wait
	qoderRefreshLockPoll = 100 * time.Millisecond // repeated database read cadence
)

// ErrQoderRefreshInProgress identifies a bounded wait that has not observed
// replacement credentials. Last read or context errors are appended as text to
// its returned wrapper.
var ErrQoderRefreshInProgress = errors.New("qoder refresh in progress")

// WaitForQoderRefresh waits for readChanged to observe replacement credentials,
// reading immediately and then polling within a three-second child context.
// Callers supply a nonnil parent and callback; changed=true returns success
// even when the same callback also returns an error. readChanged honors the
// supplied context to keep a blocking read within the shared wait budget.
//
// Read errors are retained while later polls remain possible, because another
// worker may still commit within the budget. Every expiry or cancellation
// result wraps ErrQoderRefreshInProgress with the most recent read error or
// context error as text. Those diagnostic errors are not themselves wrapped;
// request-side callers use the sentinel to report an unfinished refresh.
func WaitForQoderRefresh(ctx context.Context, readChanged func(context.Context) (bool, error)) error {
	waitCtx, cancel := context.WithTimeout(ctx, qoderRefreshLockWait)
	defer cancel()
	// Preserve read diagnostics while continuing to wait for the other
	// refresh worker, whose commit may follow a transient read failure.
	var lastErr error
	if changed, err := readChanged(waitCtx); changed {
		return nil
	} else if err != nil {
		lastErr = err
	}
	ticker := time.NewTicker(qoderRefreshLockPoll)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			// Keep the in-progress sentinel as the classified
			// error; the
			// final read or cancellation cause remains diagnostic
			// text
			// when no replacement was observed.
			if lastErr != nil {
				return fmt.Errorf("%w: %v", ErrQoderRefreshInProgress, lastErr)
			}
			return fmt.Errorf("%w: %v", ErrQoderRefreshInProgress, waitCtx.Err())
		case <-ticker.C:
			changed, err := readChanged(waitCtx)
			if changed {
				return nil
			}
			// A later successful read can still observe the
			// winner's commit;
			// retain the latest failure for expiry diagnostics and
			// keep
			// polling within the remaining budget.
			if err != nil {
				lastErr = err
			}
		}
	}
}
