package postgres_test

// These scenarios cover provider bindings, visible methods and selection, resume, and checkout, using instance queries in instances.go and management.go.

import (
	"context"
	_ "embed"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/payment"
	paymentpostgres "github.com/TokenFlux/TokenRouter/internal/payment/postgres"
	paymentadapter "github.com/TokenFlux/TokenRouter/internal/payment/provider"
	paymenttestkit "github.com/TokenFlux/TokenRouter/internal/payment/testkit"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	sqlitetest "github.com/TokenFlux/TokenRouter/internal/testutil/sqlite"
)

type webhookProviderTestDouble struct {
	key   string
	types []payment.PaymentType
}

func TestUsesOfficialWxpayVisibleMethodDerivesFromEnabledProviderInstance(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)

	_, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeWxpay).
		SetName("Official WeChat").
		SetConfig("{}").
		SetSupportedTypes("wxpay").
		SetEnabled(true).
		SetSortOrder(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create official wxpay instance: %v", err)
	}

	svc := payment.NewCheckout(nil, paymenttestkit.Configuration(client, nil, nil), nil, nil, payment.CheckoutRuntime{})

	if !svc.UsesOfficialWxpayVisibleMethod(ctx) {
		t.Fatal("expected official wxpay visible method to be detected from enabled provider instance")
	}
}

func TestUsesOfficialWxpayVisibleMethodRespectsConfiguredSourceWhenMultipleProvidersEnabled(t *testing.T) {
	tests := []struct {
		name         string
		source       string
		wantOfficial bool
	}{
		{
			name:         "official source selected",
			source:       payment.VisibleMethodSourceOfficialWechat,
			wantOfficial: true,
		},
		{
			name:         "easypay source selected",
			source:       payment.VisibleMethodSourceEasyPayWechat,
			wantOfficial: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			client := sqlitetest.NewClient(t)

			_, err := client.PaymentProviderInstance.Create().
				SetProviderKey(payment.TypeWxpay).
				SetName("Official WeChat").
				SetConfig("{}").
				SetSupportedTypes("wxpay").
				SetEnabled(true).
				SetSortOrder(1).
				Save(ctx)
			if err != nil {
				t.Fatalf("create official wxpay instance: %v", err)
			}

			_, err = client.PaymentProviderInstance.Create().
				SetProviderKey(payment.TypeEasyPay).
				SetName("EasyPay WeChat").
				SetConfig("{}").
				SetSupportedTypes("wxpay").
				SetEnabled(true).
				SetSortOrder(2).
				Save(ctx)
			if err != nil {
				t.Fatalf("create easypay wxpay instance: %v", err)
			}

			svc := payment.NewCheckout(nil, paymenttestkit.Configuration(client, &paymentConfigSettingRepoStub{
				values: map[string]string{
					payment.SettingPaymentVisibleMethodWxpaySource: tt.source,
				},
			}, nil), nil, nil, payment.CheckoutRuntime{})

			if got := svc.UsesOfficialWxpayVisibleMethod(ctx); got != tt.wantOfficial {
				t.Fatalf("usesOfficialWxpayVisibleMethod() = %v, want %v", got, tt.wantOfficial)
			}
		})
	}
}

func TestGetOrderProviderInstanceResolvesUniqueLegacyProviderKey(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeStripe).
		SetName("stripe-a").
		SetConfig(paymenttestkit.LegacyConfig(t, "stripe:sk_test_legacy_provider_key")).
		SetSupportedTypes("stripe").
		SetEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	providerKey := payment.TypeStripe
	order := &payment.Order{
		PaymentType: payment.TypeStripe,
		ProviderKey: &providerKey,
	}

	svc := payment.NewProviderBindings(paymentpostgres.NewInstanceStore(client), nil, paymenttestkit.LegacyLoadBalancer(client), payment.BindingRuntime{Factory: paymentadapter.CreateProvider, RegistryFactory: paymentadapter.CreateProvider}, false)

	got, err := svc.GetOrderProviderInstance(ctx, order)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, inst.ID, got.ID)
}

