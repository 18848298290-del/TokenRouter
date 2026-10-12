package scheduler

import "context"

// selectOnlyKey marks selection-only requests; preservedStickyKey marks bindings to retain.
type (
	selectOnlyKey      struct{}
	preservedStickyKey struct{}
)

func WithSelectOnly(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, selectOnlyKey{}, true)
}

func IsSelectOnly(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(selectOnlyKey{}).(bool)
	return v
}

func WithPreservedSticky(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, preservedStickyKey{}, true)
}

func PreserveStickyFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(preservedStickyKey{}).(bool)
	return v
}
