package logging

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// The slog adapter is checked with a capture core retaining accepted zap
// fields in serial test-owned storage. It observes a component attribute and
// rejects an extra time field, demonstrating that zap's entry metadata
// remains separate from slog.Record attributes.

// captureState retains accepted fixture writes for a serial assertion pass.
type captureState struct {
	writes []capturedWrite // serially appended accepted entries
}

// capturedWrite stores one accepted fixture entry's copied field slice.
type capturedWrite struct {
	fields []zapcore.Field // captured bound and entry fields
}

// captureCore shares serial write state while deriving independently extended bound fields.
type captureCore struct {
	// state remains shared across With-derived cores; the fixture's callers
	// serialize Write and inspection because this shared slice has no mutex.
	state      *captureState
	withFields []zapcore.Field // fixture-bound structured entry fields
}

// newCaptureCore constructs an empty serial capture core with shared write
// state.
func newCaptureCore() *captureCore {
	return &captureCore{state: &captureState{}}
}

// Enabled accepts every severity so this fixture can observe all checked
// records.
func (c *captureCore) Enabled(zapcore.Level) bool {
	return true
}

// With derives a fixture core with copied bound-field slice storage and
// shared write state. Callers use the fixture serially; writes from all
// derived cores append to that same unsynchronized state.
func (c *captureCore) With(fields []zapcore.Field) zapcore.Core {
	nextFields := make([]zapcore.Field, 0, len(c.withFields)+len(fields))
	nextFields = append(nextFields, c.withFields...)
	nextFields = append(nextFields, fields...)
	return &captureCore{
		state:      c.state,
		withFields: nextFields,
	}
}

// Check registers this fixture core so zap executes its accepted record
// writes.
func (c *captureCore) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	return ce.AddCore(entry, c)
}

// Write copies bound and entry field slices into one retained fixture
// observation. It appends to serial test-owned state and returns success;
// mutable values inside captured fields remain shared with their supplied
// values.
func (c *captureCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	allFields := make([]zapcore.Field, 0, len(c.withFields)+len(fields))
	allFields = append(allFields, c.withFields...)
	allFields = append(allFields, fields...)
	c.state.writes = append(c.state.writes, capturedWrite{
		fields: allFields,
	})
	return nil
}

// Sync reports successful fixture synchronization because captured writes use
// memory only.
func (c *captureCore) Sync() error {
	return nil
}

// TestSlogZapHandler_Handle_DoesNotAppendTimeField handles a record
// containing a component attribute and supplied event time. It requires one
// captured write with that component and no extra time attribute from the
// adapter.
func TestSlogZapHandler_Handle_DoesNotAppendTimeField(t *testing.T) {
	core := newCaptureCore()
	handler := newSlogZapHandler(zap.New(core))

	record := slog.NewRecord(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), slog.LevelInfo, "hello", 0)
	record.AddAttrs(slog.String("component", "http.access"))

	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("handle slog record: %v", err)
	}
	if len(core.state.writes) != 1 {
		t.Fatalf("write calls = %d, want 1", len(core.state.writes))
	}

	var hasComponent bool
	for _, field := range core.state.writes[0].fields {
		if field.Key == "time" {
			t.Fatalf("unexpected duplicate time field in slog adapter output")
		}
		if field.Key == "component" {
			hasComponent = true
		}
	}
	if !hasComponent {
		t.Fatalf("component field should be preserved")
	}
}
