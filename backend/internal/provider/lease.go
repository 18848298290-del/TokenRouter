package provider

import (
	"context"
	"time"
)

// AcquireSingletonLease skips contended leader locks. If the leader backend is unavailable, it tries advisory locking.
// If that backend is also absent, work proceeds without a lock. Call the release function when admitted work finishes.
func AcquireSingletonLease(ctx context.Context, leader CNMonitorLeader, advisory func(context.Context, string) (func(), bool), key, owner string, ttl time.Duration) (func(), bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if leader != nil {
		ok, err := leader.TryAcquireLeaderLock(ctx, key, owner, ttl)
		if err == nil {
			if !ok {
				return nil, false
			}
			return func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = leader.ReleaseLeaderLock(cleanup, key, owner)
			}, true
		}
	}
	if advisory != nil {
		return advisory(ctx, key)
	}
	return func() {}, true
}
