package scheduler

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// ProviderRPMInput contains the provider's RPM admission settings.
type ProviderRPMInput struct {
	ID           int64
	Enabled      bool
	Base, Buffer int
	Strategy     string
}
type rpmPrefetchKey struct{}

func PrefetchedRPM(ctx context.Context, id int64) (int, bool) {
	if v, ok := ctx.Value(rpmPrefetchKey{}).(map[int64]int); ok {
		count, found := v[id]
		return count, found
	}
	return 0, false
}

func PrefetchRPM(ctx context.Context, cache RPMCache, ids []int64) context.Context {
	if cache == nil || len(ids) == 0 {
		return ctx
	}
	counts, err := cache.GetRPMBatch(ctx, ids)
	if err != nil {
		return ctx
	}
	return context.WithValue(ctx, rpmPrefetchKey{}, counts)
}

// AllowProviderRPM applies ordinary, sticky-only, or blocked admission using a soft count; read errors use zero.
func AllowProviderRPM(ctx context.Context, cache RPMCache, input ProviderRPMInput, sticky bool) bool {
	if !input.Enabled || input.Base <= 0 {
		return true
	}
	current, found := PrefetchedRPM(ctx, input.ID)
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

func IncrementProviderRPM(ctx context.Context, cache RPMCache, id int64) error {
	if cache == nil {
		return nil
	}
	_, err := cache.IncrementRPM(ctx, id)
	return err
}
