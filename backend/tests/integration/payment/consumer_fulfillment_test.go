package payment_test

import (
	"context"
	"errors"
	"maps"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/paymentauditlog"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	paymentpostgres "github.com/TokenFlux/TokenRouter/internal/payment/postgres"
	paymenttestkit "github.com/TokenFlux/TokenRouter/internal/payment/testkit"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	sqlitetest "github.com/TokenFlux/TokenRouter/internal/testutil/sqlite"
)

// Paid-order recovery must distinguish an already applied entitlement from an
// unfinished order transition. SQLite cases seed redemption usage, subscription
// assignments, or success audits, then observe completion and repeated-work
// suppression. The recorded redeemer exposes lookup and redemption
// observations, while the affiliate stub captures calculated rebates and
// supplies a configured accrued amount for cap selection.
//
// Subscription checks use the in-memory billing repository and source-order IDs
// to detect an extra extension. Lease checks age and reacquire SQLite records
// sequentially, then submit the preceding version to completion and failure
// operations. These fixtures inspect stored order and audit effects alongside
// captured entitlement calls; their map-based affiliate callback runs directly
// and supplies no database lock.

// These compile-time assignments require the affiliate and settings fixtures to
// satisfy the repository interfaces consumed by payment fulfillment and rebate
// settings.
var (
	_ promotion.AffiliateRepository = (*paymentFulfillmentAffiliateRepoStub)(nil)
	_ settings.Repository           = (*paymentFulfillmentSettingRepoStub)(nil)
)

// paymentFulfillmentAffiliateAccrueCall records the inputs captured during one
// rebate attempt.
type paymentFulfillmentAffiliateAccrueCall struct {
	inviterID     int64   // rebate recipient user ID
	inviteeUserID int64   // purchasing invitee user ID
	amount        float64 // recorded affiliate rebate amount
	freezeHours   int     // rebate freeze duration in hours
	// sourceOrderID holds a copied order ID so later changes through the
	// original pointer cannot alter the recorded rebate input; nil retains
	// an absent source.
	sourceOrderID *int64 // copied source order identity
}

// paymentFulfillmentAffiliateRepoStub supplies profile and cap inputs and
// records rebate attempts.
type paymentFulfillmentAffiliateRepoStub struct {
	// inviteeSummary and inviterSummary are matched by user ID, with
	// inviteeSummary checked first. Matching reads shallow-copy their
	// structs; nested references remain shared.
	inviteeSummary *promotion.AffiliateSummary // configured purchasing user profile
	inviterSummary *promotion.AffiliateSummary // configured rebate recipient profile
	// accruedRebate supplies the cap calculation regardless of requested
	// users; AccrueQuota records calls but leaves this configured amount
	// unchanged for later reads.
	accruedRebate float64 // supplied earlier rebate amount
	// accrueCalls appends sequential observations without synchronization;
	// tests inspect the shared slice after the synchronous affiliate
	// callback returns.
	accrueCalls []paymentFulfillmentAffiliateAccrueCall // captured rebate attempt inputs
}

// paymentFulfillmentSettingRepoStub maintains mutable settings for sequential
// affiliate calculations.
type paymentFulfillmentSettingRepoStub struct {
	// values is retained across reads and writes, and GetAll returns it
	// directly; callers serialize access to this unsynchronized settings
	// map and its borrowed result.
	values map[string]string // mutable configured setting values
}

// redeemCodeRepoStub supplies code pointers and records fulfillment redemption
// requests.
type redeemCodeRepoStub struct {
	// codesByCode and useCalls are shared with recordedRedeemer for
	// sequential observations. Lookups return supplied pointers, and
	// redemption appends a code even when that key is missing.
	codesByCode map[string]*billing.RedeemCode // configured borrowed code pointers
	useCalls    []string                       // observed redemption code strings
}

// recordedRedeemer captures fulfillment reads and redemption calls using the
// supplied repository.
type recordedRedeemer struct {
	// FulfillmentRedeemer stays nil; only GetByCode and Redeem are
	// overridden, and an attempted promoted CreateCode call panics instead
	// of creating a credential.
	payment.FulfillmentRedeemer // unsupported code creation delegate
	// repo supplies borrowed code pointers and shared redemption call
	// storage; callers initialize it before using either recording override
	// in fulfillment.
	repo *redeemCodeRepoStub // retained redemption observation store
}

// EnsureUserAffiliate returns a shallow copy of a matching configured invitee
// or inviter summary, checking the invitee first. Other user IDs receive a new
// synthetic profile; nested references in configured summaries remain shared,
// and context is ignored.
func (r *paymentFulfillmentAffiliateRepoStub) EnsureUserAffiliate(_ context.Context, userID int64) (*promotion.AffiliateSummary, error) {
	switch {
	case r.inviteeSummary != nil && r.inviteeSummary.UserID == userID:
		cp := *r.inviteeSummary
		return &cp, nil
	case r.inviterSummary != nil && r.inviterSummary.UserID == userID:
		cp := *r.inviterSummary
		return &cp, nil
	default:
		// Supply a synthetic profile when neither configured user
		// matches so incidental affiliate reads can continue with the
		// requested user ID and known creation time.
		return &promotion.AffiliateSummary{UserID: userID, AffCode: "AFFTEST", CreatedAt: time.Now().Add(-time.Hour)}, nil
	}
}

