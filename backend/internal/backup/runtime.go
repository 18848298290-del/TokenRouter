package backup

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// runtime.go coordinates backup startup, operation admission, shutdown, and
// cleanup context lifetimes.
// Backup operations can continue after their HTTP request returns, so shutdown
// needs to account for admitted setup and launched workers together. Admission
// registers setup in the wait group under the lifecycle lock. An admitted setup
// can register its worker after shutdown begins waiting, but retains its own
// count until that registration finishes. This handoff keeps the operation set
// from reaching zero between setup and worker execution. Operation contexts also
// observe service cancellation independently of their request context.
//
// Startup and stop each have one shared completion signal. Stop records the
// first initiating caller's result after cron and tracked work finish or that
// caller's budget expires; later stop contexts do not replace this decision.
// Cleanup and final-record writes use separate contexts so cancelled work can
// still attempt compensation, but an adopted shutdown context bounds those
// attempts too. Repeated startup remains stopped after the lifetime ends.

// Options supplies backup storage configuration, logging, and clock selection.
type Options struct {
	// Log receives events inline and may run while cron or record locks are held.
	// Supplied callbacks avoid reentering service methods that acquire those same locks.
	Log func(string, string, ...any)

	// LocalPath is the root reported in storage settings. App binds the injected
	// local store to this same path when constructing the backup service.
	LocalPath string
	// EncryptionKeyConfigured authorizes persistence of newly supplied S3 secrets.
	// It describes deployment key stability separately from the supplied encryptor implementation.
	EncryptionKeyConfigured bool
	// Now supplies metadata and storage-key timestamps. Nil selects time.Now;
	// supplied callbacks support calls from operation workers as well as request-side setup.
	Now func() time.Time
}

// SettingRepository supplies stored JSON configuration and archive record-list persistence.
type SettingRepository interface {
	// GetValue reads a stored backup setting under the supplied operation context.
	// Individual readers decide whether missing or failed reads mean default settings,
	// an empty record list, or a propagated configuration error.
	GetValue(context.Context, string) (string, error)
	// Set persists the complete supplied setting text under the operation context.
	// Record callers serialize updates to their JSON list; errors are handled according
	// to each mandatory plan save or best-effort progress/finalization path.
	Set(context.Context, string, string) error
}

// SecretEncryptor converts S3 secret text for storage and later connection use.
type SecretEncryptor interface {
	// Encrypt returns the stored representation of the supplied secret credential.
	// The service calls it for newly supplied secrets after checking persistent key
	// configuration, and propagates encryption errors before saving that configuration.
	Encrypt(string) (string, error)
	// Decrypt returns a connection secret from stored credential text or an error.
	// Configuration readers retain the stored value on failure for legacy plaintext
	// compatibility, so the implementation reports unsupported ciphertext instead of returning guessed plaintext.
	Decrypt(string) (string, error)
}

// MaintenanceLock claims shared database maintenance and returns its release function.
// A successful acquired result supplies a nonnil release callback that callers
// invoke after backup or restore work; false reports contention before archive execution.
type MaintenanceLock func(context.Context) (func(), bool, error)

// ArchiveExecutor owns archive streams, subprocesses, and temporary files for each operation.
type ArchiveExecutor interface {
	// Write exports the supplied record through the selected store and returns archive
	// bytes after producer completion. Callers provide synchronous plan persistence
	// and cleanup-context callbacks; record fields are mutated during the operation.
	//
	// Progress saves may be best effort, while segment plans precede uploads so
	// recorded keys can support cleanup after uncertain storage outcomes. The executor
	// waits for its compression and dump work before returning, and reports transfer
	// and cleanup errors for service finalization rather than marking the record completed itself.
	Write(context.Context, *BackupRecord, BackupObjectStore, *BackupS3Config, BackupDumpOptions, func(context.Context, *BackupRecord) error, func(time.Duration) (context.Context, context.CancelFunc)) (int64, error)
	// Restore supplies archived SQL to the database restoration operation using the
	// record's selected storage and keys. Multipart metadata is validated before
	// database input starts; readers and temporary resources belong to the executor's cleanup.
	//
	// Multipart validation checks ordered segments, recorded sizes, and any supplied
	// digests, while a single object can stream directly into decompression and SQL
	// input. Return values describe the archive and database operation; service-side
	// final-record persistence remains a separate step whose failure does not reverse
	// a completed database restore.
	Restore(context.Context, *BackupRecord, BackupObjectStore) error
}

