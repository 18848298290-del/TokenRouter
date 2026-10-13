package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Session bindings derive a fingerprint from the HTTP middleware's client IP
// and User-Agent. Both strings are trimmed and separated by a newline before
// hashing, so the digest combines the network and client observations in one
// value. The stored representation is lowercase hex for the first sixteen
// SHA-256 bytes, with an empty value reserved for an absent or wholly empty
// binding.
//
// Context helpers retain the binding pointer supplied by middleware. Session
// issuance records its hash, and renewal compares it when settings enable
// binding. Their own rules decide whether a missing stored or current digest permits
// the request. Fingerprint construction therefore supplies the compared value;
// account validation and family revocation remain with SessionService.

// ErrSessionBindingMismatch identifies a rejected comparison between the current
// HTTP client fingerprint and the binding recorded for an enabled session.
var ErrSessionBindingMismatch = infraerrors.Unauthorized("SESSION_BINDING_MISMATCH", "session network fingerprint changed, please login again")

// SessionBinding contains the client IP and User-Agent captured by HTTP middleware.
// Authentication compares its hash when session binding is enabled and a binding is present.
type SessionBinding struct {
	IP        string // middleware-resolved client IP
	UserAgent string // received user agent text
}

// SessionBindingCtxKey identifies the client binding stored in request contexts.
type SessionBindingCtxKey struct{}

// Hash returns the session fingerprint's hexadecimal digest or an empty string
// for a nil binding or two empty trimmed fields. It combines the trimmed IP and
// User-Agent with a newline before hashing them together.
//
// The first sixteen SHA-256 bytes become thirty-two lowercase hexadecimal
// characters for token and refresh-state comparison. This formatting is shared
// by issuance and renewal callers; changing the separator, trimming or digest
// width changes the value those callers compare with existing session state.
func (b *SessionBinding) Hash() string {
	if b == nil {
		return ""
	}
	ip := strings.TrimSpace(b.IP)
	ua := strings.TrimSpace(b.UserAgent)
	if ip == "" && ua == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(ip + "\n" + ua))
	// Keep sixteen digest bytes for the stored fingerprint.
	return hex.EncodeToString(sum[:16])
}

// WithSessionBinding attaches the supplied client binding pointer to ctx.
// Nil bindings return ctx unchanged; non-nil bindings are shared with subsequent
// readers, so the caller keeps their fields stable while the request uses them.
// Attaching a non-nil binding requires a non-nil parent context.
func WithSessionBinding(ctx context.Context, binding *SessionBinding) context.Context {
	if binding == nil {
		return ctx
	}
	return context.WithValue(ctx, SessionBindingCtxKey{}, binding)
}

// SessionBindingFromContext returns the shared client binding pointer from ctx.
// Nil contexts, absent values and values of another type return nil; callers
// reading a supplied binding observe the object attached by middleware.
func SessionBindingFromContext(ctx context.Context) *SessionBinding {
	if ctx == nil {
		return nil
	}
	binding, _ := ctx.Value(SessionBindingCtxKey{}).(*SessionBinding)
	return binding
}

// SessionBindingHashFromContext computes the current context binding's fingerprint.
// Missing bindings return an empty string through Hash, allowing token issuance
// and renewal to apply their own rules for requests lacking client observations.
func SessionBindingHashFromContext(ctx context.Context) string {
	return SessionBindingFromContext(ctx).Hash()
}
