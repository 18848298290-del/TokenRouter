package requeststate

import (
	"context"
	"strings"
)

type guardianParentAffinityKey struct{}

// GuardianParentAffinity holds the current and legacy parent-session hashes for this request.
type GuardianParentAffinity struct{ CurrentSessionHash, LegacySessionHash string }

func WithGuardianParentAffinity(ctx context.Context, value GuardianParentAffinity) context.Context {
	return context.WithValue(ctx, guardianParentAffinityKey{}, value)
}

func GuardianParentAffinityFromContext(ctx context.Context) (GuardianParentAffinity, bool) {
	if ctx == nil {
		return GuardianParentAffinity{}, false
	}
	value, ok := ctx.Value(guardianParentAffinityKey{}).(GuardianParentAffinity)
	return value, ok && value.CurrentSessionHash != ""
}

// PreserveGuardianParentBinding reports whether a hash belongs to the parent and should be protected from child rebinding.
func PreserveGuardianParentBinding(ctx context.Context, hash string) bool {
	value, ok := GuardianParentAffinityFromContext(ctx)
	if !ok {
		return false
	}
	hash = strings.TrimSpace(hash)
	return hash != "" && (hash == value.CurrentSessionHash || hash == value.LegacySessionHash)
}
