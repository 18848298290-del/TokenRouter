package payment

import (
	"maps"
	"math/rand/v2"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// An order records the checkout entitlement or plan amount separately from
// gateway payment and fee quantities, together with the merchant identity used
// for later confirmation. Payment and refund timestamps describe different
// stages, and updated_at supplies the version used by conditional completion
// claims after other work has progressed.
//
// Order copies retain those scalar observations while detaching their optional
// pointers and recognized snapshot collections. Direct recursive copying keeps
// numeric types and nil/empty representation intact, but arbitrary
// unrecognized mutable values remain shared with their caller. Newly allocated
// merchant candidates use the process-local date and random ASCII text; stored
// historical numbers remain exact lookup identities, with collision checks
// owned by checkout persistence.

const (
	// OrderIDPrefix identifies merchant numbers generated for current checkout,
	// while existing orders retain the exact number stored when they were created.
	OrderIDPrefix = "tr_"
)

// Order retains checkout snapshots, payment progress, and refund observations
// for one purchase.
type Order struct {
	ID        int64  `json:"id,omitempty"`         // local payment order ID
	UserID    int64  `json:"user_id,omitempty"`    // purchasing account ID
	UserEmail string `json:"user_email,omitempty"` // checkout buyer email
	// checkout buyer display name
	UserName string `json:"user_name,omitempty"`
	// checkout buyer administration notes
	UserNotes *string `json:"user_notes,omitempty"`
	// Amount, PayAmount, FeeRate, FeeFixed, FeeRateAmount, and FeeAmount retain
	// separate checkout quantities. Balance Amount can include its recharge
	// multiplier, while PayAmount and fee amounts use the selected gateway
	// currency.
	// credited balance or plan amount
	Amount float64 `json:"amount,omitempty"`
	// gateway currency payment amount
	PayAmount float64 `json:"pay_amount,omitempty"`
	// percentage gateway fee rate
	FeeRate float64 `json:"fee_rate,omitempty"`
	// fixed gateway fee amount
	FeeFixed float64 `json:"fee_fixed,omitempty"`
	// computed percentage fee amount
	FeeRateAmount float64 `json:"fee_rate_amount,omitempty"`
	// combined gateway fee amount
	FeeAmount float64 `json:"fee_amount,omitempty"`
	// order-owned balance redeem credential
	RechargeCode string `json:"recharge_code,omitempty"`
	// stored merchant order number
	OutTradeNo string `json:"out_trade_no,omitempty"`
	// selected visible payment method
	PaymentType string `json:"payment_type,omitempty"`
	// stored provider transaction reference
	PaymentTradeNo string `json:"payment_trade_no,omitempty"`
	// provider checkout page URL
	PayURL *string `json:"pay_url,omitempty"`
	// provider scannable QR payload
	QrCode *string `json:"qr_code,omitempty"`
	// stored QR image reference
	QrCodeImg *string `json:"qr_code_img,omitempty"`
	// provider customer account identity
	PaymentCustomerID *string `json:"payment_customer_id,omitempty"`
	// provider invoice object identity
	PaymentInvoiceID *string `json:"payment_invoice_id,omitempty"`
	// hosted provider invoice URL
	PaymentInvoiceURL *string `json:"payment_invoice_url,omitempty"`
	// provider invoice PDF URL
	PaymentInvoicePdfURL *string `json:"payment_invoice_pdf_url,omitempty"`
	// reported provider invoice state
	PaymentInvoiceStatus *string `json:"payment_invoice_status,omitempty"`
	// BillingSnapshot retains the checkout billing identity as JSON-compatible
	// values. Clone detaches its recognized map/slice shapes; unsupported mutable
	// values passed programmatically remain shared.
	BillingSnapshot map[string]any `json:"billing_snapshot,omitempty"`
	// balance or subscription purchase kind
	OrderType string `json:"order_type,omitempty"`
	// purchased subscription plan ID
	PlanID *int64 `json:"plan_id,omitempty"`
	// PlanSnapshot retains the supplied checkout sale terms for later
	// subscription issuance. Clone copies its three optional quota pointers,
	// while the remaining snapshot values are scalar.
	PlanSnapshot billing.SubscriptionPlanSnapshot `json:"plan_snapshot,omitempty"`
	// ProviderInstanceID stores the selected instance as decimal text. Snapshot
	// identity can supply the pin when this column is absent, and historical
	// resolution still reads that instance's current configuration.
	ProviderInstanceID *string `json:"provider_instance_id,omitempty"`
	// ProviderKey records the selected adapter implementation. Expected
	// notification identity prefers a nonempty snapshot key before this column; a
	// resolved instance key has the delegate's highest precedence.
	ProviderKey *string `json:"provider_key,omitempty"`
	// ProviderSnapshot retains checkout merchant, currency, and display facts
	// used by callbacks, refunds, and notifications. Clone detaches recognized
	// mutable JSON shapes while keeping other arbitrary values as supplied.
	ProviderSnapshot map[string]any `json:"provider_snapshot,omitempty"`
	// current payment lifecycle state
	Status string `json:"status,omitempty"`
	// requested reversal in Amount units
	RefundAmount float64 `json:"refund_amount,omitempty"`
	// administrative refund reason text
	RefundReason *string `json:"refund_reason,omitempty"`
	// recorded refund completion time
	RefundAt *time.Time `json:"refund_at,omitempty"`
	// recorded force recovery choice
	ForceRefund bool `json:"force_refund,omitempty"`
	// user refund request time
	RefundRequestedAt *time.Time `json:"refund_requested_at,omitempty"`
	// user refund request explanation
	RefundRequestReason *string `json:"refund_request_reason,omitempty"`
	// recorded refund requester text
	RefundRequestedBy *string `json:"refund_requested_by,omitempty"`
	// current local expiration time
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	// PaidAt, CompletedAt, FailedAt, and FailedReason retain different progress
	// observations. PaidAt marks confirmed payment, CompletedAt marks granted
	// entitlements, and failure fields can record provider or entitlement work
	// failure. Recovery can clear those failures while keeping paid time.
	// local payment confirmation time
	PaidAt *time.Time `json:"paid_at,omitempty"`
	// local entitlement completion time
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	// recorded payment failure time
	FailedAt *time.Time `json:"failed_at,omitempty"`
	// recorded payment failure explanation
	FailedReason *string `json:"failed_reason,omitempty"`
	// checkout caller IP address
	ClientIP string `json:"client_ip,omitempty"`
	// checkout request host text
	SrcHost string `json:"src_host,omitempty"`
	// checkout browser referer URL
	SrcURL *string `json:"src_url,omitempty"`
	// local order creation time
	CreatedAt time.Time `json:"created_at,omitempty"`
	// UpdatedAt is the persisted mutation timestamp used as a fulfillment or
	// refund claim version. Updating other order fields can invalidate a later
	// timestamp-conditioned completion from an earlier observation.
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	Edges     struct{}  `json:"edges"` // empty HTTP compatibility object
}

// Clone returns a copied order and detaches every optional scalar pointer
// declared on Order plus the plan's three quota pointers. Nil receiver returns
// nil, and snapshot maps recurse through the mutable shapes handled by
// cloneOrderValue.
//
// Those shapes are map[string]any, []any, []string, and map[string]string,
// with typed nil and nonnil empty collections retained. Supply acyclic
// recognized collections because recursive traversal tracks no visited
// references. Other values are copied as interface values, so unsupported
// mutable objects can remain shared. Direct copying keeps numeric dynamic
// types for callers inserting arbitrary snapshot values.
func (o *Order) Clone() *Order {
	if o == nil {
		return nil
	}
	copy := *o
	if o.UserNotes != nil {
		value := *o.UserNotes
		copy.UserNotes = &value
	}
	if o.PayURL != nil {
		value := *o.PayURL
		copy.PayURL = &value
	}
	if o.QrCode != nil {
		value := *o.QrCode
		copy.QrCode = &value
	}
	if o.QrCodeImg != nil {
		value := *o.QrCodeImg
		copy.QrCodeImg = &value
	}
	if o.PaymentCustomerID != nil {
		value := *o.PaymentCustomerID
		copy.PaymentCustomerID = &value
	}
	if o.PaymentInvoiceID != nil {
		value := *o.PaymentInvoiceID
		copy.PaymentInvoiceID = &value
	}
	if o.PaymentInvoiceURL != nil {
		value := *o.PaymentInvoiceURL
		copy.PaymentInvoiceURL = &value
	}
	if o.PaymentInvoicePdfURL != nil {
		value := *o.PaymentInvoicePdfURL
		copy.PaymentInvoicePdfURL = &value
	}
	if o.PaymentInvoiceStatus != nil {
		value := *o.PaymentInvoiceStatus
		copy.PaymentInvoiceStatus = &value
	}
	if o.PlanID != nil {
		value := *o.PlanID
		copy.PlanID = &value
	}
	if o.ProviderInstanceID != nil {
		value := *o.ProviderInstanceID
		copy.ProviderInstanceID = &value
	}
	if o.ProviderKey != nil {
		value := *o.ProviderKey
		copy.ProviderKey = &value
	}
	if o.RefundReason != nil {
		value := *o.RefundReason
		copy.RefundReason = &value
	}
	if o.RefundAt != nil {
		value := *o.RefundAt
		copy.RefundAt = &value
	}
	if o.RefundRequestedAt != nil {
		value := *o.RefundRequestedAt
		copy.RefundRequestedAt = &value
	}
	if o.RefundRequestReason != nil {
		value := *o.RefundRequestReason
		copy.RefundRequestReason = &value
	}
	if o.RefundRequestedBy != nil {
		value := *o.RefundRequestedBy
		copy.RefundRequestedBy = &value
	}
	if o.PaidAt != nil {
		value := *o.PaidAt
		copy.PaidAt = &value
	}
	if o.CompletedAt != nil {
		value := *o.CompletedAt
		copy.CompletedAt = &value
	}
	if o.FailedAt != nil {
		value := *o.FailedAt
		copy.FailedAt = &value
	}
	if o.FailedReason != nil {
		value := *o.FailedReason
		copy.FailedReason = &value
	}
	if o.SrcURL != nil {
		value := *o.SrcURL
		copy.SrcURL = &value
	}
	// Copy recognized snapshot collections directly so their numeric dynamic
	// types and nil/empty distinctions survive the order copy without an encoding
	// round trip.
	copy.BillingSnapshot = cloneOrderMap(o.BillingSnapshot)
	copy.ProviderSnapshot = cloneOrderMap(o.ProviderSnapshot)
	if o.PlanSnapshot.DailyLimitUSD != nil {
		value := *o.PlanSnapshot.DailyLimitUSD
		copy.PlanSnapshot.DailyLimitUSD = &value
	}
	if o.PlanSnapshot.WeeklyLimitUSD != nil {
		value := *o.PlanSnapshot.WeeklyLimitUSD
		copy.PlanSnapshot.WeeklyLimitUSD = &value
	}
	if o.PlanSnapshot.MonthlyLimitUSD != nil {
		value := *o.PlanSnapshot.MonthlyLimitUSD
		copy.PlanSnapshot.MonthlyLimitUSD = &value
	}
	return &copy
}

// cloneOrderMap returns nil for nil input or a new string-keyed map populated
// through cloneOrderValue. Nested ownership follows that helper's recognized
// shapes, so unsupported values can remain shared with the source.
func cloneOrderMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = cloneOrderValue(value)
	}
	return out
}

