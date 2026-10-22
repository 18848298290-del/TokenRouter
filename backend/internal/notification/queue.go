package notification

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

// queue.go queues identity email tasks and coordinates worker startup,
// admission, and shutdown.
// Identity credentials are prepared when a worker consumes an accepted task,
// so queue admission records delivery intent before generation or token reuse.
// The queue has fixed storage and worker counts; accepted tasks remain in memory
// until a worker handles them or the process exits. Enqueue success describes
// admission, while delivery failures are reported by worker logs.
//
// Lifecycle locking orders admission and worker registration against channel
// closure. Shutdown closes the task channel once and waits for workers to drain
// it; a canceled shutdown wait records its first drain error and cancels the
// shared processing context. Subsequent tasks can still be consumed with that
// canceled context, and repeated shutdown callers read the retained result.

// Identity task kinds choose credential preparation and the mail operation
// performed by a worker after the bounded queue accepts the delivery intent.
const (
	TaskTypeVerifyCode    = "verify_code"    // prepare verification code
	TaskTypePasswordReset = "password_reset" // prepare reset link
)

// EmailTask describes an identity email to prepare and send.
type EmailTask struct {
	Email    string // identity delivery address
	SiteName string // caller branding hint
	TaskType string // selected identity operation
	// ResetURL supplies the password-reset frontend base passed to the identity
	// processor; verification-code tasks do not consume this task field.
	ResetURL string
	// Locale is the first optional trimmed language hint from queue admission;
	// the identity processor and notification renderer determine its later selection.
	Locale string
}

// EmailQueueService processes identity email tasks through a bounded in-memory queue.
type EmailQueueService struct {
	// runCtx and cancel bound all per-task contexts after a shutdown deadline.
	// done closes after registered workers return, while active supplies the
	// separately sampled count used alongside queued length in drain error reporting.
	runCtx context.Context    // shared processing context
	cancel context.CancelFunc // processing cancellation function
	done   chan struct{}      // worker completion signal
	active atomic.Int64       // sampled in-flight count
	// stopErr retains the first reported drain failure under lifecycleMu;
	// later StopContext calls return that stored result even after workers finish.
	stopErr      error
	emailService TaskProcessor  // identity credential processor
	taskChan     chan EmailTask // bounded accepted tasks
	wg           sync.WaitGroup // registered worker lifetime
	// stopChan closes on terminal shutdown; workers consume taskChan until it
	// drains, and this channel currently has no reader in the queue implementation.
	stopChan chan struct{}
	workers  int // configured worker count
	// lifecycleMu protects started, stopped, stopErr, and admission against
	// task-channel closure. A shutdown records stopped before later Start or
	// Enqueue calls can register additional work through that lifecycle gate.
	lifecycleMu sync.RWMutex // admission and lifecycle lock
	started     bool         // worker registration completed
	stopped     bool         // terminal admission state
}

// TaskProcessor prepares identity credentials when a worker executes the queued task.
type TaskProcessor interface {
	// SendVerifyCode prepares and sends a verification credential using the task context.
	// Queue workers pass the first language hint and log returned errors;
	// identity owns credential persistence and its sending order for this operation.
	SendVerifyCode(context.Context, string, string, ...string) error
	// SendPasswordResetEmailWithCooldown prepares or reuses a reset credential under identity cooldown rules.
	// Queue workers supply the reset URL and language hint, and the processor
	// uses the task context for credential work and eventual mail delivery.
	SendPasswordResetEmailWithCooldown(context.Context, string, string, string, ...string) error
}

// NewEmailQueueService constructs a dormant queue with storage for one hundred identity tasks.
// Nonpositive workers selects three workers. Construction creates the processing
// context and channels; Start launches work, and admission can precede that startup.
func NewEmailQueueService(emailService TaskProcessor, workers int) *EmailQueueService {
	if workers <= 0 {
		workers = 3
	}

	ctx, cancel := context.WithCancel(context.Background())
	service := &EmailQueueService{
		runCtx: ctx, cancel: cancel, done: make(chan struct{}),
		emailService: emailService,
		taskChan:     make(chan EmailTask, 100),
		stopChan:     make(chan struct{}),
		workers:      workers,
	}

	return service
}

