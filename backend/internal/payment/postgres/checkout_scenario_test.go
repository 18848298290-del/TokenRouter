package postgres

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	sqlitetest "github.com/TokenFlux/TokenRouter/internal/testutil/sqlite"
)

// Order snapshots retain channel association, merchant identity and currency
// while leaving credential configuration on the provider instance. Pure
// snapshot cases inspect that whitelist, and SQLite-backed checkout cases
// inspect stored snapshot, subscription currency and fee amounts. These
// fixtures exercise the Ent-backed implementation without establishing
// PostgreSQL lock or concurrent checkout behavior.

// TestBuildPaymentOrderProviderSnapshot_ExcludesSensitiveConfig supplies
// WeChat configuration containing merchant app and secret fields. It asserts
// the complete selected snapshot and excludes config, secrets, support labels
// and instance display name.
func TestBuildPaymentOrderProviderSnapshot_ExcludesSensitiveConfig(t *testing.T) {
	t.Parallel()

	sel := &payment.InstanceSelection{
		InstanceID:     "12",
		ProviderKey:    payment.TypeWxpay,
		SupportedTypes: "wxpay,wxpay_direct",
		PaymentMode:    "popup",
		Config: map[string]string{
			"privateKey": "secret",
			"apiV3Key":   "secret-v3",
			"appId":      "wx-app-id",
		},
	}

	snapshot := payment.BuildPaymentOrderProviderSnapshot(sel, payment.CreateOrderRequest{})
	require.Equal(t, map[string]any{
		"schema_version":       2,
		"provider_instance_id": "12",
		"provider_key":         payment.TypeWxpay,
		"payment_mode":         "popup",
		"merchant_app_id":      "wx-app-id",
		"currency":             "CNY",
	}, snapshot)
	require.NotContains(t, snapshot, "config")
	require.NotContains(t, snapshot, "privateKey")
	require.NotContains(t, snapshot, "apiV3Key")
	require.NotContains(t, snapshot, "supported_types")
	require.NotContains(t, snapshot, "instance_name")
	require.NotContains(t, snapshot, "merchant_id")
}

// TestCreateOrderInTx_WritesProviderSnapshot creates a local Alipay instance
// and balance checkout on SQLite. It asserts persisted binding and snapshot
// labels and checks that credential and display configuration is excluded.
func TestCreateOrderInTx_WritesProviderSnapshot(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)

	user, err := client.User.Create().
		SetEmail("snapshot@example.com").
		SetPasswordHash("hash").
		SetUsername("snapshot-user").
		Save(ctx)
	require.NoError(t, err)

	instance, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("Primary Alipay").
		SetConfig(`{"secretKey":"do-not-copy"}`).
		SetSupportedTypes("alipay,alipay_direct").
		SetPaymentMode("redirect").
		SetEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	svc := payment.NewCheckout(NewOrderStore(client), nil, nil, nil, payment.CheckoutRuntime{})
	order, err := svc.CreateOrderInTx(
		ctx,
		payment.CreateOrderRequest{
			UserID:      user.ID,
			PaymentType: payment.TypeAlipay,
			OrderType:   payment.OrderTypeBalance,
			ClientIP:    "127.0.0.1",
			SrcHost:     "app.example.com",
		},
		&payment.Buyer{
			ID:       user.ID,
			Email:    user.Email,
			Username: user.Username,
		},
		nil,
		&payment.PaymentConfig{
			MaxPendingOrders: 3,
			OrderTimeoutMin:  30,
		},
		88,
		88,
		payment.FeeBreakdown{BaseAmount: 88, PayAmount: 88},
		&payment.InstanceSelection{
			InstanceID:     strconv.FormatInt(instance.ID, 10),
			ProviderKey:    payment.TypeAlipay,
			SupportedTypes: "alipay,alipay_direct",
			PaymentMode:    "redirect",
			Config: map[string]string{
				"secretKey": "do-not-copy",
			},
		},
	)
	require.NoError(t, err)
	require.Equal(t, strconv.FormatInt(instance.ID, 10), valueOrEmpty(order.ProviderInstanceID))
	require.Equal(t, payment.TypeAlipay, valueOrEmpty(order.ProviderKey))
	require.Equal(t, float64(2), order.ProviderSnapshot["schema_version"])
	require.Equal(t, strconv.FormatInt(instance.ID, 10), order.ProviderSnapshot["provider_instance_id"])
	require.Equal(t, payment.TypeAlipay, order.ProviderSnapshot["provider_key"])
	require.Equal(t, "redirect", order.ProviderSnapshot["payment_mode"])
	require.NotContains(t, order.ProviderSnapshot, "config")
	require.NotContains(t, order.ProviderSnapshot, "secretKey")
	require.NotContains(t, order.ProviderSnapshot, "supported_types")
	require.NotContains(t, order.ProviderSnapshot, "instance_name")
}

