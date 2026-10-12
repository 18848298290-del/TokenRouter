package payment

import (
	"context"
	"time"
)

const (
	// Supported payment type constants.
	TypeAlipay       PaymentType = "alipay"
	TypeWxpay        PaymentType = "wxpay"
	TypeAlipayDirect PaymentType = "alipay_direct"
	TypeWxpayDirect  PaymentType = "wxpay_direct"
	TypeStripe       PaymentType = "stripe"
	TypeCard         PaymentType = "card"
	TypeLink         PaymentType = "link"
	TypeEasyPay      PaymentType = "easypay"
	TypeAirwallex    PaymentType = "airwallex"

	// Order status constants shared across payment and service layers.
	OrderStatusPending           = "PENDING"
	OrderStatusProcessing        = "PROCESSING"
	OrderStatusPaid              = "PAID"
	OrderStatusRecharging        = "RECHARGING"
	OrderStatusCompleted         = "COMPLETED"
	OrderStatusExpired           = "EXPIRED"
	OrderStatusCancelled         = "CANCELLED"
	OrderStatusFailed            = "FAILED"
	OrderStatusRefundRequested   = "REFUND_REQUESTED"
	OrderStatusRefunding         = "REFUNDING"
	OrderStatusRefundPending     = "REFUND_PENDING"
	OrderStatusPartiallyRefunded = "PARTIALLY_REFUNDED"
	OrderStatusRefunded          = "REFUNDED"
	OrderStatusRefundFailed      = "REFUND_FAILED"

	// Order types distinguish balance recharges from subscription purchases.
	OrderTypeBalance      = "balance"
	OrderTypeSubscription = "subscription"

	// Entity statuses shared across users, groups, etc.
	EntityStatusActive = "active"

	// Deduction types for refund flow.
	DeductionTypeBalance      = "balance"
	DeductionTypeSubscription = "subscription"
	DeductionTypeNone         = "none"

	// Payment notification status values.
	NotificationStatusSuccess = "success"
	NotificationStatusPaid    = "paid"

	// Provider-level status constants returned by provider implementations
	// to the service layer (lowercase, distinct from OrderStatus uppercase constants).
	ProviderStatusPending    = "pending"
	ProviderStatusProcessing = "processing"
	ProviderStatusPaid       = "paid"
	ProviderStatusSuccess    = "success"
	ProviderStatusFailed     = "failed"
	ProviderStatusRefunded   = "refunded"

	// DefaultLoadBalanceStrategy is the default load-balancing strategy
	// used when no strategy is configured.
	DefaultLoadBalanceStrategy = "round-robin"

	// ConfigKeyPublishableKey is the config map key for Stripe's publishable key.
	ConfigKeyPublishableKey = "publishableKey"

	CreatePaymentResultOrderCreated  CreatePaymentResultType = "order_created"
	CreatePaymentResultOAuthRequired CreatePaymentResultType = "oauth_required"
	CreatePaymentResultJSAPIReady    CreatePaymentResultType = "jsapi_ready"
)

// PaymentType represents a supported payment method.
type PaymentType = string

// CreatePaymentRequest contains the parameters sent to a payment provider.
type CreatePaymentRequest struct {
	OrderID               string    // Merchant order number
	Amount                string    // Amount paid in the provider instance's configured currency
	PaymentType           string    // Payment method, such as "alipay", "wxpay", "stripe", or "airwallex"
	Subject               string    // Product or order description
	NotifyURL             string    // Provider notification URL
	ReturnURL             string    // Browser return URL after payment
	OpenID                string    // Payer OpenID for WeChat JSAPI
	ClientIP              string    // Payer IP address
	IsMobile              bool      // Whether the request comes from a mobile device
	AlipayMobilePrecreate bool      // Use Alipay face-to-face precreate for mobile checkout
	InstanceSubMethods    string    // Sub-methods from the instance's supported_types
	ExpiresAt             time.Time // Local order expiration used to set the provider invoice expiration
	UserEmail             string    // Payer email copied from the local user profile
	BillingInfo           *BillingInfo
}

