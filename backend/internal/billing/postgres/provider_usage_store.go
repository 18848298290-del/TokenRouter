package postgres

import (
	"context"

	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

// Provider usage statement results determine when scheduler change events are
// requested. Total quota crossing triggers an optional Changed callback, while
// reset requests change publication and then optional direct snapshot
// synchronization. A callback failure is logged after the usage statement has
// already succeeded.
//
// Production wiring binds a plain database, so these callbacks follow its
// completed statements. A supplied transactional executor can still have an
// outer commit outstanding; caller-owned transactions use the participant when
// they need publication ordered after commit. Constructor defaults only the
// observer, leaving Changed and Sync optional and keeping each callback's
// distinct timing visible.

// ProviderUsageOptions supplies optional change/synchronization effects and
// observation for provider usage writes.
type ProviderUsageOptions struct {
	// Changed requests a scheduler change event after a successful reset or
	// total-limit crossing. Its returned error is observed and suppressed by this
	// store.
	Changed func(context.Context, int64) error // optional provider change event
	// Sync runs after reset change handling when supplied. IncrementQuotaUsed
	// does not invoke it, so ordinary total-limit crossings rely on their Changed
	// event.
	Sync func(context.Context, int64) // optional reset snapshot synchronization
	// Observe records Changed callback failures after successful usage
	// statements. NewProviderUsageStore installs a no-op when absent so optional
	// notification failure remains loggable without a nil call.
	Observe func(string, ...any) // usage callback failure observer
}

// ProviderUsageStore applies provider usage statements and their immediate
// optional event callbacks.
type ProviderUsageStore struct {
	// exec determines when statement writes commit. With an outer transaction,
	// callbacks still run immediately, so its caller arranges publication after
	// commit through the participant when needed.
	exec    postgresinfra.Executor // bound provider usage executor
	options ProviderUsageOptions   // post-statement callbacks and observation
}

// NewProviderUsageStore binds a caller-owned executor and immediate
// post-statement callbacks. Nil Observe becomes a no-op, while Changed and
// Sync remain optional; the caller owns any transaction and ensures callback
// ordering matches its executor.
func NewProviderUsageStore(exec postgresinfra.Executor, options ProviderUsageOptions) *ProviderUsageStore {
	if options.Observe == nil {
		options.Observe = func(string, ...any) {}
	}
	return &ProviderUsageStore{exec: exec, options: options}
}

// IncrementQuotaUsed applies the provider increment and invokes Changed when
// its returned total-limit crossing is true. Statement errors return; Changed
// errors are observed and suppressed, and this method does not invoke Sync.
// The executor controls statement transaction completion.
func (s *ProviderUsageStore) IncrementQuotaUsed(ctx context.Context, id int64, amount float64) error {
	crossed, err := ProviderUsageInTx(s.exec).Increment(ctx, id, amount)
	if err != nil {
		return err
	}
	// The statement reported a total-ceiling crossing; request its optional
	// scheduler event now, logging a callback failure while retaining the
	// successful usage write.
	if crossed && s.options.Changed != nil {
		if err := s.options.Changed(ctx, id); err != nil {
			s.options.Observe("[SchedulerOutbox] enqueue quota exceeded failed: provider=%d err=%v", id, err)
		}
	}
	return nil
}

// ResetQuotaUsedAndClearRateLimitCooldown resets quota/rate-limit state before
// requesting Changed and then Sync. Statement errors stop callbacks; Changed
// errors are observed and suppressed, while a configured Sync still runs. The
// caller owns any outer transaction on the executor.
func (s *ProviderUsageStore) ResetQuotaUsedAndClearRateLimitCooldown(ctx context.Context, id int64) error {
	if err := ProviderUsageInTx(s.exec).ResetAndClearRateLimitCooldown(ctx, id); err != nil {
		return err
	}
	// Quota reset has succeeded, so request its change event before optional
	// direct synchronization; notification errors are observed and do not undo
	// that statement.
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