func TestGetOrderProviderInstanceResolvesUniqueLegacyPaymentType(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeWxpay).
		SetName("wxpay-a").
		SetConfig("{}").
		SetSupportedTypes("wxpay").
		SetEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	order := &payment.Order{
		PaymentType: payment.TypeWxpayDirect,
	}

	svc := payment.NewProviderBindings(paymentpostgres.NewInstanceStore(client), nil, paymenttestkit.LegacyLoadBalancer(client), payment.BindingRuntime{Factory: paymentadapter.CreateProvider, RegistryFactory: paymentadapter.CreateProvider}, false)

	got, err := svc.GetOrderProviderInstance(ctx, order)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, inst.ID, got.ID)
}

func TestGetOrderProviderInstanceLeavesAmbiguousLegacyOrderUnresolved(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	_, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeEasyPay).
		SetName("easypay-a").
		SetConfig("{}").
		SetSupportedTypes("wxpay").
		SetEnabled(true).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeWxpay).
		SetName("wxpay-a").
		SetConfig("{}").
		SetSupportedTypes("wxpay").
		SetEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	order := &payment.Order{
		PaymentType: payment.TypeWxpay,
	}

	svc := payment.NewProviderBindings(paymentpostgres.NewInstanceStore(client), nil, paymenttestkit.LegacyLoadBalancer(client), payment.BindingRuntime{Factory: paymentadapter.CreateProvider, RegistryFactory: paymentadapter.CreateProvider}, false)

	got, err := svc.GetOrderProviderInstance(ctx, order)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestGetOrderProviderInstanceLeavesLegacyProviderKeyUnresolvedWhenHistoricalInstancesConflict(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	_, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeStripe).
		SetName("stripe-disabled-legacy").
		SetConfig("{}").
		SetSupportedTypes("stripe").
		SetEnabled(false).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeStripe).
		SetName("stripe-enabled-current").
		SetConfig("{}").
		SetSupportedTypes("stripe").
		SetEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	providerKey := payment.TypeStripe
	order := &payment.Order{
		PaymentType: payment.TypeStripe,
		ProviderKey: &providerKey,
	}

	svc := payment.NewProviderBindings(paymentpostgres.NewInstanceStore(client), nil, paymenttestkit.LegacyLoadBalancer(client), payment.BindingRuntime{Factory: paymentadapter.CreateProvider, RegistryFactory: paymentadapter.CreateProvider}, false)

	got, err := svc.GetOrderProviderInstance(ctx, order)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestGetOrderProviderInstanceLeavesProviderKeyMatchUnresolvedWhenTypeNotSupported(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	_, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeWxpay).
		SetName("wxpay-only").
		SetConfig("{}").
		SetSupportedTypes("wxpay").
		SetEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	providerKey := payment.TypeWxpay
	order := &payment.Order{
		PaymentType: payment.TypeAlipayDirect,
		ProviderKey: &providerKey,
	}

	svc := payment.NewProviderBindings(paymentpostgres.NewInstanceStore(client), nil, paymenttestkit.LegacyLoadBalancer(client), payment.BindingRuntime{Factory: paymentadapter.CreateProvider, RegistryFactory: paymentadapter.CreateProvider}, false)

	got, err := svc.GetOrderProviderInstance(ctx, order)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestGetOrderProviderInstanceUsesProviderSnapshotWhenPinnedColumnMissing(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeStripe).
		SetName("stripe-snapshot").
		SetConfig(paymenttestkit.LegacyConfig(t, "stripe:sk_snapshot")).
		SetSupportedTypes("stripe").
		SetEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	order := &payment.Order{
		ID:          42,
		PaymentType: payment.TypeStripe,
		ProviderSnapshot: map[string]any{
			"schema_version":       1,
			"provider_instance_id": strconv.FormatInt(inst.ID, 10),
			"provider_key":         payment.TypeStripe,
		},
	}

	svc := payment.NewProviderBindings(paymentpostgres.NewInstanceStore(client), nil, paymenttestkit.LegacyLoadBalancer(client), payment.BindingRuntime{Factory: paymentadapter.CreateProvider, RegistryFactory: paymentadapter.CreateProvider}, false)

	got, err := svc.GetOrderProviderInstance(ctx, order)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, inst.ID, got.ID)
}

