package payment_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	paymenthttp "github.com/TokenFlux/TokenRouter/internal/payment/httpapi"
	paymentpostgres "github.com/TokenFlux/TokenRouter/internal/payment/postgres"
)

// Webhook acknowledgements vary by payment provider: WeChat receives a JSON
// success object, Stripe and Airwallex receive empty responses, and the
// exercised form providers receive plain success text. Extractor and lookup
// cases check supplied order references and distinguish absent configuration
// from a failed database lookup. Notification matching uses configured stubs,
// so its assertions concern candidate selection and returned values.

// webhookHandlerProviderStub supplies configured verification outcomes for
// webhook candidate selection.
type webhookHandlerProviderStub struct {
	key string // provider selection and display key
	// notification and verifyErr select the fixture outcome: a nonnil error
	// wins, while a nil error returns the configured notification even when
	// that pointer is nil. notification retains caller-owned data;
	// candidate selection receives this pointer directly, so mutations
	// through the returned notification affect the supplied object.
	notification *payment.PaymentNotification // configured accepted notification
	verifyErr    error                        // configured candidate rejection
}

// TestWriteSuccessResponse checks acknowledgements for the supplied provider
// keys and an unknown key. Each response has the expected HTTP code and content
// type, followed by its JSON fields or exact plain body.
func TestWriteSuccessResponse(t *testing.T) {
	tests := []struct {
		name            string // acknowledgement scenario name
		providerKey     string // acknowledgement provider selection
		wantCode        int    // expected HTTP status
		wantContentType string // expected content-type substring
		// checkJSON selects the comparison: wantJSONCode and
		// wantJSONMessage apply to the WeChat object, while wantBody is
		// compared exactly for the other supplied providers.
		wantBody        string // expected plain response body
		checkJSON       bool   // compare WeChat JSON fields
		wantJSONCode    string // expected JSON acknowledgement code
		wantJSONMessage string // expected JSON acknowledgement message
	}{
		{
			name:            "wxpay returns JSON with code SUCCESS",
			providerKey:     "wxpay",
			wantCode:        http.StatusOK,
			wantContentType: "application/json",
			checkJSON:       true,
			wantJSONCode:    "SUCCESS",
			wantJSONMessage: "成功",
		},
		{
			name:            "stripe returns empty 200",
			providerKey:     "stripe",
			wantCode:        http.StatusOK,
			wantContentType: "text/plain",
			wantBody:        "",
		},
		{
			name:            "airwallex returns empty 200",
			providerKey:     payment.TypeAirwallex,
			wantCode:        http.StatusOK,
			wantContentType: "text/plain",
			wantBody:        "",
		},
		{
			name:            "easypay returns plain text success",
			providerKey:     "easypay",
			wantCode:        http.StatusOK,
			wantContentType: "text/plain",
			wantBody:        "success",
		},
		{
			name:            "alipay returns plain text success",
			providerKey:     "alipay",
			wantCode:        http.StatusOK,
			wantContentType: "text/plain",
			wantBody:        "success",
		},
		{
			name:            "unknown provider returns plain text success",
			providerKey:     "unknown_provider",
			wantCode:        http.StatusOK,
			wantContentType: "text/plain",
			wantBody:        "success",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			paymenthttp.WebhookWriteSuccessResponse(c, tt.providerKey)

			assert.Equal(t, tt.wantCode, w.Code)
			assert.Contains(t, w.Header().Get("Content-Type"), tt.wantContentType)

			if tt.checkJSON {
				var resp paymenthttp.WebhookWxpaySuccessResponse
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err, "response body should be valid JSON")
				assert.Equal(t, tt.wantJSONCode, resp.Code)
				assert.Equal(t, tt.wantJSONMessage, resp.Message)
			} else {
				assert.Equal(t, tt.wantBody, w.Body.String())
			}
		})
	}
}

