//go:build integration

package billing_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
)

// Subscription renewal needs the current chain state when overlapping
// administrators each add seven days. The first service is paused after a
// repository read so the test can start a second extension while the first
// still holds its operation open. The second result channel is sampled for
// thirty milliseconds before the first is released, and the final expiry must
// include fourteen added days.
//
// A sync.Once makes the read signal and release wait belong to the first
// delegated read only. The delegate's result is captured before pausing, and
// context cancellation can end that pause without replacing the captured
// result. Buffered error channels let both extension goroutines finish before
// the test receives their results and reads the persisted expiry.

// pausedSubscriptionRead exposes the first delegated read and gates its return
// during overlapping extension operations.
type pausedSubscriptionRead struct {
	// UserSubscriptionRepository supplies the actual read result retained
	// across the pause, so tests still use the PostgreSQL repository for
	// subscription state.
	billing.UserSubscriptionRepository
	// read and release coordinate the first delegated read: closing read
	// announces its result, while closing release lets that call return.
	// Initialize both channels.
	read, release chan struct{} // read signals read completion; release ends first pause
	once          sync.Once     // single first-read pause gate
}

// GetByID delegates a subscription read, then pauses the first call until
// release closes or ctx ends. The captured subscription and error are returned
// unchanged after that pause.
//
// sync.Once signals read exactly once and keeps later calls behind the first
// callback until it returns. The read signal therefore marks completion of the
// delegated read, and cancellation releases this fixture gate without
// synthesizing a new error. Callers supply initialized read and release
// channels.
func (r *pausedSubscriptionRead) GetByID(ctx context.Context, id int64) (*billing.UserSubscription, error) {
	sub, err := r.UserSubscriptionRepository.GetByID(ctx, id)
	// Publish the completed delegate read before waiting for the test
	// release; cancellation also opens this gate while leaving the captured
	// delegate result unchanged.
	r.once.Do(func() {
		close(r.read)
		select {
		case <-r.release:
		case <-ctx.Done():
		}
	})
	return sub, err
}

// TestConcurrentSubscriptionExtensionsUseLockedState pauses one PostgreSQL
// extension while starting another on the same subscription. The second stays
// incomplete during the observation interval, and both successful results
// extend expiry fourteen days.
func TestConcurrentSubscriptionExtensionsUseLockedState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := committedEntitlementClient(t)
	user, err := client.User.Create().SetEmail("test-chain@example.com").SetPasswordHash("hash").Save(ctx)
	require.NoError(t, err)
	plan, err := client.SubscriptionPlan.Create().SetName("test chain").SetPrice(10).SetValidityDays(2).Save(ctx)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	expires := now.Add(48 * time.Hour)
	sub, err := client.UserSubscription.Create().SetUserID(user.ID).SetPlanID(plan.ID).SetStartsAt(now).SetExpiresAt(expires).SetStatus(billing.SubscriptionStatusActive).SetAssignedAt(now).Save(ctx)
	require.NoError(t, err)
	repo := billingpostgres.NewUserSubscriptionRepository(client)
	paused := &pausedSubscriptionRead{UserSubscriptionRepository: repo, read: make(chan struct{}), release: make(chan struct{})}
	first := billing.NewSubscriptionService(subscriptionContractEmptyGroups{}, paused, billingpostgres.NewSubscriptionMutations(client))
	second := billing.NewSubscriptionService(subscriptionContractEmptyGroups{}, repo, billingpostgres.NewSubscriptionMutations(client))
	// Buffer each extension result so service completion cannot depend on
	// receive order; the paused first read controls when the final chain
	// can be inspected.
	result1, result2 := make(chan error, 1), make(chan error, 1)
	go func() { _, e := first.ExtendSubscription(ctx, sub.ID, 7); result1 <- e }()
	select {
	case <-paused.read:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() { _, e := second.ExtendSubscription(ctx, sub.ID, 7); result2 <- e }()
	var secondErr error
	// Sample the second result before releasing the first call. The short
	// wait detects an early completion in this schedule and bounds the
	// fixture observation.
	finished := false
	select {
	case secondErr = <-result2:
		finished = true
		t.Error("第二次延长绕过了首个操作者的状态锁")
	case <-time.After(30 * time.Millisecond):
	}
	// Release the first operation before receiving both results so the
	// competing service can obtain current locked state and add its own
	// seven-day extension.
	close(paused.release)
	require.NoError(t, <-result1)
	if !finished {
		secondErr = <-result2
	}
	require.NoError(t, secondErr)
	final, err := client.UserSubscription.Get(ctx, sub.ID)
	require.NoError(t, err)
	require.True(t, final.ExpiresAt.Equal(expires.AddDate(0, 0, 14)), "两个 +7 天操作必须累计为 +14 天，不能用旧快照覆盖")
}