// GetAffiliateByCode panics when fulfillment unexpectedly requests this
// unconfigured affiliate operation.
func (r *paymentFulfillmentAffiliateRepoStub) GetAffiliateByCode(context.Context, string) (*promotion.AffiliateSummary, error) {
	panic("unexpected GetAffiliateByCode call")
}

// BindInviter panics when fulfillment unexpectedly requests this unconfigured
// affiliate operation.
func (r *paymentFulfillmentAffiliateRepoStub) BindInviter(context.Context, int64, int64) (bool, error) {
	panic("unexpected BindInviter call")
}

// AccrueQuota captures rebate inputs and returns true with no error. It copies
// a nonnil source-order ID before recording the call, leaves nil IDs nil, and
// keeps the configured accruedRebate unchanged.
func (r *paymentFulfillmentAffiliateRepoStub) AccrueQuota(_ context.Context, inviterID, inviteeUserID int64, amount float64, freezeHours int, sourceOrderID *int64) (bool, error) {
	var sourceCopy *int64
	// Copy the optional order identity before appending the observation so
	// a caller changing its input pointer cannot rewrite this earlier
	// recorded rebate attempt.
	if sourceOrderID != nil {
		v := *sourceOrderID
		sourceCopy = &v
	}
	r.accrueCalls = append(r.accrueCalls, paymentFulfillmentAffiliateAccrueCall{
		inviterID:     inviterID,
		inviteeUserID: inviteeUserID,
		amount:        amount,
		freezeHours:   freezeHours,
		sourceOrderID: sourceCopy,
	})
	return true, nil
}

// GetAccruedRebateFromInvitee returns the configured accruedRebate for cap
// calculation. It ignores both user IDs and context; recording a later
// AccrueQuota call does not change this supplied amount.
func (r *paymentFulfillmentAffiliateRepoStub) GetAccruedRebateFromInvitee(context.Context, int64, int64) (float64, error) {
	return r.accruedRebate, nil
}

// ThawFrozenQuota panics when fulfillment unexpectedly requests this
// unconfigured affiliate operation.
func (r *paymentFulfillmentAffiliateRepoStub) ThawFrozenQuota(context.Context, int64) (float64, error) {
	panic("unexpected ThawFrozenQuota call")
}

// TransferQuotaToBalance panics when fulfillment unexpectedly requests this
// unconfigured affiliate operation.
func (r *paymentFulfillmentAffiliateRepoStub) TransferQuotaToBalance(context.Context, int64) (float64, float64, error) {
	panic("unexpected TransferQuotaToBalance call")
}

// ListInvitees panics when fulfillment unexpectedly requests this unconfigured
// affiliate operation.
func (r *paymentFulfillmentAffiliateRepoStub) ListInvitees(context.Context, int64, int) ([]promotion.AffiliateInvitee, error) {
	panic("unexpected ListInvitees call")
}

// UpdateUserAffCode panics when fulfillment unexpectedly requests this
// unconfigured affiliate operation.
func (r *paymentFulfillmentAffiliateRepoStub) UpdateUserAffCode(context.Context, int64, string) error {
	panic("unexpected UpdateUserAffCode call")
}

// ResetUserAffCode panics when fulfillment unexpectedly requests this
// unconfigured affiliate operation.
func (r *paymentFulfillmentAffiliateRepoStub) ResetUserAffCode(context.Context, int64) (string, error) {
	panic("unexpected ResetUserAffCode call")
}

// SetUserRebateRate panics when fulfillment unexpectedly requests this
// unconfigured affiliate operation.
func (r *paymentFulfillmentAffiliateRepoStub) SetUserRebateRate(context.Context, int64, *float64) error {
	panic("unexpected SetUserRebateRate call")
}

// BatchSetUserRebateRate panics when fulfillment unexpectedly requests this
// unconfigured affiliate operation.
func (r *paymentFulfillmentAffiliateRepoStub) BatchSetUserRebateRate(context.Context, []int64, *float64) error {
	panic("unexpected BatchSetUserRebateRate call")
}

// ListUsersWithCustomSettings panics when fulfillment unexpectedly requests
// this unconfigured affiliate operation.
func (r *paymentFulfillmentAffiliateRepoStub) ListUsersWithCustomSettings(context.Context, promotion.AffiliateAdminFilter) ([]promotion.AffiliateAdminEntry, int64, error) {
	panic("unexpected ListUsersWithCustomSettings call")
}

// ListAffiliateInviteRecords panics when fulfillment unexpectedly requests this
// unconfigured affiliate operation.
func (r *paymentFulfillmentAffiliateRepoStub) ListAffiliateInviteRecords(context.Context, promotion.AffiliateRecordFilter) ([]promotion.AffiliateInviteRecord, int64, error) {
	panic("unexpected ListAffiliateInviteRecords call")
}

// ListAffiliateRebateRecords panics when fulfillment unexpectedly requests this
// unconfigured affiliate operation.
func (r *paymentFulfillmentAffiliateRepoStub) ListAffiliateRebateRecords(context.Context, promotion.AffiliateRecordFilter) ([]promotion.AffiliateRebateRecord, int64, error) {
	panic("unexpected ListAffiliateRebateRecords call")
}

