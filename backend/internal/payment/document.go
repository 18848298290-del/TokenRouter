package payment

import (
	"context"
	"fmt"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Stripe orders can retain hosted invoice and PDF links, while legacy
// PaymentIntent orders may need a provider receipt lookup. Document
// queries prefer usable stored links and ask the order's provider when
// those links are absent, keeping ownership checks at the user query
// entry point.

// GetOrderPaymentDocument returns an owned order's Stripe invoice or
// legacy receipt. GetOrder checks the supplied user ID and maps storage
// errors to not-found before document selection; the helper can then
// return stored links or query the provider.
func (s *OrderQueries) GetOrderPaymentDocument(ctx context.Context, orderID, userID int64) (*PaymentDocumentResponse, error) {
	order, err := s.GetOrder(ctx, orderID, userID)
	if err != nil {
		return nil, err
	}
	return s.GetOrderPaymentDocumentValue(ctx, order)
}

// AdminGetOrderPaymentDocument returns an order invoice or receipt
// after reading its ID. The caller establishes administrator access;
// this method applies no user-ownership comparison, and document
// selection still requires a Stripe order.
func (s *OrderQueries) AdminGetOrderPaymentDocument(ctx context.Context, orderID int64) (*PaymentDocumentResponse, error) {
	order, err := s.GetOrderByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	return s.GetOrderPaymentDocumentValue(ctx, order)
}

// GetOrderPaymentDocumentValue returns usable stored invoice links or
// asks the order provider for an invoice or receipt. Pass an order
// authorized by the caller; nil input is not-found, and non-Stripe
// orders or providers without document support are rejected. A provider
// result without a usable URL is also not-found.
func (s *OrderQueries) GetOrderPaymentDocumentValue(ctx context.Context, order *Order) (*PaymentDocumentResponse, error) {
	if order == nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if GetBasePaymentType(order.PaymentType) != TypeStripe && strings.TrimSpace(refundStringValue(order.ProviderKey)) != TypeStripe {
		return nil, infraerrors.BadRequest("PAYMENT_DOCUMENT_UNSUPPORTED", "payment document is only supported for Stripe orders")
	}

	if doc := PaymentDocumentFromOrder(order); doc != nil {
		return doc, nil
	}

	// Legacy PaymentIntent orders can lack stored invoice links. Resolve
	// the historical order provider and use its invoice ID or trade
	// number to obtain the available document.
	prov, err := s.provider(ctx, order)
	if err != nil {
		return nil, fmt.Errorf("load order provider: %w", err)
	}
	docProvider, ok := prov.(DocumentProvider)
	if !ok {
		return nil, infraerrors.BadRequest("PAYMENT_DOCUMENT_UNSUPPORTED", "payment provider does not support documents")
	}
	doc, err := docProvider.GetPaymentDocument(ctx, refundStringValue(order.PaymentInvoiceID), strings.TrimSpace(order.PaymentTradeNo))
	if err != nil {
		return nil, fmt.Errorf("get payment document: %w", err)
	}
	if doc == nil || strings.TrimSpace(doc.URL) == "" {
		return nil, infraerrors.NotFound("PAYMENT_DOCUMENT_NOT_FOUND", "payment document is not available")
	}
	return doc, nil
}

// PaymentDocumentFromOrder returns an invoice document when the order
// contains a nonblank hosted or PDF URL. Hosted links take precedence;
// URLs are trimmed, while invoice ID and status are copied as stored.
// Nil orders and absent links return nil.
func PaymentDocumentFromOrder(order *Order) *PaymentDocumentResponse {
	if order == nil {
		return nil
	}
	hostedURL := strings.TrimSpace(refundStringValue(order.PaymentInvoiceURL))
	pdfURL := strings.TrimSpace(refundStringValue(order.PaymentInvoicePdfURL))
	if hostedURL == "" && pdfURL == "" {
		return nil
	}
	url := hostedURL
	// A PDF can be the only stored invoice link. Use it as the openable
	// URL while retaining the separate PDF field for clients that
	// want the download.
	if url == "" {
		url = pdfURL
	}
	return &PaymentDocumentResponse{
		Type:             "invoice",
		URL:              url,
		HostedInvoiceURL: hostedURL,
		InvoicePDF:       pdfURL,
		InvoiceID:        refundStringValue(order.PaymentInvoiceID),
		InvoiceStatus:    refundStringValue(order.PaymentInvoiceStatus),
	}
}
