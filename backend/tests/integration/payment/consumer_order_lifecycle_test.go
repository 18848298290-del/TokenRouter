package payment_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/paymentauditlog"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	paymentpostgres "github.com/TokenFlux/TokenRouter/internal/payment/postgres"
	paymenttestkit "github.com/TokenFlux/TokenRouter/internal/payment/testkit"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
	sqlitetest "github.com/TokenFlux/TokenRouter/internal/testutil/sqlite"
)

// An order query can discover payment without safely cancelling or fulfilling
// the order. SQLite lifecycle cases supply pending, paid, processing, or failed
// provider responses and inspect resulting order transitions, query references,
// cancellation attempts, and audit records. A paid response with zero amount
// remains unfulfilled unless a later query supplies a usable amount.
//
// The provider fixture records each reference before returning a configured
// error or response, allowing cases to distinguish a query retry from a
// cancellation requery. Fixed-clock expiry cases observe backoff across the
// configured delay, while reconcile cases cover stale records, retained
// fulfillment audits, and progress past the first batch. Entitlement callbacks
// and provider responses are local fixtures; the sequential cases do not
// exercise PostgreSQL worker contention.

// paymentOrderLifecycleQueryProvider records sequential query and close
// attempts with configured outcomes.
type paymentOrderLifecycleQueryProvider struct {
	// key selects the provider type; an empty key makes ProviderKey return
	// Alipay, affecting registry selection and reference choice for this
	// fixture's responses.
	key string // configured provider lookup key
	// lastQueryTradeNo, queryTradeNos, and queryCalls record each query
	// before error selection; lastCancelTradeNo and cancelCalls record each
	// close before its configured error. Callers serialize these
	// observations.
	lastQueryTradeNo  string   // latest queried order reference
	queryTradeNos     []string // ordered query reference observations
	lastCancelTradeNo string   // latest requested close reference
	queryCalls        int      // recorded provider query count
	cancelCalls       int      // recorded provider close count
	// queryErr, responses, and resp select each query outcome. An error
	// rejects the call first; otherwise responses supplies its current head
	// or an empty list selects resp. responses retains supplied pointers
	// and advances only while more than one entry remains, so the last
	// configured response repeats on later queries.
	responses []*payment.QueryOrderResponse // ordered borrowed query responses
	// resp is the borrowed result when responses is empty; nil can be
	// returned with no error, and caller mutations remain visible in later
	// fixture queries.
	resp      *payment.QueryOrderResponse // fallback borrowed query response
	queryErr  error                       // configured provider query failure
	cancelErr error                       // configured provider close failure
}

// Name returns the fixed display name for lifecycle query and cancellation
// tests.
func (p *paymentOrderLifecycleQueryProvider) Name() string {
	return "payment-order-lifecycle-query-provider"
}

// ProviderKey returns the configured key, defaulting to Alipay when key is
// empty. This selects registry lookup and query-reference rules for the same
// fixture; no provider request is made.
func (p *paymentOrderLifecycleQueryProvider) ProviderKey() string {
	if p.key != "" {
		return p.key
	}
	return payment.TypeAlipay
}

// SupportedTypes returns a new slice containing the key selected by
// ProviderKey.
func (p *paymentOrderLifecycleQueryProvider) SupportedTypes() []payment.PaymentType {
	return []payment.PaymentType{p.ProviderKey()}
}

// CreatePayment panics when lifecycle tests unexpectedly request provider
// payment creation.
func (p *paymentOrderLifecycleQueryProvider) CreatePayment(context.Context, payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	panic("unexpected call")
}

// QueryOrder records the reference and call count before applying queryErr. A
// nonempty responses list returns its first borrowed pointer, dropping it while
// another entry remains; the last entry repeats. An empty list returns resp,
// including nil, and context is ignored.
func (p *paymentOrderLifecycleQueryProvider) QueryOrder(_ context.Context, tradeNo string) (*payment.QueryOrderResponse, error) {
	p.lastQueryTradeNo = tradeNo
	p.queryTradeNos = append(p.queryTradeNos, tradeNo)
	p.queryCalls++
	if p.queryErr != nil {
		return nil, p.queryErr
	}
	// Keep the final scripted result available for repeated verification or
	// scans; earlier entries advance only when the fixture has another
	// response to expose.
	if len(p.responses) > 0 {
		resp := p.responses[0]
		if len(p.responses) > 1 {
			p.responses = p.responses[1:]
		}
		return resp, nil
	}
	return p.resp, nil
}

// VerifyNotification panics when lifecycle tests unexpectedly request webhook
// verification.
func (p *paymentOrderLifecycleQueryProvider) VerifyNotification(context.Context, string, map[string]string) (*payment.PaymentNotification, error) {
	panic("unexpected call")
}

// Refund panics when lifecycle tests unexpectedly request a provider refund.
func (p *paymentOrderLifecycleQueryProvider) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
	panic("unexpected call")
}

// CancelPayment records the requested reference and increments cancelCalls
// before returning cancelErr. It ignores context and adds no synchronization;
// callers observe configured cancellation outcomes through sequential fixture
// use.
func (p *paymentOrderLifecycleQueryProvider) CancelPayment(_ context.Context, tradeNo string) error {
	p.lastCancelTradeNo = tradeNo
	p.cancelCalls++
	return p.cancelErr
}

