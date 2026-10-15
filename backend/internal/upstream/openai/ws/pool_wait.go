package ws

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// pool_wait.go reserves and releases WebSocket queue slots while retaining
// waiter compatibility and elapsed timing.
// One waiter records a single queue reservation and start time while
// acquisition can retry selection after several pool-change wakeups. General
// reservations are counted by compatibility key, while forced continuation
// reservations count against their preferred connection.
//
// Provider-wide capacity and the preferred-connection limit are checked before
// publishing the reservation. Removal returns exactly that queue slot and
// notifies other selectors under the same provider lock, preserving one elapsed
// wait measurement across repeated wakeups. Cancellation eligibility is checked
// before enqueueing; later races with lease delivery are resolved by the
// acquisition owner. Callers therefore add once and remove once for each waiter
// rather than recreating it every time pool state changes.

// wsPoolWaiter records one queue reservation, compatibility target, and
// elapsed-wait origin.
type wsPoolWaiter struct {
	compatibility wsConnCompatibility // general waiter transport compatibility
	// preferred fixes a continuation waiter to one connection when nonnil; its
	// queue reservation then uses that connection counter rather than the
	// compatibility map.
	preferred *WSConn
	startedAt time.Time // single queue reservation timestamp
}

// addWaiterLocked reserves one queue slot after checking cancellation and
// capacity. The caller holds ap.mu, supplies a nonnil context and the effective
// maxConns, and removes the returned waiter once when waiting ends. A
// cancellation already visible at entry returns before reservation.
//
// Provider capacity is checked against existing waiter count divided by the
// positive per-connection limit, avoiding an overflowing capacity product. A
// preferred waiter also checks that connection's individual queue count. The
// reservation is recorded either on that connection or in the compatibility
// map, so later removal releases the same class of queue capacity.
func (p *WSConnPool) addWaiterLocked(ap *openAIWSProviderPool, ctx context.Context, compatibility wsConnCompatibility, preferred *WSConn, maxConns int) (*wsPoolWaiter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, waiters := providerPoolLoadLocked(ap)
	limit := p.queueLimitPerConn()
	// Divide the current queue count before comparison so capacity checking cannot
	// overflow when connection and per-connection queue limits would form a large product.
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

// removeWaiterLocked returns one existing waiter reservation and its elapsed
// duration. The caller holds ap.mu and invokes it once for the waiter; it
// removes an empty compatibility count and wakes selectors after updating the
// corresponding counter.
func removeWaiterLocked(ap *openAIWSProviderPool, waiter *wsPoolWaiter) time.Duration {
	if waiter.preferred != nil {
		waiter.preferred.waiters.Add(-1)
	} else {
		ap.waiters[waiter.compatibility]--
		if ap.waiters[waiter.compatibility] == 0 {
			delete(ap.waiters, waiter.compatibility)
		}
	}
	// Publish the returned slot after its counter changes so selectors wake
	// against the updated provider queue state rather than a stale reservation count.
	ap.signalChangedLocked()
	return time.Since(waiter.startedAt)
}

// hasCompatibleWaiterLocked reports whether a general waiter matches conn
// compatibility. Callers hold ap.mu and supply a nonnil connection; preferred
// waiters are counted separately and do not satisfy this general-queue
// predicate.
func hasCompatibleWaiterLocked(ap *openAIWSProviderPool, conn *WSConn) bool {
	return ap.waiters[conn.compatibility] > 0
}
