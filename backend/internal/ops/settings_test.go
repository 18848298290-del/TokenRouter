package ops

import (
	"context"
	"encoding/json"
	"testing"
)

// fakeCleanupReloader records a synchronous settings callback and returns its
// configured error.
type fakeCleanupReloader struct {
	// calls and last record each Reload before err is returned. These fields are
	// unsynchronized and owned by one serial settings fixture, so tests do not
	// invoke them concurrently.
	calls int             // observed reload invocation count
	last  context.Context // latest supplied callback context
	err   error           // configured reload return error
}

// Reload records its invocation count and supplied context before returning the
// fixture error. It does not inspect cancellation or rebuild a scheduler;
// settings tests use the observation to verify callback invocation.
func (f *fakeCleanupReloader) Reload(ctx context.Context) error {
	f.calls++
	f.last = ctx
	return f.err
}

// TestUpdateOpsAdvancedSettings_TriggersReload saves enabled cleanup with
// supplied cadence and retention preferences. It protects one reloader
// invocation when the advanced-settings update returns successfully in the
// serial fixture.
func TestUpdateOpsAdvancedSettings_TriggersReload(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	reloader := &fakeCleanupReloader{}
	svc := &OpsService{settingRepo: repo}
	svc.SetCleanupReloader(reloader)

	cfg := defaultOpsAdvancedSettings()
	cfg.DataRetention.CleanupEnabled = true
	cfg.DataRetention.CleanupSchedule = "0 * * * *"
	cfg.DataRetention.ErrorLogRetentionDays = 3
	cfg.DataRetention.MinuteMetricsRetentionDays = 3
	cfg.DataRetention.HourlyMetricsRetentionDays = 3

	if _, err := svc.UpdateOpsAdvancedSettings(context.Background(), cfg); err != nil {
		t.Fatalf("update: %v", err)
	}
	if reloader.calls != 1 {
		t.Fatalf("expected reloader.Reload called once, got %d", reloader.calls)
	}
}

// TestUpdateOpsAdvancedSettings_NilReloader_NoPanic updates retention with no
// cleanup callback bound. It protects successful return when the optional
// reloader is absent from the service.
func TestUpdateOpsAdvancedSettings_NilReloader_NoPanic(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}
	// cleanupReloader intentionally nil

	cfg := defaultOpsAdvancedSettings()
	cfg.DataRetention.ErrorLogRetentionDays = 7

	if _, err := svc.UpdateOpsAdvancedSettings(context.Background(), cfg); err != nil {
		t.Fatalf("update with nil reloader: %v", err)
	}
}

// TestGetOpsAdvancedSettings_DefaultSnapshotHidesOpenAITokenStats reads a
// service without initialized runtime snapshot. It protects default visibility,
// exclusions, cleanup pacing, and absence of synchronous repository reads on
// that path.
func TestGetOpsAdvancedSettings_DefaultSnapshotHidesOpenAITokenStats(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}

	cfg, err := svc.GetOpsAdvancedSettings(context.Background())
	if err != nil {
		t.Fatalf("GetOpsAdvancedSettings() error = %v", err)
	}
	if cfg.DisplayOpenAITokenStats {
		t.Fatalf("DisplayOpenAITokenStats = true, want false by default")
	}
	if !cfg.DisplayAlertEvents {
		t.Fatalf("DisplayAlertEvents = false, want true by default")
	}
	if got := cfg.IgnoredStatusCodes; len(got) != 2 || got[0] != 401 || got[1] != 403 {
		t.Fatalf("IgnoredStatusCodes = %#v, want [401 403]", got)
	}
	if cfg.DataRetention.CleanupSchedule != "0 3 * * *" {
		t.Fatalf("CleanupSchedule = %q, want 0 3 * * *", cfg.DataRetention.CleanupSchedule)
	}
	if cfg.DataRetention.CleanupBatchSize != 1000 || cfg.DataRetention.CleanupPauseMS != 200 {
		t.Fatalf("cleanup tuning = %d/%d, want 1000/200", cfg.DataRetention.CleanupBatchSize, cfg.DataRetention.CleanupPauseMS)
	}
	if repo.getValueCalls != 0 || repo.getMultipleCalls != 0 {
		t.Fatalf("hot-path snapshot read touched repository: get=%d get_multiple=%d", repo.getValueCalls, repo.getMultipleCalls)
	}
}

