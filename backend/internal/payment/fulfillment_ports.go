package payment

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// fulfillment_ports.go defines conditional order persistence and entitlement
// operations and binds fulfillment runtime callbacks.
// Verified payment observations can be replayed by callbacks, queries, or
// recovery workers while entitlement delivery is unfinished. FulfillmentStore
// exposes the conditional status and timestamp updates used to claim and
// finalize that work, and entitlement interfaces delegate replay protection to
// the order-owned code or source-order subscription.
//
// The stored lease timestamp conditions completion and failure updates after
// grant work. Balance grants, order rebates, and completion remain separately
// invoked operations, so an error can leave a previously completed grant for
// the next attempt to recognize. Runtime auditing records ordinary
// observations, while transaction-critical rebate records stay with their
// persistence operation. Completion notifications use the injected scheduler
// and sender after publication; optional enrichment reads can leave individual
// email variables empty.

type (
	// FulfillmentLease carries the stored timestamp used to condition final
	// status writes.
	FulfillmentLease struct {
		// Version is the stored updated_at value read after a successful claim.
		// Completion and failure compare it to reject an order changed by another
		// writer.
		Version time.Time
	}
	// OrderTransition describes conditional payment state changes and optional
	// persisted details.
	OrderTransition struct {
		ID int64 // local payment order ID
		// From lists accepted current statuses for TransitionOrder. The update
		// applies only while the addressed row remains in one of those supplied
		// states.
		From []string
		// Version adds an updated_at equality condition when supplied. Fulfillment
		// completion and failure use the lease timestamp; nil leaves the transition
		// guarded by status alone.
		Version *time.Time
		Status  string // requested target order state
		// TradeNo replaces the stored upstream identifier when nonempty. Empty text
		// retains the existing value, including an identifier saved by another
		// payment confirmation path.
		TradeNo string
		// PayAmount replaces the paid amount when supplied, including a zero value.
		// ConfirmPayment validates positive finite provider money before
		// constructing its paid transition.
		PayAmount *float64
		// PaidAt, CompletedAt, and FailedAt are independent optional timestamps
		// passed to TransitionOrder. Nil retains each stored timestamp, while a
		// supplied pointer stages its value for the same conditional update.
		// PaidAt: provider confirmation time; CompletedAt: entitlement completion
		// time; FailedAt: recorded fulfillment failure time
		PaidAt, CompletedAt, FailedAt *time.Time
		// FailedReason replaces stored failure text when supplied. ClearFailure
		// clears earlier failure fields first, so a supplied reason can then be
		// written by the same transition.
		FailedReason *string
		// ClearFailure clears both failed_at and failed_reason before optional
		// failure values are staged. Payment success and processing use this flag
		// when recovering earlier failures.
		ClearFailure bool
		// InvoiceID, InvoiceURL, InvoicePDF, and InvoiceStatus replace their stored
		// fields independently when nonempty. ApplyInvoiceMetadata trims reported
		// text before persistence; empty values retain existing invoice details.
		// InvoiceID: provider invoice identifier; InvoiceURL: hosted invoice page;
		// InvoicePDF: invoice PDF link; InvoiceStatus: reported invoice state
		InvoiceID, InvoiceURL, InvoicePDF, InvoiceStatus string
	}
)

// FulfillmentStore supplies conditional order writes and order-specific rebate
// persistence.
type FulfillmentStore interface {
	LifeStore // order closing and reconciliation storage
	// Order reads the persisted payment order for a local numeric ID.
	Order(context.Context, int64) (*Order, error)
	// OrderByTradeNumber reads an exact stored merchant order number for
	// notification lookup. A classified absence allows
	// FindPaymentNotificationOrder to try its legacy numeric form; other storage
	// errors are returned.
	OrderByTradeNumber(context.Context, string) (*Order, error)
	// IsNotFound identifies storage absence for notification and recovery lookup
	// callers.
	IsNotFound(error) bool
	// TransitionOrder returns the count changed by the supplied status and
	// optional timestamp conditions. Optional pointers and nonempty strings stage
	// their corresponding values in that same update. A zero count means the
	// caller lost its expected state and may need to reread the order.
	// Fulfillment uses the lease version for completion and failure, while
	// payment confirmation uses its accepted current status list.
	TransitionOrder(context.Context, OrderTransition) (int, error)
	// ClaimFulfillment changes PAID or FAILED, or sufficiently stale RECHARGING,
	// to RECHARGING and stores the supplied claim time. The stale threshold is
	// inclusive and earlier failure fields are cleared by the same update. Zero
	// count requires the caller to reread completion or competing state.
	// AcquirePaymentFulfillmentLease rereads the stored timestamp after success
	// to obtain database precision for subsequent conditional finalization.
	ClaimFulfillment(context.Context, int64, time.Time, time.Time) (int, error)
	// HasAudit reports an order audit action for skip and observation decisions.
	// The PostgreSQL adapter discards the count error and tests the returned
	// count, so callers receive no separate indication that the history read
	// failed.
	HasAudit(context.Context, int64, string) bool
	// ApplyOrderRebate owns the transaction combining an order audit claim with
	// the injected rebate operation. The order ID correlates the grant, and
	// applied/skipped audit actions stop completed replay. The PostgreSQL adapter
	// passes its Ent transaction context to the rebate operation, then commits
	// the grant and final audit together. Caller completion follows this
	// operation; failures return for fulfillment recovery and also trigger
	// best-effort failure observation.
	ApplyOrderRebate(context.Context, *Order, float64) error
}

