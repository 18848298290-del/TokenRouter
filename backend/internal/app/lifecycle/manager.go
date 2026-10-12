package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Startup and shutdown have different dependency orders. Start hooks run
// serially in increasing StartOrder, while normal shutdown runs increasing
// StopOrder and lets hooks at the same level run concurrently. A hook is
// eligible for cleanup before its start callback runs because failed startup
// can leave partially acquired resources. Hooks registered with no Start are
// eligible as soon as registered.
//
// The first Stop or Rollback owns one shared cleanup attempt and its context.
// It waits for startup to finish before selecting eligible hooks; rollback
// reverses startup and registration order and stops serially. Shutdown errors
// accumulate, while context expiry stops later levels and reports unfinished
// work. Callback and reporting goroutines can continue after the manager has
// returned that timeout.

// Hook supplies component callbacks and independent startup and shutdown ordering rules.
type Hook struct {
	// Name identifies diagnostics and pending-result tracking. Callers supply a
	// nonempty unique value; Start validates names before invoking start callbacks.
	Name string
	// StartOrder and StopOrder are independent levels. Startup is serial and
	// stable within a level; normal stops at one StopOrder run concurrently.
	// Rollback uses reverse StartOrder and reverses registration order for ties.
	StartOrder int // startup dependency level
	StopOrder  int // shutdown dependency level
	// Start being nil marks an already acquired resource. A nonnil callback
	// becomes eligible for cleanup before invocation, including when it fails.
	Start func(context.Context) error
	// Stop receives the shared cleanup context. Errors are collected after
	// callback reporting; later levels continue while the context is usable,
	// and expiry ends the drain, including a timed-out reporting wait.
	Stop func(context.Context) error
}

// entry retains one registered hook and its eligibility for resource cleanup.
type entry struct {
	hook Hook // registered component callbacks
	// active becomes true at registration for acquired resources or immediately
	// before a start attempt. Cleanup snapshots include it even after start failure.
	active bool
}

// Manager coordinates hook admission, ordered startup, and one shared cleanup attempt.
// Construct it with New so shutdown has its completion signal initialized.
type Manager struct {
	// mu guards entries and the started/closed transitions. Registration finishes
	// before startup snapshots entries; cleanup waits for startup before reading
	// each entry's cleanup eligibility under the same mutex.
	mu      sync.Mutex // lifecycle transition lock
	entries []*entry   // registered resource entries
	started bool       // first startup admitted
	closed  bool       // registration and startup sealed
	// startDone publishes startErr from the first startup, letting repeat callers
	// observe its result and cleanup wait for startup callback work to finish.
	startDone chan struct{} // first startup completion
	startErr  error         // first startup result
	// stopOnce chooses the cleanup mode and context; stopDone publishes stopErr
	// after that common attempt, even when callbacks remain unfinished after timeout.
	stopOnce sync.Once     // shared cleanup admission
	stopDone chan struct{} // shared cleanup completion
	stopErr  error         // shared cleanup result
	// report observes startup synchronously and cleanup through bounded reporting
	// waits. It must return and avoid blocking reentry into lifecycle operations.
	report Reporter
}

// Reporter observes component lifecycle events and their operation errors.
// Startup invokes it synchronously; cleanup waits for its return within the
// shared stop context, and "stopped" denotes a successful Stop callback.
type Reporter func(name, event string, err error)

// New constructs an unstarted lifecycle manager with a shared stop completion
// signal. The first optional reporter receives events; callers register hooks
// before startup or shutdown and choose unique, nonempty names for their
// components.
func New(reporters ...Reporter) *Manager {
	m := &Manager{stopDone: make(chan struct{})}
	if len(reporters) > 0 {
		m.report = reporters[0]
	}
	return m
}

// emit invokes the configured lifecycle reporter synchronously when one is
// present. Start uses this directly, while stopEntries runs reporting through
// a bounded wait; reporters must return and avoid reentering blocking
// lifecycle operations.
func (m *Manager) emit(name, event string, err error) {
	if m.report != nil {
		m.report(name, event, err)
	}
}