// ListAffiliateTransferRecords panics when fulfillment unexpectedly requests
// this unconfigured affiliate operation.
func (r *paymentFulfillmentAffiliateRepoStub) ListAffiliateTransferRecords(context.Context, promotion.AffiliateRecordFilter) ([]promotion.AffiliateTransferRecord, int64, error) {
	panic("unexpected ListAffiliateTransferRecords call")
}

// GetAffiliateUserOverview panics when fulfillment unexpectedly requests this
// unconfigured affiliate operation.
func (r *paymentFulfillmentAffiliateRepoStub) GetAffiliateUserOverview(context.Context, int64) (*promotion.AffiliateUserOverview, error) {
	panic("unexpected GetAffiliateUserOverview call")
}

// Get reports ErrSettingNotFound with a nil setting for every requested key. It
// ignores context and the configured values map; callers use GetValue or
// GetMultiple to read that fixture's configured strings.
func (s *paymentFulfillmentSettingRepoStub) Get(context.Context, string) (*settings.Setting, error) {
	return nil, settings.ErrSettingNotFound
}

// GetValue reads the supplied map and returns ErrSettingNotFound for an absent
// key or nil map. A present empty string succeeds, so callers can distinguish
// it from missing configuration.
func (s *paymentFulfillmentSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if s.values == nil {
		return "", settings.ErrSettingNotFound
	}
	value, ok := s.values[key]
	if !ok {
		return "", settings.ErrSettingNotFound
	}
	return value, nil
}

// Set stores one value, allocating the fixture map when it is nil. It reports
// success and ignores context; other readers sharing the fixture observe the
// map change.
func (s *paymentFulfillmentSettingRepoStub) Set(_ context.Context, key, value string) error {
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	return nil
}

// GetMultiple returns a new map containing every requested key. Missing entries
// become empty strings with no error, so callers receive a different absence
// shape from GetValue.
func (s *paymentFulfillmentSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		out[key] = s.values[key]
	}
	return out, nil
}

// SetMultiple merges supplied values into the retained map, allocating it when
// necessary. Keys absent from the input remain configured; the fixture ignores
// context and reports success.
func (s *paymentFulfillmentSettingRepoStub) SetMultiple(_ context.Context, values map[string]string) error {
	if s.values == nil {
		s.values = map[string]string{}
	}
	maps.Copy(s.values, values)
	return nil
}

// GetAll returns the retained settings map with no error, including nil when
// uninitialized. The map is shared, so callers modifying its entries also
// change subsequent fixture reads.
func (s *paymentFulfillmentSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	return s.values, nil
}

// Delete removes the supplied key from the retained settings map and reports
// success. Missing keys and an uninitialized map also succeed; context is
// ignored, and later fixture reads observe any removal.
func (s *paymentFulfillmentSettingRepoStub) Delete(_ context.Context, key string) error {
	delete(s.values, key)
	return nil
}

// TestParseLegacyPaymentOrderID supplies a prefixed reference with a
// lookup-not-found indication. Parsing accepts that numeric ID but rejects a
// bare number and the prefixed reference paired with a generic database-error
// indication.
func TestParseLegacyPaymentOrderID(t *testing.T) {
	t.Parallel()

	oid, ok := payment.ParseLegacyPaymentOrderID("sub2_42", dbent.IsNotFound(&dbent.NotFoundError{}))
	assert.True(t, ok)
	assert.EqualValues(t, 42, oid)

	_, ok = payment.ParseLegacyPaymentOrderID("42", dbent.IsNotFound(&dbent.NotFoundError{}))
	assert.False(t, ok)

	_, ok = payment.ParseLegacyPaymentOrderID("sub2_42", dbent.IsNotFound(errors.New("db down")))
	assert.False(t, ok)
}

// TestPaymentSuccessRecoversNonPendingOrdersIdempotently seeds
// assigned-subscription audits for processing, expired, and cancelled SQLite
// orders. Repeated success notifications complete each order and leave exactly
// one recovery audit.
func TestPaymentSuccessRecoversNonPendingOrdersIdempotently(t *testing.T) {
	statuses := []string{payment.OrderStatusProcessing, payment.OrderStatusExpired, payment.OrderStatusCancelled}
	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			client := sqlitetest.NewClient(t)
			ensurePaymentAuditOrderActionUniqueIndex(t, ctx, client)
			order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, status, time.Now())
			_, err := client.PaymentAuditLog.Create().
				SetOrderID(strconv.FormatInt(order.ID, 10)).
				SetAction("SUBSCRIPTION_ASSIGNED").
				SetDetail(`{"planID":100}`).
				SetOperator("system").
				Save(ctx)
			require.NoError(t, err)

			svc := paymenttestkit.Fulfillment(client, nil, &billing.SubscriptionService{}, nil)
			notification := &payment.PaymentNotification{
				TradeNo: "trade-recovered-" + status,
				OrderID: order.OutTradeNo,
				Amount:  order.PayAmount,
				Status:  payment.NotificationStatusSuccess,
			}
			require.NoError(t, svc.HandlePaymentNotification(ctx, notification, payment.TypeAlipay))
			require.NoError(t, svc.HandlePaymentNotification(ctx, notification, payment.TypeAlipay))

			reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, payment.OrderStatusCompleted, reloaded.Status)
			recoveredCount, err := client.PaymentAuditLog.Query().Where(
				paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)),
				paymentauditlog.ActionEQ("ORDER_RECOVERED"),
			).Count(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, recoveredCount)
		})
	}
}

