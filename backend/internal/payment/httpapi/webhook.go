package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/payment"
)

const (
	// WebhookMaxWebhookBodySize is the maximum allowed webhook request body size (1 MB).
	WebhookMaxWebhookBodySize = 1 << 20

	// WebhookWebhookLogTruncateLen is the maximum length of raw body logged on verify failure.
	WebhookWebhookLogTruncateLen = 200

	// WeChat Pay webhook success response constants.
	WebhookWxpaySuccessCode    = "SUCCESS"
	WebhookWxpaySuccessMessage = "成功"
)

// PaymentWebhookHandler handles payment provider webhook callbacks.
type PaymentWebhookHandler struct {
	paymentService *payment.Runtime
	registry       *payment.Registry
}

// WebhookWxpaySuccessResponse is the JSON response expected by WeChat Pay webhook.
type WebhookWxpaySuccessResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NewPaymentWebhookHandler creates a new PaymentWebhookHandler.
func NewPaymentWebhookHandler(paymentService *payment.Runtime, registry *payment.Registry) *PaymentWebhookHandler {
	return &PaymentWebhookHandler{
		paymentService: paymentService,
		registry:       registry,
	}
}

// EasyPayNotify handles EasyPay payment notifications.
// POST /api/v1/payment/webhook/easypay
func (h *PaymentWebhookHandler) EasyPayNotify(c *gin.Context) {
	h.handleNotify(c, payment.TypeEasyPay)
}

// AlipayNotify handles Alipay payment notifications.
// POST /api/v1/payment/webhook/alipay
func (h *PaymentWebhookHandler) AlipayNotify(c *gin.Context) {
	h.handleNotify(c, payment.TypeAlipay)
}

// WxpayNotify handles WeChat Pay payment notifications.
// POST /api/v1/payment/webhook/wxpay
func (h *PaymentWebhookHandler) WxpayNotify(c *gin.Context) {
	h.handleNotify(c, payment.TypeWxpay)
}

// StripeWebhook handles Stripe webhook events.
// POST /api/v1/payment/webhook/stripe
func (h *PaymentWebhookHandler) StripeWebhook(c *gin.Context) {
	h.handleNotify(c, payment.TypeStripe)
}

// AirwallexWebhook handles Airwallex webhook events.
// POST /api/v1/payment/webhook/airwallex
func (h *PaymentWebhookHandler) AirwallexWebhook(c *gin.Context) {
	h.handleNotify(c, payment.TypeAirwallex)
}

// handleNotify is the shared logic for all provider webhook handlers.
func (h *PaymentWebhookHandler) handleNotify(c *gin.Context, providerKey string) {
	var rawBody string
	if c.Request.Method == http.MethodGet {
		// GET callbacks (e.g. EasyPay) pass params as URL query string
		rawBody = c.Request.URL.RawQuery
	} else {
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, WebhookMaxWebhookBodySize))
		if err != nil {
			slog.Error("[Payment Webhook] failed to read body", "provider", providerKey, "error", err)
			c.String(http.StatusBadRequest, "failed to read body")
			return
		}
		rawBody = string(body)
	}

	// Extract out_trade_no to look up the order's specific provider instance.
	// This is needed when multiple instances of the same provider exist (e.g. multiple EasyPay accounts).
	outTradeNo := WebhookExtractOutTradeNo(rawBody, providerKey)

	providers, err := h.paymentService.GetWebhookProviders(c.Request.Context(), providerKey, outTradeNo)
	if err != nil {
		slog.Warn("[Payment Webhook] provider not found", "provider", providerKey, "outTradeNo", outTradeNo, "error", err)
		if !WebhookShouldAcknowledgeWebhookProviderLookupError(err) {
			c.String(http.StatusInternalServerError, "provider lookup failed")
			return
		}
		if providerKey == payment.TypeWxpay {
			c.String(http.StatusBadRequest, "verify failed")
			return
		}
		WebhookWriteSuccessResponse(c, providerKey)
		return
	}

	headers := make(map[string]string)
	for k := range c.Request.Header {
		headers[strings.ToLower(k)] = c.GetHeader(k)
	}

	resolvedProviderKey, notification, err := WebhookVerifyNotificationWithProviders(c.Request.Context(), providers, rawBody, headers)
	if err != nil {
		truncatedBody := rawBody
		if len(truncatedBody) > WebhookWebhookLogTruncateLen {
			truncatedBody = truncatedBody[:WebhookWebhookLogTruncateLen] + "...(truncated)"
		}
		slog.Error("[Payment Webhook] verify failed", "provider", providerKey, "error", err, "method", c.Request.Method, "bodyLen", len(rawBody))
		slog.Debug("[Payment Webhook] verify failed body", "provider", providerKey, "rawBody", truncatedBody)
		c.String(http.StatusBadRequest, "verify failed")
		return
	}

	// nil notification means irrelevant event (e.g. Stripe non-payment event); return success.
	if notification == nil {
		WebhookWriteSuccessResponse(c, resolvedProviderKey)
		return
	}

	if err := h.paymentService.HandlePaymentNotification(c.Request.Context(), notification, resolvedProviderKey); err != nil {
		// Acknowledge missing local orders to stop retries for callbacks that cannot be fulfilled.
		if errors.Is(err, payment.ErrOrderNotFound) {
			slog.Warn("[Payment Webhook] unknown order, acking to stop retries",
				"provider", resolvedProviderKey,
				"outTradeNo", notification.OrderID,
				"tradeNo", notification.TradeNo,
			)
			WebhookWriteSuccessResponse(c, resolvedProviderKey)
			return
		}
		slog.Error("[Payment Webhook] handle notification failed", "provider", resolvedProviderKey, "error", err)
		c.String(http.StatusInternalServerError, "handle failed")
		return
	}

	WebhookWriteSuccessResponse(c, resolvedProviderKey)
}

