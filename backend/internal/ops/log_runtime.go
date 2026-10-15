package ops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// log_runtime.go reads and changes effective logging configuration while
// coordinating saved overrides, restoration, and audit callbacks.
// Live logging and its saved runtime override are separate state changes.
// Updates apply the normalized request before persistence, and reset applies
// deployment settings before removing the override. Persistence failure
// triggers a second application attempt using old effective values, but that
// restoration error is suppressed, leaving the caller with the original write
// or deletion failure.
//
// Reading effective settings can backfill an absent key with defaults and
// treats malformed JSON as baseline data. Successfully decoded objects are
// normalized without validation, including bool false values that remain
// supplied. Successful updates and resets emit audit callbacks and reload
// cleanup because retention is shared with that worker; reload failure is
// reported separately from configuration success.

// defaultOpsRuntimeLogConfig returns deployment-derived log settings with
// thirty-day retention unless a positive system-log retention is supplied. Nil
// options use fixed logging defaults. A nonnil options object replaces level,
// caller, stacktrace, and sampling values as supplied, including empty or zero
// values.
//
// Those retained deployment values can themselves require normalization or
// validation before application. The positive retention override uses the
// system-log target, independently of error-log retention. The returned
// configuration is a new scalar object and carries no runtime-setting update
// metadata until a later update or reset assigns it.
func defaultOpsRuntimeLogConfig(cfg *Options) *OpsRuntimeLogConfig {
	out := &OpsRuntimeLogConfig{
		Level:           "info",
		EnableSampling:  false,
		SamplingInitial: 100,
		SamplingNext:    100,
		Caller:          true,
		StacktraceLevel: "error",
		RetentionDays:   30,
	}
	if cfg == nil {
		return out
	}
	out.Level = strings.ToLower(strings.TrimSpace(cfg.Log.Level))
	out.EnableSampling = cfg.Log.Sampling.Enabled
	out.SamplingInitial = cfg.Log.Sampling.Initial
	out.SamplingNext = cfg.Log.Sampling.Thereafter
	out.Caller = cfg.Log.Caller
	out.StacktraceLevel = strings.ToLower(strings.TrimSpace(cfg.Log.StacktraceLevel))
	if cfg.Ops.Cleanup.SystemLogRetentionDays > 0 {
		out.RetentionDays = cfg.Ops.Cleanup.SystemLogRetentionDays
	}
	return out
}

// normalizeOpsRuntimeLogConfig trims and lowercases level strings and fills
// missing text/nonpositive numeric values from defaults in place. Nil
// configuration or defaults is a no-op; bool values and update metadata remain
// supplied, and the helper does not validate replacement defaults.
func normalizeOpsRuntimeLogConfig(cfg *OpsRuntimeLogConfig, defaults *OpsRuntimeLogConfig) {
	if cfg == nil || defaults == nil {
		return
	}
	cfg.Level = strings.ToLower(strings.TrimSpace(cfg.Level))
	if cfg.Level == "" {
		cfg.Level = defaults.Level
	}
	cfg.StacktraceLevel = strings.ToLower(strings.TrimSpace(cfg.StacktraceLevel))
	if cfg.StacktraceLevel == "" {
		cfg.StacktraceLevel = defaults.StacktraceLevel
	}
	if cfg.SamplingInitial <= 0 {
		cfg.SamplingInitial = defaults.SamplingInitial
	}
	if cfg.SamplingNext <= 0 {
		cfg.SamplingNext = defaults.SamplingNext
	}
	if cfg.RetentionDays <= 0 {
		cfg.RetentionDays = defaults.RetentionDays
	}
}

// validateOpsRuntimeLogConfig rejects nil, unsupported logging/stacktrace
// levels, nonpositive sampling counts, or retention outside one through 3,650
// days. It compares normalized text without changing the caller object and
// returns the first violation in that order.
func validateOpsRuntimeLogConfig(cfg *OpsRuntimeLogConfig) error {
	if cfg == nil {
		return errors.New("invalid config")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Level)) {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("level must be one of: debug/info/warn/error")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.StacktraceLevel)) {
	case "none", "error", "fatal":
	default:
		return errors.New("stacktrace_level must be one of: none/error/fatal")
	}
	if cfg.SamplingInitial <= 0 {
		return errors.New("sampling_initial must be positive")
	}
	if cfg.SamplingNext <= 0 {
		return errors.New("sampling_thereafter must be positive")
	}
	// The runtime retention API bounds deletion policy to positive day values up
	// to 3,650.
	if cfg.RetentionDays < 1 || cfg.RetentionDays > 3650 {
		return errors.New("retention_days must be between 1 and 3650")
	}
	return nil
}