// TestPaymentSuccessWinsOverOutOfOrderFailure submits Stripe failure, success,
// then the earlier failure for one SQLite order. The final order remains
// completed with the successful notification's provider trade number.
func TestPaymentSuccessWinsOverOutOfOrderFailure(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	ensurePaymentAuditOrderActionUniqueIndex(t, ctx, client)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, payment.OrderStatusPending, time.Now())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).SetPaymentType(payment.TypeStripe).Save(ctx)
	require.NoError(t, err)
	_, err = client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(order.ID, 10)).
		SetAction("SUBSCRIPTION_ASSIGNED").
		SetDetail(`{"planID":100}`).
		SetOperator("system").
		Save(ctx)
	require.NoError(t, err)

	svc := paymenttestkit.Fulfillment(client, nil, &billing.SubscriptionService{}, nil)
	failure := &payment.PaymentNotification{
		TradeNo: "trade-failed-first",
		OrderID: order.OutTradeNo,
		Status:  payment.ProviderStatusFailed,
		Metadata: map[string]string{
			"currency": payment.DefaultPaymentCurrency,
		},
	}
	require.NoError(t, svc.HandlePaymentNotification(ctx, failure, payment.TypeStripe))

	success := &payment.PaymentNotification{
		TradeNo: "trade-paid-late",
		OrderID: order.OutTradeNo,
		Amount:  order.PayAmount,
		Status:  payment.NotificationStatusSuccess,
		Metadata: map[string]string{
			"currency": payment.DefaultPaymentCurrency,
		},
	}
	require.NoError(t, svc.HandlePaymentNotification(ctx, success, payment.TypeStripe))
	require.NoError(t, svc.HandlePaymentNotification(ctx, failure, payment.TypeStripe))

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusCompleted, reloaded.Status)
	require.Equal(t, "trade-paid-late", reloaded.PaymentTradeNo)
}

// TestNonStripeFailureNotificationKeepsLegacyIgnoreBehavior supplies an Alipay
// failure notification to fulfillment with absent dependencies. Handling
// returns no error for the unknown order before those unconfigured dependencies
// are used.
func TestNonStripeFailureNotificationKeepsLegacyIgnoreBehavior(t *testing.T) {
	svc := paymenttestkit.Fulfillment(nil, nil, nil, nil)
	err := svc.HandlePaymentNotification(context.Background(), &payment.PaymentNotification{
		OrderID: "unknown-order",
		Status:  payment.ProviderStatusFailed,
	}, payment.TypeAlipay)
	require.NoError(t, err)
}

// TestRetryFulfillmentRejectsFreshRechargingLease retries a recently updated
// recharging order in SQLite. It returns CONFLICT and leaves that stored order
// recharging while its lease is still fresh.
func TestRetryFulfillmentRejectsFreshRechargingLease(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, payment.OrderStatusRecharging, time.Now())

	svc := paymenttestkit.Fulfillment(client, nil, nil, nil)
	err := svc.RetryFulfillment(ctx, order.ID)
	require.Error(t, err)
	require.Equal(t, "CONFLICT", apperror.Reason(err))

	reloaded, getErr := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, getErr)
	require.Equal(t, payment.OrderStatusRecharging, reloaded.Status)
}

// TestExecuteFulfillmentRecoversStaleRechargingLease ages a recharging SQLite
// order and seeds its subscription-assigned audit. Fulfillment takes over that
// stale lease and completes the order through the existing-assignment path.
func TestExecuteFulfillmentRecoversStaleRechargingLease(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	ensurePaymentAuditOrderActionUniqueIndex(t, ctx, client)
	order := createPaymentFulfillmentSubscriptionOrder(
		t,
		ctx,
		client,
		payment.OrderStatusRecharging,
		time.Now().Add(-payment.FulfillmentLeaseDuration-time.Minute),
	)
	_, err := client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(order.ID, 10)).
		SetAction("SUBSCRIPTION_ASSIGNED").
		SetDetail(`{"planID":100}`).
		SetOperator("system").
		Save(ctx)
	require.NoError(t, err)

	svc := paymenttestkit.Fulfillment(client, nil, &billing.SubscriptionService{}, nil)

	require.NoError(t, svc.ExecuteFulfillment(ctx, order.ID))
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusCompleted, reloaded.Status)
}

