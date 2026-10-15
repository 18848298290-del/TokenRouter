package postgres

import (
	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/payment"
)

// order_values.go converts persisted payment entities into domain orders and
// delegates mutable-value copying to Order.Clone.
// Ent order rows contain optional scalar pointers and snapshot maps that
// payment callers can retain beyond the query. Domain conversion maps their
// persisted observations, then uses Order.Clone to detach its recognized
// mutable data while keeping the original numeric and nil/empty
// representation.

// OrderFromEntity returns nil for nil input or a copied payment-domain value
// for the Ent row. It maps stored fields first, then delegates pointer and
// recognized snapshot collection detachment to Order.Clone; unsupported
// mutable snapshot values retain that helper's sharing rules.
func OrderFromEntity(v *dbent.PaymentOrder) *payment.Order {
	if v == nil {
		return nil
	}
	value := &payment.Order{
		ID:                   v.ID,
		UserID:               v.UserID,
		UserEmail:            v.UserEmail,
		UserName:             v.UserName,
		UserNotes:            v.UserNotes,
		Amount:               v.Amount,
		PayAmount:            v.PayAmount,
		FeeRate:              v.FeeRate,
		FeeFixed:             v.FeeFixed,
		FeeRateAmount:        v.FeeRateAmount,
		FeeAmount:            v.FeeAmount,
		RechargeCode:         v.RechargeCode,
		OutTradeNo:           v.OutTradeNo,
		PaymentType:          v.PaymentType,
		PaymentTradeNo:       v.PaymentTradeNo,
		PayURL:               v.PayURL,
		QrCode:               v.QrCode,
		QrCodeImg:            v.QrCodeImg,
		PaymentCustomerID:    v.PaymentCustomerID,
		PaymentInvoiceID:     v.PaymentInvoiceID,
		PaymentInvoiceURL:    v.PaymentInvoiceURL,
		PaymentInvoicePdfURL: v.PaymentInvoicePdfURL,
		PaymentInvoiceStatus: v.PaymentInvoiceStatus,
		BillingSnapshot:      v.BillingSnapshot,
		OrderType:            v.OrderType,
		PlanID:               v.PlanID,
		PlanSnapshot:         v.PlanSnapshot,
		ProviderInstanceID:   v.ProviderInstanceID,
		ProviderKey:          v.ProviderKey,
		ProviderSnapshot:     v.ProviderSnapshot,
		Status:               v.Status,
		RefundAmount:         v.RefundAmount,
		RefundReason:         v.RefundReason,
		RefundAt:             v.RefundAt,
		ForceRefund:          v.ForceRefund,
		RefundRequestedAt:    v.RefundRequestedAt,
		RefundRequestReason:  v.RefundRequestReason,
		RefundRequestedBy:    v.RefundRequestedBy,
		ExpiresAt:            v.ExpiresAt,
		PaidAt:               v.PaidAt,
		CompletedAt:          v.CompletedAt,
		FailedAt:             v.FailedAt,
		FailedReason:         v.FailedReason,
		ClientIP:             v.ClientIP,
		SrcHost:              v.SrcHost,
		SrcURL:               v.SrcURL,
		CreatedAt:            v.CreatedAt,
		UpdatedAt:            v.UpdatedAt,
	}
	return value.Clone()
}
