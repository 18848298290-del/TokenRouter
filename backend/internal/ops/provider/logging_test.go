package provider

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// SettingKeyOpsRuntimeLogConfig identifies the shared setting read, backfilled,
// updated, and deleted by these runtime logging fixtures.
const SettingKeyOpsRuntimeLogConfig = ops.SettingKeyOpsRuntimeLogConfig

// ErrSettingNotFound supplies the recognized absent-key result used by baseline
// backfill and reset-deletion scenarios in these fixtures.
var ErrSettingNotFound = settings.ErrSettingNotFound

// runtimeSettingRepoStub records settings operations with configured single-key
// hooks and mutable fixture maps.
type runtimeSettingRepoStub struct {
	// values is the mutable fixture store read and written by repository methods.
	// newRuntimeSettingRepoStub allocates it; tests call this stub sequentially
	// because it has no synchronization.
	values map[string]string // fixture stored setting text
	// deleted records keys after the built-in deletion succeeds. Hook errors and
	// absent keys leave that marker map unchanged.
	deleted          map[string]bool // successful deleted-key observation map
	setCalls         int             // successful single-key write count
	getValueCalls    int             // all single-key read count
	getMultipleCalls int             // all batch-read invocation count
	// getValueFn replaces the built-in map lookup when configured, including its
	// error result. getValueCalls still increments before this hook executes.
	getValueFn func(key string) (string, error) // optional single-key read override
	// setFn runs before the built-in map write. A hook error prevents that write
	// and setCalls increment; a nil result permits both.
	setFn func(key, value string) error // optional pre-write failure hook
	// deleteFn runs before built-in presence checking and removal. A hook error
	// returns immediately, leaving the built-in value and marker changes
	// unapplied.
	deleteFn func(key string) error // optional pre-delete failure hook
}

type (
	// OpsService names the core Ops service instantiated by the logging fixtures.
	OpsService = ops.OpsService
	// OpsRuntimeLogConfig names the core runtime settings object supplied to
	// logging operations.
	OpsRuntimeLogConfig = ops.OpsRuntimeLogConfig
	// Setting names the shared stored key/value object returned by the fixture
	// repository.
	Setting = settings.Setting
)

// testServiceInputs supplies repository and deployment options to a
// logging-bound Ops fixture.
type testServiceInputs struct {
	settingRepo ops.Settings // fixture runtime settings repository
	// cfg is retained by the built service rather than copied. Tests keep this
	// deployment object stable while runtime settings and logging callbacks use
	// it.
	cfg *ops.Options // borrowed deployment option object
}

// newRuntimeSettingRepoStub constructs allocated settings and deletion-marker
// maps with zero counters and no hooks. Callers can insert fixture values
// directly and use the stub sequentially for its unsynchronized operation
// tracking.
func newRuntimeSettingRepoStub() *runtimeSettingRepoStub {
	return &runtimeSettingRepoStub{
		values:  map[string]string{},
		deleted: map[string]bool{},
	}
}

// Get wraps GetValue text in a new Setting object when the read succeeds and
// propagates its error otherwise. Its single-key read counter and override
// behavior come from GetValue.
func (s *runtimeSettingRepoStub) Get(ctx context.Context, key string) (*Setting, error) {
	value, err := s.GetValue(ctx, key)
	if err != nil {
		return nil, err
	}
	return &Setting{Key: key, Value: value}, nil
}