// New creates an inactive backup service with the supplied persistence, archive,
// storage, encryption, and maintenance dependencies. Nil logging is silent, nil
// Now uses time.Now, and nil maintenance installs an acquired no-op claim.
// Callers supply functional dependencies before using their corresponding operations
// and call StartContext to initialize cron and recover stale recorded work.
// @project-doc docs/architecture/system_architecture.md#backup_and_maintenance
func New(repo SettingRepository, options Options, encryptor SecretEncryptor, factory BackupObjectStoreFactory, local BackupObjectStore, archive ArchiveExecutor, maintenance MaintenanceLock) *BackupService {
	if options.Log == nil {
		options.Log = func(string, string, ...any) {}
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if maintenance == nil {
		maintenance = func(context.Context) (func(), bool, error) { return func() {}, true, nil }
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &BackupService{
		log:                     options.Log,
		settingRepo:             repo,
		localPath:               options.LocalPath,
		now:                     options.Now,
		encryptor:               encryptor,
		encryptionKeyConfigured: options.EncryptionKeyConfigured,
		storeFactory:            factory,
		localStore:              local,
		archive:                 archive,
		maintenance:             maintenance,
		bgCtx:                   ctx,
		bgCancel:                cancel,
		startDone:               make(chan struct{}),
		stopDone:                make(chan struct{}),
	}
}

// begin admits an operation and returns its request-derived context plus an
// idempotent completion callback, or shutdown unavailability. Callers invoke the
// callback after setup or synchronous work finishes and use separate worker
// registration when a task continues after its HTTP response.
//
// Admission and wait-group registration share the stop transition's lock so
// shutdown cannot finish waiting before an accepted operation is counted. The
// returned context follows both request and service cancellation, while completion
// detaches the service callback, cancels that context, and removes this admission
// from the wait group once even when cleanup paths invoke it again.
func (s *BackupService) begin(ctx context.Context) (context.Context, func(), error) {
	s.operationLifecycleMu.Lock()
	defer s.operationLifecycleMu.Unlock()
	if s.shuttingDown.Load() {
		return nil, nil, apperror.ServiceUnavailable("SERVER_SHUTTING_DOWN", "server is shutting down")
	}
	// Register before releasing the lifecycle lock so shutdown's waiting set includes
	// this operation, then connect service cancellation to its request-derived context.
	s.wg.Add(1)
	combined, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.bgCtx, cancel)
	var once sync.Once
	return combined, func() { once.Do(func() { stop(); cancel(); s.wg.Done() }) }, nil
}

// StartContext initializes cron and recovers recorded stale work once per service
// lifetime. A stopped service returns immediately. The first startup uses the
// service background context for its reads; ctx bounds a later caller waiting
// for that startup's completion, whose original error is not retained for replay.
//
// Startup state, cron creation, and wait-group registration happen before the
// lifecycle lock is released, so Stop accounts for the whole warmup. The shared
// completion channel closes after warmup returns, including failures. Stored
// schedule parsing and application problems degrade to an available service with
// no installed schedule, while background cancellation can return its context error.
func (s *BackupService) StartContext(ctx context.Context) error {
	s.operationLifecycleMu.Lock()
	if s.shuttingDown.Load() {
		s.operationLifecycleMu.Unlock()
		return nil
	}
	if s.started {
		done := s.startDone
		s.operationLifecycleMu.Unlock()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Publish startup and register warmup before creating cron under the admission
	// lock, so stop cannot observe a started service whose active warmup is still uncounted.
	s.started = true
	s.wg.Add(1)
	s.cronMu.Lock()
	s.cronSched = cron.New()
	s.cronSched.Start()
	s.cronMu.Unlock()
	s.operationLifecycleMu.Unlock()
	defer s.wg.Done()
	defer close(s.startDone)
	s.recoverStaleRecords()
	if s.bgCtx.Err() != nil {
		return s.bgCtx.Err()
	}
	// Offer a ten-second deadline for the startup schedule lookup.
	load, cancel := context.WithTimeout(s.bgCtx, 10*time.Second)
	defer cancel()
	schedule, err := s.GetSchedule(load)
	if err != nil {
		// Schedule loading is allowed to degrade startup. Report this error and leave
		// cron without a backup entry rather than withholding the rest of the backup service.
		s.log("service.backup", "[INFO] [Backup] Failed to load scheduled backup configuration: %v", err)
		return nil
	}
	if schedule.Enabled && schedule.CronExpr != "" {
		if err := s.applyCronSchedule(schedule); err != nil {
			// Invalid or unapplied schedule state is observable but does not fail the
			// service lifetime, allowing administrators to repair its persisted configuration after startup.
			s.log("service.backup", "[INFO] [Backup] Failed to apply scheduled backup configuration: %v", err)
		}
	}
	return nil
}

// BeginStopContext rejects future admission and cancels service work after adopting
// a shutdown context. It replaces an absent or noncancellable stored context;
// an already adopted cancellable context remains the budget for final saves and cleanup.
//
// Existing cleanup cancel functions are linked to a newly adopted budget while
// holding the lifecycle lock. Cleanup created later reads that same context under
// the lock, so either registration order receives the budget's eventual cancellation.
// Background cancellation then reaches admitted work, while StopContext separately
// waits for cron and tracked operation completion.
func (s *BackupService) BeginStopContext(ctx context.Context) {
	s.operationLifecycleMu.Lock()
	if s.shutdownContext == nil || s.shutdownContext.Done() == nil {
		// Attach the adopted budget to cleanup already registered before shutdown;
		// later cleanup registers its own link under this same lock and context selection.
		s.shutdownContext = ctx
		for _, cancel := range s.cleanupCancels {
			context.AfterFunc(ctx, cancel)
		}
	}
	s.shuttingDown.Store(true)
	s.bgCancel()
	s.operationLifecycleMu.Unlock()
}

// Stop waits for shutdown using a new thirty-second context and suppresses the
// resulting error. Callers needing the unfinished-operation diagnostic use StopContext;
// a previously initiated stop retains its original shared result and waiting budget.
func (s *BackupService) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// This convenience wrapper has no error result. Callers can inspect the shared
	// incomplete-shutdown diagnostic through StopContext after this wait returns.
	_ = s.StopContext(ctx)
}

// StopContext initiates shutdown once and returns one shared completion or timeout
// result to every caller. The first initiating ctx limits that wait; later ctx
// arguments do not independently cancel a caller or replace the original result.
//
// Admission stops before cron is stopped. A background waiter accounts for
// existing cron callbacks and the service wait group, while another selection
// records either completion or the initiating context's expiration. An expired
// result closes stopDone even while operation waiting continues, so it reports
// incomplete shutdown rather than establishing that every resource has drained.
func (s *BackupService) StopContext(ctx context.Context) error {
	s.stopOnce.Do(func() {
		s.BeginStopContext(ctx)
		s.cronMu.Lock()
		var cronDone context.Context
		if s.cronSched != nil {
			cronDone = s.cronSched.Stop()
		}
		s.cronMu.Unlock()
		// Wait for cron callbacks before the tracked operation set, and publish the
		// first caller's deadline result separately so stalled work cannot hide an incomplete shutdown.
		go func() {
			done := make(chan struct{})
			go func() {
				if cronDone != nil {
					<-cronDone.Done()
				}
				s.wg.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-ctx.Done():
				s.stopErr = fmt.Errorf("backup operations unfinished: %w", ctx.Err())
			}
			close(s.stopDone)
		}()
	})
	<-s.stopDone
	return s.stopErr
}

// saveFinal attempts record finalization under a ten-second context derived from
// the adopted shutdown budget, or background when shutdown has not begun. It logs
// and returns persistence failure; it does not roll back archive or database work
// that already succeeded before finalization was attempted.
func (s *BackupService) saveFinal(record *BackupRecord) error {
	s.operationLifecycleMu.RLock()
	base := s.shutdownContext
	s.operationLifecycleMu.RUnlock()
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, 10*time.Second)
	defer cancel()
	err := s.saveRecord(ctx, record)
	if err != nil {
		s.log("service.backup", "[INFO] [Backup] Failed to save final record %s: %v", record.ID, err)
	}
	return err
}

