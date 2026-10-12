package provider

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	qoderRefreshLockWait = 3 * time.Second
	qoderRefreshLockPoll = 100 * time.Millisecond
)

// ErrQoderRefreshInProgress indicates that polling has not observed replacement credentials.
var ErrQoderRefreshInProgress = errors.New("qoder refresh in progress")

// WaitForQoderRefresh reads immediately, then polls until credentials change or the wait budget expires.
// readChanged reports whether stored credentials have changed, plus any read error.
func WaitForQoderRefresh(ctx context.Context, readChanged func(context.Context) (bool, error)) error {
	waitCtx, cancel := context.WithTimeout(ctx, qoderRefreshLockWait)
	defer cancel()
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
			if lastErr != nil {
				return fmt.Errorf("%w: %v", ErrQoderRefreshInProgress, lastErr)
			}
			return fmt.Errorf("%w: %v", ErrQoderRefreshInProgress, waitCtx.Err())
		case <-ticker.C:
			changed, err := readChanged(waitCtx)
			if changed {
				return nil
			}
			if err != nil {
				lastErr = err
			}
		}
	}
}
