package provider

import (
	"context"
	"time"
)

// lease.go selects a maintenance lease backend and returns its release action.
// Periodic maintenance skips a round when the leader backend reports a held
// lease. A missing or failing leader backend permits the advisory alternative,
// and an installation with neither backend still performs maintenance, so
// backend failure and contention have different admission outcomes.
//
// A successful leader acquisition returns cleanup using a separate two-second
// background context, allowing release after the maintenance context is
// cancelled. The advisory callback supplies its own release function and
// admission result. Without either backend, the returned release function is a
// no-op and maintenance proceeds.

// AcquireSingletonLease returns an admission flag and a release callback for
// singleton maintenance. Nil ctx uses Background; leader contention returns
// false immediately, while a leader error or absent leader selects advisory when
// configured.
//
// Call release after admitted work finishes. Leader release uses a separate
// two-second cleanup context and discards its error; advisory admission and
// release follow the supplied callback. With no advisory callback, a nil leader
// or a leader acquisition error permits work with a no-op release. The lease is
// acquired once, so its owner supplies a TTL appropriate for the work.
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
			// Maintenance cancellation can precede release. Give the owner-matched leader
			// cleanup its own budget so the cancelled round can still relinquish its
			// lease; cleanup failure is ignored.
			return func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = leader.ReleaseLeaderLock(cleanup, key, owner)
			}, true
		}
	}
	// A leader error reaches this alternative, while ordinary contention returned
	// above. Forward the advisory result so that backend decides whether this round
	// owns a lock.
	if advisory != nil {
		return advisory(ctx, key)
	}
	// With no usable coordination backend, maintenance still runs. This also
	// follows a leader acquisition error when advisory is absent; supply callable
	// cleanup so admitted callers can retain their deferred release pattern.
	return func() {}, true
}
