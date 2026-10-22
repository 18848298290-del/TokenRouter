package logging

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logevent"
)

// logger.go constructs, publishes, and reconfigures structured loggers and
// their output bridges.
// Structured logging splits accepted lower-severity entries to stdout and
// warning-or-higher entries to stderr, with optional rotating file output.
// Initialization normalizes settings and builds output cores before
// publishing the logger, then rebinds slog and standard log bridges.
// File-core construction failure is reported directly to stderr, and
// surviving cores or the final stdout core keep logging available.
//
// Reconfiguration is serialized with level and option state, while plain and
// sugared logger pointers are published in separate atomic stores. Sink
// forwarding is attached after the output sampler and loads the current sink
// for each accepted write, allowing an existing logger to use a replaced
// sink. Rotating file writers remain process-owned across logger changes
// until CloseFiles runs after producers stop.

// Level aliases and context/event keys let callers use zap severities and
// shared telemetry values without owning logger construction or sink implementation.
const (
	LevelDebug = zapcore.DebugLevel // debug log severity
	LevelInfo  = zapcore.InfoLevel  // information log severity
	LevelWarn  = zapcore.WarnLevel  // warning log severity
	LevelError = zapcore.ErrorLevel // error log severity
	LevelFatal = zapcore.FatalLevel // fatal log severity

	// OpsSystemLogSkipField marks events that the Ops database sink should skip indexing.
	OpsSystemLogSkipField = logevent.OpsSystemLogSkipField

	loggerContextKey contextKey = "ctx_logger"
)

// Configuration uses mu while global and sugar publish separate logger pointers.
// currentSink stores one sinkState concrete type and remains independently replaceable.
// atomicLevel and initOptions track configuration, stdLogUndo restores process
// legacy logging, and bootstrapOnce limits bootstrap initialization to one attempt.
var (
	mu            sync.RWMutex
	global        atomic.Pointer[zap.Logger]
	sugar         atomic.Pointer[zap.SugaredLogger]
	atomicLevel   zap.AtomicLevel
	initOptions   InitOptions
	currentSink   atomic.Value // sinkState
	stdLogUndo    func()
	bootstrapOnce sync.Once
)

// Level aliases zap's severity type for logger callers and adapters.
type Level = zapcore.Level

// Sink accepts structured events after output admission for application-owned processing.
type Sink interface {
	// WriteLogEvent accepts a published event and may retain it for processing.
	// Implementations support concurrent writes and own their queue, startup, flushing,
	// and shutdown behavior; published mutable values remain stable for readers.
	WriteLogEvent(event *LogEvent)
}

// LogEvent aliases the shared structured event representation consumed by application sinks.
type LogEvent = logevent.LogEvent

// sinkState provides one atomic.Value publication type even when its sink is nil.
type sinkState struct {
	sink Sink // optional published event consumer
}

// sinkCore adds current-sink forwarding to checked writes from a wrapped output core.
type sinkCore struct {
	core zapcore.Core // wrapped accepted output core
	// fields retain bound attributes with copied slice storage on derived cores.
	// Mutable reflected values can remain shared and stay stable after publication.
	fields []zapcore.Field
}

// stdLogBridge maps legacy text writes to one retained structured logger.
type stdLogBridge struct {
	logger *zap.Logger // retained bridge output logger
}

// contextKey separates logger context lookup from ordinary string-keyed request values.
type contextKey string

// InitBootstrap attempts bootstrap logger initialization once through
// sync.Once. It reports an initialization error directly to stderr; later
// calls share the completed once state rather than retrying that bootstrap
// attempt.
func InitBootstrap() {
	bootstrapOnce.Do(func() {
		if err := Init(bootstrapOptions()); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "logger bootstrap init failed: %v\n", err)
		}
	})
}

// Init serializes logger construction and publication using options, then
// rebinds process slog and standard log outputs. Previously returned logger
// pointers remain usable; file writers are retained until process-level
// cleanup rather than closed during replacement.
func Init(options InitOptions) error {
	mu.Lock()
	defer mu.Unlock()
	return initLocked(options)
}

