package postgres

import (
	"context"

	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

// ProviderUsageOptions supplies change notifications, synchronization, and logging after successful usage writes.
type ProviderUsageOptions struct {
	Changed func(context.Context, int64) error
	Sync    func(context.Context, int64)
	Observe func(string, ...any)
}

// ProviderUsageStore writes usage and runs notifications afterward. Caller-owned transactions use ProviderUsageInTx.
type ProviderUsageStore struct {
	exec    postgresinfra.Executor
	options ProviderUsageOptions
}

func NewProviderUsageStore(exec postgresinfra.Executor, options ProviderUsageOptions) *ProviderUsageStore {
	if options.Observe == nil {
		options.Observe = func(string, ...any) {}
	}
	return &ProviderUsageStore{exec: exec, options: options}
}

func (s *ProviderUsageStore) IncrementQuotaUsed(ctx context.Context, id int64, amount float64) error {
	crossed, err := ProviderUsageInTx(s.exec).Increment(ctx, id, amount)
	if err != nil {
		return err
	}
	if crossed && s.options.Changed != nil {
		if err := s.options.Changed(ctx, id); err != nil {
			s.options.Observe("[SchedulerOutbox] enqueue quota exceeded failed: provider=%d err=%v", id, err)
		}
	}
	return nil
}

func (s *ProviderUsageStore) ResetQuotaUsedAndClearRateLimitCooldown(ctx context.Context, id int64) error {
	if err := ProviderUsageInTx(s.exec).ResetAndClearRateLimitCooldown(ctx, id); err != nil {
		return err
	}
	if s.options.Changed != nil {
		if err := s.options.Changed(ctx, id); err != nil {
			s.options.Observe("[SchedulerOutbox] enqueue quota reset failed: provider=%d err=%v", id, err)
		}
	}
	if s.options.Sync != nil {
		s.options.Sync(ctx, id)
	}
	return nil
}
