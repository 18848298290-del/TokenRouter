package maintenance

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// operations.go runs update, rollback, and restart actions under maintenance
// leases and tracks accepted work through shutdown.
// Accepted binary maintenance runs under an acquired lease rather than the HTTP
// request's cancellation lifetime. Admission registers work before releasing its
// mutex, so shutdown can wait for both acquisition and later operation completion.
// Lease ownership loss and service stop cancel accepted work, while the work
// wrapper adds a fifteen-minute limit to update and downloaded rollback execution.
//
// Completion stops the operation's service hook, attempts lease release with a
// separate context, and removes its wait-group entry once. Release errors are
// suppressed by this wrapper, so business results do not prove successful lease
// finalization. Stop retains the first caller's shared timeout result even if
// tracked work remains unfinished; subsequent stop contexts do not replace that
// waiting decision or make an earlier timeout into a completed drain.

type (
	// RestartRequester supplies the application restart operation used by accepted maintenance.
	RestartRequester interface {
		// RequestRestart requests application restart and reports a requester failure.
		// The app requester can return nil for unsupported platforms, pending restart,
		// or closed admission, so nil does not establish that a new timer was scheduled.
		RequestRestart() error
	}
	// UpdateAPI supplies release queries and executable update or rollback operations.
	UpdateAPI interface {
		// CheckUpdate returns update information and applies its force/cache policy.
		// Successful results are nonnil so Update can expose current and latest
		// versions when PerformUpdate reports that no installation is needed.
		CheckUpdate(context.Context, bool) (*ops.UpdateInfo, error)
		// PerformUpdate installs the selected release under the supplied operation context.
		// ErrNoUpdateAvailable allows the wrapper to fetch display information and return
		// a successful already-current result instead of requiring a restart.
		PerformUpdate(context.Context) error
		// Rollback restores the locally retained previous executable without a context parameter.
		// The wrapper checks accepted-work cancellation before invoking it; an already
		// started replacement then follows the underlying installer's completion behavior.
		Rollback() error
		// ListRollbackVersions returns display information for eligible downloadable previous versions.
		// The shared update service owns selection, allowing callers to present the
		// choices subsequently validated by RollbackToVersion.
		ListRollbackVersions(context.Context) ([]ops.RollbackVersion, error)
		// RollbackToVersion installs a requested allowed release using the supplied context.
		// The wrapper gives downloaded rollback its lease-derived lifetime and timeout,
		// independently of the HTTP caller that initiated the accepted action.
		RollbackToVersion(context.Context, string) error
	}
)

// Operations owns locks, cancellation, and shutdown waiting for accepted maintenance work.
type Operations struct {
	update UpdateAPI                   // shared executable maintenance service
	lock   *SystemOperationLockService // persistent operation lease claimant
	// restarter is optional. A nil requester still permits Restart to return its
	// accepted result after lease validation, while no application restart request is submitted.
	restarter RestartRequester
	// mu protects closed and wg admission, preventing new registration
	// after stop begins. ctx and cancel broadcast service shutdown, while wg counts
	// acquisition and accepted work through their completion and release attempts.
	mu     sync.Mutex         // maintenance admission state lock
	closed bool               // future operation admission closed
	ctx    context.Context    // service maintenance cancellation lifetime
	cancel context.CancelFunc // service shutdown cancellation function
	wg     sync.WaitGroup     // admitted maintenance operation count
	// stopOnce chooses one waiter result in stopErr, then done closes to publish it.
	// Later callers share that result even if active work continues after its timeout.
	stopOnce sync.Once     // one stop decision guard
	done     chan struct{} // shared stop result notification
	stopErr  error         // cached completion or timeout result
}