// Start registers the configured workers once and leaves repeated calls unchanged.
// A previously stopped queue cannot register workers, including shutdown before
// its first Start. The lifecycle lock orders worker registration before StopContext
// can close admission and begin waiting on the registered worker count.
//
// Each worker drains the same task channel, while fixed worker registration
// bounds concurrent processing. Accepted tasks can therefore wait before Start
// without spawning a goroutine per email, and shutdown can account for those
// queued intents even when startup never occurred.
func (s *EmailQueueService) Start() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.started || s.stopped {
		return
	}
	s.started = true
	// Register each worker while shutdown is excluded by lifecycleMu. StopContext
	// then waits for this completed registration before it can observe an empty WaitGroup.
	for i := range s.workers {
		s.wg.Add(1)
		go s.worker(i)
	}
	logger.LegacyPrintf("service.email_queue", "[EmailQueue] Started %d workers", s.workers)
}

// worker consumes accepted tasks until the task channel has closed and drained.
// It updates the atomic active count around each synchronous processing call
// and completes its WaitGroup reference on return, allowing shutdown to wait
// for that worker's current identity operation.
func (s *EmailQueueService) worker(id int) {
	// Worker completion owns one registration, including exits after a closed
	// task channel drains. Shutdown's completion monitor waits for every such reference to return.
	defer s.wg.Done()

	for task := range s.taskChan {
		// Admission and drain reporting progress independently of this worker.
		// Publish the processing count around the credential call so shutdown can sample queued and active work.
		s.active.Add(1)
		s.processTask(id, task)
		s.active.Add(-1)
	}
}

// processTask gives each identity email a 30-second budget, bounded by queue cancellation.
// The task type chooses verification or cooldown-aware password reset processing.
// Returned failures and unknown types are logged; queue admission callers receive
// no later delivery result or automatic retry from this worker.
func (s *EmailQueueService) processTask(workerID int, task EmailTask) {
	// The shared run context allows a canceled drain wait to stop credential
	// and SMTP work; release this task's timeout timer after the processor returns.
	ctx, cancel := context.WithTimeout(s.runCtx, 30*time.Second)
	defer cancel()

	switch task.TaskType {
	case TaskTypeVerifyCode:
		if err := s.emailService.SendVerifyCode(ctx, task.Email, task.SiteName, task.Locale); err != nil {
			logger.LegacyPrintf("service.email_queue", "[EmailQueue] Worker %d failed to send verify code to %s: %v", workerID, task.Email, err)
		} else {
			logger.LegacyPrintf("service.email_queue", "[EmailQueue] Worker %d sent verify code to %s", workerID, task.Email)
		}
	case TaskTypePasswordReset:
		if err := s.emailService.SendPasswordResetEmailWithCooldown(ctx, task.Email, task.SiteName, task.ResetURL, task.Locale); err != nil {
			logger.LegacyPrintf("service.email_queue", "[EmailQueue] Worker %d failed to send password reset to %s: %v", workerID, task.Email, err)
		} else {
			logger.LegacyPrintf("service.email_queue", "[EmailQueue] Worker %d sent password reset to %s", workerID, task.Email)
		}
	default:
		logger.LegacyPrintf("service.email_queue", "[EmailQueue] Worker %d unknown task type: %s", workerID, task.TaskType)
	}
}

// EnqueueVerifyCode queues verification-code delivery, returning an error when full or stopped.
// Admission is nonblocking and may occur before Start; success records an intent
// whose credentials are prepared later by a worker. Only the first optional
// language value is trimmed and stored with that intent.
func (s *EmailQueueService) EnqueueVerifyCode(email, siteName string, locale ...string) error {
	task := EmailTask{
		Email:    email,
		SiteName: siteName,
		TaskType: TaskTypeVerifyCode,
		Locale:   firstEmailLocale(locale),
	}
	return s.enqueue(task, "verify code")
}

