package logging

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Logging starts with console bootstrap defaults, then normalizes configured
// output, rotation, and sampling choices before construction. Blank fields
// receive defaults, while invalid nonblank level and format strings remain
// for the builder's own selection rules; file paths prefer an explicit
// setting, DATA_DIR, then the container path.

// Default file naming supports bootstrap and normalized logger configuration
// before directory creation and the rotating writer's first file write.
const (
	// DefaultContainerLogPath is the default log file path inside the container.
	DefaultContainerLogPath = "/app/data/logs/tokenrouter.log"
	defaultLogFilename      = "tokenrouter.log"
)

// InitOptions selects logger encoding, output, diagnostics, rotation, and sampling behavior.
type InitOptions struct {
	// Level is normalized before construction. buildLogger maps unrecognized
	// nonblank values to info even though normalized preserves their lowercase
	// text.
	Level string // requested minimum severity text
	// Format selects console only when its normalized value equals console. All
	// other nonblank values select the JSON encoder during buildLogger.
	Format          string          // requested output encoding text
	ServiceName     string          // service attribute value text
	Environment     string          // environment attribute value text
	Caller          bool            // include log callsite metadata
	StacktraceLevel string          // requested stacktrace severity text
	Output          OutputOptions   // selected logger output destinations
	Rotation        RotationOptions // rotating file writer settings
	Sampling        SamplingOptions // repeated entry sampling settings
}

// OutputOptions selects standard-stream and rotating-file outputs for logger construction.
type OutputOptions struct {
	// ToStdout and ToFile independently enable output cores. normalized selects
	// ToStdout when both are false; buildLogger also supplies stdout if file
	// construction leaves no cores.
	ToStdout bool   // enable split standard stream output
	ToFile   bool   // enable rotating file output
	FilePath string // configured rotating log path
}

// RotationOptions passes file-size and backup retention choices to lumberjack.
type RotationOptions struct {
	// MaxSizeMB is replaced with 100 when nonpositive during normalization, then
	// lumberjack uses that positive value to bound each active log file.
	MaxSizeMB  int  // file size in MiB
	MaxBackups int  // retained backups; zero keeps all
	MaxAgeDays int  // backup age days; zero unlimited
	Compress   bool // gzip rotated backup files
	LocalTime  bool // local timestamps in backup filenames
}

// SamplingOptions configures zap's per-level and per-message repeated-entry sampling counts.
type SamplingOptions struct {
	// Enabled applies Initial and Thereafter defaults only when true. Sampling
	// runs on the output core before sink forwarding is attached, so accepted
	// entries reach the sink.
	Enabled bool // enable repeated entry sampling
	// Initial and Thereafter select repeated-entry counts within each one-second
	// sampling tick. When sampling is enabled, normalized replaces each
	// nonpositive count with 100.
	Initial    int // initial entries per sampling tick
	Thereafter int // one retained per later count
}

// normalized returns a value copy with trimmed text and defaults for absent
// or invalid numeric options. Both outputs disabled selects stdout,
// nonpositive size selects 100, negative backup/age limits select defaults,
// and enabled sampling replaces nonpositive counts. Nonblank level and format
// validity is left to the builder.
func (o InitOptions) normalized() InitOptions {
	out := o
	out.Level = strings.ToLower(strings.TrimSpace(out.Level))
	if out.Level == "" {
		out.Level = "info"
	}
	out.Format = strings.ToLower(strings.TrimSpace(out.Format))
	if out.Format == "" {
		out.Format = "console"
	}
	out.ServiceName = strings.TrimSpace(out.ServiceName)
	if out.ServiceName == "" {
		out.ServiceName = "tokenrouter"
	}
	out.Environment = strings.TrimSpace(out.Environment)
	if out.Environment == "" {
		out.Environment = "production"
	}
	out.StacktraceLevel = strings.ToLower(strings.TrimSpace(out.StacktraceLevel))
	if out.StacktraceLevel == "" {
		out.StacktraceLevel = "error"
	}
	if !out.Output.ToStdout && !out.Output.ToFile {
		out.Output.ToStdout = true
	}
	out.Output.FilePath = resolveLogFilePath(out.Output.FilePath)
	if out.Rotation.MaxSizeMB <= 0 {
		out.Rotation.MaxSizeMB = 100
	}
	if out.Rotation.MaxBackups < 0 {
		out.Rotation.MaxBackups = 10
	}
	if out.Rotation.MaxAgeDays < 0 {
		out.Rotation.MaxAgeDays = 7
	}
	if out.Sampling.Enabled {
		if out.Sampling.Initial <= 0 {
			out.Sampling.Initial = 100
		}
		if out.Sampling.Thereafter <= 0 {
			out.Sampling.Thereafter = 100
		}
	}
	return out
}

// resolveLogFilePath returns a trimmed explicit path, otherwise
// DATA_DIR/logs/tokenrouter.log or the container default. It performs no
// filesystem validation, so logger construction handles directory creation
// and later file writes.
func resolveLogFilePath(explicit string) string {
	explicit = strings.TrimSpace(explicit)
	if explicit != "" {
		return explicit
	}
	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir != "" {
		return filepath.Join(dataDir, "logs", defaultLogFilename)
	}
	return DefaultContainerLogPath
}

// bootstrapOptions returns console bootstrap settings with stdout enabled and
// file output disabled. It supplies rotation and sampling values for later
// use, while bootstrap logging itself starts with sampling disabled.
func bootstrapOptions() InitOptions {
	return InitOptions{
		Level:       "info",
		Format:      "console",
		ServiceName: "tokenrouter",
		Environment: "bootstrap",
		Output: OutputOptions{
			ToStdout: true,
			ToFile:   false,
		},
		Rotation: RotationOptions{
			MaxSizeMB:  100,
			MaxBackups: 10,
			MaxAgeDays: 7,
			Compress:   true,
			LocalTime:  true,
		},
		Sampling: SamplingOptions{
			Enabled:    false,
			Initial:    100,
			Thereafter: 100,
		},
	}
}

// parseLevel converts trimmed debug, info, warn, or error text to a zap
// level. Unknown input returns info and false so callers can distinguish a
// defaulted result from a recognized selection.
func parseLevel(level string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return LevelDebug, true
	case "info":
		return LevelInfo, true
	case "warn":
		return LevelWarn, true
	case "error":
		return LevelError, true
	default:
		return LevelInfo, false
	}
}

// parseStacktraceLevel maps none, error, and fatal to stacktrace thresholds.
// none selects a value above fatal to disable stacks; other input returns
// error and false for callers needing validation.
func parseStacktraceLevel(level string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "none":
		return LevelFatal + 1, true
	case "error":
		return LevelError, true
	case "fatal":
		return LevelFatal, true
	default:
		return LevelError, false
	}
}

// samplingTick supplies the one-second reset interval for logger sampling
// counts.
func samplingTick() time.Duration {
	return time.Second
}