// cloneOrderValue copies the recognized JSON map/slice shapes and returns
// other interface values as supplied. Nested map[string]any and []any recurse,
// while []string and map[string]string copy their scalar entries.
//
// Typed nil collections retain their typed nil result, and nonnil empty
// collections stay nonnil. Scalars such as json.Number keep their original
// dynamic type because no encoding round trip occurs. Unsupported mutable
// values, including other slice or map types, remain shared; callers restrict
// snapshot data or account for those references when using Order.Clone.
func cloneOrderValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneOrderMap(typed)
	case []any:
		if typed == nil {
			return []any(nil)
		}
		out := make([]any, len(typed))
		for i, v := range typed {
			out[i] = cloneOrderValue(v)
		}
		return out
	// Retain the collection's nil versus nonnil-empty representation before
	// copying entries; response encoding and snapshot compatibility can
	// distinguish those two forms.
	case []string:
		if typed == nil {
			return []string(nil)
		}
		return append([]string{}, typed...)
	case map[string]string:
		if typed == nil {
			return map[string]string(nil)
		}
		out := make(map[string]string, len(typed))
		maps.Copy(out, typed)
		return out
	// Values outside the recognized JSON collection shapes retain their interface
	// value. Their mutable references can therefore stay shared even when the
	// containing order map was copied.
	default:
		return value
	}
}

// GenerateOutTradeNo returns OrderIDPrefix, the current process-local calendar
// date, and eight random ASCII alphanumeric characters. The result is a
// candidate merchant number; checkout storage checks existing numbers and
// database persistence handles uniqueness conflicts.
func GenerateOutTradeNo() string {
	date := time.Now().Format("20060102")
	rnd := GenerateMerchantRandomString(8)
	return OrderIDPrefix + date + rnd
}

// GenerateMerchantRandomString returns n ASCII alphanumeric characters
// selected with math/rand/v2. Zero returns empty text and a negative count
// panics during allocation; merchant-number generation uses a positive fixed
// count and does not require this helper to supply cryptographic entropy.
func GenerateMerchantRandomString(n int) string {
	// Keep generated merchant characters inside the lookup ASCII alphabet.
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = charset[rand.IntN(len(charset))]
	}
	return string(b)
}
