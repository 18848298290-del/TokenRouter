package querycache

import (
	"context"
	"fmt"
)

// context_load.go coordinates cancellable waiters sharing the same query load.
// A caller can abandon a shared query without cancelling work still needed by
// another waiter. Context-aware loads keep a waiter count under loadMu and run
// with cancellation detached from the first caller while retaining that
// caller's context values. Each waiter observes its own cancellation before
// accepting a shared result.
//
// The final departing waiter removes an unfinished flight and cancels its
// shared context. A later caller can then start a fresh flight for the same
// key. Completion checks cancellation and remaining waiters before cache
// publication, and removes the registry entry only when it still identifies
// that flight. Closing done publishes the result to its waiters after those
// decisions.

// contextLoad holds one query flight's shared context, waiter registration, and
// published result.
type contextLoad struct {
	// ctx and cancel detach the first caller's cancellation while retaining its
	// values. The last unfinished waiter or the completion path cancels this context.
	ctx    context.Context    // shared loader context values
	cancel context.CancelFunc // shared loader cancellation function
	// done closes after entry, hit, and err have been assigned. Receivers wait
	// for that close before reading these result fields outside loadMu.
	done chan struct{} // completed result publication signal
	// waiters and finished change under Cache.loadMu. Zero waiters cancels an
	// unfinished flight, while a finished flight has already published its result.
	waiters  int   // registered callers still waiting
	finished bool  // completion publication already performed
	entry    Entry // published cache or load snapshot
	hit      bool  // cache observed before loader execution
	err      error // published load or cancellation error
}

// GetOrLoadContext returns a cached or shared loaded snapshot while each caller
// observes ctx cancellation independently. ctx must be nonnil, and load must
// use the supplied shared context to respond to final-waiter cancellation. A
// nil loader returns an empty successful result after the initial cancellation
// check. Cache hits use that initial check and return without checking
// cancellation again.
//
// Nonnil caches with nonempty keys register flights by key under loadMu. The
// first caller contributes context values, and later callers join that flight
// until it finishes or all waiters leave. Completion returns a clone of the
// published entry after rechecking the caller's cancellation. The bool reports
// a cache observation, including the flight's repeated lookup.
//
// Nil caches and empty keys load directly using the caller context and recheck
// cancellation afterward. A nil cache returns a cloned payload with zero
// expiration; a nonnil empty-key cache returns an unstored entry with its
// configured TTL. Payloads need to meet Clone's restrictions, and direct-path
// loader panics propagate.
//
// Shared flights convert loader or repeated cache-lookup panics into load
// errors. Recovery precedes deferred Set, so a Clone panic during publication
// can escape the loading goroutine before its waiters receive completion
// notification. The initial synchronous lookup can also propagate Clone
// panics; use payloads accepted by Clone throughout.
func (c *Cache) GetOrLoadContext(ctx context.Context, key string, load func(context.Context) (any, error)) (Entry, bool, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, false, err
	}
	if load == nil {
		return Entry{}, false, nil
	}
	if entry, ok := c.Get(key); ok {
		return entry, true, nil
	}
	if c == nil || key == "" {
		payload, err := load(ctx)
		if err != nil {
			return Entry{}, false, err
		}
		if err = ctx.Err(); err != nil {
			return Entry{}, false, err
		}
		if c != nil {
			return c.Set(key, payload), false, nil
		}
		return Entry{Payload: Clone(payload)}, false, nil
	}
	c.loadMu.Lock()
	if c.loads == nil {
		c.loads = make(map[string]*contextLoad)
	}
	flight := c.loads[key]
	if flight == nil {
		// The first caller supplies values but owns only its own wait. Detach its
		// cancellation so a later live waiter can continue sharing this query flight.
		shared, cancel := context.WithCancel(context.WithoutCancel(ctx))
		flight = &contextLoad{ctx: shared, cancel: cancel, done: make(chan struct{})}
		c.loads[key] = flight
		go c.runContextLoad(key, flight, load)
	}
	// Register before unlocking so a finishing goroutine observes this waiter
	// when deciding whether its loaded value still has a consumer to publish to.
	flight.waiters++
	c.loadMu.Unlock()
	defer c.leaveContextLoad(key, flight)
	// Cancellation can race with completion. Recheck the caller after a completed
	// flight wakes it so a cancelled caller does not accept that shared snapshot.
	select {
	case <-ctx.Done():
		return Entry{}, false, ctx.Err()
	case <-flight.done:
		if err := ctx.Err(); err != nil {
			return Entry{}, false, err
		}
		return Clone(flight.entry), flight.hit, flight.err
	}
}

// leaveContextLoad unregisters one caller from flight under the cache's load
// lock. GetOrLoadContext calls it once after registering that caller, including
// when cancellation wins its result wait.
//
// When the final waiter leaves unfinished work, the registry removes that
// flight before cancelling its shared context. Removal checks pointer identity
// so an older flight cannot remove a replacement for the same key. A finished
// flight needs no additional cancellation here because its completion path
// already ended the shared context.
func (c *Cache) leaveContextLoad(key string, flight *contextLoad) {
	c.loadMu.Lock()
	defer c.loadMu.Unlock()
	flight.waiters--
	// Abandoned work must be removed before cancellation. A subsequent caller can
	// register a new flight while the abandoned loader is still returning its result.
	if flight.waiters == 0 && !flight.finished {
		if c.loads[key] == flight {
			delete(c.loads, key)
		}
		flight.cancel()
	}
}

// runContextLoad performs a registered flight's repeated cache lookup or loader
// invocation and publishes completion to its waiters. It runs in the goroutine
// started by GetOrLoadContext and calls load with the flight's shared context.
//
// The deferred completion path converts a loader or initial lookup panic into
// an error, then locks the registry to arbitrate cancellation and publication.
// A cancelled shared context supplies the final error, and successful new
// payloads are cached only while waiters remain. Pointer-checked removal leaves
// newer flights registered. Completion assigns result fields, cancels the
// shared context, and closes done.
//
// Clone failures during the deferred Set publication occur after its recover
// call and can escape this recovery path. Callers supply payloads accepted by
// Clone so cache publication and waiter notification can complete.
func (c *Cache) runContextLoad(key string, flight *contextLoad, load func(context.Context) (any, error)) {
	var entry Entry
	var payload any
	var hit bool
	var err error
	defer func() {
		// Recover a panic from the loader or its initial lookup to supply a load
		// error. Later publication executes after this recovery point, before
		// notifying waiters.
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("query cache load panicked: %v", recovered)
		}
		c.loadMu.Lock()
		defer c.loadMu.Unlock()
		// Cancellation can finish before an uncooperative loader returns a value.
		// Check the shared signal under the registry lock before deciding cache publication.
		if flight.ctx.Err() != nil {
			err = flight.ctx.Err()
		}
		if err == nil && !hit && flight.waiters > 0 {
			entry = c.Set(key, payload)
		}
		// Publish result fields before closing done so receivers can read a complete
		// outcome. Identity-checked removal keeps any replacement flight available to later callers.
		flight.entry, flight.hit, flight.err = entry, hit, err
		flight.finished = true
		if c.loads[key] == flight {
			delete(c.loads, key)
		}
		flight.cancel()
		close(flight.done)
	}()
	if entry, hit = c.Get(key); hit {
		return
	}
	payload, err = load(flight.ctx)
}
