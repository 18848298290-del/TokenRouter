package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/payment"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// admin_test.go verifies administrator order responses, dashboard ranges,
// and refund recovery. Administrator displays retain invoice/billing values while
// withholding internal merchant snapshot data. These tests also use the
// injected calendar across a daylight-saving date and verify that missing
// refund recovery evidence reaches HTTP as a conflict requiring manual
// verification.

// missingRefundRecovery implements only the order and preparation reads
// exercised by the HTTP recovery test.
type missingRefundRecovery struct {
	// RefundStore is a nil embedded fixture dependency for unused methods.
	// Calling an unimplemented promoted operation would panic, keeping this fake
	// limited to its tested recovery path.
	payment.RefundStore
}

// TestPaymentDashboardRangeInjectedCalendar parses the spring-transition date
// using New York's injected calendar. It requires adjacent midnights to span
// twenty-three hours and checks bad-request responses for the listed
// incomplete/invalid/reversed ranges.
func TestPaymentDashboardRangeInjectedCalendar(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	calendar := timezone.NewCalendar(loc)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/?start_date=2026-03-08&end_date=2026-03-08", nil)
	start, end, ok := parsePaymentDashboardRange(c, calendar)
	require.True(t, ok)
	require.Equal(t, time.Date(2026, 3, 8, 0, 0, 0, 0, loc), start)
	require.Equal(t, time.Date(2026, 3, 9, 0, 0, 0, 0, loc), end)
	require.Equal(t, 23*time.Hour, end.Sub(start))

	for _, query := range []string{
		"start_date=2026-03-08",
		"start_date=bad&end_date=2026-03-08",
		"start_date=2026-03-09&end_date=2026-03-08",
	} {
		t.Run(query, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/?"+query, nil)
			_, _, ok := parsePaymentDashboardRange(c, calendar)
			require.False(t, ok)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
}

// TestSanitizeAdminPaymentOrderForResponseAddsCurrencyAndKeepsForkFields
// supplies fee, invoice and billing data with a USD merchant snapshot. The
// result must expose tested display values while serialized output omits the
// provider snapshot and its test secret.
func TestSanitizeAdminPaymentOrderForResponseAddsCurrencyAndKeepsForkFields(t *testing.T) {
	now := time.Now()
	invoiceID := "in_202606250001"
	invoiceURL := "https://pay.example.com/invoices/in_202606250001"
	invoicePDF := "https://pay.example.com/invoices/in_202606250001.pdf"
	invoiceStatus := "paid"
	order := &payment.Order{
		ID:                   1,
		UserID:               2,
		Amount:               100,
		PayAmount:            108,
		FeeRate:              8,
		FeeFixed:             2,
		FeeRateAmount:        6,
		FeeAmount:            8,
		OutTradeNo:           "sub2_202606250001",
		PaymentType:          "stripe",
		OrderType:            "subscription",
		Status:               "COMPLETED",
		PaymentInvoiceID:     &invoiceID,
		PaymentInvoiceURL:    &invoiceURL,
		PaymentInvoicePdfURL: &invoicePDF,
		PaymentInvoiceStatus: &invoiceStatus,
		BillingSnapshot: map[string]any{
			"email": "buyer@example.com",
		},
		ProviderSnapshot: map[string]any{
			"schema_version": 2,
			"currency":       "USD",
			"secret":         "should-not-leak",
		},
		ExpiresAt: now,
		CreatedAt: now,
		UpdatedAt: now,
	}

	got := AdminSanitizeAdminPaymentOrderForResponse(order)
	require.NotNil(t, got)
	if got.Currency != "USD" {
		t.Fatalf("expected currency USD, got %q", got.Currency)
	}
	if got.FeeFixed != 2 || got.FeeRateAmount != 6 || got.FeeAmount != 8 {
		t.Fatalf("expected fork fee fields to be preserved, got fixed=%v rate=%v total=%v", got.FeeFixed, got.FeeRateAmount, got.FeeAmount)
	}
	if got.PaymentInvoiceID == nil || *got.PaymentInvoiceID != invoiceID {
		t.Fatalf("expected payment invoice id %q, got %#v", invoiceID, got.PaymentInvoiceID)
	}
	if got.BillingSnapshot["email"] != "buyer@example.com" {
		t.Fatalf("expected billing snapshot to be preserved, got %#v", got.BillingSnapshot)
	}

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal sanitized order: %v", err)
	}
	if strings.Contains(string(body), "provider_snapshot") || strings.Contains(string(body), "should-not-leak") {
		t.Fatalf("expected provider_snapshot to be omitted, got %s", string(body))
	}
}

// TestRefundRecoveryErrorIsVisibleToAdministrator routes a refund query into a
// fake missing-preparation result. It requires HTTP 409 with
// REFUND_RECOVERY_REQUIRED and manual-verification wording, protecting the
// administrator-visible recovery decision.
func TestRefundRecoveryErrorIsVisibleToAdministrator(t *testing.T) {
	runtime := &payment.Runtime{RefundWorkflow: payment.NewRefundWorkflow(missingRefundRecovery{}, payment.RefundRuntime{})}
	handler := NewAdminHandler(runtime, nil, nil, timezone.NewCalendar(time.Local))
	router := gin.New()
	router.POST("/orders/:id/refund/query", handler.QueryAndFinalizeRefund)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/orders/1/refund/query", nil))
	require.Equal(t, 409, recorder.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, "REFUND_RECOVERY_REQUIRED", body["reason"])
	require.Contains(t, body["message"], "manual verification")
}

// Order returns a fixed REFUNDING observation for the administrator HTTP
// recovery fixture.
func (missingRefundRecovery) Order(context.Context, int64) (*payment.Order, error) {
	return &payment.Order{ID: 1, Status: payment.OrderStatusRefunding}, nil
}

// RefundRecovery returns missing-preparation manual recovery for the fixed
// HTTP test order.
func (missingRefundRecovery) RefundRecovery(context.Context, *payment.Order) (*payment.RefundReceipt, error) {
	return nil, payment.RefundRecoveryRequired("preparation record unavailable")
}

// TestAdminOrderResultSeparatesPriceCurrency supplies a USD plan collected in
// CNY. The response must retain the original price and payment amounts while
// publishing the correct currency for each quantity.
func TestAdminOrderResultSeparatesPriceCurrency(t *testing.T) {
	order := &payment.Order{OrderType: payment.OrderTypeSubscription, Amount: 10, PayAmount: 72, FeeAmount: 2, ProviderSnapshot: map[string]any{"currency": "CNY"}}
	order.PlanSnapshot.Currency = "USD"
	result := AdminSanitizeAdminPaymentOrderForResponse(order)
	require.Equal(t, "USD", result.AmountCurrency)
	require.Equal(t, "CNY", result.Currency)
	require.Equal(t, 10.0, result.Amount)
	require.Equal(t, 72.0, result.PayAmount)
}