// TestVerifyOrderByOutTradeNoBackfillsTradeNoFromPaidQuery supplies a paid
// provider response for a pending SQLite order. Verification records the
// returned provider trade number, completes fulfillment, and checks the
// fixture's balance and redemption observations.
func TestVerifyOrderByOutTradeNoBackfillsTradeNoFromPaidQuery(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)

	user, err := client.User.Create().
		SetEmail("checkpaid@example.com").
		SetPasswordHash("hash").
		SetUsername("checkpaid-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("CHECKPAID-UPSTREAM-TRADE-NO").
		SetOutTradeNo("sub2_checkpaid_trade_no_missing").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(payment.OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	userRepo := &fulfillmentBalance{
		getByIDUser: &billing.UserSummary{
			ID:       user.ID,
			Email:    user.Email,
			Username: user.Username,
			Balance:  0,
		},
	}
	userRepo.updateBalanceFn = func(ctx context.Context, id int64, amount float64) error {
		require.Equal(t, user.ID, id)
		if userRepo.getByIDUser != nil {
			userRepo.getByIDUser.Balance += amount
		}
		return nil
	}
	redeemRepo := &paymentOrderLifecycleRedeemRepo{
		codesByCode: map[string]*billing.RedeemCode{
			order.RechargeCode: {
				ID:     1,
				Code:   order.RechargeCode,
				Type:   billing.RedeemTypeBalance,
				Value:  order.Amount,
				Status: billing.StatusUnused,
			},
		},
	}
	redeemService := newFulfillmentRedeemService(
		redeemRepo,
		userRepo,

		client,
	)
	registry := payment.NewRegistry()
	provider := &paymentOrderLifecycleQueryProvider{
		resp: &payment.QueryOrderResponse{
			TradeNo: "upstream-trade-123",
			Status:  payment.ProviderStatusPaid,
			Amount:  88,
		},
	}
	registry.Register(provider)

	svc := paymenttestkit.Lifecycle(client,
		registry,
		redeemService, nil, nil, true)

	got, err := svc.VerifyOrderByOutTradeNo(ctx, order.OutTradeNo, user.ID)
	require.NoError(t, err)
	require.Equal(t, order.OutTradeNo, provider.lastQueryTradeNo)
	require.Equal(t, payment.OrderStatusCompleted, got.Status)
	require.Equal(t, "upstream-trade-123", got.PaymentTradeNo)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusCompleted, reloaded.Status)
	require.Equal(t, "upstream-trade-123", reloaded.PaymentTradeNo)

	require.Equal(t, 88.0, userRepo.getByIDUser.Balance)
	require.Len(t, redeemRepo.useCalls, 1)
	require.Equal(t, int64(1), redeemRepo.useCalls[0].id)
	require.Equal(t, user.ID, redeemRepo.useCalls[0].userID)
}

