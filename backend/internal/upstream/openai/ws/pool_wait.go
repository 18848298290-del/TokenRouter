package ws

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// wsPoolWaiter tracks one wait; a non-nil preferred fixes the lease to that connection.
type wsPoolWaiter struct {
	compatibility wsConnCompatibility
	preferred     *WSConn
	startedAt     time.Time
}

// addWaiterLocked checks provider and preferred-connection queue limits. The caller holds ap.mu.
func (p *WSConnPool) addWaiterLocked(ap *openAIWSProviderPool, ctx context.Context, compatibility wsConnCompatibility, preferred *WSConn, maxConns int) (*wsPoolWaiter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, waiters := providerPoolLoadLocked(ap)
	limit := p.queueLimitPerConn()
	// Division avoids overflowing the product of connection and queue limits.
	if waiters/limit >= maxConns || preferred != nil && int(preferred.waiters.Load()) >= limit {
		return nil, openai.ErrOpenAIWSConnQueueFull
	}
	waiter := &wsPoolWaiter{compatibility: compatibility, preferred: preferred, startedAt: time.Now()}
	if preferred != nil {
		preferred.waiters.Add(1)
	} else {
		if ap.waiters == nil {
			ap.waiters = make(map[wsConnCompatibility]int)
		}
		ap.waiters[compatibility]++
	}
	return waiter, nil
}

// removeWaiterLocked returns a queue slot and elapsed wait time. The caller holds ap.mu.
func removeWaiterLocked(ap *openAIWSProviderPool, waiter *wsPoolWaiter) time.Duration {
	if waiter.preferred != nil {
		waiter.preferred.waiters.Add(-1)
	} else {
		ap.waiters[waiter.compatibility]--
		if ap.waiters[waiter.compatibility] == 0 {
			delete(ap.waiters, waiter.compatibility)
		}
	}
	ap.signalChangedLocked()
	return time.Since(waiter.startedAt)
}

// hasCompatibleWaiterLocked reports whether a general waiter can use conn. The caller holds ap.mu.
func hasCompatibleWaiterLocked(ap *openAIWSProviderPool, conn *WSConn) bool {
	return ap.waiters[conn.compatibility] > 0
}