// GetValue counts each single-key read before invoking its override when
// configured. Otherwise it returns fixture map text and ErrSettingNotFound for
// a missing key, preserving an existing empty string as a successful value.
func (s *runtimeSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	s.getValueCalls++
	if s.getValueFn != nil {
		return s.getValueFn(key)
	}
	value, ok := s.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

// Set invokes its configured pre-write hook, propagating any error before
// built-in fixture mutation. Success writes the value and increments setCalls;
// batch writes use a separate method and do not affect this counter.
func (s *runtimeSettingRepoStub) Set(_ context.Context, key, value string) error {
	if s.setFn != nil {
		if err := s.setFn(key, value); err != nil {
			return err
		}
	}
	s.values[key] = value
	s.setCalls++
	return nil
}

// GetMultiple increments its call counter and returns a fresh map containing
// requested keys found in values. Missing keys are omitted, and the single-key
// getValueFn hook does not affect batch reads.
func (s *runtimeSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	s.getMultipleCalls++
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

// SetMultiple copies supplied string values into the existing fixture map and
// returns nil. It bypasses the single-key setFn hook and setCalls counter, so
// tests distinguish this batch path from Set.
func (s *runtimeSettingRepoStub) SetMultiple(_ context.Context, settings map[string]string) error {
	maps.Copy(s.values, settings)
	return nil
}

// GetAll returns a new map with the current stored string values and nil error.
// Callers can mutate that top-level result without changing the fixture values
// map.
func (s *runtimeSettingRepoStub) GetAll(_ context.Context) (map[string]string, error) {
	out := make(map[string]string, len(s.values))
	maps.Copy(out, s.values)
	return out, nil
}

// Delete invokes the configured pre-delete hook before checking fixture
// presence. Hook errors or missing keys return without built-in removal;
// success removes the value and records its deleted marker.
func (s *runtimeSettingRepoStub) Delete(_ context.Context, key string) error {
	if s.deleteFn != nil {
		if err := s.deleteFn(key); err != nil {
			return err
		}
	}
	if _, ok := s.values[key]; !ok {
		return ErrSettingNotFound
	}
	delete(s.values, key)
	s.deleted[key] = true
	return nil
}

// TestUpdateRuntimeLogConfig_InvalidConfigShouldNotApply starts an info logger
// and submits an unsupported trace level. It checks validation leaves the live
// level unchanged and records exactly one successful single-key settings write.
func TestUpdateRuntimeLogConfig_InvalidConfigShouldNotApply(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := newTestService(testServiceInputs{
		settingRepo: repo,
		cfg: &ops.Options{
			Log: ops.LogOptions{
				Level:           "info",
				Caller:          true,
				StacktraceLevel: "error",
				Sampling: ops.SamplingOptions{
					Enabled:    false,
					Initial:    100,
					Thereafter: 100,
				},
			},
		},
	})

	if err := logging.Init(logging.InitOptions{
		Level:       "info",
		Format:      "json",
		ServiceName: "tokenrouter",
		Environment: "test",
		Output: logging.OutputOptions{
			ToStdout: true,
			ToFile:   false,
		},
	}); err != nil {
		t.Fatalf("init logger: %v", err)
	}

	_, err := svc.UpdateRuntimeLogConfig(context.Background(), &OpsRuntimeLogConfig{
		Level:           "trace",
		EnableSampling:  true,
		SamplingInitial: 100,
		SamplingNext:    100,
		Caller:          true,
		StacktraceLevel: "error",
		RetentionDays:   30,
	}, 1)
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if logging.CurrentLevel() != "info" {
		t.Fatalf("logger level changed unexpectedly: %s", logging.CurrentLevel())
	}
	if repo.setCalls != 1 {
		// Old-config lookup precedes request validation and backfills this absent
		// key. The rejected trace request therefore leaves the write counter at that
		// one baseline save.
		t.Fatalf("unexpected set calls: %d", repo.setCalls)
	}
}

// TestResetRuntimeLogConfig_ShouldFallbackToBaseline stores debug overrides and
// supplies warn deployment settings with distinct retention targets. It checks
// reset deletes the override, applies warn, returns baseline metadata, and uses
// system-log retention.
func TestResetRuntimeLogConfig_ShouldFallbackToBaseline(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	existing := &OpsRuntimeLogConfig{
		Level:           "debug",
		EnableSampling:  true,
		SamplingInitial: 50,
		SamplingNext:    50,
		Caller:          true,
		StacktraceLevel: "error",
		RetentionDays:   60,
		Source:          "runtime_setting",
	}
	// The fixture settings contain scalar JSON-supported values. Ignore their
	// encoding error so the scenario can prepare a stored override before
	// observing reset behavior.
	raw, _ := json.Marshal(existing)
	repo.values[SettingKeyOpsRuntimeLogConfig] = string(raw)

	svc := newTestService(testServiceInputs{
		settingRepo: repo,
		cfg: &ops.Options{
			Log: ops.LogOptions{
				Level:           "warn",
				Caller:          false,
				StacktraceLevel: "fatal",
				Sampling: ops.SamplingOptions{
					Enabled:    false,
					Initial:    100,
					Thereafter: 100,
				},
			},
			Ops: ops.RuntimeOptions{
				Cleanup: ops.CleanupOptions{
					ErrorLogRetentionDays:  7,
					SystemLogRetentionDays: 45,
				},
			},
		},
	})

	if err := logging.Init(logging.InitOptions{
		Level:       "debug",
		Format:      "json",
		ServiceName: "tokenrouter",
		Environment: "test",
		Output: logging.OutputOptions{
			ToStdout: true,
			ToFile:   false,
		},
	}); err != nil {
		t.Fatalf("init logger: %v", err)
	}

	resetCfg, err := svc.ResetRuntimeLogConfig(context.Background(), 9)
	if err != nil {
		t.Fatalf("ResetRuntimeLogConfig() error: %v", err)
	}
	if resetCfg.Source != "baseline" {
		t.Fatalf("source = %q, want baseline", resetCfg.Source)
	}
	if resetCfg.Level != "warn" {
		t.Fatalf("level = %q, want warn", resetCfg.Level)
	}
	if resetCfg.RetentionDays != 45 {
		t.Fatalf("retention_days = %d, want 45", resetCfg.RetentionDays)
	}
	if logging.CurrentLevel() != "warn" {
		t.Fatalf("logger level = %q, want warn", logging.CurrentLevel())
	}
	if !repo.deleted[SettingKeyOpsRuntimeLogConfig] {
		t.Fatalf("runtime setting key should be deleted")
	}
}

// TestResetRuntimeLogConfig_InvalidOperator requests reset with operator zero
// and a configured settings repository. It checks the operation rejects that
// identity with the exact invalid-operator error.
func TestResetRuntimeLogConfig_InvalidOperator(t *testing.T) {
	svc := newTestService(testServiceInputs{settingRepo: newRuntimeSettingRepoStub()})
	_, err := svc.ResetRuntimeLogConfig(context.Background(), 0)
	if err == nil {
		t.Fatalf("expected invalid operator error")
	}
	if err.Error() != "invalid operator id" {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestGetRuntimeLogConfig_InvalidJSONFallback stores malformed settings text
// and a warn deployment baseline. It checks the read returns the deployment
// warn level with nil error for that malformed override.
func TestGetRuntimeLogConfig_InvalidJSONFallback(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	repo.values[SettingKeyOpsRuntimeLogConfig] = `{invalid-json}`

	svc := newTestService(testServiceInputs{
		settingRepo: repo,
		cfg: &ops.Options{
			Log: ops.LogOptions{
				Level:           "warn",
				Caller:          true,
				StacktraceLevel: "error",
				Sampling: ops.SamplingOptions{
					Enabled:    false,
					Initial:    100,
					Thereafter: 100,
				},
			},
		},
	})

	got, err := svc.GetRuntimeLogConfig(context.Background())
	if err != nil {
		t.Fatalf("GetRuntimeLogConfig() error: %v", err)
	}
	if got.Level != "warn" {
		t.Fatalf("level = %q, want warn", got.Level)
	}
}

// TestUpdateRuntimeLogConfig_PersistFailureRollback starts info logging and
// injects a single-key write error before requesting debug. It checks the
// persistence error remains visible and successful old-config reapplication
// restores the live info level.
func TestUpdateRuntimeLogConfig_PersistFailureRollback(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	oldCfg := &OpsRuntimeLogConfig{
		Level:           "info",
		EnableSampling:  false,
		SamplingInitial: 100,
		SamplingNext:    100,
		Caller:          true,
		StacktraceLevel: "error",
		RetentionDays:   30,
	}
	// The scalar settings fixture supplies the prior effective object. Ignore its
	// JSON encoding error when preparing the saved value used by the rollback
	// scenario.
	raw, _ := json.Marshal(oldCfg)
	repo.values[SettingKeyOpsRuntimeLogConfig] = string(raw)
	repo.setFn = func(key, value string) error {
		if key == SettingKeyOpsRuntimeLogConfig {
			return errors.New("db down")
		}
		return nil
	}

	svc := newTestService(testServiceInputs{
		settingRepo: repo,
		cfg: &ops.Options{
			Log: ops.LogOptions{
				Level:           "info",
				Caller:          true,
				StacktraceLevel: "error",
				Sampling: ops.SamplingOptions{
					Enabled:    false,
					Initial:    100,
					Thereafter: 100,
				},
			},
		},
	})

	if err := logging.Init(logging.InitOptions{
		Level:       "info",
		Format:      "json",
		ServiceName: "tokenrouter",
		Environment: "test",
		Output: logging.OutputOptions{
			ToStdout: true,
			ToFile:   false,
		},
	}); err != nil {
		t.Fatalf("init logger: %v", err)
	}

	_, err := svc.UpdateRuntimeLogConfig(context.Background(), &OpsRuntimeLogConfig{
		Level:           "debug",
		EnableSampling:  false,
		SamplingInitial: 100,
		SamplingNext:    100,
		Caller:          true,
		StacktraceLevel: "error",
		RetentionDays:   30,
	}, 5)
	if err == nil {
		t.Fatalf("expected persist error")
	}
	// The fixture uses a working real logging adapter for restoration. This
	// assertion covers successful rollback after the write error, not every
	// rollback failure path.
	if logging.CurrentLevel() != "info" {
		t.Fatalf("logger level should rollback to info, got %s", logging.CurrentLevel())
	}
}

// TestApplyRuntimeLogConfigOnStartup stores a debug runtime object while the
// live logger begins at info. It checks startup application reaches the stored
// debug level under the real logging adapter.
func TestApplyRuntimeLogConfigOnStartup(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	cfgRaw := `{"level":"debug","enable_sampling":false,"sampling_initial":100,"sampling_thereafter":100,"caller":true,"stacktrace_level":"error","retention_days":30}`
	repo.values[SettingKeyOpsRuntimeLogConfig] = cfgRaw

	svc := newTestService(testServiceInputs{
		settingRepo: repo,
		cfg: &ops.Options{
			Log: ops.LogOptions{
				Level:           "info",
				Caller:          true,
				StacktraceLevel: "error",
				Sampling: ops.SamplingOptions{
					Enabled:    false,
					Initial:    100,
					Thereafter: 100,
				},
			},
		},
	})

	if err := logging.Init(logging.InitOptions{
		Level:       "info",
		Format:      "json",
		ServiceName: "tokenrouter",
		Environment: "test",
		Output: logging.OutputOptions{
			ToStdout: true,
			ToFile:   false,
		},
	}); err != nil {
		t.Fatalf("init logger: %v", err)
	}

	svc.ApplyRuntimeLogConfigOnStartup(context.Background())
	if logging.CurrentLevel() != "debug" {
		t.Fatalf("expected startup apply debug, got %s", logging.CurrentLevel())
	}
}

// TestGetRuntimeLogConfigFallbackAndErrors reads through a nil service, then
// injects a generic repository failure. It checks the nil-service info default
// and propagation of an unrecognized read error.
func TestGetRuntimeLogConfigFallbackAndErrors(t *testing.T) {
	var nilSvc *OpsService
	cfg, err := nilSvc.GetRuntimeLogConfig(context.Background())
	if err != nil {
		t.Fatalf("nil svc should fallback default: %v", err)
	}
	if cfg.Level != "info" {
		t.Fatalf("unexpected nil svc default level: %s", cfg.Level)
	}

	repo := newRuntimeSettingRepoStub()
	repo.getValueFn = func(key string) (string, error) {
		return "", errors.New("boom")
	}
	svc := newTestService(testServiceInputs{
		settingRepo: repo,
		cfg: &ops.Options{
			Log: ops.LogOptions{
				Level:           "warn",
				Caller:          true,
				StacktraceLevel: "error",
				Sampling: ops.SamplingOptions{
					Enabled:    false,
					Initial:    100,
					Thereafter: 100,
				},
			},
		},
	})
	if _, err := svc.GetRuntimeLogConfig(context.Background()); err == nil {
		t.Fatalf("expected get value error")
	}
}

// TestUpdateRuntimeLogConfig_PreconditionErrors supplies missing settings
// storage, a nil request, and operator zero. It checks each listed setup
// violation returns an error from the runtime update entry point.
func TestUpdateRuntimeLogConfig_PreconditionErrors(t *testing.T) {
	svc := newTestService(testServiceInputs{})
	if _, err := svc.UpdateRuntimeLogConfig(context.Background(), &OpsRuntimeLogConfig{}, 1); err == nil {
		t.Fatalf("expected setting repo not initialized")
	}

	svc = newTestService(testServiceInputs{settingRepo: newRuntimeSettingRepoStub()})
	if _, err := svc.UpdateRuntimeLogConfig(context.Background(), nil, 1); err == nil {
		t.Fatalf("expected invalid config")
	}
	if _, err := svc.UpdateRuntimeLogConfig(context.Background(), &OpsRuntimeLogConfig{
		Level:           "info",
		StacktraceLevel: "error",
		SamplingInitial: 1,
		SamplingNext:    1,
		RetentionDays:   1,
	}, 0); err == nil {
		t.Fatalf("expected invalid operator")
	}
}

// TestUpdateRuntimeLogConfig_Success starts an info logger and requests valid
// debug settings. It checks the live debug level and returned source, operator,
// and nonempty update timestamp metadata.
func TestUpdateRuntimeLogConfig_Success(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := newTestService(testServiceInputs{
		settingRepo: repo,
		cfg: &ops.Options{
			Log: ops.LogOptions{
				Level:           "info",
				Caller:          true,
				StacktraceLevel: "error",
				Sampling: ops.SamplingOptions{
					Enabled:    false,
					Initial:    100,
					Thereafter: 100,
				},
			},
		},
	})

	if err := logging.Init(logging.InitOptions{
		Level:       "info",
		Format:      "json",
		ServiceName: "tokenrouter",
		Environment: "test",
		Output: logging.OutputOptions{
			ToStdout: true,
			ToFile:   false,
		},
	}); err != nil {
		t.Fatalf("init logger: %v", err)
	}

	next, err := svc.UpdateRuntimeLogConfig(context.Background(), &OpsRuntimeLogConfig{
		Level:           "debug",
		EnableSampling:  false,
		SamplingInitial: 100,
		SamplingNext:    100,
		Caller:          true,
		StacktraceLevel: "error",
		RetentionDays:   30,
	}, 2)
	if err != nil {
		t.Fatalf("UpdateRuntimeLogConfig() error: %v", err)
	}
	if next.Source != "runtime_setting" || next.UpdatedByUserID != 2 || next.UpdatedAt == "" {
		t.Fatalf("unexpected metadata: %+v", next)
	}
	if logging.CurrentLevel() != "debug" {
		t.Fatalf("expected applied level debug, got %s", logging.CurrentLevel())
	}
}

// TestResetRuntimeLogConfig_IgnoreNotFoundDelete injects ErrSettingNotFound
// from the delete hook after baseline lookup. It checks reset treats recognized
// key absence as success rather than a persistence failure.
func TestResetRuntimeLogConfig_IgnoreNotFoundDelete(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	repo.deleteFn = func(key string) error { return ErrSettingNotFound }
	svc := newTestService(testServiceInputs{
		settingRepo: repo,
		cfg: &ops.Options{
			Log: ops.LogOptions{
				Level:           "info",
				Caller:          true,
				StacktraceLevel: "error",
				Sampling: ops.SamplingOptions{
					Enabled:    false,
					Initial:    100,
					Thereafter: 100,
				},
			},
		},
	})
	if _, err := svc.ResetRuntimeLogConfig(context.Background(), 1); err != nil {
		t.Fatalf("reset should ignore ErrSettingNotFound: %v", err)
	}
}

// TestApplyRuntimeLogConfigHelpers passes nil settings to LogControl.Apply and
// invokes startup application on a nil service. It checks the adapter rejects
// nil configuration while the startup convenience path completes.
func TestApplyRuntimeLogConfigHelpers(t *testing.T) {
	if err := (LogControl{}).Apply(nil); err == nil {
		t.Fatalf("expected nil config error")
	}

	var nilSvc *OpsService
	nilSvc.ApplyRuntimeLogConfigOnStartup(context.Background())
}

// newTestService builds an Ops service with supplied settings and deployment
// options plus the real LogControl adapter. BuildOpsService does not apply
// startup settings. Tests invoke logging operations themselves and stay
// sequential because they reconfigure the process logger.
func newTestService(v testServiceInputs) *OpsService {
	return ops.BuildOpsService(nil, v.settingRepo, v.cfg, nil, nil, nil, nil, LogControl{})
}
