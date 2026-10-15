package provider

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ollama_usage_runtime.go coordinates periodic Ollama usage scans with tracked
// manual operations and shutdown.
// Ollama usage maintenance starts with an immediate scan and then repeats after
// one-minute ticks. Manual usage work can register in the same activity tracker
// so shutdown waits for both paths, including work that continues after its
// context is cancelled.
//
// Startup and permanent stop flags share a mutex, while operationActivity owns
// admission, cancellation, and completion. The loop registers before its
// goroutine is launched, preventing shutdown from missing an initial scan. Each
// operation keeps its registration until its finish callback runs; a bounded
// stop therefore reports unfinished callbacks and retains that first result for
// later stops.

// ErrOllamaUsageStopped rejects new activity registration after Ollama runtime
// shutdown has started. Existing operations retain their cancelled contexts
// until their callers finish them.
var ErrOllamaUsageStopped = errors.New("ollama cloud usage is stopped")

// OllamaUsageRuntime coordinates one periodic loop and explicitly registered
// manual usage operations.
type OllamaUsageRuntime struct {
	mu sync.Mutex // loop startup and shutdown lock
	// started and stopped share mu with StartContext and StopContext. A
	// successful start sets started once, and stopped permanently prevents
	// launching another loop after shutdown.
	// started records loop admission; stopped rejects later loop startup.
	started, stopped bool
	// activity tracks both the loop and Begin callers; shutdown
	// cancellation
	// keeps each operation registered until its returned finish callback is
	// invoked.
	activity operationActivity
	// cycle runs serially in the loop and receives its cancellation
	// context; a
	// blocking callback keeps the loop activity unfinished until it
	// returns.
	cycle func(context.Context) error
	log   func(string, ...any) // periodic cycle error callback
}

// NewOllamaUsageRuntime constructs an idle runtime using the supplied cycle
// callback and an optional logger. Construction starts no work, and callers
// supply a usable cycle callback before invoking StartContext.
func NewOllamaUsageRuntime(cycle func(context.Context) error, log func(string, ...any)) *OllamaUsageRuntime {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &OllamaUsageRuntime{cycle: cycle, log: log}
}

// Begin registers a usage operation linked to caller and shutdown cancellation.
// Callers provide a nonnil context and invoke the returned finish exactly once
// after actual completion; stopped runtimes reject registration.
func (r *OllamaUsageRuntime) Begin(ctx context.Context) (context.Context, func(), error) {
	return r.activity.begin(ctx, ErrOllamaUsageStopped)
}

// StartContext registers and launches a single usage loop under the supplied
// nonnil parent. Concurrent or repeated starts are serialized, and a stopped
// runtime remains idle. A pre-cancelled parent fails registration before
// started is set.
//
// The first cycle runs immediately after the goroutine checks cancellation; its
// error is discarded. A one-minute ticker is created after that cycle returns,
// and later cycle errors are logged. The loop remains registered until exit so
// StopContext waits for the callback's actual completion, including callbacks
// that ignore cancellation.
func (r *OllamaUsageRuntime) StartContext(parent context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started || r.stopped {
		return nil
	}
	ctx, finish, err := r.Begin(parent)
	if err != nil {
		return err
	}
	r.started = true
	// Register the loop before launching it so concurrent shutdown includes
	// the
	// immediate cycle and every later callback in its completion wait.
	go func() {
		defer finish()
		if ctx.Err() != nil {
			return
		}
		// A failed initial scan leaves the registered loop available
		// for later
		// ticks; this first error is discarded rather than logged.
		_ = r.cycle(ctx)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if ctx.Err() != nil {
					return
				}
				if err := r.cycle(ctx); err != nil {
					r.log("run_due_failed: err=%v", err)
				}
			}
		}
	}()
	return nil
}

// StopContext prevents further loop startup and asks operationActivity to
// cancel and wait for registered work. Nil receivers succeed, and ctx bounds
// the completion wait when a callback remains active after cancellation.
//
// The startup mutex installs stopped before activity admission closes, so a
// racing StartContext cannot launch another loop. Work registered before
// activity closes is included in its cancellation and completion wait. The
// first shutdown result, including a timeout with unfinished work, remains the
// result of subsequent stops even when delayed callbacks later return.
func (r *OllamaUsageRuntime) StopContext(ctx context.Context) error {
	if r == nil {
		return nil
	}
	// Close startup admission before cancelling activities; release the
	// startup
	// mutex before the completion wait so startup calls can observe denial.
	r.mu.Lock()
	r.stopped = true
	r.mu.Unlock()
	return r.activity.stop(ctx, "Ollama Cloud usage")
}