func TestGetOrderProviderInstanceRejectsMissingSnapshotInstanceWithoutLegacyFallback(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	_, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeStripe).
		SetName("stripe-legacy-fallback").
		SetConfig(paymenttestkit.LegacyConfig(t, "stripe:sk_legacy")).
		SetSupportedTypes("stripe").
		SetEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	order := &payment.Order{
		ID:          43,
		PaymentType: payment.TypeStripe,
		ProviderSnapshot: map[string]any{
			"schema_version":       1,
			"provider_instance_id": "999999",
			"provider_key":         payment.TypeStripe,
		},
	}

	svc := payment.NewProviderBindings(paymentpostgres.NewInstanceStore(client), nil, paymenttestkit.LegacyLoadBalancer(client), payment.BindingRuntime{Factory: paymentadapter.CreateProvider, RegistryFactory: paymentadapter.CreateProvider}, false)

	got, err := svc.GetOrderProviderInstance(ctx, order)
	require.Nil(t, got)
	require.Error(t, err)
	require.Contains(t, err.Error(), "provider snapshot instance 999999 is missing")
}

func TestGetWebhookProviderRejectsAmbiguousRegistryFallback(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	wxpayConfigA := paymenttestkit.LegacyConfig(t, "wxpay:a")
	wxpayConfigB := paymenttestkit.LegacyConfig(t, "wxpay:b")
	_, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeWxpay).
		SetName("wxpay-a").
		SetConfig(wxpayConfigA).
		SetSupportedTypes("wxpay").
		SetEnabled(true).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeWxpay).
		SetName("wxpay-b").
		SetConfig(wxpayConfigB).
		SetSupportedTypes("wxpay").
		SetEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	svc := payment.NewProviderBindings(paymentpostgres.NewInstanceStore(client), payment.NewRegistry(), paymenttestkit.LegacyLoadBalancer(client), payment.BindingRuntime{Factory: paymentadapter.CreateProvider, RegistryFactory: paymentadapter.CreateProvider}, true)

	providers, err := svc.GetWebhookProviders(ctx, payment.TypeWxpay, "")
	require.NoError(t, err)
	require.Len(t, providers, 2)
}

func TestGetWebhookProvidersRejectAmbiguousFallbackForNonWxpay(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	_, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("alipay-a").
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("alipay-b").
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	svc := payment.NewProviderBindings(paymentpostgres.NewInstanceStore(client), payment.NewRegistry(), nil, payment.BindingRuntime{Factory: paymentadapter.CreateProvider, RegistryFactory: paymentadapter.CreateProvider}, true)

	_, err = svc.GetWebhookProviders(ctx, payment.TypeAlipay, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "ambiguous")
}

func TestGetWebhookProviderAllowsSingleInstanceRegistryFallback(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	_, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeStripe).
		SetName("stripe-a").
		SetConfig("{}").
		SetSupportedTypes("stripe").
		SetEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	registry := payment.NewRegistry()
	registry.Register(webhookProviderTestDouble{
		key:   payment.TypeStripe,
		types: []payment.PaymentType{payment.TypeStripe},
	})

	svc := payment.NewProviderBindings(paymentpostgres.NewInstanceStore(client), registry, nil, payment.BindingRuntime{Factory: paymentadapter.CreateProvider, RegistryFactory: paymentadapter.CreateProvider}, true)

	providers, err := svc.GetWebhookProviders(ctx, payment.TypeStripe, "")
	require.NoError(t, err)
	require.Len(t, providers, 1)
	prov := providers[0]
	require.Equal(t, payment.TypeStripe, prov.ProviderKey())
}