// TestFulfillmentLeaseVersionRejectsStaleWorker reacquires an aged SQLite lease
// sequentially and checks different versions. Completion using the earlier
// version conflicts, its failure report retains recharging state, and the newer
// version completes successfully.
func TestFulfillmentLeaseVersionRejectsStaleWorker(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	// Age the unfinished order beyond the fulfillment lease window so the
	// following acquisition or recovery can reach its stale-order path in
	// this sequential fixture.
	staleAt := time.Now().Add(-payment.FulfillmentLeaseDuration - time.Minute)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, payment.OrderStatusRecharging, staleAt)
	svc := paymenttestkit.Fulfillment(client, nil, nil, nil)

	firstLease, err := svc.AcquirePaymentFulfillmentLease(ctx, paymentpostgres.OrderFromEntity(order))
	require.NoError(t, err)
	require.NotNil(t, firstLease)

	_, err = client.PaymentOrder.UpdateOneID(order.ID).SetUpdatedAt(staleAt).Save(ctx)
	require.NoError(t, err)
	// Separate the two acquisition timestamps after aging the stored lease
	// again so this sequential fixture can assert distinct versions before
	// rejecting the older worker.
	time.Sleep(time.Millisecond)
	staleOrder, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	secondLease, err := svc.AcquirePaymentFulfillmentLease(ctx, paymentpostgres.OrderFromEntity(staleOrder))
	require.NoError(t, err)
	require.NotNil(t, secondLease)
	require.False(t, firstLease.Version.Equal(secondLease.Version))

	err = svc.MarkCompleted(ctx, paymentpostgres.OrderFromEntity(order), firstLease, "SUBSCRIPTION_SUCCESS")
	require.Error(t, err)
	require.Equal(t, "CONFLICT", apperror.Reason(err))
	svc.MarkFailed(ctx, order.ID, firstLease, errors.New("stale worker failure"))

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusRecharging, reloaded.Status)
	require.NoError(t, svc.MarkCompleted(ctx, paymentpostgres.OrderFromEntity(order), secondLease, "SUBSCRIPTION_SUCCESS"))
}

// TestExecuteBalanceFulfillmentRecoversAfterRedeemWithoutCreditingAgain seeds a
// used balance code for an unfinished order. Recovery completes it with no
// recorded redemption call, captures a rebate based on purchased points, and
// checks the rebate audit values.
func TestExecuteBalanceFulfillmentRecoversAfterRedeemWithoutCreditingAgain(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	ensurePaymentAuditOrderActionUniqueIndex(t, ctx, client)
	// Age the unfinished order beyond the fulfillment lease window so the
	// following acquisition or recovery can reach its stale-order path in
	// this sequential fixture.
	staleAt := time.Now().Add(-payment.FulfillmentLeaseDuration - time.Minute)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, payment.OrderStatusRecharging, staleAt)
	order, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetOrderType(payment.OrderTypeBalance).
		SetAmount(1000).
		SetPayAmount(100).
		ClearPlanID().
		SetUpdatedAt(staleAt).
		Save(ctx)
	require.NoError(t, err)

	redeemRepo := &redeemCodeRepoStub{codesByCode: map[string]*billing.RedeemCode{
		order.RechargeCode: {
			ID:        101,
			Code:      order.RechargeCode,
			Type:      billing.RedeemTypeBalance,
			Value:     order.Amount,
			Status:    billing.StatusUsed,
			MaxUses:   1,
			UsedCount: 1,
		},
	}}
	inviterID := int64(9001)
	affiliateRepo := &paymentFulfillmentAffiliateRepoStub{
		inviteeSummary: &promotion.AffiliateSummary{
			UserID:    order.UserID,
			InviterID: &inviterID,
			CreatedAt: time.Now().Add(-time.Hour),
		},
		inviterSummary: &promotion.AffiliateSummary{
			UserID:    inviterID,
			CreatedAt: time.Now().Add(-time.Hour),
		},
	}
	settingSvc := promotion.NewRuntimeSettings(&paymentFulfillmentSettingRepoStub{values: map[string]string{
		promotion.SettingKeyAffiliateEnabled:    "true",
		promotion.SettingKeyAffiliateRebateRate: "20",
	}})
	svc := paymenttestkit.Fulfillment(client,
		fulfillmentRedeemer(redeemRepo), nil, promotion.NewAffiliateService(affiliateRepo, settingSvc, nil, nil, promotion.Runtime{}))

	require.NoError(t, svc.ExecuteBalanceFulfillment(ctx, order.ID))
	require.Empty(t, redeemRepo.useCalls, "an already-used order code must not be redeemed again")
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusCompleted, reloaded.Status)
	require.Len(t, affiliateRepo.accrueCalls, 1)
	require.Equal(t, 200.0, affiliateRepo.accrueCalls[0].amount)
	applied, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("AFFILIATE_REBATE_APPLIED")).
		Only(ctx)
	require.NoError(t, err)
	require.Contains(t, applied.Detail, `"baseAmount":1000`)
	require.Contains(t, applied.Detail, `"rebateAmount":200`)
}