// BillingAddress contains the optional billing address supplied by the payer.
type BillingAddress struct {
	Country    string `json:"country,omitempty"`
	Line1      string `json:"line1,omitempty"`
	Line2      string `json:"line2,omitempty"`
	City       string `json:"city,omitempty"`
	State      string `json:"state,omitempty"`
	PostalCode string `json:"postal_code,omitempty"`
}

// BillingInfo contains the invoice identity and contact details sent to the provider.
type BillingInfo struct {
	Name      string          `json:"name,omitempty"`
	Email     string          `json:"email,omitempty"`
	Address   *BillingAddress `json:"address,omitempty"`
	TaxIDType string          `json:"tax_id_type,omitempty"`
	TaxID     string          `json:"tax_id,omitempty"`
}

// CreatePaymentResultType describes the shape of the create-payment result.
type CreatePaymentResultType = string

// WechatOAuthInfo describes the next step when WeChat OAuth is required before payment.
type WechatOAuthInfo struct {
	AuthorizeURL string `json:"authorize_url,omitempty"`
	AppID        string `json:"appid,omitempty"`
	OpenID       string `json:"openid,omitempty"`
	Scope        string `json:"scope,omitempty"`
	State        string `json:"state,omitempty"`
	RedirectURL  string `json:"redirect_url,omitempty"`
}

// WechatJSAPIPayload contains the fields the frontend needs to invoke WeChat JSAPI payment.
type WechatJSAPIPayload struct {
	AppID     string `json:"appId,omitempty"`
	TimeStamp string `json:"timeStamp,omitempty"`
	NonceStr  string `json:"nonceStr,omitempty"`
	Package   string `json:"package,omitempty"`
	SignType  string `json:"signType,omitempty"`
	PaySign   string `json:"paySign,omitempty"`
}

// CreatePaymentResponse is returned after successfully initiating a payment.
type CreatePaymentResponse struct {
	TradeNo       string                  // Third-party transaction ID
	PayURL        string                  // H5 payment URL (alipay/wxpay)
	QRCode        string                  // QR code content for scanning
	ClientSecret  string                  // Stripe PaymentIntent client secret
	CustomerID    string                  // Customer ID at an invoice-based provider
	InvoiceID     string                  // Provider invoice ID
	InvoiceURL    string                  // Hosted invoice page URL
	InvoicePDF    string                  // Invoice PDF URL
	InvoiceStatus string                  // Provider invoice status
	IntentID      string                  // Provider payment intent ID required by the frontend SDK
	Currency      string                  // Provider payment currency
	CountryCode   string                  // Country or region code for the provider checkout
	PaymentEnv    string                  // Provider frontend environment identifier
	ExpiresAt     time.Time               // Order expiration returned by the provider
	ResultType    CreatePaymentResultType // Result variant for the frontend payment flow
	OAuth         *WechatOAuthInfo        // WeChat OAuth bootstrap payload when required
	JSAPI         *WechatJSAPIPayload     // WeChat JSAPI invocation payload when ready
}

// QueryOrderResponse describes the payment status from the upstream provider.
type QueryOrderResponse struct {
	TradeNo  string
	Status   string  // "pending", "processing", "paid", "failed", "refunded"
	Amount   float64 // Amount in the currency returned by the provider
	PaidAt   string  // RFC3339 timestamp or empty
	Metadata map[string]string
}

// PaymentNotification is the parsed result of a webhook/notify callback.
type PaymentNotification struct {
	TradeNo  string
	OrderID  string
	Amount   float64
	Status   string // "success", "paid", "processing", or "failed"
	RawData  string // Raw notification body for audit
	Metadata map[string]string
}

// PaymentDocumentResponse contains invoice or receipt links that the user can open.
type PaymentDocumentResponse struct {
	Type             string `json:"type"`
	URL              string `json:"url,omitempty"`
	HostedInvoiceURL string `json:"hosted_invoice_url,omitempty"`
	InvoicePDF       string `json:"invoice_pdf,omitempty"`
	ReceiptURL       string `json:"receipt_url,omitempty"`
	InvoiceID        string `json:"invoice_id,omitempty"`
	InvoiceStatus    string `json:"invoice_status,omitempty"`
}