// initLocked builds normalized output cores before replacing global logger
// state while the caller holds mu. It publishes plain and sugared pointers
// separately, updates level/options, then installs slog and standard-log
// bridges for the replacement.
//
// The previous logger is synchronized after publication and its sync error is
// discarded. Registered file writers remain owned across reconfiguration for
// later CloseFiles. Each atomic pointer is safe to load individually, but a
// reader loading both during replacement may observe different generations.
func initLocked(options InitOptions) error {
	normalized := options.normalized()
	zl, al, err := buildLogger(normalized)
	if err != nil {
		return err
	}

	// Publish constructed replacements before rebinding bridges, then flush the
	// preceding logger. Separate pointer stores do not make multi-accessor reads one snapshot.
	prev := global.Load()
	global.Store(zl)
	sugar.Store(zl.Sugar())
	atomicLevel = al
	initOptions = normalized

	bridgeSlogLocked()
	bridgeStdLogLocked()

	if prev != nil {
		// Prior output may have buffered bytes after replacement. Attempt its flush
		// while retaining registered file ownership, and leave sync failure unreported here.
		_ = prev.Sync()
	}
	return nil
}

// Reconfigure copies current options under mu and passes the copy to mutator
// when present. A mutator error keeps current logger state; otherwise it
// rebuilds and publishes through initLocked. Mutators must not reenter logger
// configuration methods taking mu.
func Reconfigure(mutator func(*InitOptions) error) error {
	mu.Lock()
	defer mu.Unlock()
	next := initOptions
	if mutator != nil {
		if err := mutator(&next); err != nil {
			return err
		}
	}
	return initLocked(next)
}

// CurrentLevel returns the configured atomic severity under the
// reconfiguration read lock. Before a global logger has been published it
// reports info; existing logger copies can retain their own earlier level
// enabler.
func CurrentLevel() string {
	mu.RLock()
	defer mu.RUnlock()
	if global.Load() == nil {
		return "info"
	}
	return atomicLevel.Level().String()
}

// SetSink atomically replaces the sink used by accepted log writes, with nil
// disabling forwarding. Existing logger cores load the sink on each write;
// callers manage the sink's startup, shutdown, and concurrent WriteLogEvent
// behavior.
func SetSink(sink Sink) {
	currentSink.Store(sinkState{sink: sink})
}

// loadSink returns the currently published sink, or nil when no usable sink
// state is stored. The sinkState wrapper permits nil sink publication while
// atomic.Value keeps one concrete stored type.
func loadSink() Sink {
	v := currentSink.Load()
	if v == nil {
		return nil
	}
	state, ok := v.(sinkState)
	if !ok {
		return nil
	}
	return state.sink
}

// L returns the published structured logger, or a no-op logger before
// initialization. Returned loggers can outlive later reconfiguration; callers
// use the accessor again when they need the currently published instance.
func L() *zap.Logger {
	if l := global.Load(); l != nil {
		return l
	}
	return zap.NewNop()
}

// S returns the published sugared logger, or a no-op sugared logger before
// initialization. It is stored separately from L's pointer, so combined
// accessor reads during reconfiguration need not identify one generation.
func S() *zap.SugaredLogger {
	if s := sugar.Load(); s != nil {
		return s
	}
	return zap.NewNop().Sugar()
}

// With returns the current structured logger with the supplied fields
// attached.
func With(fields ...zap.Field) *zap.Logger {
	return L().With(fields...)
}

// Sync attempts to flush the currently published logger and discards its
// error. It is a no-op before initialization; callers stop log producers
// before final flushing and subsequent CloseFiles cleanup.
func Sync() {
	l := global.Load()
	if l != nil {
		// This convenience API has no error result. Attempt the current output
		// flush here, then let CloseFiles report failures from its separate resource
		// cleanup after producers have stopped.
		_ = l.Sync()
	}
}

// bridgeStdLogLocked restores the preceding standard-log writer, prefix, and
// flags before installing the replacement bridge. The caller holds mu, and
// the replacement uses the current logger named stdlog with standard
// formatting disabled.
//
// The saved undo closure captures the restored process configuration for the
// next reconfiguration. This prevents successive Init calls from treating a
// previously installed bridge as the external original logger and retaining a
// chain of older outputs.
func bridgeStdLogLocked() {
	if stdLogUndo != nil {
		stdLogUndo()
		stdLogUndo = nil
	}

	prevFlags := log.Flags()
	prevPrefix := log.Prefix()
	prevWriter := log.Writer()

	log.SetFlags(0)
	log.SetPrefix("")
	base := global.Load()
	if base == nil {
		base = zap.NewNop()
	}
	log.SetOutput(newStdLogBridge(base.Named("stdlog")))

	stdLogUndo = func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
		log.SetPrefix(prevPrefix)
	}
}