// TestCreateOrderInTx_WritesSubscriptionPlanCurrencySnapshot creates a
// USD-priced subscription plan and persists a checkout with a separate CNY fee
// breakdown. It asserts that the plan snapshot still retains USD currency.
func TestCreateOrderInTx_WritesSubscriptionPlanCurrencySnapshot(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)

	user, err := client.User.Create().
		SetEmail("plan-currency-snapshot@example.com").
		SetPasswordHash("hash").
		SetUsername("plan-currency-snapshot-user").
		Save(ctx)
	require.NoError(t, err)

	plan, err := client.SubscriptionPlan.Create().
		SetName("USD Plan").
		SetPrice(10).
		SetCurrency("USD").
		Save(ctx)
	require.NoError(t, err)

	svc := payment.NewCheckout(NewOrderStore(client), nil, nil, nil, payment.CheckoutRuntime{})
	order, err := svc.CreateOrderInTx(
		ctx,
		payment.CreateOrderRequest{
			UserID:      user.ID,
			PaymentType: payment.TypeAlipay,
			OrderType:   payment.OrderTypeSubscription,
			ClientIP:    "127.0.0.1",
			SrcHost:     "app.example.com",
		},
		&payment.Buyer{ID: user.ID, Email: user.Email, Username: user.Username},
		billingpostgres.PlanFromEntity(plan),
		&payment.PaymentConfig{MaxPendingOrders: 3, OrderTimeoutMin: 30},
		10,
		10,
		payment.FeeBreakdown{BaseAmount: 72, PayAmount: 72},
		&payment.InstanceSelection{ProviderKey: payment.TypeAlipay, PaymentMode: "redirect"},
	)
	require.NoError(t, err)
	require.Equal(t, "USD", order.PlanSnapshot.Currency)
}

// TestCreateOrderInTx_WritesFeeBreakdown calculates fixed and percentage fees
// before creating a SQLite balance order. It checks the persisted pay, rate,
// fixed, rate-amount and total-fee values from that breakdown.
func TestCreateOrderInTx_WritesFeeBreakdown(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)

	user, err := client.User.Create().
		SetEmail("fee@example.com").
		SetPasswordHash("hash").
		SetUsername("fee-user").
		Save(ctx)
	require.NoError(t, err)

	instance, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeStripe).
		SetName("Stripe").
		SetConfig("{}").
		SetSupportedTypes("card,link").
		SetPaymentMode("stripe").
		SetEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	breakdown := payment.CalculatePayAmountWithFeeForCurrency(100, payment.FeeConfig{FixedFee: 2.5, FeeRate: 2.2}, payment.DefaultPaymentCurrency)
	svc := payment.NewCheckout(NewOrderStore(client), nil, nil, nil, payment.CheckoutRuntime{})
	order, err := svc.CreateOrderInTx(
		ctx,
		payment.CreateOrderRequest{UserID: user.ID, PaymentType: payment.TypeStripe, OrderType: payment.OrderTypeBalance},
		&payment.Buyer{ID: user.ID, Email: user.Email, Username: user.Username},
		nil,
		&payment.PaymentConfig{MaxPendingOrders: 3, OrderTimeoutMin: 30},
		100,
		100,
		breakdown,
		&payment.InstanceSelection{InstanceID: strconv.FormatInt(instance.ID, 10), ProviderKey: payment.TypeStripe, SupportedTypes: "card,link", PaymentMode: "stripe"},
	)
	require.NoError(t, err)
	require.Equal(t, 104.7, order.PayAmount)
	require.Equal(t, 2.2, order.FeeRate)
	require.Equal(t, 2.5, order.FeeFixed)
	require.Equal(t, 2.2, order.FeeRateAmount)
	require.Equal(t, 4.7, order.FeeAmount)
}