func TestGetWebhookProviderRejectsRegistryFallbackForPinnedOrder(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	user, err := client.User.Create().
		SetEmail("webhook@example.com").
		SetPasswordHash("hash").
		SetUsername("webhook").
		Save(ctx)
	require.NoError(t, err)

	pinnedInstanceID := "999"
	_, err = client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("TEST-RECHARGE").
		SetOutTradeNo("sub2_test_pinned_order").
		SetPaymentType(payment.TypeWxpay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(payment.OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(pinnedInstanceID).
		Save(ctx)
	require.NoError(t, err)

	registry := payment.NewRegistry()
	registry.Register(webhookProviderTestDouble{
		key:   payment.TypeWxpay,
		types: []payment.PaymentType{payment.TypeWxpay},
	})

	svc := payment.NewProviderBindings(paymentpostgres.NewInstanceStore(client), registry, nil, payment.BindingRuntime{Factory: paymentadapter.CreateProvider, RegistryFactory: paymentadapter.CreateProvider}, true)

	_, err = svc.GetWebhookProviders(ctx, payment.TypeWxpay, "sub2_test_pinned_order")
	require.Error(t, err)
	require.Contains(t, err.Error(), "provider instance")
}

func TestGetWebhookProviderUsesProviderSnapshotBeforeWxpayFallback(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	user, err := client.User.Create().
		SetEmail("snapshot-webhook@example.com").
		SetPasswordHash("hash").
		SetUsername("snapshot-webhook").
		Save(ctx)
	require.NoError(t, err)

	wxpayConfigA := paymenttestkit.LegacyConfig(t, "wxpay:snapshot-a")
	wxpayConfigB := paymenttestkit.LegacyConfig(t, "wxpay:snapshot-b")
	instA, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeWxpay).
		SetName("wxpay-snapshot-a").
		SetConfig(wxpayConfigA).
		SetSupportedTypes("wxpay").
		SetEnabled(true).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeWxpay).
		SetName("wxpay-snapshot-b").
		SetConfig(wxpayConfigB).
		SetSupportedTypes("wxpay").
		SetEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(66).
		SetPayAmount(66).
		SetFeeRate(0).
		SetRechargeCode("SNAPSHOT-WEBHOOK").
		SetOutTradeNo("sub2_test_snapshot_webhook_order").
		SetPaymentType(payment.TypeWxpay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(payment.OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderSnapshot(map[string]any{
			"schema_version":       1,
			"provider_instance_id": strconv.FormatInt(instA.ID, 10),
			"provider_key":         payment.TypeWxpay,
			"payment_mode":         "native",
		}).
		Save(ctx)
	require.NoError(t, err)

	svc := payment.NewProviderBindings(paymentpostgres.NewInstanceStore(client), payment.NewRegistry(), paymenttestkit.LegacyLoadBalancer(client), payment.BindingRuntime{Factory: paymentadapter.CreateProvider, RegistryFactory: paymentadapter.CreateProvider}, true)

	providers, err := svc.GetWebhookProviders(ctx, payment.TypeWxpay, "sub2_test_snapshot_webhook_order")
	require.NoError(t, err)
	require.Len(t, providers, 1)
	require.Equal(t, payment.TypeWxpay, providers[0].ProviderKey())
}

func TestNormalizeVisibleMethods(t *testing.T) {
	t.Parallel()

	got := payment.NormalizeVisibleMethods([]string{
		"alipay_direct",
		"alipay",
		" wxpay_direct ",
		"wxpay",
		"stripe",
		"ldc",
	})

	want := []string{"alipay", "wxpay", "stripe", "ldc"}
	if len(got) != len(want) {
		t.Fatalf("NormalizeVisibleMethods len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("NormalizeVisibleMethods[%d] = %q, want %q (full=%v)", i, got[i], want[i], got)
		}
	}
}

func TestEnabledVisibleMethodsForEasyPayIncludesCustomSupportedTypes(t *testing.T) {
	t.Parallel()

	got := payment.ConfigEnabledVisibleMethodsForProvider(payment.TypeEasyPay, "alipay,ldc,usdt_trc20")
	want := []string{"alipay", "ldc", "usdt_trc20"}
	if len(got) != len(want) {
		t.Fatalf("enabledVisibleMethodsForProvider len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("enabledVisibleMethodsForProvider[%d] = %q, want %q (full=%v)", i, got[i], want[i], got)
		}
	}
}

func TestNormalizeVisibleMethodSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		input  string
		want   string
	}{
		{name: "alipay official alias", method: payment.TypeAlipay, input: "alipay", want: payment.VisibleMethodSourceOfficialAlipay},
		{name: "alipay easypay alias", method: payment.TypeAlipay, input: "easypay", want: payment.VisibleMethodSourceEasyPayAlipay},
		{name: "wxpay official alias", method: payment.TypeWxpay, input: "wxpay", want: payment.VisibleMethodSourceOfficialWechat},
		{name: "wxpay easypay alias", method: payment.TypeWxpay, input: "easypay", want: payment.VisibleMethodSourceEasyPayWechat},
		{name: "unsupported source", method: payment.TypeWxpay, input: "stripe", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := payment.NormalizeVisibleMethodSource(tt.method, tt.input); got != tt.want {
				t.Fatalf("NormalizeVisibleMethodSource(%q, %q) = %q, want %q", tt.method, tt.input, got, tt.want)
			}
		})
	}
}

func TestVisibleMethodProviderKeyForSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		source string
		want   string
		ok     bool
	}{
		{name: "official alipay", method: payment.TypeAlipay, source: payment.VisibleMethodSourceOfficialAlipay, want: payment.TypeAlipay, ok: true},
		{name: "easypay alipay", method: payment.TypeAlipay, source: payment.VisibleMethodSourceEasyPayAlipay, want: payment.TypeEasyPay, ok: true},
		{name: "official wechat", method: payment.TypeWxpay, source: payment.VisibleMethodSourceOfficialWechat, want: payment.TypeWxpay, ok: true},
		{name: "easypay wechat", method: payment.TypeWxpay, source: payment.VisibleMethodSourceEasyPayWechat, want: payment.TypeEasyPay, ok: true},
		{name: "mismatched method and source", method: payment.TypeAlipay, source: payment.VisibleMethodSourceOfficialWechat, want: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := payment.VisibleMethodProviderKeyForSource(tt.method, tt.source)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("VisibleMethodProviderKeyForSource(%q, %q) = (%q, %v), want (%q, %v)", tt.method, tt.source, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestVisibleMethodLoadBalancerUsesEnabledProviderInstance(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := sqlitetest.NewClient(t)
	_, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("Official Alipay").
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetSortOrder(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create alipay provider: %v", err)
	}

	inner := &paymenttestkit.CaptureLoadBalancer{}
	configService := paymenttestkit.Configuration(client, nil, nil)
	lb := payment.NewVisibleMethodLoadBalancer(inner, configService)

	_, err = lb.SelectInstance(ctx, "", payment.TypeAlipay, payment.StrategyRoundRobin, 12.5)
	if err != nil {
		t.Fatalf("SelectInstance returned error: %v", err)
	}
	if inner.LastProviderKey != payment.TypeAlipay {
		t.Fatalf("lastProviderKey = %q, want %q", inner.LastProviderKey, payment.TypeAlipay)
	}
}

func TestVisibleMethodLoadBalancerUsesConfiguredSourceWhenMultipleProvidersEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		method        payment.PaymentType
		officialName  string
		officialTypes string
		easyPayName   string
		easyPayTypes  string
		sourceSetting string
		wantProvider  string
	}{
		{
			name:          "alipay uses official source",
			method:        payment.TypeAlipay,
			officialName:  "Official Alipay",
			officialTypes: "alipay",
			easyPayName:   "EasyPay Alipay",
			easyPayTypes:  "alipay",
			sourceSetting: payment.VisibleMethodSourceOfficialAlipay,
			wantProvider:  payment.TypeAlipay,
		},
		{
			name:          "alipay uses easypay source",
			method:        payment.TypeAlipay,
			officialName:  "Official Alipay",
			officialTypes: "alipay",
			easyPayName:   "EasyPay Alipay",
			easyPayTypes:  "alipay",
			sourceSetting: payment.VisibleMethodSourceEasyPayAlipay,
			wantProvider:  payment.TypeEasyPay,
		},
		{
			name:          "wxpay uses official source",
			method:        payment.TypeWxpay,
			officialName:  "Official WeChat",
			officialTypes: "wxpay",
			easyPayName:   "EasyPay WeChat",
			easyPayTypes:  "wxpay",
			sourceSetting: payment.VisibleMethodSourceOfficialWechat,
			wantProvider:  payment.TypeWxpay,
		},
		{
			name:          "wxpay uses easypay source",
			method:        payment.TypeWxpay,
			officialName:  "Official WeChat",
			officialTypes: "wxpay",
			easyPayName:   "EasyPay WeChat",
			easyPayTypes:  "wxpay",
			sourceSetting: payment.VisibleMethodSourceEasyPayWechat,
			wantProvider:  payment.TypeEasyPay,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			client := sqlitetest.NewClient(t)

			officialProviderKey := payment.TypeAlipay
			if tt.method == payment.TypeWxpay {
				officialProviderKey = payment.TypeWxpay
			}

			_, err := client.PaymentProviderInstance.Create().
				SetProviderKey(officialProviderKey).
				SetName(tt.officialName).
				SetConfig("{}").
				SetSupportedTypes(tt.officialTypes).
				SetEnabled(true).
				SetSortOrder(1).
				Save(ctx)
			if err != nil {
				t.Fatalf("create official provider: %v", err)
			}

			_, err = client.PaymentProviderInstance.Create().
				SetProviderKey(payment.TypeEasyPay).
				SetName(tt.easyPayName).
				SetConfig("{}").
				SetSupportedTypes(tt.easyPayTypes).
				SetEnabled(true).
				SetSortOrder(2).
				Save(ctx)
			if err != nil {
				t.Fatalf("create easypay provider: %v", err)
			}

			inner := &paymenttestkit.CaptureLoadBalancer{}
			configService := paymenttestkit.Configuration(client, &paymentConfigSettingRepoStub{
				values: map[string]string{
					payment.ResumeVisibleMethodSourceSettingKey(tt.method): tt.sourceSetting,
				},
			}, nil)
			lb := payment.NewVisibleMethodLoadBalancer(inner, configService)

			_, err = lb.SelectInstance(ctx, "", tt.method, payment.StrategyRoundRobin, 12.5)
			if err != nil {
				t.Fatalf("SelectInstance returned error: %v", err)
			}
			if inner.LastProviderKey != tt.wantProvider {
				t.Fatalf("lastProviderKey = %q, want %q", inner.LastProviderKey, tt.wantProvider)
			}
		})
	}
}

