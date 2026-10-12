package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Hook describes a registered lifecycle action. Lower orders run first; hooks at the same stop order run
// concurrently.
// A nil Start means the resource is already acquired and needs cleanup even if construction fails.
type Hook struct {
	Name       string
	StartOrder int
	StopOrder  int
	Start      func(context.Context) error
	Stop       func(context.Context) error
}

type entry struct {
	hook   Hook
	active bool
}

// Manager accepts resource and task registrations before startup or shutdown.
// Registered components manage resources created during operation.
type Manager struct {
	mu        sync.Mutex
	entries   []*entry
	started   bool
	closed    bool
	startDone chan struct{}
	startErr  error
	stopOnce  sync.Once
	stopDone  chan struct{}
	stopErr   error
	report    Reporter
}

// Reporter receives lifecycle events; "stopped" is emitted after the hook's Stop returns successfully.
type Reporter func(name, event string, err error)

// New creates a lifecycle manager that has not started.
func New(reporters ...Reporter) *Manager {
	m := &Manager{stopDone: make(chan struct{})}
	if len(reporters) > 0 {
		m.report = reporters[0]
	}
	return m
}

func (m *Manager) emit(name, event string, err error) {
	if m.report != nil {
		m.report(name, event, err)
	}
}

// Register adds a hook before startup or shutdown, and marks resources with no Start for cleanup immediately.
func (m *Manager) Register(h Hook) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started || m.closed {
		panic("lifecycle: register after start or stop")
	}
	m.entries = append(m.entries, &entry{hook: h, active: h.Start == nil})
}

// Start runs hooks serially. Repeated calls wait for the first result unless their context expires or the
// manager is closed.
// A hook becomes eligible for cleanup before Start runs because a failed start may acquire resources.
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

// Stop drains hooks by stop order and leaves later dependencies open when its context expires.
func (m *Manager) Stop(ctx context.Context) error {
	return m.stop(ctx, false)
}

// Rollback releases acquired or attempted hooks in reverse StartOrder, reversing registration order for ties.
func (m *Manager) Rollback(ctx context.Context) error {
	return m.stop(ctx, true)
}

func (m *Manager) stop(ctx context.Context, rollback bool) error {
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
	// The first stop context bounds all internal waits; callers share the final result.
	<-m.stopDone
	return m.stopErr
}

func stopEntries(ctx context.Context, entries []*entry, rollback bool, report Reporter) error {
	if rollback {
		// Reverse startup order, including registration order within the same startup level.
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
		type result struct {
			name string
			err  error
		}
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
					// Waiting for log output also consumes the shutdown budget.
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

func pendingError(err error, entries []*entry) error {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.hook.Name)
	}
	return fmt.Errorf("lifecycle cleanup incomplete [%s]: %w", strings.Join(names, ", "), err)
}
