package payment_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/payment"
	paymenttestkit "github.com/TokenFlux/TokenRouter/internal/payment/testkit"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	sqlitetest "github.com/TokenFlux/TokenRouter/internal/testutil/sqlite"
)

// Signed resume claims must match the stored order's user and provider identity
// before a public lookup succeeds. The schema-version-two snapshot case
// deliberately disagrees with legacy provider columns and checks snapshot
// precedence. A counted provider stub distinguishes pending resume lookup,
// which queries once, from plain public verification, which returns local state
// with no provider query.

// paymentResumeLookupProvider counts sequential status queries and supplies
// pending outcomes.
type paymentResumeLookupProvider struct {
	// queryCount records calls on the shared fixture without
	// synchronization; use this provider sequentially when inspecting the
	// number of upstream lookup attempts.
	queryCount int // recorded provider query count
}

// Name returns the fixed display name for the resume-lookup provider fixture.
func (p *paymentResumeLookupProvider) Name() string { return "resume-lookup-provider" }

// ProviderKey identifies the fixture as the Alipay provider for registry
// lookup.
func (p *paymentResumeLookupProvider) ProviderKey() string { return payment.TypeAlipay }

// SupportedTypes returns a new slice containing the fixture's Alipay payment
// type.
func (p *paymentResumeLookupProvider) SupportedTypes() []payment.PaymentType {
	return []payment.PaymentType{payment.TypeAlipay}
}

// CreatePayment panics when resume-lookup tests unexpectedly request payment
// creation.
func (p *paymentResumeLookupProvider) CreatePayment(context.Context, payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	panic("unexpected call")
}

// QueryOrder counts the call and returns a new pending response. It ignores
// context and the query reference; this unsynchronized counter records
// sequential lookup observations and supplies no provider network behavior.
func (p *paymentResumeLookupProvider) QueryOrder(context.Context, string) (*payment.QueryOrderResponse, error) {
	p.queryCount++
	return &payment.QueryOrderResponse{Status: payment.ProviderStatusPending}, nil
}

// VerifyNotification panics when resume-lookup tests unexpectedly verify a
// webhook.
func (p *paymentResumeLookupProvider) VerifyNotification(context.Context, string, map[string]string) (*payment.PaymentNotification, error) {
	panic("unexpected call")
}

// Refund panics when resume-lookup tests unexpectedly request a provider
// refund.
func (p *paymentResumeLookupProvider) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
	panic("unexpected call")
}

// TestGetPublicOrderByResumeTokenReturnsMatchingOrder signs claims matching a
// SQLite order's user, provider instance, and payment type. The public lookup
// succeeds and returns that order's ID.
func TestGetPublicOrderByResumeTokenReturnsMatchingOrder(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	user, err := client.User.Create().
		SetEmail("resume@example.com").
		SetPasswordHash("hash").
		SetUsername("resume-user").
		Save(ctx)
	require.NoError(t, err)

	instanceID := "12"
	providerKey := payment.TypeEasyPay
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("RESUME-ORDER").
		SetOutTradeNo("sub2_resume_lookup").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-1").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(payment.OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(instanceID).
		SetProviderKey(providerKey).
		Save(ctx)
	require.NoError(t, err)

	resumeSvc := payment.NewPaymentResumeService([]byte("0123456789abcdef0123456789abcdef"))
	token, err := resumeSvc.CreateToken(payment.ResumeTokenClaims{
		OrderID:            order.ID,
		UserID:             user.ID,
		ProviderInstanceID: instanceID,
		ProviderKey:        providerKey,
		PaymentType:        payment.TypeAlipay,
		CanonicalReturnURL: "https://app.example.com/payment/result",
	})
	require.NoError(t, err)

	svc := paymenttestkit.Lifecycle(client, nil, nil, nil, resumeSvc, false)

	got, err := svc.GetPublicOrderByResumeToken(ctx, token)
	require.NoError(t, err)
	require.Equal(t, order.ID, got.ID)
}

