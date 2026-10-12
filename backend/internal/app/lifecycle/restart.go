package lifecycle

import (
	"sync"
	"time"
)

// Restarter requests main-loop shutdown; the entry point exits and the supervisor restarts the process.
type Restarter struct {
	mu       sync.Mutex
	platform string
	request  func()
	timer    *time.Timer
	closed   bool
	wg       sync.WaitGroup
}

func NewRestarter(platform string, request func()) *Restarter {
	return &Restarter{platform: platform, request: request}
}

// RequestRestart schedules Linux shutdown after 600 ms to allow the HTTP response to leave; other platforms
// return immediately.
func (r *Restarter) RequestRestart() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.platform != "linux" || r.closed || r.timer != nil {
		return nil
	}
	r.wg.Add(1)
	r.timer = time.AfterFunc(600*time.Millisecond, func() {
		defer r.wg.Done()
		r.request()
	})
	return nil
}

// Close cancels a pending restart and waits for a callback that has already started.
func (r *Restarter) Close() {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		if r.timer != nil && r.timer.Stop() {
			r.wg.Done()
		}
	}
	r.mu.Unlock()
	r.wg.Wait()
}
