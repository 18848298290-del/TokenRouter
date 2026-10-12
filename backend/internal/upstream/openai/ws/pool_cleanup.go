package ws

import (
	"time"
)

// requestCleanup coalesces cleanup signals; background workers close the connections.
func (p *WSConnPool) requestCleanup() {
	select {
	case p.cleanupWakeCh <- struct{}{}:
	default:
	}
}

// releaseUndeliveredConn returns an undelivered lease and schedules cleanup so cancellation can return promptly.
func (p *WSConnPool) releaseUndeliveredConn(providerID int64, conn *WSConn) {
	conn.release()
	p.notifyProviderPoolChanged(providerID)
	p.requestCleanup()
}

// connMaxAgeReached reports whether the connection has reached its configured lifetime.
func (p *WSConnPool) connMaxAgeReached(conn *WSConn, now time.Time) bool {
	maxAge := p.maxConnAge()
	return conn != nil && maxAge > 0 && conn.age(now) >= maxAge
}

// retireExpiredConnsLocked removes closed connections and expired unleased, unpinned connections. The caller holds ap.mu.
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
			// Waiters can take valid connections; expired connections require a fresh dial.
			expired = p.connMaxAgeReached(conn, now)
			if !conn.supportsIdlePingWithoutReader() && conn.idleDuration(now) >= openAIWSConnIdleRecycleAfter {
				expired = true
				p.metrics.scaleDownTotal.Add(1)
			}
		}
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
