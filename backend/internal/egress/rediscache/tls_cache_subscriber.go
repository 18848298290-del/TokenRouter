package rediscache

import (
	"context"
	"log/slog"
	"sync"

	"github.com/redis/go-redis/v9"
)

// tls_cache_subscriber.go owns Redis notification subscriptions for the TLS
// profile and router caches. Each cache admits one subscriber during its
// lifetime and supplies a callback that clears its local collection before
// refreshing service state. Callbacks execute synchronously, so a later
// notification waits for the current refresh to return.
//
// Registration records cancellation and wait-group membership under the same
// mutex that seals admission during stopping. A stop can therefore wait for
// admitted work even before its goroutine starts. Subscriber cleanup closes
// Pub/Sub before completing that wait, while the application keeps the borrowed
// Redis client open until stop returns. Cancellation ends receive waiting;
// callbacks already executing retain responsibility for returning.

// tlsCacheSubscriber tracks one cache's refresh subscription through terminal shutdown.
// Its zero value accepts registration, and its synchronization fields remain
// attached to the same owner throughout subscription and stopping.
type tlsCacheSubscriber struct {
	// mu protects cancel and stopped while registering wg work, so stop observes
	// every admitted goroutine before waiting for callback and Pub/Sub cleanup.
	mu      sync.Mutex         // lifecycle admission state lock
	cancel  context.CancelFunc // retained subscriber cancellation function
	wg      sync.WaitGroup     // admitted subscriber completion tracking
	stopped bool               // terminal subscriber admission flag
}

// start admits one Redis subscriber and returns after launching its receive
// loop. Duplicate registrations and registrations after stop are rejected, even
// when an earlier subscriber has already exited through context cancellation.
// The supplied callback must return for shutdown to complete. Callers invoke
// stop from outside callback execution to avoid waiting on that callback itself.
//
// Admission records cancellation and completion tracking together before
// launch. Every received message invokes onUpdate synchronously; the owning
// cache uses that call to invalidate local data before its service handler.
// stoppedLog identifies the cache in context-cancellation and channel-closure
// messages, retaining their distinct log levels and reasons.
func (s *tlsCacheSubscriber) start(
	ctx context.Context,
	rdb *redis.Client,
	channel string,
	stoppedLog string,
	onUpdate func(),
) {
	subscriberCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if s.cancel != nil || s.stopped {
		s.mu.Unlock()
		// Rejected admission still owns a derived context. Release its parent
		// registration immediately because no subscriber will consume that context.
		cancel()
		return
	}
	// Register cancellation and wait-group membership before releasing admission
	// state, so a concurrent terminal stop waits for this accepted goroutine.
	s.cancel = cancel
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		sub := rdb.Subscribe(subscriberCtx, channel)
		// Close Pub/Sub before publishing completion so application shutdown may
		// close the shared Redis client after stop has returned to its caller.
		defer func() { _ = sub.Close() }()

		ch := sub.Channel()
		for {
			select {
			case <-subscriberCtx.Done():
				slog.Debug(stoppedLog, "reason", "context_done")
				return
			case msg, ok := <-ch:
				if !ok || msg == nil {
					if subscriberCtx.Err() != nil {
						slog.Debug(stoppedLog, "reason", "context_done")
						return
					}
					slog.Warn(stoppedLog, "reason", "channel_closed")
					return
				}
				// Complete invalidation and service refresh together before receiving
				// another message; stop waits for this callback's outstanding work.
				onUpdate()
			}
		}
	}()
}

// stop seals subscription admission, cancels admitted work, and waits for its
// completion. Repeated calls share the retained cancellation and completion
// state, while stopping before registration permanently rejects later starts.
// Callers keep Redis available until this method returns and invoke it outside
// subscription callbacks.
//
// The mutex orders admission against stopping and is released before waiting.
// Cancellation wakes the receive loop, while a synchronous callback already in
// progress must return before deferred Pub/Sub closure and wait-group completion
// permit shutdown to continue.
func (s *tlsCacheSubscriber) stop() {
	// Seal admission before inspecting retained cancellation, so a concurrent
	// start cannot add work after an unregistered subscriber has finished stopping.
	s.mu.Lock()
	cancel := s.cancel
	s.stopped = true
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	// Wait outside lifecycle admission after cancellation. A callback already
	// running still owns completion and must return before Redis can be closed.
	cancel()
	s.wg.Wait()
}
