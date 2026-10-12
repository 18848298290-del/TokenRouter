package logging

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Logger option tests isolate DATA_DIR through test-scoped environment
// replacement and compare explicit, data-directory, and container paths.
// Normalization checks numeric/output defaults while retaining a nonblank
// invalid level, and an impossible file directory demonstrates the builder's
// setup error separately from logger-level recovery.

// TestResolveLogFilePath_Default clears DATA_DIR and supplies no explicit
// path. It requires the container default filename, protecting the final
// path-selection result when configuration provides neither earlier choice.
func TestResolveLogFilePath_Default(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	got := resolveLogFilePath("")
	if got != DefaultContainerLogPath {
		t.Fatalf("resolveLogFilePath() = %q, want %q", got, DefaultContainerLogPath)
	}
}

// TestResolveLogFilePath_WithDataDir supplies DATA_DIR and no explicit file
// path. It requires the logs directory and fixed filename beneath that
// directory, protecting environment-based path selection.
func TestResolveLogFilePath_WithDataDir(t *testing.T) {
	t.Setenv("DATA_DIR", "/tmp/tokenrouter-data")
	got := resolveLogFilePath("")
	want := filepath.Join("/tmp/tokenrouter-data", "logs", "tokenrouter.log")
	if got != want {
		t.Fatalf("resolveLogFilePath() = %q, want %q", got, want)
	}
}

// TestResolveLogFilePath_ExplicitPath supplies both DATA_DIR and an explicit
// log filename. It requires the explicit path unchanged, protecting its
// precedence over the environment-selected log directory.
func TestResolveLogFilePath_ExplicitPath(t *testing.T) {
	t.Setenv("DATA_DIR", "/tmp/ignore")
	got := resolveLogFilePath("/var/log/custom.log")
	if got != "/var/log/custom.log" {
		t.Fatalf("resolveLogFilePath() = %q, want explicit path", got)
	}
}

// TestNormalizedOptions_InvalidFallback supplies nonblank invalid text and
// invalid numeric/output settings. It requires lowercase retained level text,
// stdout and path defaults, bounded rotation defaults, and positive enabled
// sampling counts.
func TestNormalizedOptions_InvalidFallback(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	opts := InitOptions{
		Level:           "TRACE",
		Format:          "TEXT",
		ServiceName:     "",
		Environment:     "",
		StacktraceLevel: "panic",
		Output: OutputOptions{
			ToStdout: false,
			ToFile:   false,
		},
		Rotation: RotationOptions{
			MaxSizeMB:  0,
			MaxBackups: -1,
			MaxAgeDays: -1,
		},
		Sampling: SamplingOptions{
			Enabled:    true,
			Initial:    0,
			Thereafter: 0,
		},
	}
	out := opts.normalized()
	if out.Level != "trace" {
		// Normalization trims and lowercases the level; it does not validate allowed values.
		t.Fatalf("normalized level should preserve value for upstream validation, got %q", out.Level)
	}
	if !out.Output.ToStdout {
		t.Fatalf("normalized output should fallback to stdout")
	}
	if out.Output.FilePath != DefaultContainerLogPath {
		t.Fatalf("normalized file path = %q", out.Output.FilePath)
	}
	if out.Rotation.MaxSizeMB != 100 {
		t.Fatalf("normalized max_size_mb = %d", out.Rotation.MaxSizeMB)
	}
	if out.Rotation.MaxBackups != 10 {
		t.Fatalf("normalized max_backups = %d", out.Rotation.MaxBackups)
	}
	if out.Rotation.MaxAgeDays != 7 {
		t.Fatalf("normalized max_age_days = %d", out.Rotation.MaxAgeDays)
	}
	if out.Sampling.Initial != 100 || out.Sampling.Thereafter != 100 {
		t.Fatalf("normalized sampling defaults invalid: %+v", out.Sampling)
	}
}

// TestBuildFileCore_InvalidPathFallback asks the file-core builder to create
// a directory beneath os.DevNull. It requires a setup error from that
// builder, keeping directory failure observable before logger-level recovery
// selects other outputs.
func TestBuildFileCore_InvalidPathFallback(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	opts := bootstrapOptions()
	opts.Output.ToFile = true
	opts.Output.FilePath = filepath.Join(os.DevNull, "logs", "tokenrouter.log")
	encoderCfg := zapcore.EncoderConfig{
		TimeKey:     "time",
		LevelKey:    "level",
		MessageKey:  "msg",
		EncodeTime:  zapcore.ISO8601TimeEncoder,
		EncodeLevel: zapcore.CapitalLevelEncoder,
	}
	encoder := zapcore.NewJSONEncoder(encoderCfg)
	_, _, err := buildFileCore(encoder, zap.NewAtomicLevel(), opts)
	if err == nil {
		t.Fatalf("buildFileCore() expected error for invalid path")
	}
}