// GetRuntimeLogConfig returns the effective stored object or deployment
// baseline, using Background for nil context. Nil service or settings
// repository returns a fresh baseline directly. A recognized missing key
// attempts to store that baseline, suppressing its encoding/write result; other
// read errors are returned.
//
// Malformed stored JSON discards the decoded object and returns the baseline.
// Successfully decoded data, including null, is normalized with baseline text
// and numeric defaults, while false bool fields remain false. The getter does
// not validate stored or baseline values, and it neither applies them to the
// live logger nor refreshes cleanup.
func (s *OpsService) GetRuntimeLogConfig(ctx context.Context) (*OpsRuntimeLogConfig, error) {
	if s == nil || s.settingRepo == nil {
		var cfg *Options
		if s != nil {
			cfg = s.cfg
		}
		defaultCfg := defaultOpsRuntimeLogConfig(cfg)
		return defaultCfg, nil
	}
	defaultCfg := defaultOpsRuntimeLogConfig(s.cfg)
	if ctx == nil {
		ctx = context.Background()
	}

	raw, err := s.settingRepo.GetValue(ctx, SettingKeyOpsRuntimeLogConfig)
	if err != nil {
		// An absent override uses the deployment baseline. Attempt its persistence
		// for later reads, but retain the successful baseline result if that backfill
		// write fails.
		if errors.Is(err, ErrSettingNotFound) {
			b, _ := json.Marshal(defaultCfg)
			_ = s.settingRepo.Set(ctx, SettingKeyOpsRuntimeLogConfig, string(b))
			return defaultCfg, nil
		}
		return nil, err
	}

	cfg := &OpsRuntimeLogConfig{}
	// A malformed override cannot supply a complete effective object. Discard
	// partial decoding and return the already prepared baseline without publishing
	// it to the logger.
	if err := json.Unmarshal([]byte(raw), cfg); err != nil {
		return defaultCfg, nil
	}
	normalizeOpsRuntimeLogConfig(cfg, defaultCfg)
	return cfg, nil
}