// RefundRequest contains the parameters for requesting a refund.
type RefundRequest struct {
	TradeNo string
	OrderID string
	Amount  string // Refund amount formatted to 2 decimal places
	Reason  string
}

// RefundQueryRequest contains provider identifiers for querying an existing refund.
type RefundQueryRequest struct {
	TradeNo  string
	OrderID  string
	RefundID string
	Amount   string
}

// RefundResponse is returned after a refund request.
type RefundResponse struct {
	RefundID string
	Status   string // "success", "pending", "failed"
}

// InstanceSelection holds the selected provider instance and its decrypted config.
type InstanceSelection struct {
	InstanceID     string
	ProviderKey    string // Provider key of the selected instance (e.g. "alipay", "easypay")
	Config         map[string]string
	SupportedTypes string // Comma-separated list of supported payment types from the instance
	PaymentMode    string // Payment display mode: "qrcode", "redirect", "popup"
}

// Provider defines the interface that all payment providers must implement.
type Provider interface {
	// Name returns a human-readable name for this provider.
	Name() string
	// ProviderKey returns the unique key identifying this provider type (e.g. "easypay").
	ProviderKey() string
	// SupportedTypes returns the list of payment types this provider handles.
	SupportedTypes() []PaymentType
	// CreatePayment initiates a payment and returns the upstream response.
	CreatePayment(ctx context.Context, req CreatePaymentRequest) (*CreatePaymentResponse, error)
	// QueryOrder queries the payment status of the given trade number.
	QueryOrder(ctx context.Context, tradeNo string) (*QueryOrderResponse, error)
	// VerifyNotification parses and verifies a webhook callback.
	// Returns nil for unrecognized or irrelevant events (caller should return 200).
	VerifyNotification(ctx context.Context, rawBody string, headers map[string]string) (*PaymentNotification, error)
	// Refund requests a refund from the upstream provider.
	Refund(ctx context.Context, req RefundRequest) (*RefundResponse, error)
}

// RefundQueryProvider supports querying refund status.
type RefundQueryProvider interface {
	Provider
	QueryRefund(ctx context.Context, req RefundQueryRequest) (*RefundResponse, error)
}

// CancelableProvider extends Provider with the ability to cancel pending payments.
type CancelableProvider interface {
	Provider
	// CancelPayment cancels/expires a pending payment on the upstream platform.
	CancelPayment(ctx context.Context, tradeNo string) error
}

// DocumentProvider exposes invoice and receipt links for completed or historical orders.
type DocumentProvider interface {
	Provider
	// GetPaymentDocument prefers an invoice and falls back to a provider receipt.
	GetPaymentDocument(ctx context.Context, invoiceID string, tradeNo string) (*PaymentDocumentResponse, error)
}

// MerchantIdentityProvider exposes the current non-sensitive merchant identity
// derived from provider configuration for snapshot consistency checks.
type MerchantIdentityProvider interface {
	MerchantIdentityMetadata() map[string]string
}

// EncryptionKey distinguishes the wiring parameter; app decodes and validates its configuration.
type EncryptionKey []byte

// GetBasePaymentType extracts the base payment method from a composite key.
// For example, "alipay_direct" -> "alipay".
func GetBasePaymentType(t string) string {
	switch {
	case t == TypeEasyPay:
		return TypeEasyPay
	case t == TypeAirwallex:
		return TypeAirwallex
	case t == TypeStripe || t == TypeCard || t == TypeLink:
		return TypeStripe
	case len(t) >= len(TypeAlipay) && t[:len(TypeAlipay)] == TypeAlipay:
		return TypeAlipay
	case len(t) >= len(TypeWxpay) && t[:len(TypeWxpay)] == TypeWxpay:
		return TypeWxpay
	default:
		return t
	}
}