// TestGetPublicOrderByResumeTokenRejectsSnapshotMismatch signs an instance ID
// different from the stored order. Lookup returns an error with
// INVALID_RESUME_TOKEN when the supplied claims cannot identify that snapshot.
func TestGetPublicOrderByResumeTokenRejectsSnapshotMismatch(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	user, err := client.User.Create().
		SetEmail("resume-mismatch@example.com").
		SetPasswordHash("hash").
		SetUsername("resume-mismatch-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("RESUME-MISMATCH").
		SetOutTradeNo("sub2_resume_lookup_mismatch").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-2").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(payment.OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID("12").
		SetProviderKey(payment.TypeEasyPay).
		Save(ctx)
	require.NoError(t, err)

	resumeSvc := payment.NewPaymentResumeService([]byte("0123456789abcdef0123456789abcdef"))
	token, err := resumeSvc.CreateToken(payment.ResumeTokenClaims{
		OrderID:            order.ID,
		UserID:             user.ID,
		ProviderInstanceID: "99",
		ProviderKey:        payment.TypeEasyPay,
		PaymentType:        payment.TypeAlipay,
		CanonicalReturnURL: "https://app.example.com/payment/result",
	})
	require.NoError(t, err)

	svc := paymenttestkit.Lifecycle(client, nil, nil, nil, resumeSvc, false)

	_, err = svc.GetPublicOrderByResumeToken(ctx, token)
	require.Error(t, err)
	require.Equal(t, "INVALID_RESUME_TOKEN", apperror.Reason(err))
}

// TestGetPublicOrderByResumeTokenUsesSnapshotAuthorityWhenColumnsDiffer stores
// conflicting legacy columns and a schema-version-two provider snapshot. Claims
// matching the snapshot resolve the expected order ID successfully.
func TestGetPublicOrderByResumeTokenUsesSnapshotAuthorityWhenColumnsDiffer(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	user, err := client.User.Create().
		SetEmail("resume-snapshot-authority@example.com").
		SetPasswordHash("hash").
		SetUsername("resume-snapshot-authority-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("RESUME-SNAPSHOT-AUTHORITY").
		SetOutTradeNo("sub2_resume_snapshot_authority").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-snapshot-authority").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(payment.OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID("legacy-column-instance").
		SetProviderKey(payment.TypeAlipay).
		SetProviderSnapshot(map[string]any{
			"schema_version":       2,
			"provider_instance_id": "snapshot-instance",
			"provider_key":         payment.TypeEasyPay,
		}).
		Save(ctx)
	require.NoError(t, err)

	resumeSvc := payment.NewPaymentResumeService([]byte("0123456789abcdef0123456789abcdef"))
	token, err := resumeSvc.CreateToken(payment.ResumeTokenClaims{
		OrderID:            order.ID,
		UserID:             user.ID,
		ProviderInstanceID: "snapshot-instance",
		ProviderKey:        payment.TypeEasyPay,
		PaymentType:        payment.TypeAlipay,
		CanonicalReturnURL: "https://app.example.com/payment/result",
	})
	require.NoError(t, err)

	svc := paymenttestkit.Lifecycle(client, nil, nil, nil, resumeSvc, false)

	got, err := svc.GetPublicOrderByResumeToken(ctx, token)
	require.NoError(t, err)
	require.Equal(t, order.ID, got.ID)
}

// TestGetPublicOrderByResumeTokenChecksUpstreamForPendingOrder resolves signed
// claims for a pending SQLite order through a counted provider stub. Lookup
// returns the order ID and invokes provider QueryOrder once.
func TestGetPublicOrderByResumeTokenChecksUpstreamForPendingOrder(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	user, err := client.User.Create().
		SetEmail("resume-refresh@example.com").
		SetPasswordHash("hash").
		SetUsername("resume-refresh-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("RESUME-PENDING").
		SetOutTradeNo("sub2_resume_lookup_pending").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-pending").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(payment.OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	resumeSvc := payment.NewPaymentResumeService([]byte("0123456789abcdef0123456789abcdef"))
	token, err := resumeSvc.CreateToken(payment.ResumeTokenClaims{
		OrderID:            order.ID,
		UserID:             user.ID,
		PaymentType:        payment.TypeAlipay,
		CanonicalReturnURL: "https://app.example.com/payment/result",
	})
	require.NoError(t, err)

	registry := payment.NewRegistry()
	provider := &paymentResumeLookupProvider{}
	registry.Register(provider)

	svc := paymenttestkit.Lifecycle(client,
		registry, nil, nil, resumeSvc,
		true)

	got, err := svc.GetPublicOrderByResumeToken(ctx, token)
	require.NoError(t, err)
	require.Equal(t, order.ID, got.ID)
	require.Equal(t, 1, provider.queryCount)
}

// TestVerifyOrderPublicDoesNotCheckUpstreamForPendingOrder looks up a pending
// SQLite order by its local reference. It returns the expected ID while the
// configured provider query counter stays zero.
func TestVerifyOrderPublicDoesNotCheckUpstreamForPendingOrder(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	user, err := client.User.Create().
		SetEmail("public-verify@example.com").
		SetPasswordHash("hash").
		SetUsername("public-verify-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("PUBLIC-VERIFY").
		SetOutTradeNo("sub2_public_verify_pending").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-public-verify").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(payment.OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	registry := payment.NewRegistry()
	provider := &paymentResumeLookupProvider{}
	registry.Register(provider)

	svc := paymenttestkit.Lifecycle(client,
		registry, nil, nil, nil, true)

	got, err := svc.VerifyOrderPublic(ctx, order.OutTradeNo)
	require.NoError(t, err)
	require.Equal(t, order.ID, got.ID)
	require.Equal(t, 0, provider.queryCount)
}

// TestVerifyOrderPublicRejectsBlankOutTradeNoUseCase supplies a whitespace-only
// reference to plain public verification. The use case rejects that input with
// INVALID_OUT_TRADE_NO before any order is returned.
func TestVerifyOrderPublicRejectsBlankOutTradeNoUseCase(t *testing.T) {
	svc := paymenttestkit.Lifecycle(sqlitetest.NewClient(t), nil, nil, nil, nil, false)

	_, err := svc.VerifyOrderPublic(context.Background(), "   ")
	require.Error(t, err)
	require.Equal(t, "INVALID_OUT_TRADE_NO", apperror.Reason(err))
}
