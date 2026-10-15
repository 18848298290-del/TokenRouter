package requeststate

import (
	"context"
	"strings"
)

// health.go selects the model and thinking evidence reported for provider
// health observations. Explicit first-model evidence takes precedence over the
// stored hint, and an explicit false thinking hint stays distinguishable from a
// missing hint when the health adapter receives a pointer.
//
// Only the first explicit model participates in selection; a blank first entry
// permits the saved model hint even when later entries are nonblank. Storing a
// model leaves an empty candidate unchanged. Thinking evidence is returned
// through a fresh pointer so a consumer can distinguish absent state from an
// explicit false value without mutating the captured hint.

// FirstHealthModel trims the first model in values, returning empty for a
// missing or blank first entry. Later entries never supply a substitute, so
// callers order model evidence before invoking this helper.
func FirstHealthModel(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

// WithHealthModel stores a nonempty first model in a derived execution-hint
// snapshot. Empty first-model evidence returns ctx unchanged; a nonempty model
// with nil ctx uses Background before storing the hint.
func WithHealthModel(ctx context.Context, models []string) context.Context {
	model := FirstHealthModel(models)
	if model == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return updateHints(ctx, func(value *ExecutionHints) { value.HealthModel = model })
}

// HealthModel returns the nonblank first explicit model or the trimmed attempt
// hint. A missing context produces empty when explicit evidence is absent, and
// later models in the slice are ignored.
func HealthModel(ctx context.Context, models []string) string {
	if model := FirstHealthModel(models); model != "" {
		return model
	}
	if ctx == nil {
		return ""
	}
	return strings.TrimSpace(ExecutionHintsFromContext(ctx).HealthModel)
}

// HealthThinking returns a separately allocated bool for a present thinking
// hint. Missing hints return nil, while explicit false returns a nonnil pointer
// whose mutation leaves the context snapshot unchanged.
func HealthThinking(ctx context.Context) *bool {
	if value, ok := ThinkingEnabledFromContext(ctx); ok {
		return &value
	}
	return nil
}
