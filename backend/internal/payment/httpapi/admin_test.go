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

// missingRefundRecovery returns a manual-recovery error for HTTP response tests.
type missingRefundRecovery struct{ payment.RefundStore }

// TestPaymentDashboardRangeInjectedCalendar checks date ranges in the injected server time zone.
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

func (missingRefundRecovery) Order(context.Context, int64) (*payment.Order, error) {
	return &payment.Order{ID: 1, Status: payment.OrderStatusRefunding}, nil
}

func (missingRefundRecovery) RefundRecovery(context.Context, *payment.Order) (*payment.RefundReceipt, error) {
	return nil, payment.RefundRecoveryRequired("preparation record unavailable")
}