// CreateBackup admits and tracks a synchronous backup until createBackup returns.
// Its context follows request and service cancellation; the returned record and
// error retain createBackup's progress-save and completion behavior, including best-effort record finalization.
func (s *BackupService) CreateBackup(ctx context.Context, triggeredBy string, expireDays int) (*BackupRecord, error) {
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.createBackup(ctx, triggeredBy, expireDays)
}

// RestoreBackup admits and tracks a synchronous restore of the selected completed
// record. Request and service cancellation reach the operation context, and the
// underlying restore chooses storage from recorded metadata before releasing its maintenance claim.
func (s *BackupService) RestoreBackup(ctx context.Context, id string) error {
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	return s.restoreBackup(ctx, id)
}

// SetMaintenanceLock assigns the maintenance claimant before startup and operation
// execution. Callers supply a nonnil function and avoid concurrent rebinding because
// this setter is unsynchronized; New alone provides the nil-maintenance no-op default.
func (s *BackupService) SetMaintenanceLock(lock MaintenanceLock) { s.maintenance = lock }

// cleanupContext returns an independently timed cleanup context and its completion
// function. Callers invoke completion after cleanup to cancel it and remove its
// service registry entry; ordinary operation cancellation does not stop this context.
//
// Registration shares the lifecycle lock with budget adoption, so cleanup created
// before or after BeginStopContext observes the adopted shutdown context. Completion
// removes the entry under that lock and stops its directly retained AfterFunc
// callback when possible. A budget hook added later by BeginStopContext can still
// invoke the already idempotent cancel function. Cleanup timeout and adopted budget both cancel
// the context, allowing compensation while still respecting application shutdown.
func (s *BackupService) cleanupContext(limit time.Duration) (context.Context, context.CancelFunc) {
	s.operationLifecycleMu.Lock()
	cleanup, cancel := context.WithTimeout(context.Background(), limit)
	s.cleanupSequence++
	id := s.cleanupSequence
	if s.cleanupCancels == nil {
		s.cleanupCancels = map[uint64]context.CancelFunc{}
	}
	s.cleanupCancels[id] = cancel
	// Link cleanup to an existing shutdown budget while registration is still locked,
	// preventing budget adoption and this hook's publication from passing one another unnoticed.
	var stop func() bool
	if s.shutdownContext != nil {
		stop = context.AfterFunc(s.shutdownContext, cancel)
	}
	s.operationLifecycleMu.Unlock()
	return cleanup, func() {
		cancel()
		if stop != nil {
			stop()
		}
		// Remove this cleanup registration after cancellation so later shutdown-budget
		// adoption no longer treats completed cleanup as an active compensation operation.
		s.operationLifecycleMu.Lock()
		delete(s.cleanupCancels, id)
		s.operationLifecycleMu.Unlock()
	}
}
