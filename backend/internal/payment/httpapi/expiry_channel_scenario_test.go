package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/payment"
)

// Expiry shutdown is tested while a provider query is blocked in a loopback
// HTTP request. The fixture combines production expiry, lifecycle and binding
// operations with a partial store/provider so cancellation can be observed at
// the server and later phases counted without a payment gateway.

// expiryChannelOrders supplies pending reconciliation and counts later phase
// reads in the cancellation fixture.
type expiryChannelOrders struct {
	// FulfillmentStore is nil for operations outside this scenario. Its promoted
	// unimplemented methods would panic; the fixture supplies only the phase
	// reads reached during cancellation.
	payment.FulfillmentStore
	// nextPhase records calls to processing, fulfillment recovery and expiration
	// reads. Atomic access permits the test to inspect it after the production
	// worker joins during StopContext.
	nextPhase atomic.Int32
}

// expiryChannelInstances supplies empty instance reads for legacy
// loopback-provider registry resolution.
type expiryChannelInstances struct {
	// BindingStore is nil for unimplemented fixture operations. The scenario
	// provides ListInstances and CountEnabledInstances for its unbound registry
	// resolution, and other promoted calls would panic.
	payment.BindingStore
}

// expiryHTTPProvider advertises Alipay and forwards order queries to a
// loopback cancellation server.
type expiryHTTPProvider struct {
	// Provider is a nil embedded interface for operations outside this scenario.
	// QueryOrder and identity methods are supplied; other promoted operations
	// would panic if reached.
	payment.Provider
	// client is the loopback server's HTTP client with a bounded timeout.
	// QueryOrder also attaches worker context so StopContext can cancel the
	// blocked request earlier.
	client *http.Client
	url    string // loopback query destination URL
}

// TestPaymentExpiryStopCancelsChannelAndPreventsNextPhase blocks pending
// reconciliation in a loopback provider query, then stops production expiry.
// It requires the server to observe request cancellation and every later phase
// counter to remain zero after StopContext returns.
func TestPaymentExpiryStopCancelsChannelAndPreventsNextPhase(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	// Signal request entry before waiting on its context, then signal
	// cancellation separately. The test stops expiry only after entry so a
	// skipped request cannot satisfy shutdown assertions.
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(cancelled) }))
	// Close the loopback server after the cancellation assertions and worker
	// stop. Test cleanup retains server ownership even when an earlier fatal
	// assertion ends the scenario.
	t.Cleanup(server.Close)
	client := server.Client()
	// Bound loopback network waits independently to two seconds.
	client.Timeout = 2 * time.Second
	registry := payment.NewRegistry()
	registry.Register(expiryHTTPProvider{client: client, url: server.URL})
	bindings := payment.NewProviderBindings(expiryChannelInstances{}, registry, nil, payment.BindingRuntime{}, true)
	store := &expiryChannelOrders{}
	fulfillment := payment.NewFulfillment(store, bindings, registry, nil, nil, payment.FulfillmentRuntime{})
	orders := payment.NewOrderLifecycle(fulfillment, nil, nil)
	worker := payment.NewOrderExpiry(orders, time.Hour, payment.ExpiryRuntime{})
	// Use a bounded run parent to end the scenario if request entry never occurs.
	// Deferred cancellation also releases any surviving worker/request path on an
	// earlier test failure.
	runCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	worker.Start(runCtx)
	select {
	case <-entered:
	case <-runCtx.Done():
		t.Fatal("后台未进入本地渠道")
	}
	// Give shutdown a separate bounded context after the provider request has
	// entered. Its cancellation cleanup is independent of the run parent while
	// StopContext waits for worker completion.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	require.NoError(t, worker.StopContext(stopCtx))
	select {
	case <-cancelled:
	case <-stopCtx.Done():
		t.Fatal("停止没有取消渠道 HTTP 请求")
	}
	require.Zero(t, store.nextPhase.Load())
}

// PendingReconciliation returns one unbound pending Alipay order for the
// blocked loopback query.
func (s *expiryChannelOrders) PendingReconciliation(context.Context, time.Time, int) ([]*payment.Order, error) {
	return []*payment.Order{{ID: 1, PaymentType: payment.TypeAlipay, OutTradeNo: "test-channel-cancel", Status: payment.OrderStatusPending}}, nil
}

// ProcessingIDs increments the later-phase counter and returns no processing
// candidates.
func (s *expiryChannelOrders) ProcessingIDs(context.Context) ([]int64, error) {
	// Record an attempted later phase through the atomic counter before returning
	// empty data. The shutdown assertion must observe no such phase after the
	// blocked query is cancelled.
	s.nextPhase.Add(1)
	return nil, nil
}

// RecoverableFulfillmentIDs increments the later-phase counter and returns no
// fulfillment candidates.
func (s *expiryChannelOrders) RecoverableFulfillmentIDs(context.Context, time.Time, time.Duration, time.Duration) ([]int64, error) {
	// Record an attempted later phase through the atomic counter before returning
	// empty data. The shutdown assertion must observe no such phase after the
	// blocked query is cancelled.
	s.nextPhase.Add(1)
	return nil, nil
}

// ExpiredPending increments the later-phase counter and returns no timeout
// candidates.
func (s *expiryChannelOrders) ExpiredPending(context.Context, time.Time) ([]*payment.Order, error) {
	// Record an attempted later phase through the atomic counter before returning
	// empty data. The shutdown assertion must observe no such phase after the
	// blocked query is cancelled.
	s.nextPhase.Add(1)
	return nil, nil
}

// ListInstances returns no historical instances for this loopback registry
// fixture.
func (expiryChannelInstances) ListInstances(context.Context, payment.InstanceFilter) ([]*payment.ProviderInstance, error) {
	return nil, nil
}

// CountEnabledInstances returns zero so the unbound fixture can use its
// registry provider.
func (expiryChannelInstances) CountEnabledInstances(context.Context, string) (int, error) {
	return 0, nil
}

// ProviderKey identifies the loopback fixture as the official Alipay adapter.
func (expiryHTTPProvider) ProviderKey() string { return payment.TypeAlipay }

// SupportedTypes returns a fresh Alipay method slice for fixture registration.
func (expiryHTTPProvider) SupportedTypes() []payment.PaymentType {
	return []payment.PaymentType{payment.TypeAlipay}
}

// QueryOrder issues a loopback GET with the worker's context and closes any
// returned body. It returns no payment observation and the HTTP error, letting
// expiry shutdown propagate cancellation through production reconciliation.
func (p expiryHTTPProvider) QueryOrder(ctx context.Context, _ string) (*payment.QueryOrderResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return nil, err
	}
	response, err := p.client.Do(request)
	// An HTTP error can accompany a nonnil response. Close that body before
	// returning so the loopback fixture releases its transport resources on both
	// accepted and failed requests.
	if response != nil {
		_ = response.Body.Close()
	}
	return nil, err
}