// TestVerifyOrderByOutTradeNoRetriesZeroAmountPaidQueryOnce supplies a paid
// response with zero amount followed by a usable paid amount. Verification
// queries twice, completes the order, and records the expected entitlement
// observations.
func TestVerifyOrderByOutTradeNoRetriesZeroAmountPaidQueryOnce(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)

	user, err := client.User.Create().
		SetEmail("checkpaid-retry@example.com").
		SetPasswordHash("hash").
		SetUsername("checkpaid-retry-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("CHECKPAID-UPSTREAM-RETRY").
		SetOutTradeNo("sub2_checkpaid_retry_zero_amount").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(payment.OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	userRepo := &fulfillmentBalance{
		getByIDUser: &billing.UserSummary{
			ID:       user.ID,
			Email:    user.Email,
			Username: user.Username,
			Balance:  0,
		},
	}
	userRepo.updateBalanceFn = func(ctx context.Context, id int64, amount float64) error {
		require.Equal(t, user.ID, id)
		if userRepo.getByIDUser != nil {
			userRepo.getByIDUser.Balance += amount
		}
		return nil
	}
	redeemRepo := &paymentOrderLifecycleRedeemRepo{
		codesByCode: map[string]*billing.RedeemCode{
			order.RechargeCode: {
				ID:     1,
				Code:   order.RechargeCode,
				Type:   billing.RedeemTypeBalance,
				Value:  order.Amount,
				Status: billing.StatusUnused,
			},
		},
	}
	redeemService := newFulfillmentRedeemService(
		redeemRepo,
		userRepo,

		client,
	)
	registry := payment.NewRegistry()
	provider := &paymentOrderLifecycleQueryProvider{
		responses: []*payment.QueryOrderResponse{
			{
				TradeNo: "upstream-trade-zero",
				Status:  payment.ProviderStatusPaid,
				Amount:  0,
			},
			{
				TradeNo: "upstream-trade-retry",
				Status:  payment.ProviderStatusPaid,
				Amount:  88,
			},
		},
	}
	registry.Register(provider)

	svc := paymenttestkit.Lifecycle(client,
		registry,
		redeemService, nil, nil, true)

	got, err := svc.VerifyOrderByOutTradeNo(ctx, order.OutTradeNo, user.ID)
	require.NoError(t, err)
	require.Equal(t, 2, provider.queryCalls)
	require.Equal(t, payment.OrderStatusCompleted, got.Status)
	require.Equal(t, "upstream-trade-retry", got.PaymentTradeNo)
}

// TestVerifyOrderByOutTradeNoRejectsPaidQueryWithZeroAmount keeps returning a
// paid response whose amount is zero. Verification leaves the SQLite order
// pending, retains its empty trade number, and records no balance credit or
// redemption.
func TestVerifyOrderByOutTradeNoRejectsPaidQueryWithZeroAmount(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)

	user, err := client.User.Create().
		SetEmail("checkpaid-zero-amount@example.com").
		SetPasswordHash("hash").
		SetUsername("checkpaid-zero-amount-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("CHECKPAID-ZERO-AMOUNT").
		SetOutTradeNo("sub2_checkpaid_zero_amount").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(payment.OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	userRepo := &fulfillmentBalance{
		getByIDUser: &billing.UserSummary{
			ID:       user.ID,
			Email:    user.Email,
			Username: user.Username,
			Balance:  0,
		},
	}
	redeemRepo := &paymentOrderLifecycleRedeemRepo{
		codesByCode: map[string]*billing.RedeemCode{
			order.RechargeCode: {
				ID:     1,
				Code:   order.RechargeCode,
				Type:   billing.RedeemTypeBalance,
				Value:  order.Amount,
				Status: billing.StatusUnused,
			},
		},
	}
	redeemService := newFulfillmentRedeemService(
		redeemRepo,
		userRepo,

		client,
	)
	registry := payment.NewRegistry()
	provider := &paymentOrderLifecycleQueryProvider{
		resp: &payment.QueryOrderResponse{
			TradeNo: "upstream-trade-zero",
			Status:  payment.ProviderStatusPaid,
			Amount:  0,
		},
	}
	registry.Register(provider)

	svc := paymenttestkit.Lifecycle(client,
		registry,
		redeemService, nil, nil, true)

	got, err := svc.VerifyOrderByOutTradeNo(ctx, order.OutTradeNo, user.ID)
	require.NoError(t, err)
	require.Equal(t, order.OutTradeNo, provider.lastQueryTradeNo)
	require.Equal(t, payment.OrderStatusPending, got.Status)
	require.Empty(t, got.PaymentTradeNo)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusPending, reloaded.Status)
	require.Empty(t, reloaded.PaymentTradeNo)

	require.Equal(t, 0.0, userRepo.getByIDUser.Balance)
	require.Empty(t, redeemRepo.useCalls)
}

// TestVerifyOrderByOutTradeNoUsesOutTradeNoWhenPaymentTradeNoAlreadyExistsForAlipay
// seeds a provider trade number on a pending Alipay order. Verification queries
// the local out-trade reference and returns the expected provider trade number
// from the supplied paid response.
func TestVerifyOrderByOutTradeNoUsesOutTradeNoWhenPaymentTradeNoAlreadyExistsForAlipay(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)

	user, err := client.User.Create().
		SetEmail("checkpaid-existing-trade@example.com").
		SetPasswordHash("hash").
		SetUsername("checkpaid-existing-trade-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("CHECKPAID-EXISTING-TRADE-NO").
		SetOutTradeNo("sub2_checkpaid_use_out_trade_no").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("upstream-trade-existing").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(payment.OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	userRepo := &fulfillmentBalance{
		getByIDUser: &billing.UserSummary{
			ID:       user.ID,
			Email:    user.Email,
			Username: user.Username,
			Balance:  0,
		},
	}
	userRepo.updateBalanceFn = func(ctx context.Context, id int64, amount float64) error {
		require.Equal(t, user.ID, id)
		if userRepo.getByIDUser != nil {
			userRepo.getByIDUser.Balance += amount
		}
		return nil
	}
	redeemRepo := &paymentOrderLifecycleRedeemRepo{
		codesByCode: map[string]*billing.RedeemCode{
			order.RechargeCode: {
				ID:     1,
				Code:   order.RechargeCode,
				Type:   billing.RedeemTypeBalance,
				Value:  order.Amount,
				Status: billing.StatusUnused,
			},
		},
	}
	redeemService := newFulfillmentRedeemService(
		redeemRepo,
		userRepo,

		client,
	)
	registry := payment.NewRegistry()
	provider := &paymentOrderLifecycleQueryProvider{
		resp: &payment.QueryOrderResponse{
			TradeNo: "upstream-trade-existing",
			Status:  payment.ProviderStatusPaid,
			Amount:  88,
		},
	}
	registry.Register(provider)

	svc := paymenttestkit.Lifecycle(client,
		registry,
		redeemService, nil, nil, true)

	got, err := svc.VerifyOrderByOutTradeNo(ctx, order.OutTradeNo, user.ID)
	require.NoError(t, err)
	require.Equal(t, order.OutTradeNo, provider.lastQueryTradeNo)
	require.Equal(t, "upstream-trade-existing", got.PaymentTradeNo)
}

// TestVerifyOrderByOutTradeNoDoesNotCancelPendingUpstreamOrder supplies a
// pending provider response during ordinary verification. The SQLite order
// stays pending after one query, and the cancellation counter remains zero.
func TestVerifyOrderByOutTradeNoDoesNotCancelPendingUpstreamOrder(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)

	user, err := client.User.Create().
		SetEmail("checkpaid-pending@example.com").
		SetPasswordHash("hash").
		SetUsername("checkpaid-pending-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("CHECKPAID-PENDING").
		SetOutTradeNo("sub2_checkpaid_pending").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(payment.OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	registry := payment.NewRegistry()
	provider := &paymentOrderLifecycleQueryProvider{
		resp: &payment.QueryOrderResponse{
			TradeNo: "upstream-trade-pending",
			Status:  payment.ProviderStatusPending,
			Amount:  88,
		},
	}
	registry.Register(provider)

	svc := paymenttestkit.Lifecycle(client,
		registry, nil, nil, nil, true)

	got, err := svc.VerifyOrderByOutTradeNo(ctx, order.OutTradeNo, user.ID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusPending, got.Status)
	require.Equal(t, 1, provider.queryCalls)
	require.Equal(t, 0, provider.cancelCalls)
}

// TestCancelOrderStillClosesPendingUpstreamOrder supplies a pending provider
// response for explicit cancellation. The provider receives the order's local
// reference for close, and the SQLite order becomes cancelled.
func TestCancelOrderStillClosesPendingUpstreamOrder(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)

	user, err := client.User.Create().
		SetEmail("cancel-pending@example.com").
		SetPasswordHash("hash").
		SetUsername("cancel-pending-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("CANCEL-PENDING").
		SetOutTradeNo("sub2_cancel_pending").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(payment.OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	registry := payment.NewRegistry()
	provider := &paymentOrderLifecycleQueryProvider{
		resp: &payment.QueryOrderResponse{
			TradeNo: order.OutTradeNo,
			Status:  payment.ProviderStatusPending,
			Amount:  0,
		},
	}
	registry.Register(provider)

	svc := paymenttestkit.Lifecycle(client,
		registry, nil, nil, nil, true)

	outcome, err := svc.CancelOrder(ctx, order.ID, user.ID)
	require.NoError(t, err)
	require.Equal(t, payment.LifecycleCheckPaidResultCancelled, outcome)
	require.Equal(t, order.OutTradeNo, provider.lastCancelTradeNo)
	require.Equal(t, 1, provider.cancelCalls)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusCancelled, reloaded.Status)
}

// TestForceExpireOrderRecordsAuditAndRejectsRepeatedTransition force-expires a
// pending SQLite order with an administrator reason. Its audit retains that
// reason, and repeating the transition returns HTTP 409 with
// ORDER_STATUS_CHANGED.
func TestForceExpireOrderRecordsAuditAndRejectsRepeatedTransition(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)
	order := createPaymentOrderLifecycleOrder(t, ctx, client, payment.OrderStatusPending, time.Now().Add(time.Hour))
	svc := paymenttestkit.Lifecycle(client, nil, nil, nil, nil, false)

	require.NoError(t, svc.ForceExpireOrder(ctx, order.ID, "provider endpoint returned HTML"))
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusExpired, reloaded.Status)

	audit, err := client.PaymentAuditLog.Query().Where(
		paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)),
		paymentauditlog.ActionEQ("ORDER_FORCE_EXPIRED"),
	).Only(ctx)
	require.NoError(t, err)
	require.Contains(t, audit.Detail, "provider endpoint returned HTML")

	// Repeat the administrator transition after expiry so the status
	// conflict is observed on the stored terminal order whose first audit
	// already contains the supplied reason.
	err = svc.ForceExpireOrder(ctx, order.ID, "repeat")
	require.Error(t, err)
	require.Equal(t, 409, httpx.ErrorCode(err))
	require.Equal(t, "ORDER_STATUS_CHANGED", apperror.Reason(err))
}

