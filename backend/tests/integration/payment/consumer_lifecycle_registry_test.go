package payment_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	paymentpostgres "github.com/TokenFlux/TokenRouter/internal/payment/postgres"
	paymenttestkit "github.com/TokenFlux/TokenRouter/internal/payment/testkit"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Payment expiry admits one managed run and needs cancellation to reach a
// database query already in flight. These fixtures count leader attempts or
// park the first driver query, using bounded observations to inspect repeated
// Start, Start after Stop, and a query context canceled during shutdown.
//
// The driver reports SQLite dialect text but supplies query errors instead of a
// database. Its first arrival send is outside cancellation selection, so
// callers provide channel capacity before allowing the later cancelable wait.
// Provider binding cases use that driver to count failed initial-load retries
// and to inspect an existing registry entry after failed refresh, rather than
// exercising a live provider or persistent database.

// paymentDriver records query attempts and optionally gates the first lifecycle
// query before returning fixture errors.
type paymentDriver struct {
	// Driver supplies promoted operations beyond Dialect and Query. It
	// stays nil in these fixtures, so a flow reaching those unimplemented
	// operations would panic.
	dialect.Driver
	// calls, entered, and release coordinate the first read: calls chooses
	// the first attempt, entered sends its context, and release ends the
	// later parked wait.
	calls atomic.Int32 // observed driver query attempts
	// entered may be nil to disable the gate. A configured channel needs
	// capacity or a receiver because its initial send does not observe
	// context cancellation.
	entered chan context.Context
	release chan struct{} // first query release gate
}

// paymentLeader observes expiry leadership attempts while reporting that no
// lease is acquired.
type paymentLeader struct {
	// calls and entered record each attempt before returning false.
	// Initialize entered with capacity or a receiver; its send ignores
	// context and can otherwise block admission.
	calls   atomic.Int32  // observed leader acquisition attempts
	entered chan struct{} // leader attempt arrival signals
}

// Dialect returns SQLite dialect text for this error-producing lifecycle driver
// fixture.
func (d *paymentDriver) Dialect() string { return dialect.SQLite }

// Query counts the attempted read and returns a fixture database error,
// optionally parking its first call. If the first entered channel is
// configured, the method sends its context before waiting for release or
// cancellation.
//
// The arrival send itself does not select ctx.Done, so callers supply channel
// capacity or a receiver before requesting cancellation. The later selected
// cancellation case returns ctx.Err, while a release proceeds to the fixture
// error. Query text, arguments, and result storage are ignored, and later
// queries return the error directly.
func (d *paymentDriver) Query(ctx context.Context, query string, args, result any) error {
	n := d.calls.Add(1)
	// Capture first-query entry before parking it so the test can request
	// stop after obtaining the run context; channel capacity is needed for
	// that uncancelable arrival send.
	if n == 1 && d.entered != nil {
		d.entered <- ctx
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-d.release:
		}
	}
	return errors.New("fixture database unavailable")
}

// TryAcquireLeaderLock records a leader attempt and sends its entry signal
// before returning false with no error. It ignores all inputs, and callers
// provide channel capacity or a receiver so this uncancelable send can finish.
func (l *paymentLeader) TryAcquireLeaderLock(context.Context, string, string, time.Duration) (bool, error) {
	l.calls.Add(1)
	l.entered <- struct{}{}
	return false, nil
}

// ReleaseLeaderLock reports success for every supplied context, key, and owner
// without changing fixture state.
func (l *paymentLeader) ReleaseLeaderLock(context.Context, string, string) error { return nil }

// TestPaymentExpiryRepeatedStart observes the first leader attempt on an hourly
// expiry run and calls Start again. No second arrival appears during the
// eighty-millisecond observation, and stopping the run succeeds.
func TestPaymentExpiryRepeatedStart(t *testing.T) {
	l := &paymentLeader{entered: make(chan struct{}, 4)}
	s := payment.NewOrderExpiry(paymenttestkit.Lifecycle(nil, nil, nil, nil, nil, false), time.Hour, payment.ExpiryRuntime{Acquire: func(ctx context.Context) (func(), bool) {
		return provider.AcquireSingletonLease(ctx, l, nil, payment.OrderExpiryLeaderKey, "fixture", payment.OrderExpiryLeaderTTL)
	}})
	s.Start(context.Background())
	<-l.entered
	s.Start(context.Background())
	select {
	case <-l.entered:
		t.Errorf("重复 Start 又执行首轮；次数=%d", l.calls.Load())
	case <-time.After(80 * time.Millisecond):
	}
	require.NoError(t, s.StopContext(context.Background()))
}

