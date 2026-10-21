package payment

import "time"

// checkout_types.go defines purchase inputs and the order or OAuth
// continuation returned to checkout clients.
// Checkout can return either a newly created local order or a WeChat
// OAuth step required before creating it. Monetary fields describe the
// base or credited entitlement and the separate channel amount, while
// provider-specific payloads carry the next action expected by the
// checkout frontend.

// CreateOrderRequest supplies buyer, product, channel, and client context for checkout.
type CreateOrderRequest struct {
	UserID int64 // checkout buyer identity
	// Amount supplies the balance payment base before fees and the
	// recharge multiplier. Subscription checkout takes the selected plan
	// price instead, so this request field is not its authoritative price.
	Amount          float64 // requested payment base
	PaymentType     string  // selected payment method
	OpenID          string  // WeChat payer identity
	ClientIP        string  // checkout client address
	IsMobile        bool    // mobile client hint
	IsWeChatBrowser bool    // WeChat browser hint
	SrcHost         string  // checkout source host
	SrcURL          string  // checkout source URL
	// ReturnURL is passed to CanonicalizeReturnURL before invoking the
	// channel. Checkout builds the provider result URL from that canonical
	// page with local order and optional resume-token parameters.
	ReturnURL     string // requested result-page URL
	PaymentSource string // checkout origin category
	// OrderType and PlanID choose the entitlement branch. CreateOrder
	// replaces an empty OrderType with balance before validation. A subscription
	// request loads PlanID and uses the plan terms; a balance request
	// uses Amount and the configured recharge multiplier for its credit.
	OrderType string // purchased entitlement type
	PlanID    int64  // selected subscription plan
	// BillingInfo is normalized and validated for Stripe checkout with
	// the buyer email as a possible fallback. Other selected providers
	// discard this invoice input before their create-payment call.
	BillingInfo *BillingInfo // supplied invoice identity
	Locale      string       // requested display language
}

// CreateOrderResponse returns checkout amounts and the provider-specific next action.
// ResultType selects that action; an OAuth-required response precedes
// order creation and leaves OrderID and stored-order status unset.
type CreateOrderResponse struct {
	OrderID int64 `json:"order_id"` // created local order identifier
	// AmountCurrency identifies a persisted subscription's face amount. Balance
	// quantities and pre-creation authorization responses leave this field absent.
	AmountCurrency string `json:"amount_currency,omitempty"`
	// Amount is the created order's entitlement amount in the ordinary
	// response. The pre-creation WeChat OAuth response instead returns
	// the checkout base supplied to that authorization step.
	Amount        float64                 `json:"amount"`                   // returned entitlement or base
	PayAmount     float64                 `json:"pay_amount"`               // channel amount including fees
	FeeRate       float64                 `json:"fee_rate"`                 // supplied fee percentage
	FeeFixed      float64                 `json:"fee_fixed"`                // calculated fixed fee
	FeeRateAmount float64                 `json:"fee_rate_amount"`          // calculated percentage fee
	FeeAmount     float64                 `json:"fee_amount"`               // combined channel fees
	Status        string                  `json:"status"`                   // returned local order state
	ResultType    CreatePaymentResultType `json:"result_type,omitempty"`    // checkout next-action variant
	PaymentType   string                  `json:"payment_type"`             // selected payment method
	OutTradeNo    string                  `json:"out_trade_no,omitempty"`   // merchant order number
	PayURL        string                  `json:"pay_url,omitempty"`        // hosted checkout URL
	QRCode        string                  `json:"qr_code,omitempty"`        // payment QR payload
	ClientSecret  string                  `json:"client_secret,omitempty"`  // provider client-side secret
	CustomerID    string                  `json:"customer_id,omitempty"`    // provider customer identifier
	InvoiceID     string                  `json:"invoice_id,omitempty"`     // provider invoice identifier
	InvoiceURL    string                  `json:"invoice_url,omitempty"`    // hosted invoice URL
	InvoicePDF    string                  `json:"invoice_pdf,omitempty"`    // invoice PDF URL
	InvoiceStatus string                  `json:"invoice_status,omitempty"` // recorded invoice state
	IntentID      string                  `json:"intent_id,omitempty"`      // provider payment intent
	Currency      string                  `json:"currency,omitempty"`       // provider payment currency
	CountryCode   string                  `json:"country_code,omitempty"`   // provider checkout region
	PaymentEnv    string                  `json:"payment_env,omitempty"`    // provider frontend environment
	OAuth         *WechatOAuthInfo        `json:"oauth,omitempty"`          // WeChat authorization next step
	// JSAPI and JSAPIPayload are compatible response names assigned the
	// same provider JSAPI pointer by BuildCreateOrderResponse. Callers
	// treat their payload as shared when accessing either field.
	JSAPI        *WechatJSAPIPayload `json:"jsapi,omitempty"`         // WeChat invocation payload
	JSAPIPayload *WechatJSAPIPayload `json:"jsapi_payload,omitempty"` // compatible WeChat invocation payload
	ExpiresAt    time.Time           `json:"expires_at"`              // created order deadline
	PaymentMode  string              `json:"payment_mode,omitempty"`  // selected channel mode
	// ResumeToken is populated when checkout can sign a resume token for
	// a nonempty canonical return page. It binds the local order and
	// selected channel to the continuation flow.
	ResumeToken string `json:"resume_token,omitempty"` // signed order-resume token
	// AlipayMobilePrecreateDeepLink signals that the selected mobile
	// Alipay precreate flow returned a nonblank QR code. Checkout fills
	// it after building the ordinary created-order response.
	AlipayMobilePrecreateDeepLink bool `json:"alipay_mobile_precreate_deep_link,omitempty"` // Alipay mobile-link availability
}
