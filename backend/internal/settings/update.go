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

// update.go serializes combined settings sessions and runs runtime application
// after persistence.
// Combined settings updates hold an instance slot from the first storage read
// through runtime application. Preparation can therefore compare one prior snapshot
// with submitted values before a single batch is committed. Caller cancellation
// or application shutdown cancels the session context, while the caller retains
// the slot until Close after its work finishes.
//
// Prepared writes are merged before persistence, with conditional expectations
// used when present. A completed write is marked committed before post-commit
// callbacks run, and callback errors accumulate while later callbacks continue.
// Those errors identify persisted data and failed modules; closing a session
// releases admission resources and cannot undo the completed batch. Shutdown waits
// for both admitted sessions and registered waiters to leave.

// PreparedChange carries module-owned writes and a callback applied after batch persistence.
type PreparedChange struct {
	Module string // owner reported for application errors
	// Values is read during Commit and merged into its batch map. Keep supplied
	// entries stable until Commit finishes; Registry clones its returned Values maps.
	Values map[string]string
	// Expected selects conditional persistence. The map and pointed strings remain
	// shared when Registry returns them; later changes overwrite duplicate expectation
	// keys in Commit, and PostgreSQL nil expectations accept absent or empty values.
	Expected map[string]*string
	// Apply runs after persistence and can return an application error. It receives
	// the session context, and later callbacks still run after its returned error.
	Apply func(context.Context) error
}

// Updates serializes reads, preparation, persistence, and runtime application within this instance.
// @project-doc docs/interfaces/configuration.md#runtime_settings
type Updates struct {
	repo Repository // prepared settings persistence dependency
	// gate holds one token for an admitted session. Its caller returns that token
	// with Close; cancelled waiters that never acquire it do not drain the channel.
	gate chan struct{}
	// mu protects sealed and active. active counts owners and waiters, and done
	// closes when a sealed coordinator loses its final registered request.
	mu     sync.Mutex    // update lifecycle state lock
	sealed bool          // new update registration closed
	active int           // registered owners and waiting requests
	done   chan struct{} // sealed coordinator completion signal
	// run is cancelled once by Seal. Begin links its cancellation to each session
	// context so shutdown reaches owners and waiters before Stop waits for cleanup.
	run    context.Context
	cancel context.CancelFunc // coordinator shutdown cancellation function
}

// UpdateSession holds instance admission from the initial read through committed
// application. Callers Close after work finishes, releasing the slot while saved
// values remain persisted.
type UpdateSession struct {
	owner *Updates // coordinator that owns the slot
	// ctx follows caller cancellation and owner shutdown. cancel closes this
	// session context, while stop unregisters its shutdown callback during cleanup.
	ctx    context.Context    // context for all update phases
	cancel context.CancelFunc // session context cancellation function
	stop   func() bool        // shutdown callback unregister function
	once   sync.Once          // acquired slot release guard
	// committed changes after persistence succeeds. Callers sequence Commit and
	// Close on their session; this flag has no separate concurrent-use protection.
	committed bool
}

// newUpdates constructs an instance coordinator with one admitted update slot
// and a shutdown context. The completion channel closes after Seal and the last
// registered owner or waiting request has finished.
func newUpdates(repo Repository) *Updates {
	ctx, cancel := context.WithCancel(context.Background())
	return &Updates{repo: repo, gate: make(chan struct{}, 1), done: make(chan struct{}), run: ctx, cancel: cancel}
}

// Begin waits for this instance's update slot and returns a session whose context
// is cancelled by either the caller or coordinator shutdown. Callers acquire it
// before reading current settings and Close it after preparation, persistence,
// and runtime application finish.
//
// Owners and waiting requests increment active before admission. Cancellation
// while waiting unregisters that request; cancellation after a winning admission
// returns the acquired token through Close. Shutdown rejects later Begin calls
// and cancels both groups, while Stop still waits for admitted sessions to Close.
// The returned session is used sequentially by its caller.
func (u *Updates) Begin(ctx context.Context) (*UpdateSession, error) {
	u.mu.Lock()
	if u.sealed {
		u.mu.Unlock()
		return nil, apperror.ServiceUnavailable("SETTINGS_STOPPED", "settings updates are stopped")
	}
	// Register the waiter before deriving its context so Seal includes it in
	// cancellation and completion accounting even before it wins an admission token.
	u.active++
	u.mu.Unlock()
	updateCtx, cancel := context.WithCancel(ctx)
	// Link shutdown cancellation to the caller-derived context so the same select
	// can finish a waiter or cancel an admitted session when this coordinator is sealed.
	stop := context.AfterFunc(u.run, cancel)
	s := &UpdateSession{owner: u, ctx: updateCtx, cancel: cancel, stop: stop}
	select {
	case u.gate <- struct{}{}:
		if err := updateCtx.Err(); err != nil {
			// Admission won together with cancellation. Return this unused slot through
			// Close before reporting the error so another registered request can finish.
			s.Close()
			return nil, err
		}
		return s, nil
	case <-updateCtx.Done():
		// This waiter never acquired a slot. Unregister its shutdown callback and
		// request count without removing the token still held by an admitted owner.
		stop()
		cancel()
		u.finish()
		return nil, updateCtx.Err()
	}
}