// TestForceExpireOrderRollsBackWhenAuditWriteFails seeds the force-expiry audit
// under the unique order/action index. The duplicate audit write fails and the
// SQLite order remains pending after the attempted transition.
func TestForceExpireOrderRollsBackWhenAuditWriteFails(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)
	ensurePaymentAuditOrderActionUniqueIndex(t, ctx, client)
	order := createPaymentOrderLifecycleOrder(t, ctx, client, payment.OrderStatusPending, time.Now().Add(time.Hour))
	_, err := client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(order.ID, 10)).
		SetAction("ORDER_FORCE_EXPIRED").
		SetDetail(`{"reason":"existing audit record"}`).
		SetOperator("admin").
		Save(ctx)
	require.NoError(t, err)

	svc := paymenttestkit.Lifecycle(client, nil, nil, nil, nil, false)
	err = svc.ForceExpireOrder(ctx, order.ID, "provider endpoint returned HTML")
	require.Error(t, err)

	reloaded, getErr := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, getErr)
	require.Equal(t, payment.OrderStatusPending, reloaded.Status)
}

// TestCancelOrderReturnsStatusUnavailableWhenProviderQueryFails injects
// provider query failure during explicit cancellation. It returns HTTP 503 with
// PAYMENT_STATUS_UNAVAILABLE, retains pending order state, and records
// cancellation failure.
func TestCancelOrderReturnsStatusUnavailableWhenProviderQueryFails(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)
	order := createPaymentOrderLifecycleOrder(t, ctx, client, payment.OrderStatusPending, time.Now().Add(time.Hour))
	provider := &paymentOrderLifecycleQueryProvider{queryErr: errors.New("upstream unavailable")}
	registry := payment.NewRegistry()
	registry.Register(provider)
	svc := paymenttestkit.Lifecycle(client, registry, nil, nil, nil, true)

	_, err := svc.CancelOrder(ctx, order.ID, order.UserID)
	require.Error(t, err)
	require.Equal(t, 503, httpx.ErrorCode(err))
	require.Equal(t, "PAYMENT_STATUS_UNAVAILABLE", apperror.Reason(err))

	reloaded, getErr := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, getErr)
	require.Equal(t, payment.OrderStatusPending, reloaded.Status)
	require.True(t, svc.HasAuditLog(ctx, order.ID, "PAYMENT_CANCEL_FAILED"))
}

// TestCancelOrderMovesToProcessingWhenCloseRacesWithCheckoutCompletion supplies
// pending then processing responses around a rejected close. Sequential
// observations show two queries, one cancellation, a processing outcome with an
// error, and persisted processing state.
func TestCancelOrderMovesToProcessingWhenCloseRacesWithCheckoutCompletion(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)
	order := createPaymentOrderLifecycleOrder(t, ctx, client, payment.OrderStatusPending, time.Now().Add(time.Hour))

	provider := &paymentOrderLifecycleQueryProvider{
		responses: []*payment.QueryOrderResponse{
			{TradeNo: order.OutTradeNo, Status: payment.ProviderStatusPending},
			{TradeNo: "pi_processing", Status: payment.ProviderStatusProcessing, Amount: order.PayAmount},
		},
		cancelErr: errors.New("checkout session can no longer be expired"),
	}
	registry := payment.NewRegistry()
	registry.Register(provider)
	svc := paymenttestkit.Lifecycle(client, registry, nil, nil, nil, true)

	outcome, err := svc.CancelOrder(ctx, order.ID, order.UserID)
	require.Error(t, err)
	require.Equal(t, payment.LifecycleCheckPaidResultProcessing, outcome)
	require.Equal(t, 2, provider.queryCalls)
	require.Equal(t, 1, provider.cancelCalls)

	reloaded, getErr := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, getErr)
	require.Equal(t, payment.OrderStatusProcessing, reloaded.Status)
}

// TestExpireTimedOutOrdersKeepsPendingWhenCloseAndRequeryRemainUncertain
// supplies a close error and two pending responses for an overdue order. Expiry
// count stays zero after one close and two queries, retaining pending state and
// cancellation-failure audit.
func TestExpireTimedOutOrdersKeepsPendingWhenCloseAndRequeryRemainUncertain(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)
	order := createPaymentOrderLifecycleOrder(t, ctx, client, payment.OrderStatusPending, time.Now().Add(-time.Minute))

	provider := &paymentOrderLifecycleQueryProvider{
		responses: []*payment.QueryOrderResponse{
			{TradeNo: order.OutTradeNo, Status: payment.ProviderStatusPending},
			{TradeNo: order.OutTradeNo, Status: payment.ProviderStatusPending},
		},
		cancelErr: errors.New("temporary stripe error"),
	}
	registry := payment.NewRegistry()
	registry.Register(provider)
	svc := paymenttestkit.Lifecycle(client, registry, nil, nil, nil, true)

	expired, err := svc.ExpireTimedOutOrders(ctx)
	require.NoError(t, err)
	require.Zero(t, expired)
	require.Equal(t, 2, provider.queryCalls)
	require.Equal(t, 1, provider.cancelCalls)

	reloaded, getErr := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, getErr)
	require.Equal(t, payment.OrderStatusPending, reloaded.Status)
	require.True(t, svc.HasAuditLog(ctx, order.ID, "PAYMENT_CANCEL_FAILED"))
}

