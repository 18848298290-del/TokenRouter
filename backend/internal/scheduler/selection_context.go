package scheduler

import "context"

// Auxiliary requests can choose providers while leaving request slots and
// session registration untouched. Selection markers travel with the request
// context. A separate marker lets advanced selection temporarily escape an
// unhealthy sticky provider while retaining its stored binding.

// Selection context keys keep auxiliary selection and retained-affinity
// markers separate from ordinary request metadata.
type (
	// selectOnlyKey identifies slot-free selection at the scheduler call
	// site.
	selectOnlyKey struct{}
	// preservedStickyKey identifies request-scoped retention of the
	// existing provider binding.
	preservedStickyKey struct{}
)

// WithSelectOnly marks ctx for provider selection that skips slot and session
// acquisition. Nil ctx uses a background context, and derived contexts retain
// the marker for nested selection calls.
func WithSelectOnly(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, selectOnlyKey{}, true)
}

// IsSelectOnly reports whether ctx contains the slot-free selection marker.
// Nil contexts and absent or differently typed values return false, allowing
// ordinary requests to use the usual resource acquisition path.
func IsSelectOnly(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(selectOnlyKey{}).(bool)
	return v
}

// WithPreservedSticky marks ctx to retain the existing sticky provider binding
// during selection. Nil ctx uses a background context; the request can still
// select another eligible provider for this attempt.
func WithPreservedSticky(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, preservedStickyKey{}, true)
}

// PreserveStickyFromContext reports whether selection should retain the
// existing provider binding. Nil contexts and missing or differently typed
// markers return false, leaving the ordinary sticky update policy to callers.
func PreserveStickyFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(preservedStickyKey{}).(bool)
	return v
}
