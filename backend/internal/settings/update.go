package settings

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// PreparedChange contains validated values, expected stored values, and a post-commit callback.
type PreparedChange struct {
	Module   string
	Values   map[string]string
	Expected map[string]*string
	Apply    func(context.Context) error
}

// Updates serializes reads, preparation, persistence, and runtime application within this instance.
// @project-doc docs/interfaces/configuration.md#runtime_settings
type Updates struct {
	repo   Repository
	gate   chan struct{}
	mu     sync.Mutex
	sealed bool
	active int
	done   chan struct{}
	run    context.Context
	cancel context.CancelFunc
}

// UpdateSession holds update coordination from before reading current values through post-commit application.
// Callers must Close the session. Closing does not roll back committed data.
type UpdateSession struct {
	owner     *Updates
	ctx       context.Context
	cancel    context.CancelFunc
	stop      func() bool
	once      sync.Once
	committed bool
}

func newUpdates(repo Repository) *Updates {
	ctx, cancel := context.WithCancel(context.Background())
	return &Updates{repo: repo, gate: make(chan struct{}, 1), done: make(chan struct{}), run: ctx, cancel: cancel}
}

func (u *Updates) Begin(ctx context.Context) (*UpdateSession, error) {
	u.mu.Lock()
	if u.sealed {
		u.mu.Unlock()
		return nil, apperror.ServiceUnavailable("SETTINGS_STOPPED", "settings updates are stopped")
	}
	u.active++
	u.mu.Unlock()
	updateCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(u.run, cancel)
	s := &UpdateSession{owner: u, ctx: updateCtx, cancel: cancel, stop: stop}
	select {
	case u.gate <- struct{}{}:
		if err := updateCtx.Err(); err != nil {
			s.Close()
			return nil, err
		}
		return s, nil
	case <-updateCtx.Done():
		stop()
		cancel()
		u.finish()
		return nil, updateCtx.Err()
	}
}

func (s *UpdateSession) Context() context.Context { return s.ctx }

// Commit merges prepared keys and writes one batch. Application failures return SETTINGS_APPLY_FAILED with
// persisted=true; committed values remain stored.
func (s *UpdateSession) Commit(changes ...PreparedChange) error {
	if s.committed {
		return errors.New("settings update already committed")
	}
	values := make(map[string]string)
	expected := make(map[string]*string)
	for _, change := range changes {
		if change.Module == "" {
			return errors.New("settings change has no owner")
		}
		for key := range change.Values {
			if _, exists := values[key]; exists {
				return fmt.Errorf("duplicate settings key: %s", key)
			}
		}
		maps.Copy(values, change.Values)
		maps.Copy(expected, change.Expected)
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if len(expected) > 0 {
		writer, ok := s.owner.repo.(ConditionalRepository)
		if !ok {
			return errors.New("settings repository does not support conditional updates")
		}
		if err := writer.CompareAndSetMultiple(s.ctx, values, expected); err != nil {
			return err
		}
	} else if err := s.owner.repo.SetMultiple(s.ctx, values); err != nil {
		return err
	}
	s.committed = true
	var failures []error
	var modules []string
	for _, change := range changes {
		if change.Apply == nil {
			continue
		}
		if err := change.Apply(s.ctx); err != nil {
			modules = append(modules, change.Module)
			failures = append(failures, err)
		}
	}
	if len(failures) > 0 {
		return apperror.InternalServer("SETTINGS_APPLY_FAILED", "settings persisted but runtime application failed").
			WithMetadata(map[string]string{"persisted": "true", "modules": strings.Join(modules, ",")}).WithCause(errors.Join(failures...))
	}
	return nil
}

func (s *UpdateSession) Close() {
	s.once.Do(func() {
		s.stop()
		s.cancel()
		<-s.owner.gate
		s.owner.finish()
	})
}

func (u *Updates) finish() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.active--
	if u.sealed && u.active == 0 {
		close(u.done)
	}
}

// Seal rejects new updates and cancels active preparation and application contexts before HTTP shutdown waits.
func (u *Updates) Seal() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.sealed {
		return
	}
	u.sealed = true
	u.cancel()
	if u.active == 0 {
		close(u.done)
	}
}

// Stop seals updates and waits for sessions to close. Its deadline limits the wait; unfinished sessions still
// hold their dependencies.
func (u *Updates) Stop(ctx context.Context) error {
	u.Seal()
	select {
	case <-u.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("settings updates unfinished: %w", ctx.Err())
	}
}