// Register records a hook before startup or shutdown begins. Calling after
// either transition panics. A nil Start marks the resource eligible for
// cleanup immediately; other hooks become eligible just before their first
// start invocation.
//
// The admission mutex serializes this append with the first start or stop.
// Startup seals registration before taking its entries snapshot, so every
// accepted hook participates in that snapshot. Register invokes no callbacks;
// it records the resource's cleanup eligibility for later startup or rollback.
func (m *Manager) Register(h Hook) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started || m.closed {
		panic("lifecycle: register after start or stop")
	}
	m.entries = append(m.entries, &entry{hook: h, active: h.Start == nil})
}

// Start runs registered start hooks serially in stable increasing StartOrder
// and returns the first startup failure. Names are checked for emptiness and
// duplicates before invoking any start hook. Repeated callers await the first
// startup result or their own context.
//
// The first call seals registration and takes a pointer snapshot of entries
// under the mutex. Each attempted hook becomes cleanup-eligible before
// invocation so rollback includes resources from a failed start. Stop seals
// the manager and waits for startDone before selecting cleanup entries; a
// closed manager stops further callback admission at the next eligibility
// check; a callback already admitted may still run. Reporters run synchronously
// during startup.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("lifecycle: already stopped")
	}
	if m.started {
		done := m.startDone
		m.mu.Unlock()
		select {
		case <-done:
			return m.startErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Seal registration and create the first startup signal under the mutex.
	// Repeat callers then wait for the same result while stop can wait for completion.
	m.started = true
	m.startDone = make(chan struct{})
	entries := append([]*entry(nil), m.entries...)
	m.mu.Unlock()
	defer close(m.startDone)
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].hook.StartOrder < entries[j].hook.StartOrder
	})
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.hook.Name == "" || seen[e.hook.Name] {
			m.startErr = fmt.Errorf("lifecycle: invalid or duplicate hook %q", e.hook.Name)
			return m.startErr
		}
		seen[e.hook.Name] = true
	}
	for _, e := range entries {
		if e.hook.Start == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			m.startErr = err
			return err
		}
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			m.startErr = errors.New("lifecycle: stopped during startup")
			return m.startErr
		}
		// Cleanup must include a failed start that acquired partial resources.
		// Publish eligibility before invocation, while shutdown still waits for startDone.
		e.active = true
		m.mu.Unlock()
		m.emit(e.hook.Name, "starting", nil)
		if err := e.hook.Start(ctx); err != nil {
			m.emit(e.hook.Name, "start_failed", err)
			m.startErr = fmt.Errorf("start %s: %w", e.hook.Name, err)
			return m.startErr
		}
		m.emit(e.hook.Name, "started", nil)
	}
	return nil
}

// Stop requests normal cleanup in increasing StopOrder, with equal levels
// stopped concurrently. The first Stop or Rollback selects the shared cleanup
// mode and context; later calls wait for that result and do not substitute
// their own deadline.
func (m *Manager) Stop(ctx context.Context) error {
	return m.stop(ctx, false)
}

// Rollback requests serial cleanup in reverse StartOrder and reverse
// registration order for ties. It includes acquired and attempted hooks. The
// first Stop or Rollback owns the common cleanup mode and context, so
// subsequent calls reuse that attempt.
func (m *Manager) Rollback(ctx context.Context) error {
	return m.stop(ctx, true)
}

// stop starts one cleanup attempt and returns its shared result after stopDone
// closes. The first caller supplies both the rollback mode and the context
// used by every internal wait; later callers wait independently of their own
// contexts.
//
// Closing the manager prevents further registration and start admission.
// Cleanup first waits for an already running startup to publish completion,
// then snapshots active hooks with Stop callbacks under the mutex. A startup
// wait timeout returns before resource-stop callbacks begin. The cleanup
// goroutine stores stopErr before closing stopDone, publishing the result to
// every caller.
func (m *Manager) stop(ctx context.Context, rollback bool) error {
	// The first caller owns cleanup mode and budget. Once prevents later callers
	// from replacing that attempt while its callback goroutine drains eligible hooks.
	m.stopOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		startDone := m.startDone
		m.mu.Unlock()
		go func() {
			defer close(m.stopDone)
			if startDone != nil {
				select {
				case <-startDone:
				case <-ctx.Done():
					m.stopErr = fmt.Errorf("lifecycle startup still running: %w", ctx.Err())
					return
				}
			}
			m.mu.Lock()
			entries := make([]*entry, 0, len(m.entries))
			for _, e := range m.entries {
				if e.active && e.hook.Stop != nil {
					entries = append(entries, e)
				}
			}
			m.mu.Unlock()
			m.stopErr = stopEntries(ctx, entries, rollback, m.report)
		}()
	})
	// Later callers use the first attempt's completion signal and context.
	// Their own deadlines do not cut short this shared wait or restart cleanup.
	<-m.stopDone
	return m.stopErr
}