// TestDuplicatePaymentNotificationDoesNotReprocessCompletedBalanceOrder
// delivers success to a completed balance order with an unused fixture code.
// The order remains completed and the recorded redeemer receives no redemption
// call.
func TestDuplicatePaymentNotificationDoesNotReprocessCompletedBalanceOrder(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, payment.OrderStatusCompleted, time.Now())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetOrderType(payment.OrderTypeBalance).
		ClearPlanID().
		Save(ctx)
	require.NoError(t, err)

	redeemRepo := &redeemCodeRepoStub{codesByCode: map[string]*billing.RedeemCode{
		order.RechargeCode: {
			ID:     102,
			Code:   order.RechargeCode,
			Type:   billing.RedeemTypeBalance,
			Value:  order.Amount,
			Status: billing.StatusUnused,
		},
	}}
	svc := paymenttestkit.Fulfillment(client,
		fulfillmentRedeemer(redeemRepo), nil, nil)

	notification := &payment.PaymentNotification{
		TradeNo: "alipay-trade-replayed",
		OrderID: order.OutTradeNo,
		Amount:  order.PayAmount,
		Status:  payment.NotificationStatusSuccess,
	}
	require.NoError(t, svc.HandlePaymentNotification(ctx, notification, payment.TypeAlipay))
	require.NoError(t, svc.HandlePaymentNotification(ctx, notification, payment.TypeAlipay))

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusCompleted, reloaded.Status)
	require.Empty(t, redeemRepo.useCalls, "a duplicate notification must not redeem the balance code again")
}

// TestPaymentNotificationRejectsAmountMismatchBeforeFulfillment supplies a
// success notification whose amount disagrees with the pending SQLite order.
// Handling fails and the stored order remains pending before fulfillment.
func TestPaymentNotificationRejectsAmountMismatchBeforeFulfillment(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, payment.OrderStatusPending, time.Now())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetOrderType(payment.OrderTypeBalance).
		ClearPlanID().
		Save(ctx)
	require.NoError(t, err)

	svc := paymenttestkit.Fulfillment(client, nil, nil, nil)
	err = svc.HandlePaymentNotification(ctx, &payment.PaymentNotification{
		TradeNo: "alipay-trade-wrong-amount",
		OrderID: order.OutTradeNo,
		Amount:  order.PayAmount - 1,
		Status:  payment.NotificationStatusSuccess,
	}, payment.TypeAlipay)
	require.ErrorContains(t, err, "amount mismatch")

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusPending, reloaded.Status)
}

// TestExecuteSubscriptionFulfillmentRecoversCommittedAssignmentWithoutExtendingAgain
// seeds an in-memory subscription associated with an unfinished SQLite order.
// Recovery preserves its expiry and leaves one assigned audit, including after
// another forced-stale execution.
func TestExecuteSubscriptionFulfillmentRecoversCommittedAssignmentWithoutExtendingAgain(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	ensurePaymentAuditOrderActionUniqueIndex(t, ctx, client)
	// Age the unfinished order beyond the fulfillment lease window so the
	// following acquisition or recovery can reach its stale-order path in
	// this sequential fixture.
	staleAt := time.Now().Add(-payment.FulfillmentLeaseDuration - time.Minute)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, payment.OrderStatusRecharging, staleAt)

	// Seed a future expiry at whole-second precision for exact recovery
	// comparisons.
	expiresAt := time.Now().Add(30 * 24 * time.Hour).Truncate(time.Second)
	sourceOrderID := order.ID
	subRepo := billingtestkit.NewSubscriptionRepository()
	subRepo.Seed(&billing.UserSubscription{
		ID:            99,
		UserID:        order.UserID,
		PlanID:        *order.PlanID,
		StartsAt:      time.Now().Add(-time.Hour),
		ExpiresAt:     expiresAt,
		Status:        billing.SubscriptionStatusActive,
		SourceOrderID: &sourceOrderID,
		Notes:         "payment order already assigned",
	})
	svc := paymenttestkit.Fulfillment(client, nil, billing.NewSubscriptionService(nil, subRepo, billingpostgres.NewSubscriptionMutations(nil)), nil)

	require.NoError(t, svc.ExecuteSubscriptionFulfillment(ctx, order.ID))
	assertPaymentSubscriptionExpiry(t, subRepo, order, expiresAt)

	assignmentAuditCount, err := client.PaymentAuditLog.Query().
		Where(
			paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)),
			paymentauditlog.ActionEQ("SUBSCRIPTION_ASSIGNED"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, assignmentAuditCount)

	// Reopen and age the completed order while retaining its assignment
	// audit so the next execution can expose an extra extension or
	// duplicate audit in outside reads.
	_, err = client.PaymentOrder.UpdateOneID(order.ID).
		SetStatus(payment.OrderStatusRecharging).
		SetUpdatedAt(staleAt).
		ClearCompletedAt().
		Save(ctx)
	require.NoError(t, err)
	require.NoError(t, svc.ExecuteSubscriptionFulfillment(ctx, order.ID))
	assertPaymentSubscriptionExpiry(t, subRepo, order, expiresAt)

	assignmentAuditCount, err = client.PaymentAuditLog.Query().
		Where(
			paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)),
			paymentauditlog.ActionEQ("SUBSCRIPTION_ASSIGNED"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, assignmentAuditCount)
}

// assertPaymentSubscriptionExpiry reads the in-memory subscription records
// associated with order.ID and requires exactly one result with the expected
// expiry. It fails the test on lookup errors and compares the captured
// entitlement after fulfillment.
func assertPaymentSubscriptionExpiry(t *testing.T, repo *billingtestkit.SubscriptionRepository, order *dbent.PaymentOrder, expected time.Time) {
	t.Helper()
	subs, err := repo.ListBySourceOrderID(context.Background(), order.ID)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	sub := subs[0]
	require.True(t, sub.ExpiresAt.Equal(expected), "subscription expiry changed from %s to %s", expected, sub.ExpiresAt)
}