// TestWebhookConstants checks the configured webhook body-size and
// log-truncation constants. The assertions protect their numeric values; they
// do not send an oversized body or exercise truncation.
func TestWebhookConstants(t *testing.T) {
	t.Run("paymenthttp.WebhookMaxWebhookBodySize is 1MB", func(t *testing.T) {
		assert.Equal(t, int64(1<<20), int64(paymenthttp.WebhookMaxWebhookBodySize))
	})

	t.Run("paymenthttp.WebhookWebhookLogTruncateLen is 200", func(t *testing.T) {
		assert.Equal(t, 200, paymenthttp.WebhookWebhookLogTruncateLen)
	})
}

// TestWebhookProviderLookupErrorsOnlyAcknowledgeMissingConfiguration compares
// missing provider configuration with a generic database error. The
// acknowledgement predicate accepts the missing-provider sentinel and rejects
// the supplied lookup failure.
func TestWebhookProviderLookupErrorsOnlyAcknowledgeMissingConfiguration(t *testing.T) {
	require.True(t, paymenthttp.WebhookShouldAcknowledgeWebhookProviderLookupError(payment.ErrProviderNotFound))
	require.False(t, paymenthttp.WebhookShouldAcknowledgeWebhookProviderLookupError(errors.New("database unavailable")))
}

// TestStripeWebhookReturnsServerErrorWhenProviderLookupDatabaseFails closes the
// SQLite client before handling a Stripe payload with an order reference. The
// handler returns HTTP 500 and the provider-lookup failure body.
func TestStripeWebhookReturnsServerErrorWhenProviderLookupDatabaseFails(t *testing.T) {
	db, err := sql.Open("sqlite", "file:payment_webhook_lookup_failure?mode=memory&cache=shared")
	require.NoError(t, err)
	driver := entsql.OpenDB(dialect.SQLite, db)
	client := dbent.NewClient(dbent.Driver(driver))
	bindings := payment.NewProviderBindings(paymentpostgres.NewInstanceStore(client), payment.NewRegistry(), nil, payment.BindingRuntime{}, false)
	paymentSvc := &payment.Runtime{ProviderBindings: bindings}
	// Close the retained client before provider lookup so the handler
	// encounters a database failure while resolving this payload, rather
	// than an absent configured provider.
	require.NoError(t, client.Close())

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/payment/webhook/stripe", strings.NewReader(
		`{"data":{"object":{"metadata":{"orderId":"sub2_database_failure"}}}}`,
	))

	paymenthttp.NewPaymentWebhookHandler(paymentSvc).StripeWebhook(ctx)

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Equal(t, "provider lookup failed", recorder.Body.String())
}

// TestExtractOutTradeNo supplies form, Stripe metadata, and Airwallex
// merchant-order payloads to the provider-specific extractor. It checks the
// expected references and the empty result for the supplied unsupported WeChat
// shape.
func TestExtractOutTradeNo(t *testing.T) {
	tests := []struct {
		name        string // reference extraction scenario
		providerKey string // payload format selector
		rawBody     string // supplied notification payload
		want        string // expected extracted reference
	}{
		{
			name:        "easypay query payload",
			providerKey: "easypay",
			rawBody:     "out_trade_no=sub2_123&trade_status=TRADE_SUCCESS",
			want:        "sub2_123",
		},
		{
			name:        "alipay query payload",
			providerKey: "alipay",
			rawBody:     "notify_time=2026-04-20+12%3A00%3A00&out_trade_no=sub2_456",
			want:        "sub2_456",
		},
		{
			name:        "stripe invoice payload metadata",
			providerKey: "stripe",
			rawBody:     `{"data":{"object":{"metadata":{"orderId":" sub2_stripe_invoice "}}}}`,
			want:        "sub2_stripe_invoice",
		},
		{
			name:        "unknown provider",
			providerKey: "wxpay",
			rawBody:     "{}",
			want:        "",
		},
		{
			name:        "airwallex payment intent payload",
			providerKey: payment.TypeAirwallex,
			rawBody:     `{"name":"payment_intent.succeeded","data":{"object":{"merchant_order_id":"sub2_awx_123"}}}`,
			want:        "sub2_awx_123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, paymenthttp.WebhookExtractOutTradeNo(tt.rawBody, tt.providerKey))
		})
	}
}

