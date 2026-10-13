package provider

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"
)

// An Antigravity missing-project error is recovered through five ordered
// managed-refresh writes. Every step compares credential identity and error
// text; window, quota-scope, model-limit and temporary-pause steps additionally
// compare the state each clears. The expected version advances after each
// applied update. A conflict, cancellation or store error ends later steps;
// production autocommit keeps earlier successful writes.
//
// The final read compares credential identity and recovery-owned state while
// permitting unrelated Extra changes. Runtime cleanup is then conditional on
// the sampled mutation generation. That generation represents the unblocker's
// tracked changes, so its implementation defines which later publications
// invalidate the fence. These checks preserve separate persistence and runtime
// outcomes: a false completion can follow already-applied primary steps, and
// the returned current record can expose a later conflicting state.

// Recovery step order follows error, global windows, quota scopes, model limits
// and temporary pause so expected state advances between each conditional
// update.
const (
	ManagedRecoveryError       ManagedRecoveryStep = iota // error status cleanup
	ManagedRecoveryRateLimit                              // global window cleanup
	ManagedRecoveryQuotaScopes                            // platform scope cleanup
	ManagedRecoveryModelLimits                            // model limit cleanup
	ManagedRecoveryTemporary                              // temporary pause cleanup
)

// ErrManagedRecoveryUnavailable reports absent conditional persistence or
// runtime-fence support before managed refresh can safely select its cleanup
// sequence.
var ErrManagedRecoveryUnavailable = errors.New("managed refresh conditional recovery is not configured")

// ManagedRecoveryStep identifies one ordered conditional health-cleanup
// operation during managed refresh.
type ManagedRecoveryStep uint8

// ManagedRecoveryVersion holds observed credential identity and recovery-owned
// health for conditional updates.
type ManagedRecoveryVersion struct {
	// UsageObservationVersion captures identity and global health inputs through
	// ObserveUsageVersion. That helper copies direct proxy, parent and window
	// pointers plus supported credential collections; unsupported nested values
	// remain shared.
	UsageObservationVersion        // identity and global observations
	ErrorMessage            string `json:"-"` // observed error diagnostic
	// Until is a copied temporary deadline compared with the stored pause during
	// its cleanup step; Advance clears it after that step applies.
	Until  *time.Time `json:"-"`
	Reason string     `json:"-"` // observed pause diagnostic
	// Extra is copied through CloneValues; unsupported mutable values remain
	// shared, so callers keep them stable throughout recovery. Matches compares only
	// model_rate_limits and antigravity_quota_scopes, while
	// Advance removes the applied owned key from this expectation.
	Extra map[string]any `json:"-"`
}

// ManagedRecoveryWriter supplies conditional primary cleanup using the
// advancing observed recovery version.
type ManagedRecoveryWriter interface {
	// ApplyManagedRecoveryStep applies the chosen cleanup while its observed
	// identity, error text and step-owned state match. False reports a conflict;
	// errors report failed work, and the caller advances expectation only after
	// true. Earlier production calls can remain committed.
	ApplyManagedRecoveryStep(context.Context, ManagedRecoveryStep, ManagedRecoveryVersion) (bool, error)
}

// ManagedRecoveryUnblocker conditions runtime cleanup on a sampled provider
// mutation generation.
type ManagedRecoveryUnblocker interface {
	// ManagedRecoveryFence reads the runtime mutation generation for the provider
	// before primary recovery starts. Pass that exact value to conditional cleanup
	// afterward; the implementation defines which mutations advance its
	// generation.
	ManagedRecoveryFence(int64) uint64
	// ClearProviderSchedulingBlockIfFence clears runtime state when its mutation
	// generation still equals expected. False reports a mismatch or invalid
	// target; callers retain the already completed primary recovery outcome.
	//
	// Production RuntimeBlockState serializes comparison and clear under the
	// provider lock, then advances its generation. Its clear includes
	// credential-failure entries, cooldown and retry state. The fence protects
	// tracked generation changes, so callers use the unblocker's own mutation
	// rules instead of assuming every possible publication advances it.
	ClearProviderSchedulingBlockIfFence(int64, uint64) bool
}

// ManagedCredentialRecovery supplies the conditional Antigravity
// missing-project cleanup selected by managed refresh.
type ManagedCredentialRecovery interface {
	// ClearManagedRefreshError attempts the observed Antigravity missing-project
	// recovery and reports completion separately from its returned record. False
	// can follow partial primary cleanup, and errors can leave earlier successful
	// production steps applied.
	ClearManagedRefreshError(context.Context, *Record) (*Record, bool, error)
}

// ObserveManagedRecovery captures identity and recovery-owned state from a
// usable nonnil record. Direct observation time pointers and supported
// credential/Extra collections are copied through their helpers; unsupported
// mutable values retain CloneValues sharing and require stable inputs.
func ObserveManagedRecovery(value *Record) ManagedRecoveryVersion {
	return ManagedRecoveryVersion{
		UsageObservationVersion: ObserveUsageVersion(value), ErrorMessage: value.ErrorMessage,
		Until: clonePointer(value.TempUnschedulableUntil), Reason: value.TempUnschedulableReason, Extra: CloneValues(value.Extra),
	}
}