// TestBuildPaymentOrderProviderSnapshot_UsesWxpayJSAPIAppIDForOpenIDOrders
// supplies both WeChat app identities with an OpenID request. It expects
// mpAppId, merchant ID and CNY in the snapshot for that JSAPI association.
func TestBuildPaymentOrderProviderSnapshot_UsesWxpayJSAPIAppIDForOpenIDOrders(t *testing.T) {
	t.Parallel()

	snapshot := payment.BuildPaymentOrderProviderSnapshot(&payment.InstanceSelection{
		InstanceID:  "88",
		ProviderKey: payment.TypeWxpay,
		Config: map[string]string{
			"appId":   "wx-open-app",
			"mpAppId": "wx-mp-app",
			"mchId":   "mch-88",
		},
		PaymentMode: "jsapi",
	}, payment.CreateOrderRequest{OpenID: "openid-123"})

	require.Equal(t, "wx-mp-app", snapshot["merchant_app_id"])
	require.Equal(t, "mch-88", snapshot["merchant_id"])
	require.Equal(t, "CNY", snapshot["currency"])
}

// TestBuildPaymentOrderProviderSnapshot_IncludesAlipayMerchantIdentity
// supplies an Alipay app ID and private key to snapshot construction. It
// retains the merchant app identity and excludes the private key from saved
// metadata.
func TestBuildPaymentOrderProviderSnapshot_IncludesAlipayMerchantIdentity(t *testing.T) {
	t.Parallel()

	snapshot := payment.BuildPaymentOrderProviderSnapshot(&payment.InstanceSelection{
		InstanceID:  "21",
		ProviderKey: payment.TypeAlipay,
		Config: map[string]string{
			"appId":      "alipay-app-21",
			"privateKey": "secret",
		},
		PaymentMode: "redirect",
	}, payment.CreateOrderRequest{})

	require.Equal(t, "alipay-app-21", snapshot["merchant_app_id"])
	require.NotContains(t, snapshot, "privateKey")
}

// TestBuildPaymentOrderProviderSnapshot_IncludesEasyPayMerchantIdentity
// supplies an EasyPay merchant pid and signing secret for visible Alipay. It
// retains merchant identity and excludes pkey from the resulting provider
// snapshot.
func TestBuildPaymentOrderProviderSnapshot_IncludesEasyPayMerchantIdentity(t *testing.T) {
	t.Parallel()

	snapshot := payment.BuildPaymentOrderProviderSnapshot(&payment.InstanceSelection{
		InstanceID:  "66",
		ProviderKey: payment.TypeEasyPay,
		Config: map[string]string{
			"pid":  "easypay-merchant-66",
			"pkey": "secret",
		},
		PaymentMode: "popup",
	}, payment.CreateOrderRequest{PaymentType: payment.TypeAlipay})

	require.Equal(t, "easypay-merchant-66", snapshot["merchant_id"])
	require.NotContains(t, snapshot, "pkey")
}

// TestBuildPaymentOrderProviderSnapshot_IncludesProviderCurrency supplies
// lowercase Stripe HKD and Airwallex USD configuration. It expects normalized
// snapshot currencies and the Airwallex account label in saved merchant
// identity.
func TestBuildPaymentOrderProviderSnapshot_IncludesProviderCurrency(t *testing.T) {
	t.Parallel()

	stripeSnapshot := payment.BuildPaymentOrderProviderSnapshot(&payment.InstanceSelection{
		InstanceID:  "77",
		ProviderKey: payment.TypeStripe,
		Config: map[string]string{
			"currency": "hkd",
		},
	}, payment.CreateOrderRequest{})
	require.Equal(t, "HKD", stripeSnapshot["currency"])

	airwallexSnapshot := payment.BuildPaymentOrderProviderSnapshot(&payment.InstanceSelection{
		InstanceID:  "78",
		ProviderKey: payment.TypeAirwallex,
		Config: map[string]string{
			"currency":  "usd",
			"accountId": "acct-78",
		},
	}, payment.CreateOrderRequest{})
	require.Equal(t, "USD", airwallexSnapshot["currency"])
	require.Equal(t, "acct-78", airwallexSnapshot["merchant_id"])
}

// valueOrEmpty dereferences an optional saved string, returning empty for nil.
// Snapshot assertions use it to compare nullable provider labels with their
// expected concrete values from the fixture.
func valueOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
