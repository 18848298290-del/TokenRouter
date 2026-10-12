package requeststate

import (
	"context"
	"strings"
)

// Guardian review requests can reuse their parent session's provider affinity.
// Both current and legacy hashes identify that parent, so selection protects
// matching parent bindings when a child would otherwise delete or replace a
// sticky association.

// guardianParentAffinityKey separates parent-session affinity from other
// request context values.
type guardianParentAffinityKey struct{}

// GuardianParentAffinity holds current and legacy hashes for a validated review
// parent.
type GuardianParentAffinity struct {
	// CurrentSessionHash establishes presence, and LegacySessionHash permits a
	// match under the earlier hashing algorithm. Selection protects either value
	// as the same validated review parent.
	CurrentSessionHash, LegacySessionHash string // current parent-session hash and legacy parent-session hash
}

// WithGuardianParentAffinity stores the supplied parent-session hashes in a
// derived context. Callers provide a nonnil parent and validated lineage;
// storing affinity does not perform authorization or trim the supplied hashes.
func WithGuardianParentAffinity(ctx context.Context, value GuardianParentAffinity) context.Context {
	return context.WithValue(ctx, guardianParentAffinityKey{}, value)
}

// GuardianParentAffinityFromContext returns stored hashes and reports presence
// when CurrentSessionHash is nonempty. Nil contexts and wrong value types
// report absent; a legacy hash alone does not establish a usable parent
// binding.
func GuardianParentAffinityFromContext(ctx context.Context) (GuardianParentAffinity, bool) {
	if ctx == nil {
		return GuardianParentAffinity{}, false
	}
	value, ok := ctx.Value(guardianParentAffinityKey{}).(GuardianParentAffinity)
	return value, ok && value.CurrentSessionHash != ""
}

// PreserveGuardianParentBinding reports whether the trimmed candidate hash
// equals either stored parent hash. The selector uses true to protect that
// sticky binding from child rebinding, and absent parent affinity yields false.
func PreserveGuardianParentBinding(ctx context.Context, hash string) bool {
	value, ok := GuardianParentAffinityFromContext(ctx)
	if !ok {
		return false
	}
	hash = strings.TrimSpace(hash)
	return hash != "" && (hash == value.CurrentSessionHash || hash == value.LegacySessionHash)
}