// TestExpireTimedOutOrdersBacksOffAfterProviderFailure scans one overdue order
// using supplied times before and after the retry delay. Query failure retains
// pending state, skips the intermediate provider attempt, and permits the later
// query.
func TestExpireTimedOutOrdersBacksOffAfterProviderFailure(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)
	now := time.Now()
	order := createPaymentOrderLifecycleOrder(t, ctx, client, payment.OrderStatusPending, now.Add(-time.Minute))
	provider := &paymentOrderLifecycleQueryProvider{queryErr: errors.New("upstream unavailable")}
	registry := payment.NewRegistry()
	registry.Register(provider)
	svc := paymenttestkit.Lifecycle(client, registry, nil, nil, nil, true)

	expired, err := svc.ExpireTimedOutOrdersAt(ctx, now)
	require.NoError(t, err)
	require.Zero(t, expired)
	require.Equal(t, 1, provider.queryCalls)

	// Inspect the same overdue order inside its failure backoff, then after
	// the configured delay; query counts distinguish a skipped scan from
	// another provider attempt.
	expired, err = svc.ExpireTimedOutOrdersAt(ctx, now.Add(time.Minute))
	require.NoError(t, err)
	require.Zero(t, expired)
	require.Equal(t, 1, provider.queryCalls)

	expired, err = svc.ExpireTimedOutOrdersAt(ctx, now.Add(payment.LifecyclePaymentExpiryRetryDelay+time.Minute))
	require.NoError(t, err)
	require.Zero(t, expired)
	require.Equal(t, 2, provider.queryCalls)

	reloaded, getErr := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, getErr)
	require.Equal(t, payment.OrderStatusPending, reloaded.Status)
}

// TestReconcileProcessingOrdersFinalizesFailureAndAuditsStaleOnlyOnce
// repeatedly scans an aged processing order before changing its response to
// failed. It retains one stale audit, then expires the order with a failure
// audit and zero paid recoveries.
func TestReconcileProcessingOrdersFinalizesFailureAndAuditsStaleOnlyOnce(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)
	staleOrder := createPaymentOrderLifecycleOrder(t, ctx, client, payment.OrderStatusProcessing, time.Now().Add(time.Hour))
	// Move the processing timestamp beyond the stale threshold while
	// keeping the order's expiry in the future so repeated scans exercise
	// the stale audit path.
	staleOrder, err := client.PaymentOrder.UpdateOneID(staleOrder.ID).
		SetUpdatedAt(time.Now().Add(-payment.LifecycleProcessingStaleAfter - time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	provider := &paymentOrderLifecycleQueryProvider{
		resp: &payment.QueryOrderResponse{TradeNo: "pi_stale", Status: payment.ProviderStatusProcessing, Amount: staleOrder.PayAmount},
	}
	registry := payment.NewRegistry()
	registry.Register(provider)
	svc := paymenttestkit.Lifecycle(client, registry, nil, nil, nil, true)

	for range 2 {
		recovered, err := svc.ReconcileProcessingOrders(ctx)
		require.NoError(t, err)
		require.Zero(t, recovered)
	}
	staleAuditCount, err := client.PaymentAuditLog.Query().Where(
		paymentauditlog.OrderIDEQ(strconv.FormatInt(staleOrder.ID, 10)),
		paymentauditlog.ActionEQ("PAYMENT_PROCESSING_STALE"),
	).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, staleAuditCount)

	provider.resp = &payment.QueryOrderResponse{TradeNo: "pi_failed", Status: payment.ProviderStatusFailed}
	recovered, err := svc.ReconcileProcessingOrders(ctx)
	require.NoError(t, err)
	require.Zero(t, recovered)
	reloaded, err := client.PaymentOrder.Get(ctx, staleOrder.ID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusExpired, reloaded.Status)
	require.True(t, svc.HasAuditLog(ctx, staleOrder.ID, "PAYMENT_FAILED"))
}

// TestReconcileProcessingOrdersRecoversPaidOrderAfterServiceRestart constructs
// a lifecycle facade over seeded processing order and assignment-audit rows in
// SQLite. Reconciliation queries the checkout-session reference and completes
// the order with the returned paid trade number.
func TestReconcileProcessingOrdersRecoversPaidOrderAfterServiceRestart(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	ensurePaymentAuditOrderActionUniqueIndex(t, ctx, client)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, payment.OrderStatusProcessing, time.Now())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetPaymentType(payment.TypeStripe).
		SetPaymentTradeNo("cs_restart_recovery").
		Save(ctx)
	require.NoError(t, err)
	_, err = client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(order.ID, 10)).
		SetAction("SUBSCRIPTION_ASSIGNED").
		SetDetail(`{"planID":100}`).
		SetOperator("system").
		Save(ctx)
	require.NoError(t, err)

	provider := &paymentOrderLifecycleQueryProvider{
		key: payment.TypeStripe,
		resp: &payment.QueryOrderResponse{
			TradeNo: "pi_restart_recovery",
			Status:  payment.ProviderStatusPaid,
			Amount:  order.PayAmount,
			Metadata: map[string]string{
				"currency": payment.DefaultPaymentCurrency,
			},
		},
	}
	registry := payment.NewRegistry()
	registry.Register(provider)
	svc := paymenttestkit.Lifecycle(client, registry, nil, &billing.SubscriptionService{}, nil, true)

	recovered, err := svc.ReconcileProcessingOrders(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, recovered)
	require.Equal(t, "cs_restart_recovery", provider.lastQueryTradeNo)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusCompleted, reloaded.Status)
	require.Equal(t, "pi_restart_recovery", reloaded.PaymentTradeNo)
}