// bridgeSlogLocked replaces the process slog default with an adapter for the
// current logger named slog. The caller holds mu; missing global state
// selects a no-op logger for the installed handler.
func bridgeSlogLocked() {
	base := global.Load()
	if base == nil {
		base = zap.NewNop()
	}
	slog.SetDefault(slog.New(newSlogZapHandler(base.Named("slog"))))
}

// buildLogger constructs encoded output cores and returns the logger with its
// level control. Invalid severity text selects info, console selects its
// encoder, and other format text selects JSON; configured file-directory
// failures are reported to stderr.
//
// After selecting surviving output cores, it adds stdout when none exist,
// applies optional sampling, then attaches current-sink forwarding. Caller
// and stacktrace options govern zap metadata, and service/environment fields
// bind every entry. File ownership is registered during core creation for
// process cleanup rather than reconfiguration cleanup.
func buildLogger(options InitOptions) (*zap.Logger, zap.AtomicLevel, error) {
	level, _ := parseLevel(options.Level)
	atomic := zap.NewAtomicLevelAt(level)

	encoderCfg := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.MillisDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	var enc zapcore.Encoder
	if options.Format == "console" {
		enc = zapcore.NewConsoleEncoder(encoderCfg)
	} else {
		enc = zapcore.NewJSONEncoder(encoderCfg)
	}

	sinkCore := newSinkCore()
	cores := make([]zapcore.Core, 0, 3)

	if options.Output.ToStdout {
		infoPriority := zap.LevelEnablerFunc(func(lvl zapcore.Level) bool {
			return lvl >= atomic.Level() && lvl < zapcore.WarnLevel
		})
		errPriority := zap.LevelEnablerFunc(func(lvl zapcore.Level) bool {
			return lvl >= atomic.Level() && lvl >= zapcore.WarnLevel
		})
		cores = append(cores, zapcore.NewCore(enc, zapcore.Lock(os.Stdout), infoPriority))
		cores = append(cores, zapcore.NewCore(enc, zapcore.Lock(os.Stderr), errPriority))
	}

	if options.Output.ToFile {
		fileCore, filePath, fileErr := buildFileCore(enc, atomic, options)
		if fileErr != nil {
			// Report file setup failure directly before this logger exists. Keep
			// surviving output cores, or let the later empty-core guard select stdout.
			_, _ = fmt.Fprintf(os.Stderr, "time=%s level=WARN msg=\"Failed to initialize file logging, using standard output only\" path=%s err=%v\n",
				time.Now().Format(time.RFC3339Nano),
				filePath,
				fileErr,
			)
		} else {
			cores = append(cores, fileCore)
		}
	}

	// A file-only selection can fail during directory setup. Supply stdout when
	// no core survived so the constructed logger still has an output destination.
	if len(cores) == 0 {
		cores = append(cores, zapcore.NewCore(enc, zapcore.Lock(os.Stdout), atomic))
	}

	core := zapcore.NewTee(cores...)
	if options.Sampling.Enabled {
		core = zapcore.NewSamplerWithOptions(core, samplingTick(), options.Sampling.Initial, options.Sampling.Thereafter)
	}
	core = sinkCore.Wrap(core)

	stacktraceLevel, _ := parseStacktraceLevel(options.StacktraceLevel)
	zapOpts := make([]zap.Option, 0, 5)
	if options.Caller {
		zapOpts = append(zapOpts, zap.AddCaller())
	}
	if stacktraceLevel <= zapcore.FatalLevel {
		zapOpts = append(zapOpts, zap.AddStacktrace(stacktraceLevel))
	}

	logger := zap.New(core, zapOpts...).With(
		zap.String("service", options.ServiceName),
		zap.String("env", options.Environment),
	)
	return logger, atomic, nil
}

// buildFileCore creates the target directory and a lumberjack output core,
// returning its resolved path with any directory error. The rotating writer
// opens its file lazily on write; successful construction registers ownership
// for later CloseFiles.
func buildFileCore(enc zapcore.Encoder, atomic zap.AtomicLevel, options InitOptions) (zapcore.Core, string, error) {
	filePath := options.Output.FilePath
	if strings.TrimSpace(filePath) == "" {
		filePath = resolveLogFilePath("")
	}

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, filePath, err
	}
	lj := &lumberjack.Logger{
		Filename:   filePath,
		MaxSize:    options.Rotation.MaxSizeMB,
		MaxBackups: options.Rotation.MaxBackups,
		MaxAge:     options.Rotation.MaxAgeDays,
		Compress:   options.Rotation.Compress,
		LocalTime:  options.Rotation.LocalTime,
	}
	ownFileWriter(lj)
	return zapcore.NewCore(enc, zapcore.AddSync(lj), atomic), filePath, nil
}