func TestVisibleMethodLoadBalancerPreservesLegacyCrossProviderRoutingWhenSourceMissing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := sqlitetest.NewClient(t)

	_, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("Official Alipay").
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetSortOrder(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create official provider: %v", err)
	}

	_, err = client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeEasyPay).
		SetName("EasyPay Alipay").
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetSortOrder(2).
		Save(ctx)
	if err != nil {
		t.Fatalf("create easypay provider: %v", err)
	}

	inner := &paymenttestkit.CaptureLoadBalancer{}
	configService := paymenttestkit.Configuration(client, &paymentConfigSettingRepoStub{
		values: map[string]string{
			payment.ResumeVisibleMethodSourceSettingKey(payment.TypeAlipay): "",
		},
	}, nil)
	lb := payment.NewVisibleMethodLoadBalancer(inner, configService)

	_, err = lb.SelectInstance(ctx, "", payment.TypeAlipay, payment.StrategyRoundRobin, 9.9)
	if err != nil {
		t.Fatalf("SelectInstance returned error: %v", err)
	}
	if inner.LastProviderKey != "" {
		t.Fatalf("lastProviderKey = %q, want legacy cross-provider empty key", inner.LastProviderKey)
	}
	if inner.LastPaymentType != payment.TypeAlipay {
		t.Fatalf("lastPaymentType = %q, want %q", inner.LastPaymentType, payment.TypeAlipay)
	}
}