// TestReconcilePageIDsCyclesAcrossEntireBacklog supplies forty-five IDs and
// three cursor values to the pure page selector. Each page respects the
// reconcile limit and their collected IDs cover the supplied backlog.
func TestReconcilePageIDsCyclesAcrossEntireBacklog(t *testing.T) {
	t.Parallel()

	// Use more IDs than two full pages can hold.
	ids := make([]int64, 45)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	seen := make(map[int64]bool, len(ids))
	for cursor := range uint64(3) {
		page := payment.ReconcilePageIDs(ids, cursor, payment.LifecycleProcessingReconcileLimit)
		require.LessOrEqual(t, len(page), payment.LifecycleProcessingReconcileLimit)
		for _, id := range page {
			seen[id] = true
		}
	}
	require.Len(t, seen, len(ids))
}

// TestReconcileProcessingOrdersCyclesPastFirstBatch stores one more processing
// order than a reconcile batch can hold. Two scans query every distinct order
// reference while the configured processing response yields zero paid
// recoveries.
func TestReconcileProcessingOrdersCyclesPastFirstBatch(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)
	provider := &paymentOrderLifecycleQueryProvider{
		resp: &payment.QueryOrderResponse{Status: payment.ProviderStatusProcessing},
	}
	registry := payment.NewRegistry()
	registry.Register(provider)
	svc := paymenttestkit.Lifecycle(client, registry, nil, nil, nil, true)

	for range payment.LifecycleProcessingReconcileLimit + 1 {
		order := createPaymentOrderLifecycleOrder(t, ctx, client, payment.OrderStatusProcessing, time.Now().Add(time.Hour))
		_, err := client.PaymentOrder.UpdateOneID(order.ID).
			SetPaymentTradeNo(order.OutTradeNo).
			Save(ctx)
		require.NoError(t, err)
	}

	for range 2 {
		recovered, err := svc.ReconcileProcessingOrdersAt(ctx, time.Now())
		require.NoError(t, err)
		require.Zero(t, recovered)
	}
	uniqueTradeNos := make(map[string]bool, len(provider.queryTradeNos))
	for _, tradeNo := range provider.queryTradeNos {
		uniqueTradeNos[tradeNo] = true
	}
	require.Len(t, uniqueTradeNos, payment.LifecycleProcessingReconcileLimit+1)
}

// TestReconcilePaidFulfillmentOrdersRetriesAfterQueryRecoveryFailure first
// records paid-query recovery failure with no subscription service. A
// reconstructed facade and later retry time complete the same SQLite order
// through its retained assignment audit.
func TestReconcilePaidFulfillmentOrdersRetriesAfterQueryRecoveryFailure(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	ensurePaymentAuditOrderActionUniqueIndex(t, ctx, client)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, payment.OrderStatusProcessing, time.Now())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetPaymentType(payment.TypeStripe).
		SetPaymentTradeNo("cs_fulfillment_retry").
		Save(ctx)
	require.NoError(t, err)
	_, err = client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(order.ID, 10)).
		SetAction("SUBSCRIPTION_ASSIGNED").
		SetDetail(`{"planID":100}`).
		SetOperator("system").
		Save(ctx)
	require.NoError(t, err)

	provider := &paymentOrderLifecycleQueryProvider{
		key: payment.TypeStripe,
		resp: &payment.QueryOrderResponse{
			TradeNo: "pi_fulfillment_retry",
			Status:  payment.ProviderStatusPaid,
			Amount:  order.PayAmount,
			Metadata: map[string]string{
				"currency": payment.DefaultPaymentCurrency,
			},
		},
	}
	registry := payment.NewRegistry()
	registry.Register(provider)
	svc := paymenttestkit.Lifecycle(client, registry, nil, nil, nil, true)

	recovered, err := svc.ReconcileProcessingOrdersAt(ctx, time.Now())
	require.NoError(t, err)
	require.Zero(t, recovered)
	failed, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusFailed, failed.Status)
	require.NotNil(t, failed.PaidAt)

	// Supply the missing subscription dependency on the same SQLite client
	// and registry, then advance beyond fulfillment backoff so the retained
	// assignment audit can complete recovery.
	svc = paymenttestkit.Lifecycle(client, registry, nil, &billing.SubscriptionService{}, nil, true)
	recovered, err = svc.ReconcilePaidFulfillmentOrdersAt(ctx, time.Now().Add(payment.LifecycleFulfillmentRetryDelay+time.Minute))
	require.NoError(t, err)
	require.Equal(t, 1, recovered)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusCompleted, reloaded.Status)
}

// TestReconcilePaidFulfillmentOrdersRecoversOnlyExpiredLease stores fresh and
// aged recharging orders with assigned-subscription audits. Reconciliation
// completes the aged order, reports one recovery, and leaves the fresh order
// recharging.
func TestReconcilePaidFulfillmentOrdersRecoversOnlyExpiredLease(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	ensurePaymentAuditOrderActionUniqueIndex(t, ctx, client)
	now := time.Now()
	staleOrder := createPaymentFulfillmentSubscriptionOrder(
		t,
		ctx,
		client,
		payment.OrderStatusRecharging,
		now.Add(-payment.FulfillmentLeaseDuration-time.Minute),
	)
	freshOrder := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, payment.OrderStatusRecharging, now)
	for _, order := range []*dbent.PaymentOrder{staleOrder, freshOrder} {
		_, err := client.PaymentAuditLog.Create().
			SetOrderID(strconv.FormatInt(order.ID, 10)).
			SetAction("SUBSCRIPTION_ASSIGNED").
			SetDetail(`{"planID":100}`).
			SetOperator("system").
			Save(ctx)
		require.NoError(t, err)
	}

	svc := paymenttestkit.Lifecycle(client, nil, nil, &billing.SubscriptionService{}, nil, false)
	recovered, err := svc.ReconcilePaidFulfillmentOrdersAt(ctx, now)
	require.NoError(t, err)
	require.Equal(t, 1, recovered)

	reloadedStale, err := client.PaymentOrder.Get(ctx, staleOrder.ID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusCompleted, reloadedStale.Status)
	reloadedFresh, err := client.PaymentOrder.Get(ctx, freshOrder.ID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusRecharging, reloadedFresh.Status)
}

