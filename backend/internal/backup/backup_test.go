package backup

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	TestSettingKeyBackupS3Config      = settingKeyBackupS3Config
	TestSettingKeyBackupStorageConfig = settingKeyBackupStorageConfig
	TestSettingKeyBackupRecords       = settingKeyBackupRecords
)

type backupRecordSettings struct {
	runtimeSettings
	events  *[]string
	records []BackupRecord
}

type backupRecordArchive struct {
	runtimeArchive
	events *[]string
}

func TestBackupInitialRecord(t *testing.T) {
	for _, async := range []bool{false, true} {
		name := "sync"
		if async {
			name = "async"
		}
		t.Run(name, func(t *testing.T) {
			var events []string
			repo := &backupRecordSettings{events: &events}
			archive := &backupRecordArchive{events: &events}
			nowCalls := 0
			start := time.Date(2026, 10, 11, 23, 59, 59, 0, time.UTC)
			s := New(repo, Options{DatabaseName: "test", Now: func() time.Time {
				events = append(events, "now")
				nowCalls++
				return start.Add(time.Duration(nowCalls-1) * time.Second)
			}}, nil, nil, nil, archive, nil)
			defer s.Stop()
			var record *BackupRecord
			var err error
			if async {
				record, err = s.StartBackup(context.Background(), "manual", 2)
			} else {
				record, err = s.createBackup(context.Background(), "manual", 2)
			}
			require.NoError(t, err)
			s.wg.Wait()
			require.Len(t, repo.records[0].ID, 8)
			require.Regexp(t, `^[0-9a-f]{8}$`, repo.records[0].ID)
			progress := "dumping"
			if async {
				progress = "pending"
			}
			require.Equal(t, BackupRecord{
				ID: record.ID, Status: "running", BackupType: "postgres",
				FileName: "test_20261011_235959.sql.gz", StorageType: BackupStorageTypeLocal,
				StorageKey: "2026/10/12/test_20261011_235959.sql.gz", TriggeredBy: "manual",
				StartedAt: "2026-10-11T23:59:59Z", ExpiresAt: "2026-10-13T23:59:59Z", Progress: progress,
			}, repo.records[0])
			wantEvents := []string{settingKeyBackupStorageConfig, settingKeyBackupS3Config, "now", "now", settingKeyBackupContentConfig}
			if async {
				wantEvents = append(wantEvents, "save:pending")
				require.Equal(t, repo.records[0], *record)
			}
			wantEvents = append(wantEvents, "save:dumping", "archive", "now", "save:")
			require.Equal(t, wantEvents, events)
		})
	}
}

func TestBackupInitialRecordFailure(t *testing.T) {
	for _, async := range []bool{false, true} {
		for _, corrupt := range []bool{false, true} {
			var events []string
			repo := &backupRecordSettings{events: &events}
			if corrupt {
				repo.values = map[string]string{settingKeyBackupContentConfig: "invalid JSON"}
			} else {
				repo.fail = true
			}
			archive := &backupRecordArchive{events: &events}
			s := New(repo, Options{DatabaseName: "test", Now: func() time.Time {
				return time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
			}}, nil, nil, nil, archive, nil)
			var record *BackupRecord
			var err error
			if async {
				record, err = s.StartBackup(context.Background(), "manual", 0)
			} else {
				record, err = s.createBackup(context.Background(), "manual", 0)
			}
			s.wg.Wait()
			s.Stop()
			if async {
				require.Error(t, err)
				require.Nil(t, record)
				require.NotContains(t, events, "archive")
			} else {
				require.Empty(t, record.ExpiresAt)
				if corrupt {
					require.ErrorIs(t, err, ErrBackupContentConfigCorrupt)
					require.Equal(t, "failed", record.Status)
					require.NotContains(t, events, "archive")
				} else {
					require.NoError(t, err)
					require.Equal(t, "completed", record.Status)
					require.Contains(t, events, "archive")
				}
			}
		}
	}
}

func (r *backupRecordSettings) GetValue(ctx context.Context, key string) (string, error) {
	if key != settingKeyBackupRecords {
		*r.events = append(*r.events, key)
	}
	return r.runtimeSettings.GetValue(ctx, key)
}

func (r *backupRecordSettings) Set(ctx context.Context, key, value string) error {
	var records []BackupRecord
	if err := json.Unmarshal([]byte(value), &records); err != nil {
		return err
	}
	r.records = append(r.records, records[0])
	*r.events = append(*r.events, "save:"+records[0].Progress)
	return r.runtimeSettings.Set(ctx, key, value)
}

func (a *backupRecordArchive) Write(_ context.Context, _ *BackupRecord, _ BackupObjectStore, _ *BackupS3Config, _ BackupDumpOptions, _ func(context.Context, *BackupRecord) error, _ func(time.Duration) (context.Context, context.CancelFunc)) (int64, error) {
	*a.events = append(*a.events, "archive")
	return 0, nil
}

func TestRestoreMustRegister(t *testing.T) {
	repo := &runtimeSettings{}
	archive := &runtimeArchive{}
	s := runtimeBackup(repo, archive)
	data, err := json.Marshal([]BackupRecord{{ID: "fixture", Status: "completed", StorageType: "local", StorageKey: "fixture.gz"}})
	require.NoError(t, err)
	require.NoError(t, repo.Set(context.Background(), settingKeyBackupRecords, string(data)))
	repo.fail = true
	_, err = s.StartRestore(context.Background(), "fixture")
	require.Error(t, err)
	s.Stop()
	require.Zero(t, archive.calls.Load())
}

func AccessSaveRecord(s *BackupService, c context.Context, r *BackupRecord) error {
	return s.saveRecord(c, r)
}

func AccessLoadRecords(s *BackupService, c context.Context) ([]BackupRecord, error) {
	return s.loadRecords(c)
}

func AccessLoadS3(s *BackupService, c context.Context) (*BackupS3Config, error) {
	return s.loadS3Config(c)
}

func AccessRecover(s *BackupService) { s.recoverStaleRecords() }

func AccessWait(s *BackupService) { s.wg.Wait() }

func AccessBackingUp(s *BackupService) { s.opMu.Lock(); s.backingUp = true; s.opMu.Unlock() }

func AccessArchive(s *BackupService) ArchiveExecutor { return s.archive }