// Context returns the update context linked to caller cancellation and coordinator shutdown.
func (s *UpdateSession) Context() context.Context { return s.ctx }

// Commit merges prepared writes, persists them, and executes post-commit
// callbacks in supplied order. Missing owners and duplicate written keys fail
// before storage. Expectations are merged in change order; any expectation selects
// the repository's conditional batch writer.
//
// Persistence success marks the session committed before callbacks begin. Every
// callback is attempted even after an earlier callback returns an error; the
// combined SETTINGS_APPLY_FAILED error records persisted=true and failed modules.
// The values remain stored, so callers handle that response as a committed update
// with application failures. A successful persistence cannot be repeated through
// this session, and its caller sequences Commit with Close.
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
	// Compare expectations within the value write so a stale content revision
	// rejects this complete prepared batch before any application callback can run.
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
	// Record successful persistence before invoking applications so a callback
	// failure cannot authorize another batch write through the same update session.
	s.committed = true
	var failures []error
	var modules []string
	// Attempt every supplied application callback after the committed batch,
	// retaining failures for one persisted=true response instead of abandoning later modules.
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

// Close cancels this session and releases its acquired update slot once.
// The caller invokes it after its work finishes; repeated Close calls have no
// further effect, and committed storage remains as written.
func (s *UpdateSession) Close() {
	// Unregister the shutdown callback and cancel the session before releasing
	// its token; finish then closes lifecycle completion when this is the final session.
	s.once.Do(func() {
		s.stop()
		s.cancel()
		<-s.owner.gate
		s.owner.finish()
	})
}

// finish unregisters one admitted owner or waiting request under the lifecycle
// mutex. Begin increments that count before admission, so a waiting cancellation
// and an owner's later Close each remove their own registration while sharing
// the same shutdown completion state.
//
// After Seal, the final transition to zero closes done for Stop waiters. Before
// sealing, a zero count leaves done open because another request can still Begin.
// Callers invoke finish once per registered request, after returning any acquired
// gate token or after a waiter cancels without acquiring one.
func (u *Updates) finish() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.active--
	// Publish completion after the last registered request leaves a sealed
	// coordinator; an unsealed zero count still permits later Begin calls.
	if u.sealed && u.active == 0 {
		close(u.done)
	}
}

// Seal closes update admission and cancels the coordinator context under the
// lifecycle lock. It is idempotent, and subsequent Begin calls receive SETTINGS_STOPPED
// before adding themselves to active accounting. Registered owners and waiters
// receive cancellation through their derived contexts.
//
// A coordinator with no active registrations closes done immediately. Otherwise,
// finish closes it after waiting cancellations and owner Close calls remove the
// remaining registrations. Cancellation requests cooperation; Seal does not return
// an owner's slot or finish its work, so Stop still waits for that cleanup.
func (u *Updates) Seal() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.sealed {
		return
	}
	// Seal registration under the lifecycle lock before cancellation so requests
	// arriving during shutdown cannot add themselves after completion has been selected.
	u.sealed = true
	u.cancel()
	if u.active == 0 {
		close(u.done)
	}
}

// Stop seals update admission and waits for all registered requests to finish.
// The supplied context bounds that wait; a timeout returns an error while active
// sessions can still hold the stores and runtime dependencies needed for cleanup.
func (u *Updates) Stop(ctx context.Context) error {
	u.Seal()
	select {
	case <-u.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("settings updates unfinished: %w", ctx.Err())
	}
}