// TestUpdateOpsAdvancedSettings_PersistsOpenAITokenStatsVisibility changes the two
// display flags and reads the resulting published snapshot. It protects
// returned preferences and subsequent cached visibility without another
// repository read.
func TestUpdateOpsAdvancedSettings_PersistsOpenAITokenStatsVisibility(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}

	cfg := defaultOpsAdvancedSettings()
	cfg.DisplayOpenAITokenStats = true
	cfg.DisplayAlertEvents = false

	updated, err := svc.UpdateOpsAdvancedSettings(context.Background(), cfg)
	if err != nil {
		t.Fatalf("UpdateOpsAdvancedSettings() error = %v", err)
	}
	if !updated.DisplayOpenAITokenStats {
		t.Fatalf("DisplayOpenAITokenStats = false, want true")
	}
	if updated.DisplayAlertEvents {
		t.Fatalf("DisplayAlertEvents = true, want false")
	}
	readsAfterUpdate := repo.getValueCalls + repo.getMultipleCalls

	reloaded, err := svc.GetOpsAdvancedSettings(context.Background())
	if err != nil {
		t.Fatalf("GetOpsAdvancedSettings() after update error = %v", err)
	}
	if !reloaded.DisplayOpenAITokenStats {
		t.Fatalf("reloaded DisplayOpenAITokenStats = false, want true")
	}
	if reloaded.DisplayAlertEvents {
		t.Fatalf("reloaded DisplayAlertEvents = true, want false")
	}
	if got := repo.getValueCalls + repo.getMultipleCalls; got != readsAfterUpdate {
		t.Fatalf("snapshot reload performed repository read: before=%d after=%d", readsAfterUpdate, got)
	}
}

// TestGetOpsAdvancedSettings_BackfillsNewDisplayFlagsFromDefaults stores legacy
// JSON in the fixture but leaves the runtime snapshot uninitialized. It
// protects fallback defaults for display, exclusions, and cleanup tuning; this
// call does not load that legacy JSON.
func TestGetOpsAdvancedSettings_BackfillsNewDisplayFlagsFromDefaults(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}

	legacyCfg := map[string]any{
		"data_retention": map[string]any{
			"cleanup_enabled":               false,
			"cleanup_schedule":              "0 2 * * *",
			"error_log_retention_days":      30,
			"minute_metrics_retention_days": 30,
			"hourly_metrics_retention_days": 30,
		},
		"aggregation": map[string]any{
			"aggregation_enabled": false,
		},
		"ignore_count_tokens_errors":    true,
		"ignore_context_canceled":       true,
		"ignore_no_available_providers": false,
		"ignore_invalid_api_key_errors": true,
		"auto_refresh_enabled":          false,
		"auto_refresh_interval_seconds": 30,
	}
	// Serialize the legacy fixture into repository storage without initializing or
	// refreshing the runtime snapshot. The following getter still exercises the
	// snapshot-absent default branch.
	raw, err := json.Marshal(legacyCfg)
	if err != nil {
		t.Fatalf("marshal legacy config: %v", err)
	}
	repo.values[SettingKeyOpsAdvancedSettings] = string(raw)

	cfg, err := svc.GetOpsAdvancedSettings(context.Background())
	if err != nil {
		t.Fatalf("GetOpsAdvancedSettings() error = %v", err)
	}
	if cfg.DisplayOpenAITokenStats {
		t.Fatalf("DisplayOpenAITokenStats = true, want false default backfill")
	}
	if !cfg.DisplayAlertEvents {
		t.Fatalf("DisplayAlertEvents = false, want true default backfill")
	}
	if got := cfg.IgnoredStatusCodes; len(got) != 2 || got[0] != 401 || got[1] != 403 {
		t.Fatalf("IgnoredStatusCodes = %#v, want default backfill [401 403]", got)
	}
	if cfg.DataRetention.CleanupBatchSize != 1000 || cfg.DataRetention.CleanupPauseMS != 200 {
		t.Fatalf("legacy cleanup tuning = %d/%d, want default backfill 1000/200", cfg.DataRetention.CleanupBatchSize, cfg.DataRetention.CleanupPauseMS)
	}
}

// TestUpdateOpsAdvancedSettings_NormalizesIgnoredStatusCodes supplies duplicate
// and reversed authentication exclusions. It protects sorted distinct status
// codes in the successful returned preferences after validation and
// normalization.
func TestUpdateOpsAdvancedSettings_NormalizesIgnoredStatusCodes(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}

	cfg := defaultOpsAdvancedSettings()
	cfg.IgnoredStatusCodes = []int{403, 401, 401}

	updated, err := svc.UpdateOpsAdvancedSettings(context.Background(), cfg)
	if err != nil {
		t.Fatalf("UpdateOpsAdvancedSettings() error = %v", err)
	}
	if got := updated.IgnoredStatusCodes; len(got) != 2 || got[0] != 401 || got[1] != 403 {
		t.Fatalf("IgnoredStatusCodes = %#v, want normalized [401 403]", got)
	}
}

// TestUpdateOpsAdvancedSettings_AllowsEmptyIgnoredStatusCodes supplies an
// allocated empty exclusion policy. It protects acceptance and retained empty
// output, distinguishing deliberate absence of exclusions from the default nil
// policy.
func TestUpdateOpsAdvancedSettings_AllowsEmptyIgnoredStatusCodes(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}

	cfg := defaultOpsAdvancedSettings()
	cfg.IgnoredStatusCodes = []int{}

	updated, err := svc.UpdateOpsAdvancedSettings(context.Background(), cfg)
	if err != nil {
		t.Fatalf("UpdateOpsAdvancedSettings() error = %v", err)
	}
	if len(updated.IgnoredStatusCodes) != 0 {
		t.Fatalf("IgnoredStatusCodes = %#v, want empty", updated.IgnoredStatusCodes)
	}
}