// FulfillmentRedeemer grants balance through the order-owned redeem code and
// usage record.
type FulfillmentRedeemer interface {
	// GetByCode reads the order-owned redeem credential and its usage state.
	// ResolveRedeemAction treats any lookup error or nil result as creation, so
	// callers keep later creation/redemption failures visible.
	GetByCode(context.Context, string) (*billing.RedeemCode, error)
	// CreateCode persists a balance redeem code for an unfinished checkout order.
	// DoBalance invokes it before redemption when lookup yields no usable code;
	// uniqueness and errors belong to the redeem implementation.
	CreateCode(context.Context, *billing.RedeemCode) error
	// Redeem grants the supplied user the order code's balance entitlement and
	// records usage under billing rules. Payment passes
	// ContextSkipRedeemAffiliate, then applies its separate order rebate before
	// marking completion.
	Redeem(context.Context, int64, string) (*billing.RedeemCode, error)
}

// FulfillmentSubscriptions grants source-order subscriptions and reads current
// notification entitlement details.
type FulfillmentSubscriptions interface {
	// AssignOrExtendSubscription grants or reuses the subscription described by
	// its input and reports whether it was queued. Payment supplies a positive
	// source order ID for billing replay and recorded duration/quotas when
	// available.
	AssignOrExtendSubscription(context.Context, *billing.AssignSubscriptionInput) (*billing.UserSubscription, bool, error)
	// GetActiveSubscription reads the active grant for a user and plan to enrich
	// a purchase notification. A missing or failed read leaves the notification's
	// expiry variable empty.
	GetActiveSubscription(context.Context, int64, int64) (*billing.UserSubscription, error)
}

// PaymentNotice carries the destination, order source, language, and
// notification variables.
type PaymentNotice struct {
	// Event, RecipientEmail, RecipientName, SourceType, SourceID, and Locale are
	// passed together to the injected sender. Its delivery policy interprets the
	// event, destination, order source, and preferred language.
	//
	// Event: notification event name. RecipientEmail: target recipient email.
	// RecipientName: displayed recipient name. SourceType: notification source
	// category. SourceID: decimal order identity. Locale: requested display
	// language.
	Event, RecipientEmail, RecipientName, SourceType, SourceID, Locale string
	// recipient account ID
	UserID int64
	// Variables is passed as supplied to the injected Notify callback. Balance
	// and subscription senders build the string map for their event before
	// dispatch.
	Variables map[string]string
}

// FulfillmentRuntime supplies audit, logging, notification dispatch, user
// reads, and the clock.
type FulfillmentRuntime struct {
	Now func() time.Time // payment state observation clock
	// Audit records ordinary payment events through an errorless callback.
	// Mandatory rebate and refund records use their stores separately; this
	// callback's persistence failures cannot be returned by fulfillment.
	Audit func(context.Context, int64, string, string, map[string]any)
	// payment diagnostic logging callback
	Log func(string, string, ...any)
	// Background schedules the completion notification after order publication.
	// Dispatch calls it when Notify is configured, so callers provide the
	// scheduler before enabling that notification path.
	Background func(string, func())
	// Notify sends a payment event using its order source and locale. It receives
	// an independent timed context during dispatch, and its errors are logged
	// after completion remains persisted.
	Notify func(context.Context, PaymentNotice) error
	// User optionally reads current balance for a recharge email. Failed or nil
	// reads retain the empty current_balance variable and allow the notification
	// send to continue.
	User func(context.Context, int64) (*RefundUser, error)
	// RebateEnabled gates the separate order rebate step when a valid
	// purchased-point base exists. A missing callback skips that step and leaves
	// entitlement completion eligible.
	RebateEnabled func(context.Context) bool
}

// Fulfillment confirms provider observations and resumes order-specific
// entitlement delivery.
type Fulfillment struct {
	store         FulfillmentStore    // order state and rebate persistence
	bindings      *ProviderBindings   // historical order provider resolver
	registry      *Registry           // legacy method binding registry
	redeemService FulfillmentRedeemer // balance code grant service
	// subscription grant and lookup service
	subscriptionSvc FulfillmentSubscriptions
	runtime         FulfillmentRuntime // injected payment completion operations
}

// NewFulfillment binds order storage, provider resolution, redemption, and
// subscription operations. Missing clock, logger, and audit callbacks receive
// defaults; notification dispatch still requires Background when Notify is
// configured and a completion event is sent.
func NewFulfillment(store FulfillmentStore, bindings *ProviderBindings, registry *Registry, redeem FulfillmentRedeemer, subscriptions FulfillmentSubscriptions, runtime FulfillmentRuntime) *Fulfillment {
	if runtime.Now == nil {
		runtime.Now = time.Now
	}
	if runtime.Log == nil {
		runtime.Log = func(string, string, ...any) {}
	}
	if runtime.Audit == nil {
		runtime.Audit = func(context.Context, int64, string, string, map[string]any) {}
	}
	return &Fulfillment{
		store:           store,
		bindings:        bindings,
		registry:        registry,
		redeemService:   redeem,
		subscriptionSvc: subscriptions,
		runtime:         runtime,
	}
}