// TestExtractStripeOutTradeNo checks a trimmed metadata order ID alongside
// malformed JSON and absent metadata. Parallel cases use independent string
// inputs and expect empty results for the invalid or missing shapes.
func TestExtractStripeOutTradeNo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string // Stripe extraction scenario
		rawBody string // supplied Stripe JSON payload
		want    string // expected trimmed order reference
	}{
		{
			name:    "extracts order id from metadata",
			rawBody: `{"data":{"object":{"metadata":{"orderId":" sub2_123 "}}}}`,
			want:    "sub2_123",
		},
		{
			name:    "invalid json returns empty",
			rawBody: `{`,
			want:    "",
		},
		{
			name:    "missing metadata returns empty",
			rawBody: `{"data":{"object":{}}}`,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, paymenthttp.WebhookExtractStripeOutTradeNo(tt.rawBody))
		})
	}
}

// TestVerifyNotificationWithProvidersReturnsMatchedProvider supplies one
// rejecting stub followed by one configured notification. Matching succeeds
// with the second provider key and the expected order ID from its returned
// notification.
func TestVerifyNotificationWithProvidersReturnsMatchedProvider(t *testing.T) {
	firstErr := errors.New("wrong provider")
	providers := []payment.Provider{
		webhookHandlerProviderStub{
			key:       payment.TypeWxpay,
			verifyErr: firstErr,
		},
		webhookHandlerProviderStub{
			key: payment.TypeWxpay,
			notification: &payment.PaymentNotification{
				OrderID: "sub2_42",
				TradeNo: "trade-42",
				Status:  payment.NotificationStatusSuccess,
			},
		},
	}

	providerKey, notification, err := paymenthttp.WebhookVerifyNotificationWithProviders(context.Background(), providers, "{}", map[string]string{"wechatpay-signature": "sig"})
	require.NoError(t, err)
	require.Equal(t, payment.TypeWxpay, providerKey)
	require.NotNil(t, notification)
	require.Equal(t, "sub2_42", notification.OrderID)
}

// TestVerifyNotificationWithProvidersFailsWhenAllProvidersReject supplies two
// stubs with distinct verification errors. Candidate selection returns an error
// when neither configured provider accepts the notification input.
func TestVerifyNotificationWithProvidersFailsWhenAllProvidersReject(t *testing.T) {
	providers := []payment.Provider{
		webhookHandlerProviderStub{
			key:       payment.TypeWxpay,
			verifyErr: errors.New("verify failed a"),
		},
		webhookHandlerProviderStub{
			key:       payment.TypeWxpay,
			verifyErr: errors.New("verify failed b"),
		},
	}

	_, _, err := paymenthttp.WebhookVerifyNotificationWithProviders(context.Background(), providers, "{}", nil)
	require.Error(t, err)
}

// Name returns the configured provider key as this fixture's display name.
func (p webhookHandlerProviderStub) Name() string { return p.key }

// ProviderKey returns the configured key used for webhook candidate selection.
func (p webhookHandlerProviderStub) ProviderKey() string { return p.key }

// SupportedTypes returns a new slice containing the fixture's configured
// payment key.
func (p webhookHandlerProviderStub) SupportedTypes() []payment.PaymentType {
	return []payment.PaymentType{p.key}
}

// CreatePayment panics when webhook tests unexpectedly enter payment creation.
func (p webhookHandlerProviderStub) CreatePayment(context.Context, payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	panic("unexpected call")
}

// QueryOrder panics when webhook tests unexpectedly request provider order
// status.
func (p webhookHandlerProviderStub) QueryOrder(context.Context, string) (*payment.QueryOrderResponse, error) {
	panic("unexpected call")
}

// VerifyNotification returns verifyErr when configured, otherwise the retained
// notification pointer. It ignores context, payload, and headers; callers can
// receive nil with no error and share the supplied notification's mutable data.
func (p webhookHandlerProviderStub) VerifyNotification(context.Context, string, map[string]string) (*payment.PaymentNotification, error) {
	if p.verifyErr != nil {
		return nil, p.verifyErr
	}
	return p.notification, nil
}

// Refund panics when webhook tests unexpectedly enter a provider refund.
func (p webhookHandlerProviderStub) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
	panic("unexpected call")
}