func TestVisibleMethodLoadBalancerRejectsInvalidSourceWhenMultipleProvidersEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		method      payment.PaymentType
		sourceValue string
		wantMessage string
	}{
		{
			name:        "invalid wxpay source",
			method:      payment.TypeWxpay,
			sourceValue: "stripe",
			wantMessage: "wxpay source must be one of the supported payment providers",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			client := sqlitetest.NewClient(t)

			officialProviderKey := payment.TypeAlipay
			officialSupportedTypes := "alipay"
			officialName := "Official Alipay"
			easyPaySupportedTypes := "alipay"
			easyPayName := "EasyPay Alipay"
			if tt.method == payment.TypeWxpay {
				officialProviderKey = payment.TypeWxpay
				officialSupportedTypes = "wxpay"
				officialName = "Official WeChat"
				easyPaySupportedTypes = "wxpay"
				easyPayName = "EasyPay WeChat"
			}

			_, err := client.PaymentProviderInstance.Create().
				SetProviderKey(officialProviderKey).
				SetName(officialName).
				SetConfig("{}").
				SetSupportedTypes(officialSupportedTypes).
				SetEnabled(true).
				SetSortOrder(1).
				Save(ctx)
			if err != nil {
				t.Fatalf("create official provider: %v", err)
			}

			_, err = client.PaymentProviderInstance.Create().
				SetProviderKey(payment.TypeEasyPay).
				SetName(easyPayName).
				SetConfig("{}").
				SetSupportedTypes(easyPaySupportedTypes).
				SetEnabled(true).
				SetSortOrder(2).
				Save(ctx)
			if err != nil {
				t.Fatalf("create easypay provider: %v", err)
			}

			inner := &paymenttestkit.CaptureLoadBalancer{}
			configService := paymenttestkit.Configuration(client, &paymentConfigSettingRepoStub{
				values: map[string]string{
					payment.ResumeVisibleMethodSourceSettingKey(tt.method): tt.sourceValue,
				},
			}, nil)
			lb := payment.NewVisibleMethodLoadBalancer(inner, configService)

			_, err = lb.SelectInstance(ctx, "", tt.method, payment.StrategyRoundRobin, 9.9)
			if err == nil {
				t.Fatal("SelectInstance should reject invalid visible method source configuration")
			}
			if apperror.Reason(err) != "INVALID_PAYMENT_VISIBLE_METHOD_SOURCE" {
				t.Fatalf("Reason(err) = %q, want %q", apperror.Reason(err), "INVALID_PAYMENT_VISIBLE_METHOD_SOURCE")
			}
			if apperror.Message(err) != tt.wantMessage {
				t.Fatalf("Message(err) = %q, want %q", apperror.Message(err), tt.wantMessage)
			}
		})
	}
}

func TestVisibleMethodLoadBalancerRejectsMissingEnabledVisibleMethodProvider(t *testing.T) {
	t.Parallel()

	inner := &paymenttestkit.CaptureLoadBalancer{}
	configService := paymenttestkit.Configuration(sqlitetest.NewClient(t), nil, nil)
	lb := payment.NewVisibleMethodLoadBalancer(inner, configService)

	if _, err := lb.SelectInstance(context.Background(), "", payment.TypeWxpay, payment.StrategyRoundRobin, 9.9); err == nil {
		t.Fatal("SelectInstance should reject when no enabled provider instance exists")
	}
}

func (p webhookProviderTestDouble) Name() string { return p.key }

func (p webhookProviderTestDouble) ProviderKey() string { return p.key }

func (p webhookProviderTestDouble) SupportedTypes() []payment.PaymentType { return p.types }

func (p webhookProviderTestDouble) CreatePayment(context.Context, payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	panic("unexpected call")
}

func (p webhookProviderTestDouble) QueryOrder(context.Context, string) (*payment.QueryOrderResponse, error) {
	panic("unexpected call")
}

func (p webhookProviderTestDouble) VerifyNotification(context.Context, string, map[string]string) (*payment.PaymentNotification, error) {
	panic("unexpected call")
}

func (p webhookProviderTestDouble) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
	panic("unexpected call")
}
