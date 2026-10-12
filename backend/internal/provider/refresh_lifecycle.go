package provider

import (
	"context"
	"errors"
)

var ErrRefreshStopped = errors.New("oauth refresh coordinator is stopped")

type refreshActivity = operationActivity

// beginRefresh registers active work and cancellation, rejecting operations after stop.
func (api *OAuthRefreshAPI) beginRefresh(parent context.Context) (context.Context, func(), error) {
	return api.activity.begin(parent, ErrRefreshStopped)
}

// checkRefreshActive rechecks stop state after locking so a stopped coordinator cannot begin an exchange.
func (api *OAuthRefreshAPI) checkRefreshActive(ctx context.Context) error {
	api.activity.mu.Lock()
	defer api.activity.mu.Unlock()
	if api.activity.stopped {
		return context.Canceled
	}
	return ctx.Err()
}

// StopContext retains the first stop result, including a timeout with unfinished work.
func (api *OAuthRefreshAPI) StopContext(ctx context.Context) error {
	if api == nil {
		return nil
	}
	return api.activity.stop(ctx, "oauth refresh")
}