// TestPaymentExpiryStartAfterStop stops an expiry instance before calling
// Start. No leader attempt appears during the eighty-millisecond observation,
// and a repeated stop succeeds for the already stopped instance.
func TestPaymentExpiryStartAfterStop(t *testing.T) {
	l := &paymentLeader{entered: make(chan struct{}, 4)}
	s := payment.NewOrderExpiry(paymenttestkit.Lifecycle(nil, nil, nil, nil, nil, false), time.Hour, payment.ExpiryRuntime{Acquire: func(ctx context.Context) (func(), bool) {
		return provider.AcquireSingletonLease(ctx, l, nil, payment.OrderExpiryLeaderKey, "fixture", payment.OrderExpiryLeaderTTL)
	}})
	require.NoError(t, s.StopContext(context.Background()))
	s.Start(context.Background())
	select {
	case <-l.entered:
		t.Error("Stop 后 Start 仍领取首轮任务")
	case <-time.After(80 * time.Millisecond):
	}
	require.NoError(t, s.StopContext(context.Background()))
}

// TestPaymentExpiryStopCancelsQuery parks a driver query and requests expiry
// shutdown on another goroutine. The observed stop result succeeds and the
// captured query context is canceled; release also permits cleanup if the
// bounded check fails.
func TestPaymentExpiryStopCancelsQuery(t *testing.T) {
	d := &paymentDriver{entered: make(chan context.Context, 1), release: make(chan struct{})}
	client := dbent.NewClient(dbent.Driver(d))
	s := payment.NewOrderExpiry(paymenttestkit.Lifecycle(client, nil, nil, nil, nil, false), time.Hour, payment.ExpiryRuntime{})
	s.Start(context.Background())
	runCtx := <-d.entered
	// Request stop separately from the blocked query so its returned result
	// and query cancellation can be inspected before the fixture opens its
	// manual release gate.
	done := make(chan error, 1)
	go func() { done <- s.StopContext(context.Background()); close(done) }()
	select {
	case err := <-done:
		require.NoError(t, err)
		if runCtx.Err() == nil {
			t.Error("停止未取消运行 context")
		}
	case <-time.After(80 * time.Millisecond):
		t.Error("Stop 未取消支持 context 的查询，仍被在途操作阻塞")
	}
	// Open the query gate even after the nonfatal timeout observation so
	// its worker can finish, then receive or observe the closed stop-result
	// channel before leaving.
	close(d.release)
	<-done
}

// TestPaymentProvidersFailedInitialLoadRetries invokes provider initialization
// twice against an error-producing driver. The query count is two, showing that
// the failed first load did not suppress the next attempted load.
func TestPaymentProvidersFailedInitialLoadRetries(t *testing.T) {
	d := &paymentDriver{}
	s := payment.NewProviderBindings(paymentpostgres.NewInstanceStore(dbent.NewClient(dbent.Driver(d))), payment.NewRegistry(), nil, payment.BindingRuntime{}, false)
	s.EnsureProviders(context.Background())
	s.EnsureProviders(context.Background())
	if d.calls.Load() != 2 {
		t.Errorf("首次加载失败被标成已加载，后续不重试：query calls=%d", d.calls.Load())
	}
}

// TestPaymentProvidersFailedRefreshPreservesPublished seeds a Stripe registry
// entry before refreshing through a failing driver. Registry lookup still
// returns that published provider after the refresh attempt fails.
func TestPaymentProvidersFailedRefreshPreservesPublished(t *testing.T) {
	d := &paymentDriver{}
	reg := payment.NewRegistry()
	reg.Register(paymenttestkit.StaticProvider{Key: payment.TypeStripe, Types: []string{payment.TypeStripe}})
	s := payment.NewProviderBindings(paymentpostgres.NewInstanceStore(dbent.NewClient(dbent.Driver(d))), reg, nil, payment.BindingRuntime{}, false)
	s.RefreshProviders(context.Background())
	if _, err := reg.GetProvider(payment.TypeStripe); err != nil {
		t.Error("刷新读取失败丢失了已发布的 provider")
	}
}
