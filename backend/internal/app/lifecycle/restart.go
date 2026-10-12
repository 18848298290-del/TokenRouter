package lifecycle

import (
	"sync"
	"time"
)

// Restart requests schedule a main-loop shutdown callback on Linux after a
// 600-millisecond response-send window. Once scheduled, later requests cause no
// additional callback, and other platform names cause none. The process entry point
// handles exit and the supervisor performs the subsequent process restart.
//
// Scheduling and closing share a mutex so no callback is added after admission
// closes. A wait-group slot is registered before the timer can fire. If Close
// successfully stops the timer, it completes that slot; otherwise the callback
// completes it on return. Close waits for that completion before its caller
// releases resources used by the callback.

// Restarter coordinates delayed main-loop shutdown with cancellation of pending restart work.
type Restarter struct {
	// mu protects timer and closed while scheduling and closing choose who
	// completes the wg slot. Callback completion occurs after request returns.
	mu sync.Mutex // restart admission lock
	// platform permits scheduling only for the literal "linux" value; other
	// names make RequestRestart return successfully without invoking request.
	platform string
	// request invokes main-loop shutdown after the delay. It must return and
	// must not call Close, which waits for this callback's own completion.
	request func()
	// timer, closed, and wg connect scheduling with Close. Under mu, scheduling
	// reserves a wg slot before assigning timer; Close seals admission and waits
	// for cancellation or callback completion to balance that slot. timer remains
	// set after firing or cancellation, suppressing subsequent restart attempts.
	timer  *time.Timer    // scheduled restart timer
	closed bool           // restart admission sealed
	wg     sync.WaitGroup // pending callback completion
}

// NewRestarter stores the platform and shutdown callback in an idle restarter.
// Supply a callable request for Linux scheduling, and close the restarter before
// releasing resources the callback uses.
func NewRestarter(platform string, request func()) *Restarter {
	return &Restarter{platform: platform, request: request}
}

// RequestRestart schedules the shutdown callback once for Linux, after a
// 600-millisecond response-send window. Other platforms, closed instances, and
// instances with a timer return nil without scheduling another callback.
//
// The callback's wait-group slot is added under the admission lock before
// AfterFunc can run. Close uses the same lock to seal scheduling, then either
// balances a successfully cancelled timer or waits for the callback to finish.
// The delay defines a response-send window; application and network timing
// determine whether the client receives the response during that window.
func (r *Restarter) RequestRestart() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.platform != "linux" || r.closed || r.timer != nil {
		return nil
	}
	// Register completion before the timer can fire. Close sees the timer and
	// its wait-group slot together because scheduling remains under the same lock.
	r.wg.Add(1)
	r.timer = time.AfterFunc(600*time.Millisecond, func() {
		defer r.wg.Done()
		r.request()
	})
	return nil
}

// Close seals restart admission and cancels the pending timer when Stop
// succeeds. A timer that already fired can still invoke the callback during
// Close, and this method waits for that callback's completion.
//
// Cancellation owns the wait-group decrement only when Timer.Stop reports
// success; otherwise the callback's defer owns it. The mutex prevents new
// registrations before Wait begins, making repeated closes safe for the
// recorded work. Call Close outside request, since the callback cannot wait
// for its own return.
func (r *Restarter) Close() {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		if r.timer != nil && r.timer.Stop() {
			// A stopped timer will not invoke the callback's completion defer.
			// Balance its registered slot here; fired timers leave completion to the callback.
			r.wg.Done()
		}
	}
	r.mu.Unlock()
	r.wg.Wait()
}
