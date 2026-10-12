package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// ErrSessionBindingMismatch rejects a request whose fingerprint differs from its session binding.
var ErrSessionBindingMismatch = infraerrors.Unauthorized("SESSION_BINDING_MISMATCH", "session network fingerprint changed, please login again")

// SessionBinding contains the client IP and User-Agent captured by HTTP middleware.
// Authentication compares its hash when session binding is enabled and a binding is present.
type SessionBinding struct {
	IP        string
	UserAgent string
}

type SessionBindingCtxKey struct{}

// Hash returns a truncated SHA-256 digest of the trimmed IP and User-Agent, or empty for an absent binding.
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
	return hex.EncodeToString(sum[:16])
}

// WithSessionBinding adds the HTTP client fingerprint to ctx when binding is non-nil.
func WithSessionBinding(ctx context.Context, binding *SessionBinding) context.Context {
	if binding == nil {
		return ctx
	}
	return context.WithValue(ctx, SessionBindingCtxKey{}, binding)
}

// SessionBindingFromContext returns the client fingerprint or nil when absent.
func SessionBindingFromContext(ctx context.Context) *SessionBinding {
	if ctx == nil {
		return nil
	}
	binding, _ := ctx.Value(SessionBindingCtxKey{}).(*SessionBinding)
	return binding
}

// SessionBindingHashFromContext returns the fingerprint hash or an empty string when absent.
func SessionBindingHashFromContext(ctx context.Context) string {
	return SessionBindingFromContext(ctx).Hash()
}