// Advance updates the expected version after the selected step reports applied.
// Call in recovery order on a usable version; scoped steps remove their owned
// Extra key, unknown steps make no change, and this mutates the expectation
// only.
func (v *ManagedRecoveryVersion) Advance(step ManagedRecoveryStep) {
	switch step {
	case ManagedRecoveryError:
		v.Status, v.ErrorMessage = StatusActive, ""
	case ManagedRecoveryRateLimit:
		v.RateLimitedAt, v.RateLimitResetAt, v.OverloadUntil = nil, nil, nil
	case ManagedRecoveryQuotaScopes:
		delete(v.Extra, "antigravity_quota_scopes")
	case ManagedRecoveryModelLimits:
		delete(v.Extra, "model_rate_limits")
	case ManagedRecoveryTemporary:
		v.Until, v.Reason = nil, ""
	}
}

// Matches compares the observed credential identity and advancing recovery
// state with a current record. Nil or changed credential versions fail. Supply
// stable records and expectation values through this synchronous comparison.
//
// Only the two recovery-owned Extra keys participate, with presence and
// reflect.DeepEqual checked independently. Other Extra keys are excluded from
// the remaining struct comparison so unrelated configuration can change. The
// observed global fields, error text and temporary deadline/reason still need
// exact equality. The remaining struct also compares captured credential map
// representations with reflect.DeepEqual. This is a final read check; it
// supplies no lock across
// earlier primary writes and later runtime cleanup.
func (v ManagedRecoveryVersion) Matches(value *Record) bool {
	if value == nil || !MatchesCredentialVersion(value, v.CredentialVersion) {
		return false
	}
	current := ObserveManagedRecovery(value)
	// Compare owned scope keys before excluding Extra from the final struct
	// comparison. This retains the health comparison while allowing unrelated
	// Extra keys to advance between recovery calls.
	for _, key := range []string{"antigravity_quota_scopes", "model_rate_limits"} {
		want, hasWant := v.Extra[key]
		got, hasGot := current.Extra[key]
		if hasWant != hasGot || !reflect.DeepEqual(want, got) {
			return false
		}
	}
	current.Extra, v.Extra = nil, nil
	return reflect.DeepEqual(current, v)
}

// ClearManagedRefreshError attempts ordered conditional cleanup for an errored
// Antigravity missing-project record. Other or nil records return false without
// work. The conditional writer is required; a configured RuntimeBlocker also
// supplies the fence methods. Missing required capabilities return
// ErrManagedRecoveryUnavailable before primary cleanup begins.
//
// Capture the runtime fence, apply five steps and advance expectation only
// after each true result. Cancellation, error or conflict stops later steps
// while earlier successful production writes may remain. A final matching read
// then admits conditional runtime clearing when a blocker is configured;
// mismatch or changed generation returns false. Completion true confirms that
// final decision. A nonnil record returned with false comes from the final read
// after primary cleanup and can expose later changes.
func (s *Admin) ClearManagedRefreshError(ctx context.Context, value *Record) (*Record, bool, error) {
	if value == nil || value.Platform != PlatformAntigravity || value.Status != StatusError || !strings.Contains(value.ErrorMessage, "missing_project_id:") {
		return nil, false, nil
	}
	writer, ok := s.providerRepo.(ManagedRecoveryWriter)
	if !ok {
		return nil, false, ErrManagedRecoveryUnavailable
	}
	var blocker ManagedRecoveryUnblocker
	var fence uint64
	if s.options.RuntimeBlocker != nil {
		blocker, ok = s.options.RuntimeBlocker.(ManagedRecoveryUnblocker)
		if !ok {
			return nil, false, ErrManagedRecoveryUnavailable
		}
		fence = blocker.ManagedRecoveryFence(value.ID)
	}
	// Apply each conditional step against the state left by its predecessor.
	// Advance only after true so a conflict cannot let later cleanup use an
	// expectation that storage never accepted.
	expected := ObserveManagedRecovery(value)
	for step := ManagedRecoveryError; step <= ManagedRecoveryTemporary; step++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		applied, err := writer.ApplyManagedRecoveryStep(ctx, step, expected)
		if err != nil || !applied {
			return nil, false, err
		}
		expected.Advance(step)
	}
	// Primary steps are already applied before this final read and runtime fence
	// check. A later mismatch retains their store outcomes while leaving
	// conditional runtime completion unconfirmed.
	current, err := s.providerRepo.GetByID(ctx, value.ID)
	if err != nil || !expected.Matches(current) {
		return current, false, err
	}
	if blocker != nil && !blocker.ClearProviderSchedulingBlockIfFence(value.ID, fence) {
		return current, false, nil
	}
	return current, true, nil
}
