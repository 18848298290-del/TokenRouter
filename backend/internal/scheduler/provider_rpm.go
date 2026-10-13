package scheduler

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// Provider RPM gates classify a soft minute observation before a request is
// recorded. Batch observations travel in the request context, and a missing
// prefetched entry can be read individually. A successful individual read
// therefore supplements a partial batch, while failed observations keep a zero
// count.
//
// Allowance is then determined from the provider base, reserve, strategy, and
// request affinity. Unavailable storage does not itself reject a provider, and
// disabled RPM bypasses the count lookup. Since observing and incrementing are
// separate operations, concurrent requests can be admitted from the same
// observed count. This eligibility check supplies the selection decision;
// attempt execution records the later increment.

// ProviderRPMInput holds the provider identity, switch, and reserve policy for
// RPM eligibility.
type ProviderRPMInput struct {
	ID int64 // provider counter identifier
	// Enabled and Base jointly enable the gate: false or a nonpositive
	// base bypasses observation. Base gives the ordinary allowance, and
	// Buffer extends affinity admission for reserve-limited strategies.
	Enabled      bool // RPM eligibility switch
	Base, Buffer int  // base and sticky reserve counts
	// Strategy selects reserve handling. The sticky_exempt value admits
	// existing affinity after the base is reached, irrespective of the
	// Buffer ceiling.
	Strategy string // reserve admission policy
}

// rpmPrefetchKey identifies request-local provider RPM observations stored in
// context values.
type rpmPrefetchKey struct{}

// PrefetchedRPM returns the provider count stored by a batch prefetch and its
// presence. Missing context values or provider entries return false; callers
// supply a non-nil context and may then perform an individual read.
func PrefetchedRPM(ctx context.Context, id int64) (int, bool) {
	if v, ok := ctx.Value(rpmPrefetchKey{}).(map[int64]int); ok {
		count, found := v[id]
		return count, found
	}
	return 0, false
}

// PrefetchRPM attaches a batch observation to ctx when the cache read
// succeeds. A nil cache, empty provider list, or read failure retains ctx,
// leaving downstream eligibility free to request individual counts.
func PrefetchRPM(ctx context.Context, cache RPMCache, ids []int64) context.Context {
	if cache == nil || len(ids) == 0 {
		return ctx
	}
	counts, err := cache.GetRPMBatch(ctx, ids)
	if err != nil {
		// Batch observation failed, so retain the context without a
		// prefetch map. Eligibility can still try individual counters
		// for the providers it inspects.
		return ctx
	}
	return context.WithValue(ctx, rpmPrefetchKey{}, counts)
}

// AllowProviderRPM classifies the observed provider count and applies the
// request affinity flag. Disabled RPM or a nonpositive base admits
// immediately.
//
// A present prefetched count wins over an individual read. Missing or failed
// observations use zero, then policy.CheckRPM decides ordinary, affinity-only,
// or blocked admission. This is a soft eligibility check rather than a
// reservation: concurrent requests may observe the same count, and recording
// is performed separately after an attempt has been selected.
func AllowProviderRPM(ctx context.Context, cache RPMCache, input ProviderRPMInput, sticky bool) bool {
	if !input.Enabled || input.Base <= 0 {
		return true
	}
	current, found := PrefetchedRPM(ctx, input.ID)
	// An absent batch entry leaves this provider unobserved. Try its
	// individual counter while keeping zero on failure so the observation
	// outage does not block selection.
	if !found && cache != nil {
		if count, err := cache.GetRPM(ctx, input.ID); err == nil {
			current = count
		}
	}
	switch policy.CheckRPM(current, input.Base, input.Buffer, input.Strategy) {
	case policy.RPMAllowed:
		return true
	case policy.RPMStickyOnly:
		return sticky
	case policy.RPMBlocked:
		return false
	}
	return true
}

// IncrementProviderRPM records one request when provider RPM storage is
// configured. A nil cache succeeds without recording; a configured cache
// returns its increment failure to the caller.
func IncrementProviderRPM(ctx context.Context, cache RPMCache, id int64) error {
	if cache == nil {
		return nil
	}
	_, err := cache.IncrementRPM(ctx, id)
	return err
}
