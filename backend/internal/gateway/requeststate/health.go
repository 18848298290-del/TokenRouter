package requeststate

import (
	"context"
	"strings"
)

// FirstHealthModel trims the first supplied model, returning empty for an empty slice.
func FirstHealthModel(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

// WithHealthModel sets a derived health hint; an empty first model returns ctx unchanged, including nil.
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

// HealthModel prefers the first supplied model and otherwise reads the attempt hint.
func HealthModel(ctx context.Context, models []string) string {
	if model := FirstHealthModel(models); model != "" {
		return model
	}
	if ctx == nil {
		return ""
	}
	return strings.TrimSpace(ExecutionHintsFromContext(ctx).HealthModel)
}

// HealthThinking returns an independent bool pointer, or nil when the hint is absent.
func HealthThinking(ctx context.Context) *bool {
	if value, ok := ThinkingEnabledFromContext(ctx); ok {
		return &value
	}
	return nil
}