// EnqueuePasswordReset queues password-reset delivery, returning an error when full or stopped.
// The supplied reset URL and first trimmed language hint are stored for later
// identity processing. A successful enqueue does not establish that credentials
// have been generated or the SMTP server has received a message.
func (s *EmailQueueService) EnqueuePasswordReset(email, siteName, resetURL string, locale ...string) error {
	task := EmailTask{
		Email:    email,
		SiteName: siteName,
		TaskType: TaskTypePasswordReset,
		ResetURL: resetURL,
		Locale:   firstEmailLocale(locale),
	}
	return s.enqueue(task, "password reset")
}

// enqueue admits an already constructed identity task or returns a full or stopped error.
// Callers provide the task's readable name for the success log and can admit
// work before Start registers workers.
//
// The lifecycle read lock covers the stopped check, nonblocking send, and
// success log. StopContext therefore waits for the admission decision and its
// log before acquiring the write lock that closes the channel, so each sender
// completes before closure or observes terminal admission.
func (s *EmailQueueService) enqueue(task EmailTask, taskName string) error {
	s.lifecycleMu.RLock()
	defer s.lifecycleMu.RUnlock()
	if s.stopped {
		return fmt.Errorf("email queue is stopped")
	}

	// Use a default branch because waiting for capacity while holding this
	// read lock could prevent StopContext from acquiring the write lock to close admission.
	select {
	case s.taskChan <- task:
		logger.LegacyPrintf("service.email_queue", "[EmailQueue] Enqueued %s task for %s", taskName, task.Email)
		return nil
	default:
		return fmt.Errorf("email queue is full")
	}
}

// Stop closes admission and waits for accepted tasks to finish, discarding the drain error.
// It uses a background context and can wait for cooperative task processors
// indefinitely. Call StopContext when the caller needs a deadline and the
// recorded drain result for shutdown reporting.
func (s *EmailQueueService) Stop() { _ = s.StopContext(context.Background()) }

// StopContext closes queue admission once and waits for registered workers to return.
// A completed wait returns the retained drain result. If the context branch
// wins, it records the first error with sampled queued length and active count,
// then cancels the shared processing context before returning that result.
//
// Channel closure and worker registration share lifecycleMu, excluding a racing
// Start or Enqueue. The monitor reports queued intents left after zero registered
// workers finish, covering Stop-before-Start. Repeated callers use the same done
// channel and first stopErr; simultaneous completion and context readiness are
// resolved by the select branch chosen for that call.
func (s *EmailQueueService) StopContext(ctx context.Context) error {
	s.lifecycleMu.Lock()
	if !s.stopped {
		// Publish terminal admission before closing the work channel. The write
		// lock excludes enqueue sends and startup registration while the completion monitor is installed.
		s.stopped = true
		close(s.taskChan)
		close(s.stopChan)
		go func() {
			// Waiting precedes the final queued-length check, so registered workers
			// finish consuming accepted tasks before the monitor reports work that never obtained a worker.
			s.wg.Wait()
			s.lifecycleMu.Lock()
			if len(s.taskChan) > 0 && s.stopErr == nil {
				s.stopErr = fmt.Errorf("email queue drain incomplete: %d tasks", len(s.taskChan))
			}
			s.lifecycleMu.Unlock()
			close(s.done)
		}()
	}
	done := s.done
	s.lifecycleMu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		// A deadline limits the caller's wait, not worker registration lifetime.
		// Retain the first sampled drain failure, then cancel processing so cooperative identity calls can return.
		s.lifecycleMu.Lock()
		if s.stopErr == nil {
			s.stopErr = fmt.Errorf("email queue drain incomplete: %d tasks: %w", int64(len(s.taskChan))+s.active.Load(), ctx.Err())
		}
		s.lifecycleMu.Unlock()
		s.cancel()
	}
	s.lifecycleMu.RLock()
	defer s.lifecycleMu.RUnlock()
	return s.stopErr
}

// firstEmailLocale returns the first trimmed optional language hint or an empty string.
// Additional values are ignored, and this helper performs no language
// normalization; identity and notification readers interpret the stored hint later.
func firstEmailLocale(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}