// UpdateRuntimeLogConfig validates a nonnil request and positive operator,
// applies its normalized scalar copy to live logging, then persists metadata
// and settings. Missing service/repository fails first, nil context uses
// Background, and reading the old settings can backfill an absent key before
// logging-field validation.
//
// Application failure stops persistence. A later settings-write error attempts
// to reapply the old effective configuration but discards that rollback result,
// so failure does not guarantee restoration of the prior logger. After a
// successful save, change audit is emitted and cleanup reload is attempted;
// reload failure is reported while the completed configuration update remains
// successful.
func (s *OpsService) UpdateRuntimeLogConfig(ctx context.Context, req *OpsRuntimeLogConfig, operatorID int64) (*OpsRuntimeLogConfig, error) {
	if s == nil || s.settingRepo == nil {
		return nil, errors.New("setting repository not initialized")
	}
	if req == nil {
		return nil, errors.New("invalid config")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if operatorID <= 0 {
		return nil, errors.New("invalid operator id")
	}

	oldCfg, err := s.GetRuntimeLogConfig(ctx)
	if err != nil {
		return nil, err
	}
	next := *req
	normalizeOpsRuntimeLogConfig(&next, defaultOpsRuntimeLogConfig(s.cfg))
	if err := validateOpsRuntimeLogConfig(&next); err != nil {
		s.auditRuntimeLogConfigFailure(operatorID, oldCfg, &next, "validation_failed: "+err.Error())
		return nil, err
	}

	// Apply live options before storing the requested override. A successful apply
	// can therefore precede a later settings failure that needs a separate
	// restoration attempt.
	if err := s.applyRuntimeLogConfig(&next); err != nil {
		s.auditRuntimeLogConfigFailure(operatorID, oldCfg, &next, "apply_failed: "+err.Error())
		return nil, err
	}

	next.Source = "runtime_setting"
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	next.UpdatedByUserID = operatorID

	encoded, err := json.Marshal(&next)
	if err != nil {
		return nil, err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyOpsRuntimeLogConfig, string(encoded)); err != nil {
		// The new live settings may already be active. Try the prior effective object
		// after persistence failure; rollback errors are suppressed and the original
		// write error remains the result.
		_ = s.applyRuntimeLogConfig(oldCfg)
		s.auditRuntimeLogConfigFailure(operatorID, oldCfg, &next, "persist_failed: "+err.Error())
		return nil, err
	}

	s.auditRuntimeLogConfigChange(operatorID, oldCfg, &next, "updated")
	// The persisted retention value also feeds cleanup settings. Reload after the
	// save so cleanup can see it, reporting reload failure without undoing the
	// completed update.
	if s.cleanupReloader != nil {
		if reloadErr := s.cleanupReloader.Reload(ctx); reloadErr != nil {
			s.report(
				"[OpsSettings] cleanup reload after runtime-log update failed: %v", reloadErr)
		}
	}

	return &next, nil
}

// ResetRuntimeLogConfig applies the normalized deployment baseline for a
// positive operator, then deletes the stored runtime override. Missing
// service/repository fails, nil context uses Background, and old-setting lookup
// can backfill an absent key before reset proceeds.
//
// Deletion treats the recognized missing-key error as success. Other deletion
// failures attempt old-config reapplication, discard its error, and return the
// persistence failure. Successful reset assigns response-only baseline metadata
// after deletion, emits the change audit, and attempts cleanup reload. Reload
// errors are reported without failing the reset, while the returned metadata is
// not saved as a replacement override.
func (s *OpsService) ResetRuntimeLogConfig(ctx context.Context, operatorID int64) (*OpsRuntimeLogConfig, error) {
	if s == nil || s.settingRepo == nil {
		return nil, errors.New("setting repository not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if operatorID <= 0 {
		return nil, errors.New("invalid operator id")
	}

	oldCfg, err := s.GetRuntimeLogConfig(ctx)
	if err != nil {
		return nil, err
	}

	resetCfg := defaultOpsRuntimeLogConfig(s.cfg)
	normalizeOpsRuntimeLogConfig(resetCfg, defaultOpsRuntimeLogConfig(s.cfg))
	if err := validateOpsRuntimeLogConfig(resetCfg); err != nil {
		s.auditRuntimeLogConfigFailure(operatorID, oldCfg, resetCfg, "reset_validation_failed: "+err.Error())
		return nil, err
	}
	// Apply the deployment baseline before override deletion. If deletion later
	// fails, stored and live settings can disagree until the old-config
	// reapplication is attempted.
	if err := s.applyRuntimeLogConfig(resetCfg); err != nil {
		s.auditRuntimeLogConfigFailure(operatorID, oldCfg, resetCfg, "reset_apply_failed: "+err.Error())
		return nil, err
	}

	// Deleting the override leaves later reads to choose deployment settings.
	// Recognized key absence already meets that goal; another deletion error
	// triggers best-effort live restoration.
	if err := s.settingRepo.Delete(ctx, SettingKeyOpsRuntimeLogConfig); err != nil && !errors.Is(err, ErrSettingNotFound) {
		// The baseline was already applied before this deletion failed. Try restoring
		// old effective settings, but return the deletion error even if restoration
		// also fails.
		_ = s.applyRuntimeLogConfig(oldCfg)
		s.auditRuntimeLogConfigFailure(operatorID, oldCfg, resetCfg, "reset_persist_failed: "+err.Error())
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	resetCfg.Source = "baseline"
	resetCfg.UpdatedAt = now
	resetCfg.UpdatedByUserID = operatorID

	s.auditRuntimeLogConfigChange(operatorID, oldCfg, resetCfg, "reset")
	// Cleanup derives retention from effective settings. Reload after override
	// deletion, keeping the completed reset result when only schedule refresh
	// fails.
	if s.cleanupReloader != nil {
		if reloadErr := s.cleanupReloader.Reload(ctx); reloadErr != nil {
			s.report(
				"[OpsSettings] cleanup reload after runtime-log reset failed: %v", reloadErr)
		}
	}
	return resetCfg, nil
}

// ApplyRuntimeLogConfigOnStartup attempts to read and apply effective log
// settings, doing nothing for nil service. Read and application errors are
// suppressed here; service startup can continue without proving that its stored
// override reached the live core.
func (s *OpsService) ApplyRuntimeLogConfigOnStartup(ctx context.Context) {
	if s == nil {
		return
	}
	cfg, err := s.GetRuntimeLogConfig(ctx)
	// A settings lookup failure ends this startup attempt before applying
	// anything. Lifecycle callers receive no error from this method and continue
	// with the existing logging state.
	if err != nil {
		return
	}
	// Startup accepts a failed live reconfiguration here. The attempt has no
	// returned error, so later diagnostics must observe the actual published
	// logging state.
	_ = s.applyRuntimeLogConfig(cfg)
}

// applyRuntimeLogConfig delegates live settings to the bound LogControl and
// returns its result. An absent control reports success without changing the
// logging core, allowing settings-only service instances to use the same update
// path.
func (s *OpsService) applyRuntimeLogConfig(c *OpsRuntimeLogConfig) error {
	if s.logging == nil {
		return nil
	}
	return s.logging.Apply(c)
}

// auditRuntimeLogConfigChange invokes the bound configuration-change callback
// with operator, old/new settings, and action. Missing logging control is a
// no-op; the callback has no error channel and cannot establish delivery to an
// audit sink.
func (s *OpsService) auditRuntimeLogConfigChange(id int64, old, next *OpsRuntimeLogConfig, action string) {
	if s.logging != nil {
		s.logging.Changed(id, old, next, action)
	}
}

// auditRuntimeLogConfigFailure invokes the bound failure callback with
// attempted settings and diagnostic reason. Missing control is a no-op; the
// void callback does not replace the operation error or prove that its rollback
// succeeded.
func (s *OpsService) auditRuntimeLogConfigFailure(id int64, old, next *OpsRuntimeLogConfig, reason string) {
	if s.logging != nil {
		s.logging.Failed(id, old, next, reason)
	}
}
