package logging

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// slog_handler.go converts slog records and bound attributes into zap entries.
// Bound slog groups prefix an outer attribute key with a dotted path, while
// attributes of group kind become nested zap objects. Children of those
// objects are converted without repeating the bound path, so the outer
// object carries that path once and its children retain relative keys.
//
// Derived handlers copy storage for the attribute or group slice they extend
// while retaining the other slice and logger. Mutable attribute values need
// stable contents while handlers read them. Severity maps to debug, info,
// warn, or error, and zap supplies entry time and optional caller metadata;
// slog.Record Time and PC are not installed on the emitted zap entry.

// slogZapHandler binds zap logging with slog attributes and dotted group prefixes.
type slogZapHandler struct {
	logger *zap.Logger // shared structured output logger
	// attrs and groups are treated as immutable on a published handler. Derived
	// handlers copy the slice they extend while retaining the other slice's storage.
	attrs  []slog.Attr // bound structured log attributes
	groups []string    // bound dotted key prefixes
}

// zapObjectFields encodes nested slog attributes through zap's object encoder interface.
type zapObjectFields []zap.Field

// newSlogZapHandler returns a slog handler using logger for accepted records
// and attribute encoding. Nil logger selects a no-op zap logger; bound
// attribute and group slices start empty for derived handlers.
func newSlogZapHandler(logger *zap.Logger) slog.Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &slogZapHandler{
		logger: logger,
		attrs:  make([]slog.Attr, 0, 8),
		groups: make([]string, 0, 4),
	}
}

// Enabled maps slog severity to debug, info, warn, or error and consults the
// zap core. Intermediate levels use the adjacent implemented range; record
// context is unused by this adapter.
func (h *slogZapHandler) Enabled(_ context.Context, level slog.Level) bool {
	switch {
	case level >= slog.LevelError:
		return h.logger.Core().Enabled(LevelError)
	case level >= slog.LevelWarn:
		return h.logger.Core().Enabled(LevelWarn)
	case level <= slog.LevelDebug:
		return h.logger.Core().Enabled(LevelDebug)
	default:
		return h.logger.Core().Enabled(LevelInfo)
	}
}

// Handle converts bound and record attributes and logs the message at its
// mapped zap severity. It leaves record time and PC to zap's entry metadata,
// ignores context, and returns nil after logging; bound groups prefix
// attribute keys.
func (h *slogZapHandler) Handle(_ context.Context, record slog.Record) error {
	fields := make([]zap.Field, 0, len(h.attrs)+record.NumAttrs()+3)
	fields = append(fields, slogAttrsToZapFields(h.groups, h.attrs)...)
	record.Attrs(func(attr slog.Attr) bool {
		fields = append(fields, slogAttrToZapField(h.groups, attr))
		return true
	})

	switch {
	case record.Level >= slog.LevelError:
		h.logger.Error(record.Message, fields...)
	case record.Level >= slog.LevelWarn:
		h.logger.Warn(record.Message, fields...)
	case record.Level <= slog.LevelDebug:
		h.logger.Debug(record.Message, fields...)
	default:
		h.logger.Info(record.Message, fields...)
	}
	return nil
}

// WithAttrs returns a derived handler with copied attribute slice storage
// plus attrs appended. Group storage and the zap logger remain shared;
// mutable attribute values remain caller-owned and need stable contents while
// handlers read them.
func (h *slogZapHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &next
}

// WithGroup trims name and returns the same handler for empty input. Other
// names append to copied group storage on a derived handler, while existing
// attributes and the zap logger remain shared. The added prefix also applies
// to previously bound attributes when Handle encodes the derived handler.
func (h *slogZapHandler) WithGroup(name string) slog.Handler {
	name = strings.TrimSpace(name)
	if name == "" {
		return h
	}
	next := *h
	next.groups = append(append([]string{}, h.groups...), name)
	return &next
}

// slogAttrsToZapFields converts attributes into zap fields using the supplied
// bound groups.
func slogAttrsToZapFields(groups []string, attrs []slog.Attr) []zap.Field {
	fields := make([]zap.Field, 0, len(attrs))
	for _, attr := range attrs {
		fields = append(fields, slogAttrToZapField(groups, attr))
	}
	return fields
}

// slogAttrToZapField resolves an attribute and maps its value to a zap field.
// Bound groups prefix the outer key with dots, while recognized scalar kinds
// use their corresponding typed zap representation. Any values follow zap's
// encoding behavior, with time.Time handled as a time field.
//
// Attribute groups become nested objects. Their children recurse with no
// bound-group prefix, keeping each child inside its owning object instead of
// repeating the outer dotted name at every level.
func slogAttrToZapField(groups []string, attr slog.Attr) zap.Field {
	// Apply the handler's group path to the outer field once. Nested attributes
	// remain relative to the object created from this field's value.
	if len(groups) > 0 {
		attr.Key = strings.Join(append(append([]string{}, groups...), attr.Key), ".")
	}
	value := attr.Value.Resolve()
	switch value.Kind() {
	case slog.KindBool:
		return zap.Bool(attr.Key, value.Bool())
	case slog.KindInt64:
		return zap.Int64(attr.Key, value.Int64())
	case slog.KindUint64:
		return zap.Uint64(attr.Key, value.Uint64())
	case slog.KindFloat64:
		return zap.Float64(attr.Key, value.Float64())
	case slog.KindDuration:
		return zap.Duration(attr.Key, value.Duration())
	case slog.KindTime:
		return zap.Time(attr.Key, value.Time())
	case slog.KindString:
		return zap.String(attr.Key, value.String())
	case slog.KindGroup:
		groupFields := make([]zap.Field, 0, len(value.Group()))
		for _, nested := range value.Group() {
			groupFields = append(groupFields, slogAttrToZapField(nil, nested))
		}
		return zap.Object(attr.Key, zapObjectFields(groupFields))
	case slog.KindAny:
		if t, ok := value.Any().(time.Time); ok {
			return zap.Time(attr.Key, t)
		}
		return zap.Any(attr.Key, value.Any())
	default:
		return zap.String(attr.Key, value.String())
	}
}

// MarshalLogObject adds each retained zap field to enc in slice order. It
// returns nil after field encoding, leaving field-owned reflected values and
// encoder error representation to zapcore.
func (z zapObjectFields) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	for _, field := range z {
		field.AddTo(enc)
	}
	return nil
}