func WebhookShouldAcknowledgeWebhookProviderLookupError(err error) bool {
	return errors.Is(err, payment.ErrProviderNotFound)
}

// WebhookExtractOutTradeNo parses the webhook body to find the out_trade_no.
// This allows looking up the correct provider instance before verification.
func WebhookExtractOutTradeNo(rawBody, providerKey string) string {
	switch providerKey {
	case payment.TypeEasyPay, payment.TypeAlipay:
		values, err := url.ParseQuery(rawBody)
		if err == nil {
			return values.Get("out_trade_no")
		}
	case payment.TypeStripe:
		return WebhookExtractStripeOutTradeNo(rawBody)
	case payment.TypeAirwallex:
		var payload struct {
			Data struct {
				Object struct {
					MerchantOrderID string `json:"merchant_order_id"`
				} `json:"object"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(rawBody), &payload); err == nil {
			return strings.TrimSpace(payload.Data.Object.MerchantOrderID)
		}
	}
	// Other direct providers require candidate verification because the order number cannot be reliably extracted here.
	return ""
}

func WebhookExtractStripeOutTradeNo(rawBody string) string {
	var payload struct {
		Data struct {
			Object struct {
				Metadata map[string]string `json:"metadata"`
			} `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(rawBody), &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Data.Object.Metadata["orderId"])
}

func WebhookVerifyNotificationWithProviders(ctx context.Context, providers []payment.Provider, rawBody string, headers map[string]string) (string, *payment.PaymentNotification, error) {
	var lastErr error
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		notification, err := provider.VerifyNotification(ctx, rawBody, headers)
		if err != nil {
			lastErr = err
			continue
		}
		return provider.ProviderKey(), notification, nil
	}
	if lastErr != nil {
		return "", nil, lastErr
	}
	return "", nil, fmt.Errorf("no webhook provider could verify notification")
}

// WebhookWriteSuccessResponse writes the acknowledgement required by each provider.
// WeChat Pay expects WebhookWxpaySuccessResponse with its protocol success message.
// Stripe and Airwallex accept an empty 200 response; other providers accept plain-text "success".
func WebhookWriteSuccessResponse(c *gin.Context, providerKey string) {
	switch providerKey {
	case payment.TypeWxpay:
		c.JSON(http.StatusOK, WebhookWxpaySuccessResponse{Code: WebhookWxpaySuccessCode, Message: WebhookWxpaySuccessMessage})
	case payment.TypeStripe, payment.TypeAirwallex:
		c.String(http.StatusOK, "")
	default:
		c.String(http.StatusOK, "success")
	}
}
