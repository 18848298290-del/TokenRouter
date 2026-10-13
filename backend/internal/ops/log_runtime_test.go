package ops

import (
	"testing"
)

// TestDefaultNormalizeAndValidateRuntimeLogConfig supplies uppercase deployment
// levels, sampling limits, and separate retention targets. It checks runtime
// defaults use system-log retention and normalization fills the specified
// text/numeric omissions before validation succeeds.
func TestDefaultNormalizeAndValidateRuntimeLogConfig(t *testing.T) {
	defaults := defaultOpsRuntimeLogConfig(&Options{
		Log: LogOptions{
			Level:           "DEBUG",
			Caller:          false,
			StacktraceLevel: "FATAL",
			Sampling: SamplingOptions{
				Enabled:    true,
				Initial:    50,
				Thereafter: 20,
			},
		},
		Ops: RuntimeOptions{
			Cleanup: CleanupOptions{
				ErrorLogRetentionDays:  7,
				SystemLogRetentionDays: 11,
			},
		},
	})
	if defaults.Level != "debug" || defaults.StacktraceLevel != "fatal" || defaults.RetentionDays != 11 {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}

	cfg := &OpsRuntimeLogConfig{
		Level:           " ",
		EnableSampling:  true,
		SamplingInitial: 0,
		SamplingNext:    -1,
		Caller:          true,
		StacktraceLevel: "",
		RetentionDays:   0,
	}
	normalizeOpsRuntimeLogConfig(cfg, defaults)
	if cfg.Level != "debug" || cfg.StacktraceLevel != "fatal" {
		t.Fatalf("normalize level/stacktrace failed: %+v", cfg)
	}
	if cfg.SamplingInitial != 50 || cfg.SamplingNext != 20 || cfg.RetentionDays != 11 {
		t.Fatalf("normalize numeric defaults failed: %+v", cfg)
	}
	if err := validateOpsRuntimeLogConfig(cfg); err != nil {
		t.Fatalf("validate normalized config should pass: %v", err)
	}
}

// TestValidateRuntimeLogConfigErrors supplies nil configuration, unsupported
// levels, nonpositive sampling limits, and zero retention. It checks each
// listed invalid input returns a validation error.
func TestValidateRuntimeLogConfigErrors(t *testing.T) {
	cases := []struct {
		name string               // invalid configuration case name
		cfg  *OpsRuntimeLogConfig // submitted invalid settings fixture
	}{
		{name: "nil", cfg: nil},
		{name: "bad level", cfg: &OpsRuntimeLogConfig{Level: "trace", StacktraceLevel: "error", SamplingInitial: 1, SamplingNext: 1, RetentionDays: 1}},
		{name: "bad stack", cfg: &OpsRuntimeLogConfig{Level: "info", StacktraceLevel: "warn", SamplingInitial: 1, SamplingNext: 1, RetentionDays: 1}},
		{name: "bad initial", cfg: &OpsRuntimeLogConfig{Level: "info", StacktraceLevel: "error", SamplingInitial: 0, SamplingNext: 1, RetentionDays: 1}},
		{name: "bad next", cfg: &OpsRuntimeLogConfig{Level: "info", StacktraceLevel: "error", SamplingInitial: 1, SamplingNext: 0, RetentionDays: 1}},
		{name: "bad retention", cfg: &OpsRuntimeLogConfig{Level: "info", StacktraceLevel: "error", SamplingInitial: 1, SamplingNext: 1, RetentionDays: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateOpsRuntimeLogConfig(tc.cfg); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

// TestNormalizeRuntimeLogConfigNilInputs calls normalization with a nil
// configuration and then nil defaults. Completion protects the no-op nil guards
// used by configuration construction; this test makes no mutation assertion.
func TestNormalizeRuntimeLogConfigNilInputs(t *testing.T) {
	normalizeOpsRuntimeLogConfig(nil, &OpsRuntimeLogConfig{Level: "info"})
	normalizeOpsRuntimeLogConfig(&OpsRuntimeLogConfig{Level: "debug"}, nil)
}