// newSinkCore constructs an unbound forwarding core for a later Wrap call.
func newSinkCore() *sinkCore {
	return &sinkCore{}
}

// Wrap returns a shallow forwarding-core copy using core as its underlying
// output. A usable core is required before Check, Enabled, Write, or Sync can
// participate in logger operation.
func (s *sinkCore) Wrap(core zapcore.Core) zapcore.Core {
	cp := *s
	cp.core = core
	return &cp
}

// Enabled delegates severity admission to the wrapped zap output core.
func (s *sinkCore) Enabled(level zapcore.Level) bool {
	return s.core.Enabled(level)
}

// With returns a derived forwarding core with copied bound-field slice
// storage and the underlying core's derived fields. Mutable values inside
// fields remain shared, so callers leave published field contents stable.
func (s *sinkCore) With(fields []zapcore.Field) zapcore.Core {
	nextFields := append([]zapcore.Field{}, s.fields...)
	nextFields = append(nextFields, fields...)
	return &sinkCore{
		core:   s.core.With(fields),
		fields: nextFields,
	}
}

// Check lets the wrapped core register accepted writes and adds sink
// forwarding when a checked entry is present. Underlying output cores retain
// their own registrations, so forwarding does not write their output again.
func (s *sinkCore) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	// Underlying outputs and the sampler decide admission first. Register forwarding
	// on the resulting checked entry so accepted entries reach the current sink.
	ce = s.core.Check(entry, ce)
	if ce != nil {
		ce = ce.AddCore(entry, s)
	}
	return ce
}

// Write converts bound and entry fields into a new map and forwards an event
// to the current sink when present. Entry fields follow bound fields during
// encoding, so duplicate names can overwrite earlier map values. Sink calls
// run synchronously here and must support concurrent logger writes.
func (s *sinkCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	// Output cores already registered their writes in Check. Load the current
	// sink here so an existing logger observes sink replacement for this accepted entry.
	sink := loadSink()
	if sink == nil {
		return nil
	}

	enc := zapcore.NewMapObjectEncoder()
	for _, f := range s.fields {
		f.AddTo(enc)
	}
	for _, f := range fields {
		f.AddTo(enc)
	}

	event := &LogEvent{
		Time:       entry.Time,
		Level:      strings.ToLower(entry.Level.String()),
		Component:  entry.LoggerName,
		Message:    entry.Message,
		LoggerName: entry.LoggerName,
		Fields:     enc.Fields,
	}
	sink.WriteLogEvent(event)
	return nil
}

// Sync returns the wrapped core's output synchronization result to zap
// callers. Forwarded sinks retain their own flush and shutdown lifecycle,
// managed separately by the application holding that sink.
func (s *sinkCore) Sync() error {
	return s.core.Sync()
}

// newStdLogBridge creates a writer using l for normalized standard-log
// messages. Nil l selects a no-op logger; its retained logger remains fixed
// until process reconfiguration installs another bridge.
func newStdLogBridge(l *zap.Logger) io.Writer {
	if l == nil {
		l = zap.NewNop()
	}
	return &stdLogBridge{logger: l}
}

// Write normalizes p and emits a message through the retained logger using
// inferred severity and a legacy_stdlog marker. Blank content is accepted
// silently, and the returned count acknowledges all input bytes regardless of
// downstream output errors.
func (b *stdLogBridge) Write(p []byte) (int, error) {
	msg := normalizeStdLogMessage(string(p))
	if msg == "" {
		return len(p), nil
	}

	level := inferStdLogLevel(msg)
	entry := b.logger.WithOptions(zap.AddCallerSkip(4))

	switch level {
	case LevelDebug:
		entry.Debug(msg, zap.Bool("legacy_stdlog", true))
	case LevelWarn:
		entry.Warn(msg, zap.Bool("legacy_stdlog", true))
	case LevelError, LevelFatal:
		entry.Error(msg, zap.Bool("legacy_stdlog", true))
	default:
		entry.Info(msg, zap.Bool("legacy_stdlog", true))
	}
	return len(p), nil
}