// TestExecuteSubscriptionFulfillmentAppliesAffiliateRebateFromPurchasedPoints
// purchases a plan whose points exceed its payment price. Fulfillment records
// one in-memory assignment, captures rebate inputs from those points, and
// persists assignment and rebate audits.
func TestExecuteSubscriptionFulfillmentAppliesAffiliateRebateFromPurchasedPoints(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	ensurePaymentAuditOrderActionUniqueIndex(t, ctx, client)

	user, err := client.User.Create().
		SetEmail("subscription-affiliate@example.com").
		SetPasswordHash("hash").
		SetUsername("subscription-affiliate-user").
		Save(ctx)
	require.NoError(t, err)

	monthlyPoints := 1000.0
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(100).
		SetPayAmount(100).
		SetFeeRate(0).
		SetRechargeCode("PAY-SUB-AFFILIATE").
		SetOutTradeNo("sub2_subscription_affiliate").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-sub-affiliate").
		SetOrderType(payment.OrderTypeSubscription).
		SetPlanID(99).
		SetPlanSnapshot(billing.SubscriptionPlanSnapshot{
			Name:            "Pro",
			Price:           100,
			ValidityDays:    30,
			MonthlyLimitUSD: &monthlyPoints,
		}).
		SetStatus(payment.OrderStatusPaid).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	inviterID := int64(9001)
	affiliateRepo := &paymentFulfillmentAffiliateRepoStub{
		inviteeSummary: &promotion.AffiliateSummary{
			UserID:    user.ID,
			AffCode:   "INVITEE",
			InviterID: &inviterID,
			CreatedAt: time.Now().Add(-24 * time.Hour),
		},
		inviterSummary: &promotion.AffiliateSummary{
			UserID:    inviterID,
			AffCode:   "INVITER",
			CreatedAt: time.Now().Add(-48 * time.Hour),
		},
	}
	settingSvc := promotion.NewRuntimeSettings(&paymentFulfillmentSettingRepoStub{values: map[string]string{
		promotion.SettingKeyAffiliateEnabled:           "true",
		promotion.SettingKeyAffiliateRebateRate:        "20",
		promotion.SettingKeyAffiliateRebateFreezeHours: "0",
	}})
	subRepo := billingtestkit.NewSubscriptionRepository()
	subscriptionSvc := billing.NewSubscriptionService(nil, subRepo, billingpostgres.NewSubscriptionMutations(nil))
	svc := paymenttestkit.Fulfillment(client, nil, subscriptionSvc,
		promotion.NewAffiliateService(affiliateRepo, settingSvc, nil, nil, promotion.Runtime{}))

	err = svc.ExecuteSubscriptionFulfillment(ctx, order.ID)
	require.NoError(t, err)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusCompleted, reloaded.Status)
	require.Len(t, affiliateRepo.accrueCalls, 1)
	require.Equal(t, inviterID, affiliateRepo.accrueCalls[0].inviterID)
	require.Equal(t, user.ID, affiliateRepo.accrueCalls[0].inviteeUserID)
	require.Equal(t, 200.0, affiliateRepo.accrueCalls[0].amount)
	require.NotNil(t, affiliateRepo.accrueCalls[0].sourceOrderID)
	require.Equal(t, order.ID, *affiliateRepo.accrueCalls[0].sourceOrderID)
	require.Equal(t, 1, subRepo.CreateCalls)

	sub, err := subRepo.GetLatestByUserIDAndPlanID(ctx, user.ID, 99)
	require.NoError(t, err)
	require.Equal(t, order.ID, *sub.SourceOrderID)

	assigned, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("SUBSCRIPTION_ASSIGNED")).
		Only(ctx)
	require.NoError(t, err)
	require.Contains(t, assigned.Detail, `"planID":99`)

	applied, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("AFFILIATE_REBATE_APPLIED")).
		Only(ctx)
	require.NoError(t, err)
	require.Contains(t, applied.Detail, `"baseAmount":1000`)
	require.Contains(t, applied.Detail, `"rebateAmount":200`)
}

// TestAccrueInviteRebateCapsPurchasedPoints supplies earlier accrual close to
// the configured cap for one inviter and invitee pair. The purchase records the
// remaining seventy-five points after applying the configured rebate rate.
func TestAccrueInviteRebateCapsPurchasedPoints(t *testing.T) {
	ctx := context.Background()
	inviterID := int64(9001)
	inviteeID := int64(9002)
	repo := &paymentFulfillmentAffiliateRepoStub{
		inviteeSummary: &promotion.AffiliateSummary{
			UserID:    inviteeID,
			InviterID: &inviterID,
			CreatedAt: time.Now().Add(-time.Hour),
		},
		inviterSummary: &promotion.AffiliateSummary{
			UserID:    inviterID,
			CreatedAt: time.Now().Add(-time.Hour),
		},
		accruedRebate: 175,
	}
	settingSvc := promotion.NewRuntimeSettings(&paymentFulfillmentSettingRepoStub{values: map[string]string{
		promotion.SettingKeyAffiliateEnabled:             "true",
		promotion.SettingKeyAffiliateRebateRate:          "20",
		promotion.SettingKeyAffiliateRebatePerInviteeCap: "250",
	}})
	svc := promotion.NewAffiliateService(repo, settingSvc, nil, nil, promotion.Runtime{})

	rebate, err := svc.AccrueInviteRebate(ctx, inviteeID, 1000)
	require.NoError(t, err)
	require.Equal(t, 75.0, rebate)
	require.Len(t, repo.accrueCalls, 1)
	require.Equal(t, 75.0, repo.accrueCalls[0].amount)
}