// TestReconcilePendingPaymentOrdersBackfillsPaidOrder supplies a paid WeChat
// response for a pending SQLite order. Reconciliation reports one recovery,
// completes the order, records its fixture credit and redemption, and makes no
// cancellation call.
func TestReconcilePendingPaymentOrdersBackfillsPaidOrder(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)

	user, err := client.User.Create().
		SetEmail("wxpay-reconcile@example.com").
		SetPasswordHash("hash").
		SetUsername("wxpay-reconcile-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(50).
		SetPayAmount(50).
		SetFeeRate(0).
		SetRechargeCode("WXPAY-RECONCILE").
		SetOutTradeNo("sub2_wxpay_reconcile").
		SetPaymentType(payment.TypeWxpay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(payment.OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	userRepo := &fulfillmentBalance{
		getByIDUser: &billing.UserSummary{
			ID:       user.ID,
			Email:    user.Email,
			Username: user.Username,
			Balance:  0,
		},
	}
	userRepo.updateBalanceFn = func(ctx context.Context, id int64, amount float64) error {
		require.Equal(t, user.ID, id)
		if userRepo.getByIDUser != nil {
			userRepo.getByIDUser.Balance += amount
		}
		return nil
	}
	redeemRepo := &paymentOrderLifecycleRedeemRepo{
		codesByCode: map[string]*billing.RedeemCode{
			order.RechargeCode: {
				ID:     1,
				Code:   order.RechargeCode,
				Type:   billing.RedeemTypeBalance,
				Value:  order.Amount,
				Status: billing.StatusUnused,
			},
		},
	}
	redeemService := newFulfillmentRedeemService(
		redeemRepo,
		userRepo,

		client,
	)
	registry := payment.NewRegistry()
	provider := &paymentOrderLifecycleQueryProvider{
		key: payment.TypeWxpay,
		resp: &payment.QueryOrderResponse{
			TradeNo: "wxpay-upstream-trade-123",
			Status:  payment.ProviderStatusPaid,
			Amount:  50,
			Metadata: map[string]string{
				"trade_state": "SUCCESS",
			},
		},
	}
	registry.Register(provider)

	svc := paymenttestkit.Lifecycle(client,
		registry,
		redeemService, nil, nil, true)

	recovered, err := svc.ReconcilePendingPaymentOrders(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, recovered)
	require.Equal(t, order.OutTradeNo, provider.lastQueryTradeNo)
	require.Zero(t, provider.cancelCalls)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusCompleted, reloaded.Status)
	require.Equal(t, "wxpay-upstream-trade-123", reloaded.PaymentTradeNo)
	require.Equal(t, 50.0, userRepo.getByIDUser.Balance)
	require.Len(t, redeemRepo.useCalls, 1)
}

// TestReconcilePendingPaymentOrdersQueriesAlipayOrder supplies a pending Alipay
// response to the pending-order scan. The scan reports zero recoveries and
// makes one provider query using that order's local out-trade reference.
func TestReconcilePendingPaymentOrdersQueriesAlipayOrder(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)

	user, err := client.User.Create().
		SetEmail("alipay-reconcile@example.com").
		SetPasswordHash("hash").
		SetUsername("alipay-reconcile-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(50).
		SetPayAmount(50).
		SetFeeRate(0).
		SetRechargeCode("ALIPAY-RECONCILE").
		SetOutTradeNo("sub2_alipay_reconcile").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(payment.OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	registry := payment.NewRegistry()
	provider := &paymentOrderLifecycleQueryProvider{
		key: payment.TypeAlipay,
		resp: &payment.QueryOrderResponse{
			TradeNo: order.OutTradeNo,
			Status:  payment.ProviderStatusPending,
		},
	}
	registry.Register(provider)

	svc := paymenttestkit.Lifecycle(client,
		registry, nil, nil, nil, true)

	recovered, err := svc.ReconcilePendingPaymentOrders(ctx)
	require.NoError(t, err)
	require.Zero(t, recovered)
	require.Equal(t, 1, provider.queryCalls)
	require.Equal(t, order.OutTradeNo, provider.lastQueryTradeNo)
}

// TestVerifyOrderByOutTradeNoUsesOutTradeNoWhenPaymentTradeNoAlreadyExistsForAlipayUpstreamRegression
// supplies an existing provider trade number on a pending Alipay order. The
// regression checks the queried local out-trade reference and returned provider
// trade number.
func TestVerifyOrderByOutTradeNoUsesOutTradeNoWhenPaymentTradeNoAlreadyExistsForAlipayUpstreamRegression(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)

	user, err := client.User.Create().
		SetEmail("checkpaid-existing-trade@example.com").
		SetPasswordHash("hash").
		SetUsername("checkpaid-existing-trade-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("CHECKPAID-EXISTING-TRADE-NO").
		SetOutTradeNo("sub2_checkpaid_use_out_trade_no").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("upstream-trade-existing").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(payment.OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	userRepo := &fulfillmentBalance{
		getByIDUser: &billing.UserSummary{
			ID:       user.ID,
			Email:    user.Email,
			Username: user.Username,
			Balance:  0,
		},
	}
	userRepo.updateBalanceFn = func(ctx context.Context, id int64, amount float64) error {
		require.Equal(t, user.ID, id)
		if userRepo.getByIDUser != nil {
			userRepo.getByIDUser.Balance += amount
		}
		return nil
	}
	redeemRepo := &paymentOrderLifecycleRedeemRepo{
		codesByCode: map[string]*billing.RedeemCode{
			order.RechargeCode: {
				ID:     1,
				Code:   order.RechargeCode,
				Type:   billing.RedeemTypeBalance,
				Value:  order.Amount,
				Status: billing.StatusUnused,
			},
		},
	}
	redeemService := newFulfillmentRedeemService(
		redeemRepo,
		userRepo,

		client,
	)
	registry := payment.NewRegistry()
	provider := &paymentOrderLifecycleQueryProvider{
		resp: &payment.QueryOrderResponse{
			TradeNo: "upstream-trade-existing",
			Status:  payment.ProviderStatusPaid,
			Amount:  88,
		},
	}
	registry.Register(provider)

	svc := paymenttestkit.Lifecycle(client,
		registry,
		redeemService, nil, nil, true)

	got, err := svc.VerifyOrderByOutTradeNo(ctx, order.OutTradeNo, user.ID)
	require.NoError(t, err)
	require.Equal(t, order.OutTradeNo, provider.lastQueryTradeNo)
	require.Equal(t, "upstream-trade-existing", got.PaymentTradeNo)
}

// TestPaymentOrderAllowsRegistryFallbackOnlyForLegacyOrdersWithoutPinnedProviderState
// supplies three order representations to the registry predicate. A plain
// legacy order permits lookup, while an instance ID or version-two snapshot
// disables it.
func TestPaymentOrderAllowsRegistryFallbackOnlyForLegacyOrdersWithoutPinnedProviderState(t *testing.T) {
	t.Parallel()

	require.True(t, payment.PaymentOrderAllowsRegistryFallback(paymentpostgres.OrderFromEntity(&dbent.PaymentOrder{
		PaymentType: payment.TypeAlipay,
	})))

	instanceID := "12"
	require.False(t, payment.PaymentOrderAllowsRegistryFallback(paymentpostgres.OrderFromEntity(&dbent.PaymentOrder{
		PaymentType:        payment.TypeAlipay,
		ProviderInstanceID: &instanceID,
	})))

	require.False(t, payment.PaymentOrderAllowsRegistryFallback(paymentpostgres.OrderFromEntity(&dbent.PaymentOrder{
		PaymentType: payment.TypeAlipay,
		ProviderSnapshot: map[string]any{
			"schema_version":       2,
			"provider_instance_id": "12",
		},
	})))
}

// TestPaymentOrderQueryReferenceUsesOutTradeNoForOfficialProviders supplies a
// WeChat order with distinct local and provider references. Both exercised
// official-provider selections use the local out-trade reference for their
// query.
func TestPaymentOrderQueryReferenceUsesOutTradeNoForOfficialProviders(t *testing.T) {
	t.Parallel()

	order := &dbent.PaymentOrder{
		PaymentType:    payment.TypeWxpay,
		OutTradeNo:     "sub2_out_trade_no",
		PaymentTradeNo: "wx-transaction-id",
	}

	require.Equal(t, "sub2_out_trade_no", payment.PaymentOrderQueryReference(paymentpostgres.OrderFromEntity(order), &paymentOrderLifecycleQueryProvider{}))
	require.Equal(t, "sub2_out_trade_no", payment.PaymentOrderQueryReference(paymentpostgres.OrderFromEntity(order), paymenttestkit.StaticProvider{
		Key: payment.TypeWxpay,
	}))
}

// TestPaymentOrderQueryReferenceUsesInvoiceIDForStripeOrders supplies distinct
// invoice, payment-intent, and local references. Selection returns the Stripe
// invoice ID with either a configured Stripe provider or nil provider.
func TestPaymentOrderQueryReferenceUsesInvoiceIDForStripeOrders(t *testing.T) {
	t.Parallel()

	invoiceID := "in_123"
	providerKey := payment.TypeStripe
	order := &dbent.PaymentOrder{
		PaymentType:      payment.TypeStripe,
		OutTradeNo:       "sub2_out_trade_no",
		PaymentTradeNo:   "pi_123",
		PaymentInvoiceID: &invoiceID,
		ProviderKey:      &providerKey,
	}

	require.Equal(t, "in_123", payment.PaymentOrderQueryReference(paymentpostgres.OrderFromEntity(order), paymenttestkit.StaticProvider{
		Key: payment.TypeStripe,
	}))
	require.Equal(t, "in_123", payment.PaymentOrderQueryReference(paymentpostgres.OrderFromEntity(order), nil))
}

// TestPaymentOrderQueryReferenceKeepsCheckoutSessionForProcessingStripeOrders
// supplies a Stripe checkout-session reference alongside an invoice ID. The
// query helper retains that session reference for the supplied order instead of
// selecting its invoice.
func TestPaymentOrderQueryReferenceKeepsCheckoutSessionForProcessingStripeOrders(t *testing.T) {
	t.Parallel()

	invoiceID := "in_123"
	providerKey := payment.TypeStripe
	order := &dbent.PaymentOrder{
		PaymentType:      payment.TypeStripe,
		OutTradeNo:       "sub2_out_trade_no",
		PaymentTradeNo:   "cs_123",
		PaymentInvoiceID: &invoiceID,
		ProviderKey:      &providerKey,
	}

	require.Equal(t, "cs_123", payment.PaymentOrderQueryReference(paymentpostgres.OrderFromEntity(order), paymenttestkit.StaticProvider{
		Key: payment.TypeStripe,
	}))
}

// TestPaymentOrderQueryReferenceFallsBackToTradeNoForLegacyStripeOrders
// supplies a Stripe order with a legacy payment-intent reference and no
// invoice. The query helper returns the stored provider trade number.
func TestPaymentOrderQueryReferenceFallsBackToTradeNoForLegacyStripeOrders(t *testing.T) {
	t.Parallel()

	order := &dbent.PaymentOrder{
		PaymentType:    payment.TypeStripe,
		OutTradeNo:     "sub2_out_trade_no",
		PaymentTradeNo: "pi_legacy",
	}

	require.Equal(t, "pi_legacy", payment.PaymentOrderQueryReference(paymentpostgres.OrderFromEntity(order), paymenttestkit.StaticProvider{
		Key: payment.TypeStripe,
	}))
}
