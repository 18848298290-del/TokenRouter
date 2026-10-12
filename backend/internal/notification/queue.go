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

// Task type constants.
const (
	TaskTypeVerifyCode    = "verify_code"
	TaskTypePasswordReset = "password_reset"
)

// EmailTask describes an identity email to prepare and send.
type EmailTask struct {
	Email    string
	SiteName string
	TaskType string // "verify_code" or "password_reset"
	ResetURL string // Only used for password_reset task type
	Locale   string // Optional Accept-Language locale hint
}

// EmailQueueService processes identity email tasks through a bounded in-memory queue.
type EmailQueueService struct {
	runCtx       context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	active       atomic.Int64
	stopErr      error
	emailService TaskProcessor
	taskChan     chan EmailTask
	wg           sync.WaitGroup
	stopChan     chan struct{}
	workers      int
	lifecycleMu  sync.RWMutex
	started      bool
	stopped      bool
}

// TaskProcessor prepares identity credentials when a worker executes the queued task.
type TaskProcessor interface {
	SendVerifyCode(context.Context, string, string, ...string) error
	SendPasswordResetEmailWithCooldown(context.Context, string, string, string, ...string) error
}

// NewEmailQueueService creates a queue. Call Start to launch its workers.
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

// Start launches workers once. A stopped queue cannot restart.
func (s *EmailQueueService) Start() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.started || s.stopped {
		return
	}
	s.started = true
	for i := range s.workers {
		s.wg.Add(1)
		go s.worker(i)
	}
	logger.LegacyPrintf("service.email_queue", "[EmailQueue] Started %d workers", s.workers)
}

func (s *EmailQueueService) worker(id int) {
	defer s.wg.Done()

	for task := range s.taskChan {
		s.active.Add(1)
		s.processTask(id, task)
		s.active.Add(-1)
	}
}

// processTask gives each identity email a 30-second budget, bounded by queue cancellation.
func (s *EmailQueueService) processTask(workerID int, task EmailTask) {
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
func (s *EmailQueueService) EnqueueVerifyCode(email, siteName string, locale ...string) error {
	s.lifecycleMu.RLock()
	defer s.lifecycleMu.RUnlock()
	if s.stopped {
		return fmt.Errorf("email queue is stopped")
	}

	task := EmailTask{
		Email:    email,
		SiteName: siteName,
		TaskType: TaskTypeVerifyCode,
		Locale:   firstEmailLocale(locale),
	}

	select {
	case s.taskChan <- task:
		logger.LegacyPrintf("service.email_queue", "[EmailQueue] Enqueued verify code task for %s", email)
		return nil
	default:
		return fmt.Errorf("email queue is full")
	}
}

// EnqueuePasswordReset queues password-reset delivery, returning an error when full or stopped.
func (s *EmailQueueService) EnqueuePasswordReset(email, siteName, resetURL string, locale ...string) error {
	s.lifecycleMu.RLock()
	defer s.lifecycleMu.RUnlock()
	if s.stopped {
		return fmt.Errorf("email queue is stopped")
	}

	task := EmailTask{
		Email:    email,
		SiteName: siteName,
		TaskType: TaskTypePasswordReset,
		ResetURL: resetURL,
		Locale:   firstEmailLocale(locale),
	}

	select {
	case s.taskChan <- task:
		logger.LegacyPrintf("service.email_queue", "[EmailQueue] Enqueued password reset task for %s", email)
		return nil
	default:
		return fmt.Errorf("email queue is full")
	}
}

// Stop closes admission and waits for accepted tasks to finish, discarding the drain error.
func (s *EmailQueueService) Stop() { _ = s.StopContext(context.Background()) }

// StopContext closes admission and waits for accepted tasks. On cancellation, it cancels in-flight delivery
// and records the unfinished task count.
func (s *EmailQueueService) StopContext(ctx context.Context) error {
	s.lifecycleMu.Lock()
	if !s.stopped {
		s.stopped = true
		close(s.taskChan)
		close(s.stopChan)
		go func() {
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

func firstEmailLocale(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}