// TestExecuteSubscriptionFulfillmentDoesNotDuplicateWorkAfterLegacySuccessAudit
// seeds historical subscription and rebate success audits before supplying an
// empty in-memory subscription repository. Recovery completes the order with
// zero assignment creations and no affiliate accrual call.
func TestExecuteSubscriptionFulfillmentDoesNotDuplicateWorkAfterLegacySuccessAudit(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	ensurePaymentAuditOrderActionUniqueIndex(t, ctx, client)

	user, err := client.User.Create().
		SetEmail("subscription-affiliate-idempotent@example.com").
		SetPasswordHash("hash").
		SetUsername("subscription-affiliate-idempotent-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(80).
		SetPayAmount(80).
		SetFeeRate(0).
		SetRechargeCode("PAY-SUB-AFFILIATE-IDEMPOTENT").
		SetOutTradeNo("sub2_subscription_affiliate_idempotent").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-sub-affiliate-idempotent").
		SetOrderType(payment.OrderTypeSubscription).
		SetPlanID(100).
		SetPlanSnapshot(billing.SubscriptionPlanSnapshot{
			Name:         "Legacy",
			Price:        80,
			ValidityDays: 30,
		}).
		SetStatus(payment.OrderStatusPaid).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	_, err = client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(order.ID, 10)).
		SetAction("SUBSCRIPTION_SUCCESS").
		SetDetail(`{"planID":100}`).
		SetOperator("system").
		Save(ctx)
	require.NoError(t, err)
	_, err = client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(order.ID, 10)).
		SetAction("AFFILIATE_REBATE_APPLIED").
		SetDetail(`{"baseAmount":80,"rebateAmount":16}`).
		SetOperator("system").
		Save(ctx)
	require.NoError(t, err)

	inviterID := int64(9001)
	affiliateRepo := &paymentFulfillmentAffiliateRepoStub{
		inviteeSummary: &promotion.AffiliateSummary{
			UserID:    user.ID,
			AffCode:   "INVITEE",
			InviterID: &inviterID,
			CreatedAt: time.Now().Add(-24 * time.Hour),
		},
		inviterSummary: &promotion.AffiliateSummary{
			UserID:    inviterID,
			AffCode:   "INVITER",
			CreatedAt: time.Now().Add(-48 * time.Hour),
		},
	}
	settingSvc := promotion.NewRuntimeSettings(&paymentFulfillmentSettingRepoStub{values: map[string]string{
		promotion.SettingKeyAffiliateEnabled:    "true",
		promotion.SettingKeyAffiliateRebateRate: "20",
	}})
	subRepo := billingtestkit.NewSubscriptionRepository()
	subscriptionSvc := billing.NewSubscriptionService(nil, subRepo, billingpostgres.NewSubscriptionMutations(nil))
	svc := paymenttestkit.Fulfillment(client, nil, subscriptionSvc,
		promotion.NewAffiliateService(affiliateRepo, settingSvc, nil, nil, promotion.Runtime{}))

	err = svc.ExecuteSubscriptionFulfillment(ctx, order.ID)
	require.NoError(t, err)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusCompleted, reloaded.Status)
	require.Empty(t, affiliateRepo.accrueCalls)
	require.Zero(t, subRepo.CreateCalls)
}

// WithLockedInviter invokes fn immediately with the supplied context and
// returns its error. It ignores inviter ID and adds no lock; callers use this
// fixture for sequential rebate observations.
func (r *paymentFulfillmentAffiliateRepoStub) WithLockedInviter(ctx context.Context, _ int64, fn func(context.Context) error) error {
	return fn(ctx)
}

// fulfillmentRedeemer returns a recording fulfillment adapter retaining repo.
// It overrides code lookup and redemption; the embedded interface remains nil,
// so CreateCode is unsupported and panics if invoked.
func fulfillmentRedeemer(repo *redeemCodeRepoStub) payment.FulfillmentRedeemer {
	return &recordedRedeemer{repo: repo}
}

// GetByCode returns the borrowed code pointer indexed by the supplied string. A
// missing key returns nil with no error; the configured repository is required
// and context is ignored.
func (r *recordedRedeemer) GetByCode(_ context.Context, code string) (*billing.RedeemCode, error) {
	return r.repo.codesByCode[code], nil
}

// Redeem appends the supplied code to the shared call record and returns its
// borrowed configured pointer. It ignores context and user ID, and supplies no
// balance write or usage validation.
func (r *recordedRedeemer) Redeem(_ context.Context, _ int64, code string) (*billing.RedeemCode, error) {
	r.repo.useCalls = append(r.repo.useCalls, code)
	return r.repo.codesByCode[code], nil
}