// stopEntries invokes cleanup callbacks in normal drain order or reverse
// startup order for rollback. Normal cleanup runs one StopOrder level
// concurrently; rollback handles each entry serially after reversing stable
// startup order. Callback errors accumulate while the context remains usable.
//
// Each level has a buffered result channel sized for all launched callbacks,
// allowing late returns after timeout. Reporting is also waited on within the
// same context, so a blocked reporter can prevent later dependency levels from
// starting. Context expiry returns an error naming unfinished callbacks and
// unstarted entries; it does not terminate goroutines already launched. The
// caller supplies hooks whose stop behavior honors their resource obligations.
func stopEntries(ctx context.Context, entries []*entry, rollback bool, report Reporter) error {
	if rollback {
		// Rollback unwinds attempted acquisition one hook at a time. Reverse
		// both startup levels and same-level registration so dependencies close last.
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].hook.StartOrder < entries[j].hook.StartOrder
		})
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
	} else {
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].hook.StopOrder < entries[j].hook.StopOrder
		})
	}
	var failures []error
	for len(entries) > 0 {
		n := 1
		if !rollback {
			for n < len(entries) && entries[n].hook.StopOrder == entries[0].hook.StopOrder {
				n++
			}
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, pendingError(err, entries))...)
		}
		// result identifies one callback completion for pending-name accounting.
		type result struct {
			name string // completed component name
			err  error  // stop callback result
		}
		// Capacity covers every launched callback, so their result sends can finish
		// after a timeout returns and stops this receiver from draining the level.
		results := make(chan result, n)
		pending := make(map[string]bool, n)
		for _, e := range entries[:n] {
			h := e.hook
			pending[h.Name] = true
			go func() { results <- result{h.Name, h.Stop(ctx)} }()
		}
		for range n {
			select {
			case r := <-results:
				delete(pending, r.name)
				if report != nil {
					event := "stopped"
					if r.err != nil {
						event = "stop_failed"
					}
					// Reporting can block after a hook has stopped. Wait within the same
					// budget so logger delays cannot admit later dependencies after expiry.
					reported := make(chan struct{})
					go func() { report(r.name, event, r.err); close(reported) }()
					select {
					case <-reported:
					case <-ctx.Done():
						unfinished := append([]*entry(nil), entries[n:]...)
						for _, e := range entries[:n] {
							if pending[e.hook.Name] {
								unfinished = append(unfinished, e)
							}
						}
						failures = append(failures, fmt.Errorf("cleanup report %s incomplete: %w", r.name, ctx.Err()), pendingError(ctx.Err(), unfinished))
						return errors.Join(failures...)
					}
				}
				if r.err != nil {
					failures = append(failures, fmt.Errorf("stop %s: %w", r.name, r.err))
				}
			case <-ctx.Done():
				// Stop admitting later cleanup levels and report the outstanding names.
				// Already launched callbacks retain their buffered return path and context.
				var unfinished []*entry
				for _, e := range entries {
					if pending[e.hook.Name] || e.hook.StopOrder != entries[0].hook.StopOrder || rollback {
						unfinished = append(unfinished, e)
					}
				}
				return errors.Join(append(failures, pendingError(ctx.Err(), unfinished))...)
			}
		}
		entries = entries[n:]
	}
	return errors.Join(failures...)
}

// pendingError wraps the cleanup cause with the names of entries still
// reported unfinished, retaining their supplied order.
func pendingError(err error, entries []*entry) error {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.hook.Name)
	}
	return fmt.Errorf("lifecycle cleanup incomplete [%s]: %w", strings.Join(names, ", "), err)
}
