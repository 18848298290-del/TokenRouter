package backup

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Options supplies backup startup configuration and logging.
type Options struct {
	Log func(string, string, ...any)

	DatabaseName, LocalPath string
	EncryptionKeyConfigured bool
	Now                     func() time.Time
}
type SettingRepository interface {
	GetValue(context.Context, string) (string, error)
	Set(context.Context, string, string) error
}
type SecretEncryptor interface {
	Encrypt(string) (string, error)
	Decrypt(string) (string, error)
}
type MaintenanceLock func(context.Context) (func(), bool, error)

// ArchiveExecutor owns archive streams, subprocesses, and temporary files for each operation.
type ArchiveExecutor interface {
	Write(context.Context, *BackupRecord, BackupObjectStore, *BackupS3Config, BackupDumpOptions, func(context.Context, *BackupRecord) error, func(time.Duration) (context.Context, context.CancelFunc)) (int64, error)
	Restore(context.Context, *BackupRecord, BackupObjectStore) error
}

// New constructs the service; the application calls StartContext to start background work.
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
		databaseName:            options.DatabaseName,
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

func (s *BackupService) begin(ctx context.Context) (context.Context, func(), error) {
	s.operationLifecycleMu.Lock()
	defer s.operationLifecycleMu.Unlock()
	if s.shuttingDown.Load() {
		return nil, nil, apperror.ServiceUnavailable("SERVER_SHUTTING_DOWN", "server is shutting down")
	}
	s.wg.Add(1)
	combined, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.bgCtx, cancel)
	var once sync.Once
	return combined, func() { once.Do(func() { stop(); cancel(); s.wg.Done() }) }, nil
}

// StartContext shares one startup operation among callers and skips cron creation after shutdown begins.
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
	load, cancel := context.WithTimeout(s.bgCtx, 10*time.Second)
	defer cancel()
	schedule, err := s.GetSchedule(load)
	if err != nil {
		s.log("service.backup", "[Backup] 加载定时备份配置失败: %v", err)
		return nil
	} // A schedule read failure leaves the service available without scheduled backups.
	if schedule.Enabled && schedule.CronExpr != "" {
		if err := s.applyCronSchedule(schedule); err != nil {
			s.log("service.backup", "[Backup] 应用定时备份配置失败: %v", err)
		}
	}
	return nil
}

// BeginStopContext records the remaining shutdown budget before canceling work.
func (s *BackupService) BeginStopContext(ctx context.Context) {
	s.operationLifecycleMu.Lock()
	if s.shutdownContext == nil || s.shutdownContext.Done() == nil {
		s.shutdownContext = ctx
		for _, cancel := range s.cleanupCancels {
			context.AfterFunc(ctx, cancel)
		}
	}
	s.shuttingDown.Store(true)
	s.bgCancel()
	s.operationLifecycleMu.Unlock()
}

func (s *BackupService) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = s.StopContext(ctx)
}

// StopContext shares one stop result among callers and reports incomplete shutdown on timeout.
func (s *BackupService) StopContext(ctx context.Context) error {
	s.stopOnce.Do(func() {
		s.BeginStopContext(ctx)
		s.cronMu.Lock()
		var cronDone context.Context
		if s.cronSched != nil {
			cronDone = s.cronSched.Stop()
		}
		s.cronMu.Unlock()
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
		s.log("service.backup", "[Backup] 保存收尾记录失败 %s: %v", record.ID, err)
	}
	return err
}

// CreateBackup tracks the synchronous operation's resources while using the scheduled backup implementation.
func (s *BackupService) CreateBackup(ctx context.Context, triggeredBy string, expireDays int) (*BackupRecord, error) {
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.createBackup(ctx, triggeredBy, expireDays)
}

func (s *BackupService) RestoreBackup(ctx context.Context, id string) error {
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	return s.restoreBackup(ctx, id)
}

// SetMaintenanceLock binds the maintenance lock before startup for compatibility with existing assembly code.
func (s *BackupService) SetMaintenanceLock(lock MaintenanceLock) { s.maintenance = lock }

// cleanupContext gives cleanup its own timeout, bounded by the application's remaining shutdown budget.
func (s *BackupService) cleanupContext(limit time.Duration) (context.Context, context.CancelFunc) {
	s.operationLifecycleMu.Lock()
	cleanup, cancel := context.WithTimeout(context.Background(), limit)
	s.cleanupSequence++
	id := s.cleanupSequence
	if s.cleanupCancels == nil {
		s.cleanupCancels = map[uint64]context.CancelFunc{}
	}
	s.cleanupCancels[id] = cancel
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
		s.operationLifecycleMu.Lock()
		delete(s.cleanupCancels, id)
		s.operationLifecycleMu.Unlock()
	}
}
