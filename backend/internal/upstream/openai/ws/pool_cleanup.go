package ws

import (
	"time"
)

// Queue cancellation can return a connection before the caller receives its
// lease. Returning capacity and notifying waiters happen before a coalesced
// cleanup signal, so connection closing can run in the background while
// cancellation completes promptly.
//
// Retirement examines provider connections under the provider mutex and returns
// sockets for later closing. Closed entries are removed directly; live entries
// remain protected by leases or pins. Unprotected connections retire at maximum
// age or the idle threshold for transports unable to ping without a reader.
// Compatible waiters can receive still-valid capacity, but they do not extend
// an expired socket lifetime. Map mutation and wakeup publication stay under
// the mutex, while actual close handshakes belong to the consuming cleanup
// path.

// requestCleanup schedules a background sweep through the one-slot wake
// channel. A full channel coalesces additional requests because cleanup is
// already pending; callers require a constructed pool and its worker lifecycle
// owner.
func (p *WSConnPool) requestCleanup() {
	// A sweep reads current provider state, so one pending wake covers later
	// reservation changes without accumulating a distinct signal for each cancelled acquisition.
	select {
	case p.cleanupWakeCh <- struct{}{}:
	default:
	}
}

// releaseUndeliveredConn returns an acquired connection that cannot be
// delivered to the caller. It releases the connection lease, publishes
// provider-pool change, and schedules cleanup, in that order. The enclosing
// acquisition path owns the cancellation decision and supplies the selected
// connection.
//
// Capacity becomes available before waiters are notified, allowing another
// acquisition to claim it immediately. Cleanup notification is coalesced after
// that wakeup so this path can return promptly while background code retires
// excess idle sockets. It performs no close handshake itself, separating
// undelivered-lease recovery from slow transport cleanup.
func (p *WSConnPool) releaseUndeliveredConn(providerID int64, conn *WSConn) {
	// Publish token capacity before waking selectors so they can use the returned
	// connection while its separately scheduled cleanup examines current demand.
	conn.release()
	p.notifyProviderPoolChanged(providerID)
	p.requestCleanup()
}

// connMaxAgeReached reports whether a nonnil connection has reached a positive
// lifetime limit. Nil connections and nonpositive limits return false, and
// equality counts as expiry for acquisition and idle retirement.
func (p *WSConnPool) connMaxAgeReached(conn *WSConn, now time.Time) bool {
	maxAge := p.maxConnAge()
	return conn != nil && maxAge > 0 && conn.age(now) >= maxAge
}

// retireExpiredConnsLocked removes unusable provider entries and returns
// connections requiring later closure. Callers hold ap.mu and close the
// returned sockets after releasing that mutex. Nil entries lose both connection
// and pin bookkeeping.
//
// Closed connections retire even when their prior lease or pin state remains.
// Live connections retain protection while leased or pinned; unprotected
// entries retire at maximum age or the reader-dependent idle recycle threshold.
// Waiters do not extend age eligibility. Evictions clear matching pins and
// publish one provider-change signal under the lock, keeping acquisition
// observations consistent before the caller performs transport cleanup.
func (p *WSConnPool) retireExpiredConnsLocked(ap *openAIWSProviderPool, now time.Time) []*WSConn {
	var evicted []*WSConn
	for id, conn := range ap.conns {
		if conn == nil {
			delete(ap.conns, id)
			delete(ap.pinnedConns, id)
			continue
		}
		expired := false
		select {
		case <-conn.closedCh:
			expired = true
		default:
			if conn.isLeased() || p.isConnPinnedLocked(ap, id) {
				continue
			}
			// Waiters can receive still-valid idle capacity, but maximum age takes
			// precedence over queue retention so expired sockets cannot serve a fresh lease.
			expired = p.connMaxAgeReached(conn, now)
			if !conn.supportsIdlePingWithoutReader() && conn.idleDuration(now) >= openAIWSConnIdleRecycleAfter {
				expired = true
				p.metrics.scaleDownTotal.Add(1)
			}
		}
		// Remove connection and pin bookkeeping together before notifying waiters,
		// which must observe the retired socket as unavailable when they retry selection.
		if expired {
			delete(ap.conns, id)
			delete(ap.pinnedConns, id)
			evicted = append(evicted, conn)
		}
	}
	if len(evicted) > 0 {
		ap.signalChangedLocked()
	}
	return evicted
}