// NewOperations retains update, lease, and optional restart dependencies with an
// independent service cancellation lifetime. Construction admits no work; callers
// finish binding dependencies before serving actions and call StopContext to await accepted operations.
// @project-doc docs/operations/deployment_and_migrations.md#maintenance_execution
func NewOperations(update UpdateAPI, lock *SystemOperationLockService, restart RestartRequester) *Operations {
	ctx, cancel := context.WithCancel(context.Background())
	return &Operations{update: update, lock: lock, restarter: restart, ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

// BeginStop closes admission and cancels acquisition or accepted work through the
// service context. Callers use StopContext afterward to wait for the already
// registered maintenance operations and observe its shared completion or timeout result.
//
// Closing and admission registration use the same mutex. An acquisition counted
// before this transition remains in the wait group through its completion path;
// a later acquisition sees closed and is rejected before registering. Cancellation
// reaches existing acquisition and accepted-work hooks, while those operations
// retain responsibility for completing their tracked cleanup.
func (s *Operations) BeginStop() { s.mu.Lock(); s.closed = true; s.cancel(); s.mu.Unlock() }

// StopContext begins stop once and returns its shared completion or timeout
// result. The first caller's context selects the waiting deadline; later contexts
// do not independently interrupt the wait or replace an already published result.
//
// Admission closes before operation waiting starts. A separate waiter continues
// tracking work even after the deciding goroutine records a timeout and closes
// done. A timeout therefore reports unfinished operations rather than a completed
// resource drain, while repeated calls retain the same evidence of that first
// decision until a new Operations instance is constructed.
func (s *Operations) StopContext(ctx context.Context) error {
	s.stopOnce.Do(func() {
		s.BeginStop()
		// Keep operation completion waiting separate from timeout publication so the
		// first caller receives an unfinished-work diagnostic even when a worker has not returned yet.
		go func() {
			finished := make(chan struct{})
			go func() { s.wg.Wait(); close(finished) }()
			select {
			case <-finished:
			case <-ctx.Done():
				s.stopErr = fmt.Errorf("maintenance operations unfinished: %w", ctx.Err())
			}
			close(s.done)
		}()
	})
	<-s.done
	return s.stopErr
}

// workContext derives a fifteen-minute operation context and links service stop
// to its cancellation. Callers run it from the accepted lock context and invoke
// its completion function to stop the service hook and release the timeout resources.
func (s *Operations) workContext(ctx context.Context) (context.Context, context.CancelFunc) {
	work, cancel := context.WithTimeout(ctx, 15*time.Minute)
	stop := context.AfterFunc(s.ctx, cancel)
	return work, func() { stop(); cancel() }
}

// acquire admits a maintenance action and returns its lock plus idempotent
// completion callback, or the admission/storage error. Acquisition follows the
// caller and service contexts; a successful lock then supplies request-detached work.
//
// Admission and wait-group registration share the close mutex. Failure removes
// that registration immediately; success retains it until completion stops the
// service cancellation hook and attempts lease release under a separate two-second
// context. Release errors are discarded, and the lease service may wait for its
// renewal loop before reaching that context-bounded store call. Callers invoke
// completion after the business action with its result and failure reason.
func (s *Operations) acquire(ctx context.Context, id string) (*SystemOperationLock, func(string, bool), error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, apperror.ServiceUnavailable("SERVER_SHUTTING_DOWN", "server is shutting down")
	}
	// Count admission before releasing its mutex so shutdown waiting includes this
	// acquisition even when database claim setup has not yet returned an owner or error.
	s.wg.Add(1)
	s.mu.Unlock()
	if s.lock == nil {
		s.wg.Done()
		return nil, nil, ErrIdempotencyStoreUnavail
	}
	acquisition, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	lock, err := s.lock.Acquire(acquisition, id)
	stop()
	cancel()
	if err != nil {
		s.wg.Done()
		return nil, nil, err
	}
	// Acquisition context is no longer needed after the claim succeeds. Link the
	// accepted lock's independent cancellation to service stop before returning the business-action handle.
	stopWork := context.AfterFunc(s.ctx, lock.cancel)
	var once sync.Once
	return lock, func(reason string, success bool) {
		once.Do(func() {
			defer s.wg.Done()
			stopWork()
			release, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			// Business results remain the action's returned outcome when lease finalization
			// fails. Attempt bounded store release and discard that error while still completing operation accounting.
			_ = s.lock.Release(release, lock, success, reason)
		})
	}, nil
}

// Update runs accepted executable maintenance under its acquired lease context
// and work timeout, returning completion data or the update/query error. A no-update
// sentinel triggers a follow-up display query and returns an already-current result.
//
// Caller cancellation applies during acquisition, then accepted work survives
// HTTP disconnects until service stop, ownership loss, or its work timeout. The
// completion callback runs for every result with its success flag and failure
// reason; its lease finalization error is suppressed independently of the business
// result, so success describes installation or eligibility rather than stored release confirmation.
func (s *Operations) Update(ctx context.Context, operationID string) (any, error) {
	lock, release, err := s.acquire(ctx, operationID)
	if err != nil {
		return nil, err
	}
	var releaseReason string
	succeeded := false
	defer func() {
		release(releaseReason, succeeded)
	}()

	updateCtx, cancel := s.workContext(lock.Context())
	defer cancel()

	if err := s.update.PerformUpdate(updateCtx); err != nil {
		if errors.Is(err, ErrNoUpdateAvailable) {
			// A current installation needs display information rather than another apply
			// attempt; fetch that view and release this accepted operation as success when the query completes.
			info, checkErr := s.update.CheckUpdate(updateCtx, false)
			if checkErr != nil {
				releaseReason = "SYSTEM_UPDATE_FAILED"
				return nil, checkErr
			}
			succeeded = true
			return map[string]any{
				"message":            "Already up to date",
				"already_up_to_date": true,
				"current_version":    info.CurrentVersion,
				"latest_version":     info.LatestVersion,
				"operation_id":       lock.OperationID(),
			}, nil
		}
		releaseReason = "SYSTEM_UPDATE_FAILED"
		return nil, err
	}
	succeeded = true

	return map[string]any{
		"message":      "Update completed. Please restart the service.",
		"need_restart": true,
		"operation_id": lock.OperationID(),
	}, nil
}

// Rollback restores the local previous executable when targetVersion is empty,
// or installs an allowed selected release under the accepted work context. It
// checks cancellation before either path and returns restart-needed result data
// after the selected installation operation succeeds.
//
// Downloaded rollback uses the work timeout independently of HTTP cancellation.
// Local rollback has no context parameter once called, leaving its replacement
// completion to the installer. Deferred completion attempts lease release for
// both paths and suppresses its error, preserving the underlying installation
// result while shutdown accounting remains active until that release attempt returns.
func (s *Operations) Rollback(ctx context.Context, operationID, targetVersion string) (any, error) {
	lock, release, err := s.acquire(ctx, operationID)
	if err != nil {
		return nil, err
	}
	var releaseReason string
	succeeded := false
	defer func() {
		release(releaseReason, succeeded)
	}()

	if err := lock.Context().Err(); err != nil {
		return nil, err
	}
	if targetVersion != "" {
		// A selected release needs download preparation and installation. Use the accepted
		// lease lifetime and work timeout after acquisition has detached HTTP cancellation.
		rollbackCtx, cancel := s.workContext(lock.Context())
		defer cancel()
		err = s.update.RollbackToVersion(rollbackCtx, targetVersion)
	} else {
		err = s.update.Rollback()
	}
	if err != nil {
		releaseReason = "SYSTEM_ROLLBACK_FAILED"
		return nil, err
	}
	succeeded = true

	return map[string]any{
		"message":      "Rollback completed. Please restart the service.",
		"need_restart": true,
		"version":      targetVersion,
		"operation_id": lock.OperationID(),
	}, nil
}

// Restart requests application restart after lease acquisition and accepted-work
// cancellation checks. A nil restarter still returns the accepted operation
// result; requester errors are returned before the action is marked successful.
// Completion always attempts release and retains operation accounting through
// that attempt, while its finalization error is suppressed by the shared callback.
func (s *Operations) Restart(ctx context.Context, operationID string) (any, error) {
	lock, release, err := s.acquire(ctx, operationID)
	if err != nil {
		return nil, err
	}
	succeeded := false
	defer func() {
		release("", succeeded)
	}()

	if err := lock.Context().Err(); err != nil {
		return nil, err
	}
	if s.restarter != nil {
		if err := s.restarter.RequestRestart(); err != nil {
			return nil, err
		}
	}

	succeeded = true
	return map[string]any{
		"message":      "Service restart initiated",
		"operation_id": lock.OperationID(),
	}, nil
}