// normalizeStdLogMessage joins whitespace-separated message text into one
// trimmed line. Newlines become spaces and empty input returns empty text,
// giving printf and standard-log callers one normalized message for severity
// inference.
func normalizeStdLogMessage(raw string) string {
	msg := strings.TrimSpace(strings.ReplaceAll(raw, "\n", " "))
	if msg == "" {
		return ""
	}
	return strings.Join(strings.Fields(msg), " ")
}

// inferStdLogLevel selects severity from normalized message prefixes, then
// ordered error and warning substrings. Explicit prefixes override words in
// the message and interpolated values. Empty or unmatched text selects info;
// error hints win over warning hints and fatal/panic text is logged as error.
func inferStdLogLevel(msg string) Level {
	lower := strings.ToLower(strings.TrimSpace(msg))
	if lower == "" {
		return LevelInfo
	}

	if strings.HasPrefix(lower, "[debug]") || strings.HasPrefix(lower, "debug:") {
		return LevelDebug
	}
	if strings.HasPrefix(lower, "[info]") || strings.HasPrefix(lower, "info:") {
		return LevelInfo
	}
	if strings.HasPrefix(lower, "[warn]") || strings.HasPrefix(lower, "[warning]") || strings.HasPrefix(lower, "warn:") || strings.HasPrefix(lower, "warning:") {
		return LevelWarn
	}
	if strings.HasPrefix(lower, "[error]") || strings.HasPrefix(lower, "error:") || strings.HasPrefix(lower, "fatal:") || strings.HasPrefix(lower, "panic:") {
		return LevelError
	}

	if strings.Contains(lower, " failed") || strings.Contains(lower, "error") || strings.Contains(lower, "panic") || strings.Contains(lower, "fatal") {
		return LevelError
	}
	if strings.Contains(lower, "warning") || strings.Contains(lower, "warn") || strings.Contains(lower, " queue full") || strings.Contains(lower, "fallback") {
		return LevelWarn
	}
	return LevelInfo
}

// LegacyPrintf formats and normalizes one message, ignoring empty output.
// Before initialization it uses standard log; afterward it adds an optional
// component attribute and legacy_printf marker to the inferred severity, with
// caller skip pointing at its consumer.
func LegacyPrintf(component, format string, args ...any) {
	msg := normalizeStdLogMessage(fmt.Sprintf(format, args...))
	if msg == "" {
		return
	}

	initialized := global.Load() != nil
	if !initialized {
		// Early callers can log before structured initialization. Send their normalized
		// message through standard log so its existing process output remains available.
		log.Print(msg)
		return
	}

	l := L()
	if component != "" {
		l = l.With(zap.String("component", component))
	}
	l = l.WithOptions(zap.AddCallerSkip(1))

	switch inferStdLogLevel(msg) {
	case LevelDebug:
		l.Debug(msg, zap.Bool("legacy_printf", true))
	case LevelWarn:
		l.Warn(msg, zap.Bool("legacy_printf", true))
	case LevelError, LevelFatal:
		l.Error(msg, zap.Bool("legacy_printf", true))
	default:
		l.Info(msg, zap.Bool("legacy_printf", true))
	}
}

// IntoContext stores l under the private logger key, using background for nil
// ctx. Nil l selects the current logger, so later FromContext reads retain
// that instance even after global reconfiguration.
func IntoContext(ctx context.Context, l *zap.Logger) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if l == nil {
		l = L()
	}
	return context.WithValue(ctx, loggerContextKey, l)
}

// FromContext returns a non-nil logger stored in ctx or the current global
// accessor result. Nil contexts, absent values, and unusable typed values use
// L, which supplies a no-op before initialization.
func FromContext(ctx context.Context) *zap.Logger {
	if ctx == nil {
		return L()
	}
	if l, ok := ctx.Value(loggerContextKey).(*zap.Logger); ok && l != nil {
		return l
	}
	return L()
}

// Event logs alternating string keys and values, discarding non-string keys
// and an unmatched trailing item. Exact error and warn levels select those
// methods; all other level text selects debug using the currently published
// logger.
func Event(level, event string, fields ...any) {
	values := make([]zap.Field, 0, len(fields)/2)
	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if ok {
			values = append(values, zap.Any(key, fields[i+1]))
		}
	}
	switch level {
	case "error":
		L().Error(event, values...)
	case "warn":
		L().Warn(event, values...)
	default:
		L().Debug(event, values...)
	}
}
