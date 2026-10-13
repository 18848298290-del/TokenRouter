//go:build integration

package promotion_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	promotionpostgres "github.com/TokenFlux/TokenRouter/internal/promotion/postgres"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	settingspostgres "github.com/TokenFlux/TokenRouter/internal/settings/postgres"
)

// Two rebate accruals can compete for the same invitee cap even when each
// individual purchase fits it. The fixture releases two calls together before
// the PostgreSQL inviter lock, and the service reads prior accrual inside that
// lock before writing another credit. The observed total must reach ten from
// two requested purchases of eight at a one-hundred-percent rate.
//
// An atomic arrival count makes the second call close the shared ready channel,
// allowing both delegates to begin their locked transactions. The gate also
// observes context cancellation before delegation. Buffered error results are
// received before the final stored total is read, and settings cleanup removes
// every override before restoring the prior snapshot so originally absent keys
// remain absent.

// affiliateLockBarrier coordinates two accrual arrivals before their delegated
// inviter-lock work begins.
type affiliateLockBarrier struct {
	// AffiliateRepository owns locked PostgreSQL callback execution after
	// the gate opens, including the service's current-total read and its
	// subsequent accrual write.
	promotion.AffiliateRepository
	// ready and calls coordinate the first two arrivals. The call counted
	// as two closes the freshly initialized ready channel; later arrivals
	// observe the open gate.
	ready chan struct{} // delegated lock admission gate
	calls atomic.Int32  // observed lock callback arrivals
}

// WithLockedInviter counts arrivals and waits for the second call before
// entering the delegate's inviter lock. A selected cancellation case returns
// ctx.Err before invoking the callback through the repository.
//
// The call whose atomic count reaches two closes ready, opening the gate for
// other waiting calls and later arrivals. Callers provide a fresh channel and
// enough attempts to release the first waiter. The repository retains
// transaction and callback ownership, while this wrapper only chooses when its
// delegated lock work can start.
func (r *affiliateLockBarrier) WithLockedInviter(ctx context.Context, id int64, fn func(context.Context) error) error {
	// Open admission on the second observed call so both accruals can begin
	// competing for the database lock after their earlier service reads
	// have completed.
	if r.calls.Add(1) == 2 {
		close(r.ready)
	}
	select {
	case <-r.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	return r.AffiliateRepository.WithLockedInviter(ctx, id, fn)
}

// TestAffiliateCapConcurrentAccrualUsesLockedLatestTotal releases two rebate
// accruals before their PostgreSQL inviter lock with a cap of ten. Both calls
// succeed and the final stored invitee accrual equals that cap.
func TestAffiliateCapConcurrentAccrualUsesLockedLatestTotal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c := testEntClient(t)
	repo := promotionpostgres.NewAffiliateRepository(c, func(tx *dbent.Tx) promotionpostgres.TransferBalance { return billingpostgres.BalanceInTx(tx) })
	inviter, e := c.User.Create().SetEmail("test-inviter@example.com").SetPasswordHash("hash").Save(ctx)
	require.NoError(t, e)
	invitee, e := c.User.Create().SetEmail("test-invitee@example.com").SetPasswordHash("hash").Save(ctx)
	require.NoError(t, e)
	_, e = repo.EnsureUserAffiliate(ctx, inviter.ID)
	require.NoError(t, e)
	_, e = repo.EnsureUserAffiliate(ctx, invitee.ID)
	require.NoError(t, e)
	_, e = repo.BindInviter(ctx, invitee.ID, inviter.ID)
	require.NoError(t, e)
	sr := settings.New(settingspostgres.NewSettingRepository(c))
	keys := []string{promotion.SettingKeyAffiliateEnabled, promotion.SettingKeyAffiliateRebateRate, promotion.SettingKeyAffiliateRebatePerInviteeCap, promotion.SettingKeyAffiliateRebateFreezeHours, promotion.SettingKeyAffiliateRebateDurationDays}
	original, err := sr.GetMultiple(ctx, keys)
	require.NoError(t, err)
	// Remove every temporary override before restoring the original map so
	// keys absent from that snapshot do not keep this case's cap and rate
	// values.
	t.Cleanup(func() {
		for _, key := range keys {
			require.NoError(t, sr.Delete(context.Background(), key))
		}
		require.NoError(t, sr.SetMultiple(context.Background(), original))
	})
	require.NoError(t, sr.SetMultiple(ctx, map[string]string{promotion.SettingKeyAffiliateEnabled: "true", promotion.SettingKeyAffiliateRebateRate: "100", promotion.SettingKeyAffiliateRebatePerInviteeCap: "10", promotion.SettingKeyAffiliateRebateFreezeHours: "0", promotion.SettingKeyAffiliateRebateDurationDays: "0"}))
	barrier := &affiliateLockBarrier{AffiliateRepository: repo, ready: make(chan struct{})}
	svc := promotion.NewAffiliateService(barrier, promotion.NewRuntimeSettings(sr), nil, nil, promotion.Runtime{})
	// Buffer each accrual's result and receive both before reading the
	// aggregate, so the cap assertion observes both completed PostgreSQL
	// transactions rather than one pending credit.
	errs := make(chan error, 2)
	for range 2 {
		go func() { _, e := svc.AccrueInviteRebate(ctx, invitee.ID, 8); errs <- e }()
	}
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	amount, e := repo.GetAccruedRebateFromInvitee(ctx, inviter.ID, invitee.ID)
	require.NoError(t, e)
	t.Logf("cap=10 accrued=%v", amount)
	require.Equal(t, 10.0, amount, "并发计提不能超过单被邀请人上限")
}
